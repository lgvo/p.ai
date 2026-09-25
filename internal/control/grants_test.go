package control

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type grantTestAncestor struct{ os.FileInfo }

func (grantTestAncestor) Mode() os.FileMode { return os.ModeDir | 0555 }
func (grantTestAncestor) IsDir() bool       { return true }
func (grantTestAncestor) Sys() any          { return &syscall.Stat_t{Uid: 0, Gid: 0, Ino: 1} }

func grantTestLstat(root string) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil || path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return info, err
		}
		return grantTestAncestor{info}, nil
	}
}

func TestFilesystemGrantClosedShapeAndCeiling(t *testing.T) {
	boundary := GrantBoundary{Ceilings: []string{"/var/lib/p-vm/endpoints", "/var/lib/p-vm/grants"},
		StateDir: "/var/lib/p-vm/state", EndpointPrefix: "/var/lib/p-vm/endpoints/pdev", IncusSocket: "/var/lib/incus/unix.socket.user"}
	good := ProjectPolicy{Network: "none", Command: []string{"/bin/sh"}, FilesystemMounts: []FilesystemGrant{
		{Name: "data", Source: "/var/lib/p-vm/grants/pdev/ro", Type: "directory", Access: "read-only"},
		{Name: "scratch", Source: "/var/lib/p-vm/grants/pdev/rw", Type: "directory", Access: "read-write", Executable: true},
	}}
	if err := ValidateConfiguredGrants(good, boundary); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]FilesystemGrant{
		"traversal":         {Name: "escape", Source: "/var/lib/p-vm/grants/../incus", Type: "directory", Access: "read-only"},
		"outside ceiling":   {Name: "other", Source: "/srv/other", Type: "directory", Access: "read-only"},
		"endpoint":          {Name: "endpoint", Source: "/var/lib/p-vm/endpoints/pdev/socket", Type: "file", Access: "read-only"},
		"sibling endpoint":  {Name: "sibling", Source: "/var/lib/p-vm/endpoints/another/socket", Type: "file", Access: "read-only"},
		"credential tree":   {Name: "home", Source: "/home/p/.ssh", Type: "directory", Access: "read-only"},
		"bad name":          {Name: "../bad", Source: "/var/lib/p-vm/grants/pdev/bad", Type: "directory", Access: "read-only"},
		"nested":            {Name: "nested", Source: "/var/lib/p-vm/grants/pdev/ro/nested", Type: "directory", Access: "read-only"},
		"supplied identity": {Name: "id", Source: "/var/lib/p-vm/grants/pdev/id", Type: "file", Access: "read-only", SourceIdentity: &SourceIdentity{Inode: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := good
			policy.FilesystemMounts = append(append([]FilesystemGrant(nil), good.FilesystemMounts...), changed)
			if err := ValidateConfiguredGrants(policy, boundary); err == nil {
				t.Fatal("unsafe grant accepted")
			}
		})
	}
}

func TestFilesystemGrantSourceIdentityRejectsSwapAndSymlink(t *testing.T) {
	root := t.TempDir()
	lstat := grantTestLstat(root)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	grant := FilesystemGrant{Name: "data", Source: source, Type: "directory", Access: "read-only"}
	first, err := inspectGrantSourceWith(grant, lstat)
	if err != nil || first.Inode == 0 {
		t.Fatalf("source capture: %+v %v", first, err)
	}
	if err := os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	second, err := inspectGrantSourceWith(grant, lstat)
	if err != nil || second == first {
		t.Fatalf("source swap not observed: %+v %+v %v", first, second, err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source+"-old", source); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectGrantSourceWith(grant, lstat); err == nil {
		t.Fatal("source symlink accepted")
	}
	sticky := filepath.Join(root, "sticky")
	if err := os.Mkdir(sticky, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, 01777); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(sticky, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectGrantSourceWith(FilesystemGrant{Name: "child", Source: child, Type: "directory", Access: "read-only"}, lstat); err == nil {
		t.Fatal("writable sticky ancestor accepted")
	}
}
