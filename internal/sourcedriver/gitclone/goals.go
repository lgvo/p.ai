package gitclone

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// prepare takes the clone to prepared: a private backup of what the rename
// changes, written while the clone is still on the old branch. A backup
// already there must be the backup of the clone as it is now. It asks for
// the workspace root and the Git directory before anything in them, and
// for anything under the reflog root only once that root is checked.
func (c *clone) prepare() ([]sourcedriver.WorkspaceFile, string, error) {
	old, next := c.r.Old, c.r.New
	gitDirs := []string{".", ".git", ".git/refs", ".git/refs/heads"}
	c.ask(0, gitDirs...)
	if err := c.ready(); err != nil {
		return nil, "", err
	}
	for _, dir := range gitDirs {
		if err := c.safeDirectory(dir); err != nil {
			return nil, "", err
		}
	}
	refDirs := append(between(".git/refs/heads", refPath(old)), between(".git/refs/heads", refPath(next))...)
	c.ask(0, forbidden...)
	c.ask(0, c.locks()...)
	c.ask(0, refDirs...)
	c.ask(0, ".git/logs")
	c.ask(maxBackup, c.backupPath())
	c.ask(maxHead, headPath)
	c.ask(maxConfig, configPath)
	if err := c.ready(); err != nil {
		return nil, "", err
	}
	if err := c.layoutSupported("rename Git layout unsupported"); err != nil {
		return nil, "", err
	}
	if err := c.locksAbsent(); err != nil {
		return nil, "", err
	}
	if _, exists, err := c.present(c.backupPath()); err != nil {
		return nil, "", err
	} else if exists {
		return c.verifyPrepared()
	}
	for _, dir := range refDirs {
		if err := c.safeDirectory(dir); err != nil {
			return nil, "", err
		}
	}
	logRoot, logs, err := c.present(".git/logs")
	if err != nil || logs && (logRoot.Type != sourcedriver.FileDirectory || logRoot.UID != c.r.User || logRoot.GID != c.r.User) {
		return nil, "", errors.Join(err, errors.New("rename Git log root unsafe"))
	}
	c.ask(maxRef, refPath(old))
	c.ask(0, refPath(next))
	if logs {
		logDirs := append([]string{".git/logs", ".git/logs/refs", ".git/logs/refs/heads"},
			append(between(".git/logs/refs/heads", logPath(old)), between(".git/logs/refs/heads", logPath(next))...)...)
		c.ask(0, logDirs...)
		if err := c.ready(); err != nil {
			return nil, "", err
		}
		for _, dir := range logDirs {
			if err := c.safeDirectory(dir); err != nil {
				return nil, "", err
			}
		}
	}
	c.ask(maxLog, logPath(old))
	c.ask(0, logPath(next))
	if err := c.ready(); err != nil {
		return nil, "", err
	}
	if err := c.absent(logPath(next), "destination branch reflog occupied"); err != nil {
		return nil, "", err
	}
	if err := c.absent(refPath(next), "destination local branch occupied"); err != nil {
		return nil, "", err
	}
	b, err := c.backupOf()
	if err != nil {
		return nil, "", err
	}
	p, _, err := b.details(old, next)
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxBackup {
		return nil, "", errors.New("rename backup exceeds bound")
	}
	return []sourcedriver.WorkspaceFile{c.write(c.backupPath(), raw, 0o600)}, p.String(), nil
}

// locks are the lock files of everything the rename changes: Git holds one
// while it writes.
func (c *clone) locks() []string {
	return []string{".git/index.lock", ".git/HEAD.lock", ".git/config.lock", ".git/packed-refs.lock",
		refPath(c.r.Old) + ".lock", refPath(c.r.New) + ".lock"}
}

func (c *clone) locksAbsent() error {
	for _, lock := range c.locks() {
		if err := c.absent(lock, "Git metadata lock makes rename unavailable"); err != nil {
			return err
		}
	}
	return nil
}

func (c *clone) layoutSupported(refusal string) error {
	for _, p := range forbidden {
		if err := c.absent(p, refusal); err != nil {
			return err
		}
	}
	return nil
}

