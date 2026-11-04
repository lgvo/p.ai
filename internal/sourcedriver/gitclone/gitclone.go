// Package gitclone computes the Git edits that rename a stopped session's
// clone, from the clone's files a caller read. It reads and writes nothing
// itself: the caller reads the paths it asks for, through whatever reaches
// the session's workspace, and applies the edits it returns. A session's
// clone does not depend on where P's repositories live, so every source
// driver whose backend is Git answers RenameWorkspace with it.
//
// Only the ordinary Git administration files of the clone change: HEAD, the
// config, the branch's ref and reflog, and a backup the clone keeps until
// the rename is done. The workspace's own files and the object database are
// never asked for. Unsupported Git layouts are refused before the backup is
// written, which core does before P's new head is created.
package gitclone

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// Rename answers RenameWorkspace for r on the clone the files show: the
// paths to read before calling again, or the edits, in order, that take
// the clone to r.Goal, with the rename's plan. It decides from r and the
// files only.
func Rename(r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	if err := checkRename(r); err != nil {
		return sourcedriver.WorkspaceEdits{}, err
	}
	c, err := newClone(r, files)
	if err != nil {
		return sourcedriver.WorkspaceEdits{}, err
	}
	var edits []sourcedriver.WorkspaceFile
	var p string
	switch r.Goal {
	case sourcedriver.GoalPrepared:
		edits, p, err = c.prepare()
	case sourcedriver.GoalRenamed:
		edits, p, err = c.rename()
	case sourcedriver.GoalDone:
		// The backup's removal reads nothing the plan fixes, and the backup
		// may already be gone: the plan passed stays the rename's.
		edits, err = c.removeBackup()
		p = r.Plan
	}
	switch {
	case errors.Is(err, errUnread):
		return sourcedriver.WorkspaceEdits{Read: c.unread}, nil
	case err != nil:
		return sourcedriver.WorkspaceEdits{}, err
	case r.Plan != "" && p != r.Plan:
		return sourcedriver.WorkspaceEdits{}, errors.New("prepared workspace changed")
	}
	return sourcedriver.WorkspaceEdits{Edits: edits, Plan: p}, nil
}

// checkRename refuses a rename core never asks for: a name Git refuses, a
// rename ID that cannot name the backup, an unknown goal or user, or a plan
// no call returned, each as ErrInvalid; and names outside the closed layout
// the rename edits, or a rename to the same name.
func checkRename(r sourcedriver.WorkspaceRename) error {
	switch {
	case !gitnames.Branch(r.Old) || !gitnames.RenameBranch(r.New):
		return fmt.Errorf("rename branch name: %w", sourcedriver.ErrInvalid)
	case !validUUID(r.ID):
		return fmt.Errorf("rename operation identity invalid: %w", sourcedriver.ErrInvalid)
	case r.Goal != sourcedriver.GoalPrepared && r.Goal != sourcedriver.GoalRenamed && r.Goal != sourcedriver.GoalDone:
		return fmt.Errorf("rename goal unknown: %w", sourcedriver.ErrInvalid)
	case r.User < 0:
		return fmt.Errorf("rename session user invalid: %w", sourcedriver.ErrInvalid)
	case r.Plan != "" && !validPlan(r.Plan):
		return fmt.Errorf("rename plan malformed: %w", sourcedriver.ErrInvalid)
	case !closedLayoutName(r.Old) || !closedLayoutName(r.New) || r.Old == r.New:
		return errors.New("rename names outside closed Git layout")
	}
	return nil
}

// validUUID reports whether s is a UUID in lowercase canonical form, as
// core names its operations.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c < '0' || c > '9' && (c < 'a' || c > 'f'):
			return false
		}
	}
	return true
}

// plan is what a rename's backup fixes and core records: the clone's head
// commit and the SHA-256 of its Git config after the rename.
type plan struct{ head, configSHA256 string }

func (p plan) String() string { return p.head + " " + p.configSHA256 }

func parsePlan(s string) (plan, bool) {
	head, sum, ok := strings.Cut(s, " ")
	digest, err := hex.DecodeString(sum)
	return plan{head: head, configSHA256: sum},
		ok && gitnames.OID(head) && err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == sum
}

func validPlan(s string) bool {
	_, ok := parsePlan(s)
	return ok
}

// The clone's paths, relative to the workspace root.
const (
	headPath   = ".git/HEAD"
	configPath = ".git/config"
)

func refPath(name string) string { return ".git/refs/heads/" + name }
func logPath(name string) string { return ".git/logs/refs/heads/" + name }

