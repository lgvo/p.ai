package gitservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// SnapshotLimits bound both the host staging area and the later guest transfer.
// The builder must copy these bytes through its closed Incus file API; Path is
// never a host mount grant or a plugin-visible path.
const (
	SnapshotMaxEntries            = 20000
	SnapshotMaxPathBytes          = 4096
	SnapshotMaxTreeBytes          = 16 << 20
	SnapshotMaxSourceBytes  int64 = 256 << 20
	SnapshotMaxSymlinkBytes int64 = 4096
)

// SourceSnapshot is a disposable, read-only materialization of one committed
// Git tree. Path is private to P. Close removes only its own staging directory.
type SourceSnapshot struct {
	project   string
	commitOID string
	treeOID   string
	entries   int
	bytes     int64
	path      string
	root      os.FileInfo
	closed    bool
}

func (s *SourceSnapshot) Project() string   { return s.project }
func (s *SourceSnapshot) CommitOID() string { return s.commitOID }
func (s *SourceSnapshot) TreeOID() string   { return s.treeOID }
func (s *SourceSnapshot) Entries() int      { return s.entries }
func (s *SourceSnapshot) Bytes() int64      { return s.bytes }

// Path returns the private host staging path to trusted builder composition.
// It is never passed to a WASI package or mounted into a guest.
func (s *SourceSnapshot) Path() string { return s.path }

func (s *SourceSnapshot) Close() error {
	if s == nil || s.closed {
		return nil
	}
	current, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.closed = true
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(s.root, current) {
		return errors.New("source snapshot staging path changed")
	}
	_ = unsealSnapshot(s.path)
	if err := os.RemoveAll(s.path); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func unsealSnapshot(path string) error {
	return filepath.WalkDir(path, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(p, 0700)
		}
		return nil
	})
}

// CaptureCommittedSource uses the selected source-Git package to resolve one
// authorized source. The captured OID, rather than any later branch tip or
// working tree, is the source for every byte in the returned snapshot.
func (b *Backend) CaptureCommittedSource(ctx context.Context, project string, source plugin.GitSourceSelector) (snapshot *SourceSnapshot, err error) {
	if !validSourceSelector(source) {
		return nil, control.ErrInvalid
	}
	err = b.WithOrigin(ctx, project, func(_ *OriginScope) error {
		oid, e := b.ObserveSource(ctx, project, source)
		if e != nil {
			return e
		}
		snapshot, e = b.captureCommit(ctx, project, oid)
		return e
	})
	return snapshot, err
}

// CapturePinnedCommit materializes only a full commit OID already recorded in
// durable core creation evidence. It never resolves a mutable branch or
// current origin ref during exact retry.
func (b *Backend) CapturePinnedCommit(ctx context.Context, project, commitOID string) (snapshot *SourceSnapshot, err error) {
	if !validProject(project) || !validOID(commitOID) || allZero(commitOID) {
		return nil, control.ErrInvalid
	}
	err = b.WithOrigin(ctx, project, func(_ *OriginScope) error {
		var e error
		snapshot, e = b.captureCommit(ctx, project, commitOID)
		return e
	})
	return snapshot, err
}