// verifyPrepared proves that the backup there is this rename's, of the
// clone as it is now: still on the old branch, the new name still free.
// It runs right before P's new head is created.
func (c *clone) verifyPrepared() ([]sourcedriver.WorkspaceFile, string, error) {
	old, next := c.r.Old, c.r.New
	b, p, _, err := c.readBackup()
	if err != nil {
		return nil, "", err
	}
	c.ask(maxRef, refPath(old))
	c.ask(0, refPath(next))
	c.ask(maxLog, logPath(old))
	c.ask(0, logPath(next))
	if err := c.ready(); err != nil {
		return nil, "", err
	}
	for _, check := range []struct {
		path string
		want []byte
	}{{headPath, b.Head}, {refPath(old), b.Ref}, {configPath, b.Config}} {
		got, _, err := c.safeFile(check.path)
		if err != nil || !bytes.Equal(got, check.want) {
			return nil, "", errors.Join(err, errors.New("prepared rename source changed"))
		}
	}
	if b.HasLog {
		got, _, err := c.safeFile(logPath(old))
		if err != nil || !bytes.Equal(got, b.Log) {
			return nil, "", errors.Join(err, errors.New("prepared branch reflog changed"))
		}
	} else if err := c.absent(logPath(old), "new source branch reflog appeared"); err != nil {
		return nil, "", err
	}
	if err := c.absent(logPath(next), "prepared destination reflog occupied"); err != nil {
		return nil, "", err
	}
	if err := c.absent(refPath(next), "prepared rename destination occupied"); err != nil {
		return nil, "", err
	}
	return nil, p.String(), nil
}

// rename takes the clone to renamed, as its backup plans. Each file the
// rename changes must be as the backup holds it, and is edited, or as the
// goal needs it, and is left; anything else is refused. The edits come in
// an order that leaves a valid clone after each: the new ref before HEAD
// names it, and the old ref last.
func (c *clone) rename() ([]sourcedriver.WorkspaceFile, string, error) {
	old, next := c.r.Old, c.r.New
	c.ask(maxBackup, c.backupPath())
	c.ask(maxRef, refPath(next), refPath(old))
	c.ask(maxHead, headPath)
	c.ask(maxConfig, configPath)
	c.ask(maxLog, logPath(next), logPath(old))
	if err := c.ready(); err != nil {
		return nil, "", err
	}
	if _, exists, err := c.present(c.backupPath()); err != nil {
		return nil, "", err
	} else if !exists {
		return c.verifyRenamed()
	}
	b, p, configAfter, err := c.readBackup()
	if err != nil {
		return nil, "", err
	}
	var edits []sourcedriver.WorkspaceFile
	if write, err := c.toWrite(refPath(next), b.Ref, "destination local ref metadata changed", "destination local ref changed"); err != nil {
		return nil, "", err
	} else if write {
		edits = append(edits, c.write(refPath(next), b.Ref, 0o644))
	}
	newHead := []byte("ref: refs/heads/" + next + "\n")
	head, _, err := c.safeFile(headPath)
	if err != nil {
		return nil, "", err
	}
	if bytes.Equal(head, b.Head) {
		edits = append(edits, c.write(headPath, newHead, b.HeadMode))
	} else if !bytes.Equal(head, newHead) {
		return nil, "", errors.New("local HEAD changed outside rename")
	}
	config, _, err := c.safeFile(configPath)
	if err != nil {
		return nil, "", err
	}
	if bytes.Equal(config, b.Config) && !bytes.Equal(config, configAfter) {
		edits = append(edits, c.write(configPath, configAfter, b.ConfigMode))
	} else if !bytes.Equal(config, configAfter) {
		return nil, "", errors.New("local Git config changed outside rename")
	}
	if b.HasLog {
		if write, err := c.toWrite(logPath(next), b.Log, "destination branch reflog changed", "destination branch reflog differs"); err != nil {
			return nil, "", err
		} else if write {
			edits = append(edits, c.write(logPath(next), b.Log, 0o644))
		}
		if remove, err := c.toRemove(logPath(old), b.Log, "source branch reflog changed", "source branch reflog changed"); err != nil {
			return nil, "", err
		} else if remove {
			edits = append(edits, removal(logPath(old)))
		}
	} else {
		// Without a reflog to move, the rename leaves neither branch one.
		if err := c.absent(logPath(old), "old branch reflog still present"); err != nil {
			return nil, "", err
		}
		if err := c.absent(logPath(next), "unexpected destination branch reflog"); err != nil {
			return nil, "", err
		}
	}
	if remove, err := c.toRemove(refPath(old), b.Ref, "source local ref metadata changed", "source local ref changed"); err != nil {
		return nil, "", err
	} else if remove {
		edits = append(edits, removal(refPath(old)))
	}
	return edits, p.String(), nil
}

