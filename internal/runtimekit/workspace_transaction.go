package runtimekit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Only this fixed helper owns this scratch area. It is deliberately outside
// /workspace and inaccessible to p, including during failed creation. It is not
// an operation journal: unpublished contents can always be reconstructed.
const workspaceScratch = "/.p-workspace-init"

// InitWorkspace supervises first initialization as container root. Repository
// commands run as p in a private mount namespace; root only manages scratch and
// atomically publishes the finished directory. No repository command runs root.
func InitWorkspace() error {
	if os.Geteuid() != 0 {
		return errors.New("workspace publication requires container root")
	}
	if _, err := workspaceInputs(); err != nil {
		return err
	}
	if err := endpointParent("/"); err != nil {
		return err
	}
	return workspaceTransaction("/workspace", workspaceScratch, 1000, 1000, func(_ string, lock *os.File) error {
		cmd := exec.Command(KitPath, "stage-workspace")
		cmd.Dir = "/"
		cmd.Env = workspaceHelperEnvironment()
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.ExtraFiles = []*os.File{lock}
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		return cmd.Run()
	}, publishWorkspace)
}

// StageWorkspace is the fixed root child of InitWorkspace. CLONE_NEWNS happens
// before exec, so all Go threads share the new namespace. Make propagation
// private before adding the bind; inherited endpoint mounts remain available.
func StageWorkspace() error {
	if os.Geteuid() != 0 {
		return errors.New("workspace staging requires container root")
	}
	self, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	parent, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", os.Getppid()))
	if err != nil || self == parent {
		return errors.New("workspace staging requires a private mount namespace")
	}
	if err := workspaceScratchDirectory(workspaceScratch); err != nil {
		return err
	}
	stage := filepath.Join(workspaceScratch, "tree")
	if err := workspaceDirectory(stage, 1000); err != nil {
		return err
	}
	lock := os.NewFile(3, "workspace-lock")
	defer lock.Close()
	lockStat, err := lock.Stat()
	if err != nil {
		return err
	}
	parentStat, err := os.Stat(workspaceScratch)
	if err != nil || !os.SameFile(lockStat, parentStat) {
		return errors.New("workspace staging lock missing")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := syscall.Mount(stage, "/workspace", "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	stageFile, err := os.Open(stage)
	if err != nil {
		return err
	}
	defer stageFile.Close()
	cmd := populateWorkspaceCommand(stageFile)
	return cmd.Run()
}

// The scratch lock belongs only to root. Passing its open file description to
// p would let the child release the supervisor's flock or enumerate scratch.
func populateWorkspaceCommand(stageFile *os.File) *exec.Cmd {
	cmd := exec.Command(KitPath, "populate-workspace")
	cmd.Dir = "/workspace"
	cmd.Env = workspaceHelperEnvironment()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{stageFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{1000}}}
	return cmd
}

// PopulateWorkspace has access only to its staging bind and the existing
// session-scoped Git credentials/endpoints. The root-private parent stays hidden.
func PopulateWorkspace() error {
	if os.Geteuid() != 1000 {
		return errors.New("workspace population requires p uid 1000")
	}
	stage := os.NewFile(3, "workspace-stage")
	defer stage.Close()
	stageStat, err := stage.Stat()
	if err != nil {
		return err
	}
	workStat, err := os.Stat("/workspace")
	if err != nil || !os.SameFile(stageStat, workStat) {
		return errors.New("workspace staging bind missing")
	}
	c, err := workspaceInputs()
	if err != nil {
		return err
	}
	return initWorkspaceAt(c, "/workspace", "/run/current-system/sw/bin/git")
}

func workspaceHelperEnvironment() []string {
	return []string{"PATH=/run/current-system/sw/bin", "HOME=/home/p", "LANG=C"}
}

// workspaceTransaction has no destructive path into /workspace. Scratch may be
// discarded only behind the protected parent. The rename also fails atomically
// if the previously empty destination acquires an unexpected file.
func workspaceTransaction(work, scratch string, uid, gid uint32, populate func(string, *os.File) error, publish func(string, string) error) error {
	if err := workspaceDirectory(work, uid); err != nil {
		return err
	}
	if initialized, err := workspaceInitialized(work, uid); err != nil || initialized {
		return err
	}
	if err := emptyWorkspace(work); err != nil {
		return err
	}
	if err := os.Mkdir(scratch, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := workspaceScratchDirectory(scratch); err != nil {
		return err
	}
	lock, err := os.Open(scratch)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Recheck after serialization. A concurrent publisher may have finished.
	if initialized, err := workspaceInitialized(work, uid); err != nil || initialized {
		return err
	}
	if err := emptyWorkspace(work); err != nil {
		return err
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "tree" || !entry.IsDir() {
			return errors.New("unexpected workspace scratch contents; refusing cleanup")
		}
	}
	stage := filepath.Join(scratch, "tree")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.Mkdir(stage, 0755); err != nil {
		return err
	}
	if err := os.Chown(stage, int(uid), int(gid)); err != nil {
		return err
	}
	if err := populate(stage, lock); err != nil {
		return err
	}
	if initialized, err := workspaceInitialized(stage, uid); err != nil || !initialized {
		return errors.New("staged workspace initialization incomplete")
	}
	if err := syncWorkspaceTree(stage); err != nil {
		return err
	}
	if err := emptyWorkspace(work); err != nil {
		return err
	}
	if err := publish(stage, work); err != nil {
		return err
	}
	// Flush both sides of the atomic rename. The completion marker was flushed
	// with the checkout first, so a published tree always contains that guard.
	if err := syncWorkspaceDirectory(filepath.Dir(work)); err != nil {
		return err
	}
	return syncWorkspaceDirectory(scratch)
}

// os.Rename rejects an existing directory before issuing the syscall. Linux
// rename replaces an empty directory atomically and refuses a nonempty one.
func publishWorkspace(from, to string) error {
	if err := syscall.Rename(from, to); err != nil {
		return &os.LinkError{Op: "publish workspace", Old: from, New: to, Err: err}
	}
	return nil
}

func workspaceDirectory(path string, uid uint32) error {
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || owner(st) != uid {
		return errors.New("workspace root missing or unsafe")
	}
	return nil
}

func workspaceScratchDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || owner(st) != uint32(os.Geteuid()) || st.Mode().Perm() != 0700 {
		return errors.New("workspace scratch parent is unsafe; refusing cleanup")
	}
	return nil
}

func emptyWorkspace(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("partial or unexpected workspace; refusing initialization")
	}
	return nil
}

func syncWorkspaceDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncWorkspaceTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil // The containing directory persists the link itself.
		}
		if !entry.Type().IsRegular() {
			return errors.New("unexpected staged workspace file type")
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncWorkspaceDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}