// captureCommit accepts only an OID captured by the trusted caller. It does
// not resolve refs, evaluate Nix, invoke hooks, or expose repository config.
func (b *Backend) captureCommit(ctx context.Context, project, commitOID string) (_ *SourceSnapshot, err error) {
	if !validProject(project) || !validOID(commitOID) || allZero(commitOID) {
		return nil, control.ErrInvalid
	}
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return nil, err
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, b.gitPath, append([]string{"-C", repo}, args...)...)
		cmd.Env = append(b.gitEnv(), "GIT_NO_LAZY_FETCH=1")
		var out, diagnostic sourceGitOutput
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		if e := cmd.Run(); e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("Git snapshot object inspection failed")
		}
		return strings.TrimSpace(string(out.text)), nil
	}
	if kind, e := git("cat-file", "-t", commitOID); e != nil || kind != "commit" {
		if e != nil {
			return nil, e
		}
		return nil, errors.New("snapshot source is not a commit")
	}
	treeOID, err := git("rev-parse", "--verify", commitOID+"^{tree}")
	if err != nil || !validOID(treeOID) || allZero(treeOID) {
		return nil, errors.New("Git snapshot tree is invalid")
	}
	if err := privateDirectory(b.stateDir); err != nil {
		return nil, err
	}
	base := filepath.Join(b.stateDir, "source-snapshots")
	if e := os.Mkdir(base, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	if err := privateDirectory(base); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(base, ".p-source-")
	if err != nil {
		return nil, err
	}
	root, err := os.Lstat(path)
	if err != nil {
		_ = os.RemoveAll(path)
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = unsealSnapshot(path)
			_ = os.RemoveAll(path)
		}
	}()
	s := &SourceSnapshot{project: project, commitOID: commitOID, treeOID: treeOID, path: path, root: root}
	state := snapshotWalker{backend: b, repo: repo, ctx: ctx, snapshot: s, links: make(map[string]string)}
	if err = state.walk(treeOID, path, ""); err != nil {
		return nil, err
	}
	if err = state.validateLinks(); err != nil {
		return nil, err
	}
	if err = state.seal(); err != nil {
		return nil, err
	}
	return s, nil
}

type snapshotWalker struct {
	backend   *Backend
	repo      string
	ctx       context.Context
	snapshot  *SourceSnapshot
	treeBytes int
	dirs      []string
	links     map[string]string
}

func (w *snapshotWalker) command(args ...string) *exec.Cmd {
	cmd := exec.CommandContext(w.ctx, w.backend.gitPath, append([]string{"-C", w.repo}, args...)...)
	cmd.Env = append(w.backend.gitEnv(), "GIT_NO_LAZY_FETCH=1")
	return cmd
}

func (w *snapshotWalker) walk(treeOID, dir, prefix string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	// Git's -z format has one record per direct child, preserving raw path
	// bytes and modes. It does not honor export-ignore or checkout filters.
	cmd := w.command("ls-tree", "-z", treeOID)
	var out limitedSnapshotOutput
	out.remaining = SnapshotMaxTreeBytes - w.treeBytes
	cmd.Stdout = &out
	var diagnostic sourceGitOutput
	cmd.Stderr = &diagnostic
	if e := cmd.Run(); e != nil || out.overflow {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		return errors.New("Git snapshot tree listing failed or exceeded limit")
	}
	w.treeBytes += len(out.bytes)
	seen := make(map[string]bool)
	for _, record := range bytes.Split(out.bytes, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if err := w.ctx.Err(); err != nil {
			return err
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 {
			return errors.New("malformed Git tree entry")
		}
		name := string(fields[1])
		if !safeSnapshotName(name) {
			return fmt.Errorf("unsafe Git tree path %q", name)
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return errors.New("Git tree path collision")
		}
		seen[fold] = true
		path := prefix + name
		if len(path) > SnapshotMaxPathBytes {
			return errors.New("Git tree path exceeds limit")
		}
		w.snapshot.entries++
		if w.snapshot.entries > SnapshotMaxEntries {
			return errors.New("Git tree entry count exceeds limit")
		}
		metadata := strings.Split(string(fields[0]), " ")
		if len(metadata) != 3 || !validOID(metadata[2]) {
			return errors.New("malformed Git tree entry")
		}
		mode, kind, oid := metadata[0], metadata[1], metadata[2]
		destination := filepath.Join(dir, name)
		switch {
		case mode == "040000" && kind == "tree":
			if err := os.Mkdir(destination, 0700); err != nil {
				return err
			}
			w.dirs = append(w.dirs, destination)
			if err := w.walk(oid, destination, path+"/"); err != nil {
				return err
			}
		case (mode == "100644" || mode == "100755") && kind == "blob":
			perm := os.FileMode(0400)
			if mode == "100755" {
				perm = 0500
			}
			if err := w.writeBlob(oid, destination, perm, false, path); err != nil {
				return err
			}
		case mode == "120000" && kind == "blob":
			if err := w.writeBlob(oid, destination, 0, true, path); err != nil {
				return err
			}
		case mode == "160000":
			return errors.New("Git submodules are unsupported in environment source")
		default:
			return errors.New("unsupported Git tree entry mode or type")
		}
	}
	return nil
}

func (w *snapshotWalker) writeBlob(oid, destination string, perm os.FileMode, link bool, sourcePath string) error {
	cmd := w.command("cat-file", "-s", oid)
	var out, diagnostic sourceGitOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		if w.ctx.Err() != nil {
			return w.ctx.Err()
		}
		return errors.New("Git snapshot blob size failed")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out.text)), 10, 64)
	if err != nil || size < 0 || size > SnapshotMaxSourceBytes-w.snapshot.bytes || link && size > SnapshotMaxSymlinkBytes {
		return errors.New("Git snapshot source bytes exceed limit")
	}
	w.snapshot.bytes += size
	cmd = w.command("cat-file", "blob", oid)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var diagnostics sourceGitOutput
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		return err
	}
	// Always wait for the Git child, including a failed bounded copy.
	var copyErr error
	if link {
		var target bytes.Buffer
		_, copyErr = io.CopyN(&target, pipe, size)
		if copyErr == nil {
			if !safeSnapshotLink(sourcePath, target.String()) {
				copyErr = errors.New("unsafe Git symlink target")
			} else {
				copyErr = os.Symlink(target.String(), destination)
				if copyErr == nil {
					w.links[sourcePath] = target.String()
				}
			}
		}
	} else {
		var f *os.File
		f, copyErr = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if copyErr == nil {
			_, copyErr = io.CopyN(f, pipe, size)
			if e := f.Close(); copyErr == nil {
				copyErr = e
			}
		}
		if copyErr == nil {
			copyErr = os.Chmod(destination, perm)
		}
	}
	if copyErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if w.ctx.Err() != nil {
		return w.ctx.Err()
	}
	if copyErr != nil {
		return copyErr
	}
	if waitErr != nil {
		return errors.New("Git snapshot blob read failed")
	}
	return nil
}