// toWrite reports whether the rename still has to write p, absent before
// the rename, with data: absent, it has; holding exactly data, it has not.
// Anything else is refused with changedType when p is not a regular file
// of the session user, and with changed otherwise.
func (c *clone) toWrite(p string, data []byte, changedType, changed string) (bool, error) {
	f, exists, err := c.present(p)
	switch {
	case err != nil || exists && (f.Type != sourcedriver.FileRegular || f.UID != c.r.User || f.GID != c.r.User):
		return false, errors.Join(err, errors.New(changedType))
	case !exists:
		return true, nil
	case !c.owned(f, sourcedriver.FileRegular) || !bytes.Equal(f.Data, data):
		return false, errors.New(changed)
	}
	return false, nil
}

// toRemove reports whether the rename still has to remove p, which held data
// before the rename: holding exactly data, it has; absent, it has not.
// Anything else is refused as toWrite refuses it.
func (c *clone) toRemove(p string, data []byte, changedType, changed string) (bool, error) {
	f, exists, err := c.present(p)
	switch {
	case err != nil || exists && (f.Type != sourcedriver.FileRegular || f.UID != c.r.User || f.GID != c.r.User):
		return false, errors.Join(err, errors.New(changedType))
	case !exists:
		return false, nil
	case !c.owned(f, sourcedriver.FileRegular) || !bytes.Equal(f.Data, data):
		return false, errors.New(changed)
	}
	return true, nil
}

// verifyRenamed proves the clone renamed as the plan passed says. Once the
// backup is gone, only the plan tells what the rename wrote.
func (c *clone) verifyRenamed() ([]sourcedriver.WorkspaceFile, string, error) {
	p, ok := parsePlan(c.r.Plan)
	if !ok {
		return nil, "", errors.New("rename backup unavailable")
	}
	for _, check := range []struct{ path, want string }{
		{headPath, "ref: refs/heads/" + c.r.New + "\n"},
		{refPath(c.r.New), p.head + "\n"},
	} {
		got, _, err := c.safeFile(check.path)
		if err != nil || string(got) != check.want {
			return nil, "", errors.Join(err, errors.New("renamed workspace metadata mismatch"))
		}
	}
	if err := c.absent(refPath(c.r.Old), "old workspace branch still present"); err != nil {
		return nil, "", err
	}
	config, _, err := c.safeFile(configPath)
	if err != nil {
		return nil, "", err
	}
	if sum := sha256.Sum256(config); hex.EncodeToString(sum[:]) != p.configSHA256 {
		return nil, "", errors.New("renamed workspace config changed")
	}
	// A source reflog is refused even when logging was disabled.
	if err := c.absent(logPath(c.r.Old), "old branch reflog still present"); err != nil {
		return nil, "", err
	}
	return nil, c.r.Plan, nil
}

// removeBackup takes the clone to done: the backup removed, whatever the
// rest of the clone holds.
func (c *clone) removeBackup() ([]sourcedriver.WorkspaceFile, error) {
	c.ask(maxBackup, c.backupPath())
	if err := c.ready(); err != nil {
		return nil, err
	}
	f, exists, err := c.present(c.backupPath())
	if err != nil || exists && (f.Type != sourcedriver.FileRegular || f.UID != c.r.User || f.GID != c.r.User || f.Mode != 0o600) {
		return nil, errors.Join(err, errors.New("rename backup identity changed"))
	}
	if !exists {
		return nil, nil
	}
	return []sourcedriver.WorkspaceFile{removal(c.backupPath())}, nil
}