// The bounds of what rename reads.
const (
	maxHead   = 256
	maxRef    = 128
	maxConfig = 32 << 10
	maxLog    = 32 << 10
	maxBackup = 128 << 10
)

// forbidden are the Git layouts rename does not support.
var forbidden = []string{".git/worktrees", ".git/commondir", ".git/config.worktree", ".git/packed-refs"}

// between is every directory strictly between root and p, a path under it.
func between(root, p string) []string {
	var dirs []string
	for dir := path.Dir(p); dir != root && dir != "."; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	return dirs
}

// errUnread stops a decision that needs files not given yet; Rename asks
// for them instead.
var errUnread = errors.New("rename needs files not read yet")

// clone is a session's clone as the files given show it, with the paths
// the rename asked for.
type clone struct {
	r      sourcedriver.WorkspaceRename
	files  map[string]sourcedriver.File
	bounds map[string]int64 // every path asked for, with its bound
	unread map[string]int64 // the paths asked for and not given
}

func newClone(r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (*clone, error) {
	c := &clone{r: r, files: make(map[string]sourcedriver.File, len(files)), bounds: map[string]int64{}, unread: map[string]int64{}}
	for _, f := range files {
		if _, twice := c.files[f.Path]; twice {
			return nil, fmt.Errorf("workspace path %q given twice: %w", f.Path, sourcedriver.ErrInvalid)
		}
		c.files[f.Path] = f.File
	}
	return c, nil
}

func (c *clone) backupPath() string { return ".git/p-rename-" + c.r.ID + ".json" }

// ask asks for paths, each within bound, unless they were given.
func (c *clone) ask(bound int64, paths ...string) {
	for _, p := range paths {
		c.bounds[p] = bound
		if _, given := c.files[p]; !given {
			c.unread[p] = bound
		}
	}
}

// ready is errUnread while a path asked for was not given.
func (c *clone) ready() error {
	if len(c.unread) != 0 {
		return errUnread
	}
	return nil
}

// get is p as given. A file larger than its bound is refused, as its read
// would have been.
func (c *clone) get(p string) (sourcedriver.File, error) {
	bound, asked := c.bounds[p]
	f, given := c.files[p]
	if !asked || !given {
		return sourcedriver.File{}, fmt.Errorf("rename decided on %s before reading it", p)
	}
	if int64(len(f.Data)) > bound {
		return sourcedriver.File{}, fmt.Errorf("rename Git file exceeds its bound: %s", p)
	}
	return f, nil
}

// owned reports whether f is of type typ, owned by the session user and
// writable by no one else, with no special bits.
func (c *clone) owned(f sourcedriver.File, typ sourcedriver.FileType) bool {
	return f.Type == typ && f.UID == c.r.User && f.GID == c.r.User && f.Mode&0o7000 == 0 && f.Mode&0o022 == 0
}

// present is p, and whether it exists.
func (c *clone) present(p string) (sourcedriver.File, bool, error) {
	f, err := c.get(p)
	return f, err == nil && f.Type != sourcedriver.FileAbsent, err
}

// absent refuses with refusal unless p is absent.
func (c *clone) absent(p, refusal string) error {
	if _, exists, err := c.present(p); err != nil || exists {
		return errors.Join(err, errors.New(refusal))
	}
	return nil
}

// safeFile is the bytes and mode of p, a regular file the session user
// owns.
func (c *clone) safeFile(p string) ([]byte, uint32, error) {
	f, err := c.get(p)
	if err != nil || !c.owned(f, sourcedriver.FileRegular) {
		return nil, 0, errors.Join(err, fmt.Errorf("rename Git file metadata unavailable: %s", p))
	}
	return f.Data, f.Mode, nil
}

// safeDirectory refuses p unless it is a directory the session user owns.
func (c *clone) safeDirectory(p string) error {
	f, err := c.get(p)
	if err != nil || !c.owned(f, sourcedriver.FileDirectory) {
		return errors.Join(err, fmt.Errorf("rename Git directory unavailable: %s", p))
	}
	return nil
}

// write is the edit that writes p, a regular file of the session user.
func (c *clone) write(p string, data []byte, mode uint32) sourcedriver.WorkspaceFile {
	return sourcedriver.WorkspaceFile{Path: p, File: sourcedriver.File{Type: sourcedriver.FileRegular, UID: c.r.User, GID: c.r.User, Mode: mode, Data: data}}
}

// removal is the edit that removes p.
func removal(p string) sourcedriver.WorkspaceFile {
	return sourcedriver.WorkspaceFile{Path: p, File: sourcedriver.File{Type: sourcedriver.FileAbsent}}
}