func (w *snapshotWalker) seal() error {
	for i := len(w.dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(w.dirs[i], 0500); err != nil {
			return err
		}
	}
	return os.Chmod(w.snapshot.path, 0500)
}

type limitedSnapshotOutput struct {
	bytes     []byte
	remaining int
	overflow  bool
}

func (o *limitedSnapshotOutput) Write(p []byte) (int, error) {
	if len(p) > o.remaining {
		o.overflow = true
		return 0, errors.New("Git tree listing exceeds limit")
	}
	o.bytes = append(o.bytes, p...)
	o.remaining -= len(p)
	return len(p), nil
}

func safeSnapshotName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.EqualFold(name, ".git") || len(name) > SnapshotMaxPathBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeSnapshotLink(path, target string) bool {
	if target == "" || filepath.IsAbs(target) || !utf8.ValidString(target) || strings.ContainsRune(target, '\x00') {
		return false
	}
	for _, r := range target {
		if unicode.IsControl(r) {
			return false
		}
	}
	clean := filepath.Clean(filepath.Join(filepath.Dir(path), target))
	return clean != ".." && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(clean)
}

// validateLinks resolves every symlink against the complete Git tree in
// memory. It never follows a host symlink. Lexical checks alone are insufficient:
// a later link component can change where a following '..' is applied.
func (w *snapshotWalker) validateLinks() error {
	var totalSteps int
	for path, target := range w.links {
		current := make([]string, 0, 8)
		queue := append(strings.Split(filepath.Dir(path), "/"), strings.Split(target, "/")...)
		expansions := 0
		for len(queue) > 0 {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			part := queue[0]
			queue = queue[1:]
			totalSteps++
			if totalSteps > 1<<20 {
				return errors.New("Git symlink resolution exceeds limit")
			}
			switch part {
			case "", ".":
				continue
			case "..":
				if len(current) == 0 {
					return fmt.Errorf("Git symlink %q escapes source", path)
				}
				current = current[:len(current)-1]
			default:
				candidate := strings.Join(append(current, part), "/")
				if next, ok := w.links[candidate]; ok {
					expansions++
					if expansions > 40 {
						return fmt.Errorf("Git symlink %q has a cycle or exceeds resolution limit", path)
					}
					queue = append(strings.Split(next, "/"), queue...)
				} else {
					current = append(current, part)
				}
			}
		}
	}
	return nil
}
