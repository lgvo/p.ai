package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Rename changes only the ordinary Git administration files of the clone in
// a stopped session's workspace, through the runtime driver's workspace
// operations at the incarnation the rename recorded. The workspace files,
// object database and home are never read. Unsupported Git layouts are
// refused before the P ref commit point. Every write and removal passes a
// gate that records the rename's phase; one whose outcome is unknown is
// settled and never sent again.

// renameUID is the session user, who owns the clone.
const renameUID = 1000

// workspaceRenamePlan is what a rename's backup fixes: the clone's head
// commit and the digest of its Git config after the rename.
type workspaceRenamePlan struct {
	HeadOID           string
	ConfigAfterSHA256 string
}

// workspaceAssignmentMismatchError contains only validated Git names or fixed
// absence markers, never configuration contents, URLs, or credentials.
type workspaceAssignmentMismatchError struct {
	Field, Expected, Actual string
}

func (e *workspaceAssignmentMismatchError) Error() string {
	return fmt.Sprintf("workspace %s mismatch: expected %q, actual %q", e.Field, e.Expected, e.Actual)
}

func checkRenameHead(head []byte, old string) error {
	if bytes.Equal(head, []byte("ref: refs/heads/"+old+"\n")) {
		return nil
	}
	actual := "<unavailable>"
	value := strings.TrimSuffix(string(head), "\n")
	if strings.HasPrefix(value, "ref: refs/heads/") {
		name := strings.TrimPrefix(value, "ref: refs/heads/")
		if validWorkspaceRenameName(name) {
			actual = "refs/heads/" + name
		}
	} else if api.ValidOID(value) {
		actual = "<detached> " + value
	}
	return &workspaceAssignmentMismatchError{Field: "branch", Expected: "refs/heads/" + old, Actual: actual}
}

// workspaceRenameBackup is what the clone held before the rename, and the
// modes the rename writes HEAD and the config with: the ones they had.
type workspaceRenameBackup struct {
	Schema     string `json:"schema"`
	Old        string `json:"old"`
	New        string `json:"new"`
	Head       []byte `json:"head"`
	HeadMode   uint32 `json:"head_mode"`
	Ref        []byte `json:"ref"`
	Config     []byte `json:"config"`
	ConfigMode uint32 `json:"config_mode"`
	HasLog     bool   `json:"has_log"`
	Log        []byte `json:"log,omitempty"`
}

// The clone's paths, relative to the workspace root.
const (
	renameHeadPath   = ".git/HEAD"
	renameConfigPath = ".git/config"
)

func renameBackupPath(opID string) string { return ".git/p-rename-" + opID + ".json" }
func renameRefPath(name string) string    { return ".git/refs/heads/" + name }
func renameLogPath(name string) string    { return ".git/logs/refs/heads/" + name }

// The bounds of what rename reads.
const (
	maxRenameHead   = 256
	maxRenameRef    = 128
	maxRenameConfig = 32 << 10
	maxRenameLog    = 32 << 10
	maxRenameBackup = 128 << 10
)

// renameForbidden are Git layouts rename does not support.
var renameForbidden = []string{".git/worktrees", ".git/commondir", ".git/config.worktree", ".git/packed-refs"}

func validWorkspaceRenameName(name string) bool {
	if len(name) == 0 || len(name) > 100 || !safeGitToken.MatchString(name) ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

// renameClone is the clone of one stopped session at the incarnation its
// rename recorded. Each workspace operation it calls has the driver prove
// its boundary.
type renameClone struct {
	driver runtimedriver.Driver
	target runtimedriver.Target
}

// sessionOwned reports whether f is of type typ, owned by the session user
// and writable by no one else, with no special bits.
func sessionOwned(f runtimedriver.File, typ runtimedriver.FileType) bool {
	return f.Type == typ && f.UID == renameUID && f.GID == renameUID && f.Mode&0o7000 == 0 && f.Mode&0o022 == 0
}

// present reads p within limit and reports whether it exists.
func (c renameClone) present(ctx context.Context, p string, limit int64) (runtimedriver.File, bool, error) {
	f, err := c.driver.ReadWorkspace(ctx, c.target, p, limit)
	return f, err == nil && f.Type != runtimedriver.FileAbsent, err
}

// absent refuses with refusal unless p is absent. It reads p with no room
// for its bytes: a file holding any fails the read, and refuses too.
func (c renameClone) absent(ctx context.Context, p string, refusal string) error {
	if _, exists, err := c.present(ctx, p, 0); err != nil || exists {
		return errors.Join(err, errors.New(refusal))
	}
	return nil
}

func (c renameClone) safeFile(ctx context.Context, p string, limit int64) ([]byte, uint32, error) {
	f, err := c.driver.ReadWorkspace(ctx, c.target, p, limit)
	if err != nil || !sessionOwned(f, runtimedriver.FileRegular) {
		return nil, 0, errors.Join(err, fmt.Errorf("rename Git file metadata unavailable: %s", p))
	}
	return f.Data, f.Mode, nil
}

func (c renameClone) safeDirectory(ctx context.Context, p string) error {
	f, err := c.driver.ReadWorkspace(ctx, c.target, p, 0)
	if err != nil || !sessionOwned(f, runtimedriver.FileDirectory) {
		return errors.Join(err, fmt.Errorf("rename Git directory unavailable: %s", p))
	}
	return nil
}

// settle succeeds only when the driver settles e as applied; otherwise it
// returns unresolved, with what the driver said.
func (c renameClone) settle(ctx context.Context, e runtimedriver.Effect, unresolved error) error {
	settled, err := c.driver.Settle(ctx, e)
	if err == nil && settled == runtimedriver.Applied {
		return nil
	}
	return errors.Join(unresolved, err)
}

// writeEffect is the write of a regular file of the session user at p with
// exactly data and mode.
func (c renameClone) writeEffect(p string, data []byte, mode uint32) runtimedriver.Effect {
	return runtimedriver.Effect{Kind: runtimedriver.EffectWrite, Target: c.target, Path: p,
		Content: runtimedriver.File{Type: runtimedriver.FileRegular, UID: renameUID, GID: renameUID, Mode: mode, Data: data}}
}

// removeEffect is the removal of the file at p.
func (c renameClone) removeEffect(p string) runtimedriver.Effect {
	return runtimedriver.Effect{Kind: runtimedriver.EffectRemove, Target: c.target, Path: p}
}

// write writes a regular file of the session user at p with exactly data
// and mode, after g. A write whose outcome is unknown is settled, never
// sent again; either way the file must then read back exactly.
func (c renameClone) write(ctx context.Context, p string, data []byte, mode uint32, g runtimedriver.Gate) error {
	e := c.writeEffect(p, data, mode)
	sent := false
	if err := c.driver.WriteWorkspace(ctx, c.target, p, e.Content, tracked(g, &sent)); err != nil {
		if !sent {
			return err
		}
		if err = c.settle(ctx, e, fmt.Errorf("%s write outcome unresolved: %w", p, err)); err != nil {
			return err
		}
	}
	got, gotMode, err := c.safeFile(ctx, p, int64(len(data)))
	if err != nil || gotMode != mode || !bytes.Equal(got, data) {
		return errors.Join(err, errors.New("rename Git metadata write postcondition unavailable"))
	}
	return nil
}

// remove removes the file at p after g. A removal whose outcome is unknown
// is settled, never sent again; either way p must then be absent.
func (c renameClone) remove(ctx context.Context, p string, g runtimedriver.Gate) error {
	sent := false
	if err := c.driver.RemoveWorkspace(ctx, c.target, p, tracked(g, &sent)); err != nil {
		if !sent {
			return err
		}
		if err = c.settle(ctx, c.removeEffect(p), fmt.Errorf("%s removal outcome unresolved: %w", p, err)); err != nil {
			return err
		}
	}
	return c.absent(ctx, p, p+" removal postcondition unavailable")
}

func (c renameClone) locksAbsent(ctx context.Context, old, next string) error {
	for _, name := range []string{
		".git/index.lock", ".git/HEAD.lock", ".git/config.lock", ".git/packed-refs.lock", renameRefPath(old) + ".lock", renameRefPath(next) + ".lock",
	} {
		if err := c.absent(ctx, name, "Git metadata lock makes rename unavailable"); err != nil {
			return err
		}
	}
	return nil
}

func (c renameClone) layoutSupported(ctx context.Context, refusal string) error {
	for _, forbidden := range renameForbidden {
		if err := c.absent(ctx, forbidden, refusal); err != nil {
			return err
		}
	}
	return nil
}

func renameConfig(raw []byte, old, next string) ([]byte, error) {
	safe, err := safeGitConfig(raw)
	if err != nil {
		var upstream *gitUpstreamConfigError
		if errors.As(err, &upstream) && upstream.branch == old {
			return nil, &workspaceAssignmentMismatchError{Field: "upstream", Expected: "origin:refs/heads/" + old, Actual: "<incomplete>"}
		}
		return nil, err
	}
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("rename Git config terminator unavailable")
	}
	oldHeader := "[branch \"" + old + "\"]"
	newHeader := "[branch \"" + next + "\"]"
	// Read only the sanitized configuration. Its parser validates names and
	// omits every URL, user identity, and executable setting.
	inside, remote, merge := false, "", ""
	for _, line := range strings.Split(string(safe), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inside = trimmed == oldHeader
		} else if inside {
			key, value, _ := strings.Cut(trimmed, "=")
			switch strings.TrimSpace(key) {
			case "remote":
				remote = strings.TrimSpace(value)
			case "merge":
				merge = strings.TrimSpace(value)
			}
		}
	}
	actual := "<unset>"
	if remote != "" && merge != "" {
		actual = remote + ":" + merge
	}
	if remote != "origin" || merge != "refs/heads/"+old {
		return nil, &workspaceAssignmentMismatchError{Field: "upstream", Expected: "origin:refs/heads/" + old, Actual: actual}
	}
	seenOld := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == newHeader {
			return nil, errors.New("destination Git branch config occupied")
		}
		if trimmed == oldHeader {
			if seenOld {
				return nil, errors.New("source Git branch config ambiguous")
			}
			seenOld = true
		}
	}
	if !seenOld {
		return bytes.Clone(raw), nil
	}
	// safeGitConfig has already rejected includes, continuations, and unknown
	// executable controls. Preserve every other byte of the user's config.
	var out strings.Builder
	insideOld := false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			insideOld = trimmed == oldHeader
		}
		if trimmed == oldHeader {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out.WriteString(indent + newHeader + "\n")
		} else if insideOld && strings.HasPrefix(strings.ToLower(trimmed), "merge") {
			key, value, ok := strings.Cut(trimmed, "=")
			if !ok || strings.TrimSpace(strings.ToLower(key)) != "merge" || strings.TrimSpace(value) != "refs/heads/"+old {
				return nil, errors.New("source Git upstream config unsupported")
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out.WriteString(indent + "merge = refs/heads/" + next + "\n")
		} else {
			out.WriteString(line)
		}
	}
	return []byte(out.String()), nil
}

func renameBackupDetails(v workspaceRenameBackup, old, next string) (workspaceRenamePlan, []byte, error) {
	if err := checkRenameHead(v.Head, old); err != nil {
		return workspaceRenamePlan{}, nil, err
	}
	if v.Schema != "p.workspace-rename/v1" || v.Old != old || v.New != next ||
		!bytes.Equal(v.Head, []byte("ref: refs/heads/"+old+"\n")) || len(v.Ref) < 41 || v.Ref[len(v.Ref)-1] != '\n' ||
		!api.ValidOID(strings.TrimSuffix(string(v.Ref), "\n")) || v.HeadMode&^0o755 != 0 || v.ConfigMode&^0o755 != 0 {
		return workspaceRenamePlan{}, nil, errors.New("rename backup identity unavailable")
	}
	after, err := renameConfig(v.Config, old, next)
	if err != nil {
		return workspaceRenamePlan{}, nil, err
	}
	sum := sha256.Sum256(after)
	return workspaceRenamePlan{HeadOID: strings.TrimSuffix(string(v.Ref), "\n"), ConfigAfterSHA256: hex.EncodeToString(sum[:])}, after, nil
}

func (c renameClone) readBackup(ctx context.Context, opID, old, next string) (workspaceRenameBackup, workspaceRenamePlan, []byte, error) {
	var backup workspaceRenameBackup
	if !api.ValidUUID(opID) {
		return backup, workspaceRenamePlan{}, nil, errors.New("rename operation identity invalid")
	}
	raw, mode, err := c.safeFile(ctx, renameBackupPath(opID), maxRenameBackup)
	if err != nil || mode != 0o600 {
		return backup, workspaceRenamePlan{}, nil, errors.Join(err, errors.New("rename backup unavailable"))
	}
	if err = json.Unmarshal(raw, &backup); err != nil {
		return backup, workspaceRenamePlan{}, nil, errors.New("rename backup malformed")
	}
	plan, after, err := renameBackupDetails(backup, old, next)
	return backup, plan, after, err
}

// prepare writes a private backup into the clone before the P ref commit
// point, after issued. A retry can verify the exact backup without reading
// or storing config contents in the control database.
func (c renameClone) prepare(ctx context.Context, opID, old, next string, issued runtimedriver.Gate) (workspaceRenamePlan, error) {
	if !validWorkspaceRenameName(old) || !validWorkspaceRenameName(next) || old == next || !api.ValidUUID(opID) {
		return workspaceRenamePlan{}, errors.New("rename names outside closed Git layout")
	}
	for _, dir := range []string{".", ".git", ".git/refs", ".git/refs/heads"} {
		if err := c.safeDirectory(ctx, dir); err != nil {
			return workspaceRenamePlan{}, err
		}
	}
	if err := c.layoutSupported(ctx, "rename Git layout unsupported"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.locksAbsent(ctx, old, next); err != nil {
		return workspaceRenamePlan{}, err
	}
	if _, exists, err := c.present(ctx, renameBackupPath(opID), maxRenameBackup); err != nil {
		return workspaceRenamePlan{}, err
	} else if exists {
		return c.verifyPrepared(ctx, opID, old, next)
	}
	for _, branch := range []string{old, next} {
		for parent := path.Dir(renameRefPath(branch)); parent != ".git/refs/heads"; parent = path.Dir(parent) {
			if err := c.safeDirectory(ctx, parent); err != nil {
				return workspaceRenamePlan{}, err
			}
		}
	}
	logRoot, logsPresent, err := c.present(ctx, ".git/logs", 0)
	if err != nil || logsPresent && (logRoot.Type != runtimedriver.FileDirectory || logRoot.UID != renameUID || logRoot.GID != renameUID) {
		return workspaceRenamePlan{}, errors.Join(err, errors.New("rename Git log root unsafe"))
	}
	if logsPresent {
		for _, dir := range []string{".git/logs", ".git/logs/refs", ".git/logs/refs/heads"} {
			if err := c.safeDirectory(ctx, dir); err != nil {
				return workspaceRenamePlan{}, err
			}
		}
		for _, branch := range []string{old, next} {
			for parent := path.Dir(renameLogPath(branch)); parent != ".git/logs/refs/heads"; parent = path.Dir(parent) {
				if err := c.safeDirectory(ctx, parent); err != nil {
					return workspaceRenamePlan{}, err
				}
			}
		}
	}
	if err := c.absent(ctx, renameLogPath(next), "destination branch reflog occupied"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.absent(ctx, renameRefPath(next), "destination local branch occupied"); err != nil {
		return workspaceRenamePlan{}, err
	}
	backup, err := c.backupOf(ctx, old, next)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	plan, _, err := renameBackupDetails(backup, old, next)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	raw, _ := json.Marshal(backup)
	if len(raw) > maxRenameBackup {
		return workspaceRenamePlan{}, errors.New("rename backup exceeds bound")
	}
	if err := c.write(ctx, renameBackupPath(opID), raw, 0o600, issued); err != nil {
		return workspaceRenamePlan{}, err
	}
	_, confirmed, _, err := c.readBackup(ctx, opID, old, next)
	if err != nil || confirmed != plan {
		return workspaceRenamePlan{}, errors.Join(err, errors.New("rename backup postcondition unavailable"))
	}
	return plan, nil
}

// backupOf is the backup of the clone's branch old as the clone holds it
// now.
func (c renameClone) backupOf(ctx context.Context, old, next string) (workspaceRenameBackup, error) {
	head, headMode, err := c.safeFile(ctx, renameHeadPath, maxRenameHead)
	if err != nil {
		return workspaceRenameBackup{}, err
	}
	if err := checkRenameHead(head, old); err != nil {
		return workspaceRenameBackup{}, err
	}
	ref, _, err := c.safeFile(ctx, renameRefPath(old), maxRenameRef)
	if err != nil {
		return workspaceRenameBackup{}, err
	}
	config, configMode, err := c.safeFile(ctx, renameConfigPath, maxRenameConfig)
	if err != nil {
		return workspaceRenameBackup{}, err
	}
	backup := workspaceRenameBackup{Schema: "p.workspace-rename/v1", Old: old, New: next, Head: head, HeadMode: headMode, Ref: ref,
		Config: config, ConfigMode: configMode}
	oldLog, exists, err := c.present(ctx, renameLogPath(old), maxRenameLog)
	if err != nil {
		return workspaceRenameBackup{}, err
	}
	if exists {
		if !sessionOwned(oldLog, runtimedriver.FileRegular) {
			return workspaceRenameBackup{}, fmt.Errorf("rename Git file metadata unavailable: %s", renameLogPath(old))
		}
		backup.Log, backup.HasLog = oldLog.Data, true
	}
	return backup, nil
}

// settlePrepared settles the backup write an interrupted prepare sent: it
// must have written exactly the backup of the clone as the clone is now.
func (c renameClone) settlePrepared(ctx context.Context, opID, old, next string) error {
	backup, err := c.backupOf(ctx, old, next)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(backup)
	return c.settle(ctx, c.writeEffect(renameBackupPath(opID), raw, 0o600), errors.New("rename backup write outcome unresolved"))
}

// verifyPrepared rechecks the original local mapping and the exact backup
// immediately before the irreversible P ref create.
func (c renameClone) verifyPrepared(ctx context.Context, opID, old, next string) (workspaceRenamePlan, error) {
	backup, plan, _, err := c.readBackup(ctx, opID, old, next)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	for _, check := range []struct {
		name string
		want []byte
		max  int64
	}{{renameHeadPath, backup.Head, maxRenameHead}, {renameRefPath(old), backup.Ref, maxRenameRef}, {renameConfigPath, backup.Config, maxRenameConfig}} {
		got, _, err := c.safeFile(ctx, check.name, check.max)
		if err != nil || !bytes.Equal(got, check.want) {
			return workspaceRenamePlan{}, errors.Join(err, errors.New("prepared rename source changed"))
		}
	}
	if backup.HasLog {
		got, _, err := c.safeFile(ctx, renameLogPath(old), maxRenameLog)
		if err != nil || !bytes.Equal(got, backup.Log) {
			return workspaceRenamePlan{}, errors.Join(err, errors.New("prepared branch reflog changed"))
		}
	} else if err := c.absent(ctx, renameLogPath(old), "new source branch reflog appeared"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.absent(ctx, renameLogPath(next), "prepared destination reflog occupied"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.absent(ctx, renameRefPath(next), "prepared rename destination occupied"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.layoutSupported(ctx, "prepared rename Git layout changed"); err != nil {
		return workspaceRenamePlan{}, err
	}
	if err := c.locksAbsent(ctx, old, next); err != nil {
		return workspaceRenamePlan{}, err
	}
	return plan, nil
}

// apply renames the clone as its backup plans, each write and removal after
// issued. With issued nil it writes nothing: it only proves that every edit
// is already in place, since an edit an interrupted apply may have sent is
// never sent again.
func (c renameClone) apply(ctx context.Context, opID, old, next string, issued runtimedriver.Gate) (workspaceRenamePlan, error) {
	backup, plan, configAfter, err := c.readBackup(ctx, opID, old, next)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	newPath, oldPath := renameRefPath(next), renameRefPath(old)
	newRef, newExists, err := c.present(ctx, newPath, maxRenameRef)
	if err != nil || newExists && (newRef.Type != runtimedriver.FileRegular || newRef.UID != renameUID || newRef.GID != renameUID) {
		return workspaceRenamePlan{}, errors.Join(err, errors.New("destination local ref metadata changed"))
	}
	if !newExists {
		if issued == nil {
			return workspaceRenamePlan{}, errors.New("local ref creation outcome unresolved")
		}
		if err := c.write(ctx, newPath, backup.Ref, 0o644, issued); err != nil {
			return workspaceRenamePlan{}, err
		}
	} else if !sessionOwned(newRef, runtimedriver.FileRegular) || !bytes.Equal(newRef.Data, backup.Ref) {
		return workspaceRenamePlan{}, errors.New("destination local ref changed")
	}
	newHead := []byte("ref: refs/heads/" + next + "\n")
	head, _, err := c.safeFile(ctx, renameHeadPath, maxRenameHead)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	if bytes.Equal(head, backup.Head) {
		if issued == nil {
			return workspaceRenamePlan{}, errors.New("local HEAD update outcome unresolved")
		}
		if err := c.write(ctx, renameHeadPath, newHead, backup.HeadMode, issued); err != nil {
			return workspaceRenamePlan{}, err
		}
	} else if !bytes.Equal(head, newHead) {
		return workspaceRenamePlan{}, errors.New("local HEAD changed outside rename")
	}
	config, _, err := c.safeFile(ctx, renameConfigPath, maxRenameConfig)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	if bytes.Equal(config, backup.Config) && !bytes.Equal(config, configAfter) {
		if issued == nil {
			return workspaceRenamePlan{}, errors.New("local Git config update outcome unresolved")
		}
		if err := c.write(ctx, renameConfigPath, configAfter, backup.ConfigMode, issued); err != nil {
			return workspaceRenamePlan{}, err
		}
	} else if !bytes.Equal(config, configAfter) {
		return workspaceRenamePlan{}, errors.New("local Git config changed outside rename")
	}
	if backup.HasLog {
		newLog, exists, err := c.present(ctx, renameLogPath(next), maxRenameLog)
		if err != nil || exists && (newLog.Type != runtimedriver.FileRegular || newLog.UID != renameUID || newLog.GID != renameUID) {
			return workspaceRenamePlan{}, errors.Join(err, errors.New("destination branch reflog changed"))
		}
		if !exists {
			if issued == nil {
				return workspaceRenamePlan{}, errors.New("branch reflog creation outcome unresolved")
			}
			if err := c.write(ctx, renameLogPath(next), backup.Log, 0o644, issued); err != nil {
				return workspaceRenamePlan{}, err
			}
		} else if !sessionOwned(newLog, runtimedriver.FileRegular) || !bytes.Equal(newLog.Data, backup.Log) {
			return workspaceRenamePlan{}, errors.New("destination branch reflog differs")
		}
		oldLog, exists, err := c.present(ctx, renameLogPath(old), maxRenameLog)
		if err != nil {
			return workspaceRenamePlan{}, err
		}
		if exists {
			if !sessionOwned(oldLog, runtimedriver.FileRegular) || !bytes.Equal(oldLog.Data, backup.Log) {
				return workspaceRenamePlan{}, errors.New("source branch reflog changed")
			}
			if issued == nil {
				return workspaceRenamePlan{}, errors.New("source reflog removal outcome unresolved")
			}
			if err := c.remove(ctx, renameLogPath(old), issued); err != nil {
				return workspaceRenamePlan{}, err
			}
		}
	}
	oldRef, exists, err := c.present(ctx, oldPath, maxRenameRef)
	if err != nil {
		return workspaceRenamePlan{}, err
	}
	if exists {
		if oldRef.Type != runtimedriver.FileRegular || oldRef.UID != renameUID || oldRef.GID != renameUID {
			return workspaceRenamePlan{}, errors.New("source local ref metadata changed")
		}
		if !sessionOwned(oldRef, runtimedriver.FileRegular) || !bytes.Equal(oldRef.Data, backup.Ref) {
			return workspaceRenamePlan{}, errors.New("source local ref changed")
		}
		if issued == nil {
			return workspaceRenamePlan{}, errors.New("source local ref removal outcome unresolved")
		}
		if err := c.remove(ctx, oldPath, issued); err != nil {
			return workspaceRenamePlan{}, err
		}
	}
	if err := c.verifyRenamed(ctx, opID, old, next, plan); err != nil {
		return workspaceRenamePlan{}, err
	}
	return plan, nil
}

// settleRenamed settles every edit an interrupted apply may have sent, as
// the backup plans them; none is sent again.
func (c renameClone) settleRenamed(ctx context.Context, opID, old, next string) error {
	backup, _, configAfter, err := c.readBackup(ctx, opID, old, next)
	if err != nil {
		return err
	}
	edits := []runtimedriver.Effect{
		c.writeEffect(renameRefPath(next), backup.Ref, 0o644),
		c.writeEffect(renameHeadPath, []byte("ref: refs/heads/"+next+"\n"), backup.HeadMode),
		c.writeEffect(renameConfigPath, configAfter, backup.ConfigMode),
	}
	if backup.HasLog {
		edits = append(edits, c.writeEffect(renameLogPath(next), backup.Log, 0o644), c.removeEffect(renameLogPath(old)))
	}
	for _, e := range append(edits, c.removeEffect(renameRefPath(old))) {
		if err := c.settle(ctx, e, fmt.Errorf("%s %s outcome unresolved", e.Path, e.Kind)); err != nil {
			return err
		}
	}
	return nil
}

// verifyRenamed proves the clone renamed as plan says.
func (c renameClone) verifyRenamed(ctx context.Context, opID, old, next string, plan workspaceRenamePlan) error {
	if !api.ValidOID(plan.HeadOID) || len(plan.ConfigAfterSHA256) != 64 {
		return errors.New("rename workspace plan unavailable")
	}
	checks := []struct {
		path, value string
		max         int64
	}{
		{renameHeadPath, "ref: refs/heads/" + next + "\n", maxRenameHead},
		{renameRefPath(next), plan.HeadOID + "\n", maxRenameRef},
	}
	for _, check := range checks {
		got, _, err := c.safeFile(ctx, check.path, check.max)
		if err != nil || string(got) != check.value {
			return errors.Join(err, errors.New("renamed workspace metadata mismatch"))
		}
	}
	if err := c.absent(ctx, renameRefPath(old), "old workspace branch still present"); err != nil {
		return err
	}
	config, _, err := c.safeFile(ctx, renameConfigPath, maxRenameConfig)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(config)
	if hex.EncodeToString(sum[:]) != plan.ConfigAfterSHA256 {
		return errors.New("renamed workspace config changed")
	}
	// A missing source reflog is required even when logging was disabled. If
	// the backup still exists, its exact content also proves that a present
	// new reflog retained the old history.
	if err := c.absent(ctx, renameLogPath(old), "old branch reflog still present"); err != nil {
		return err
	}
	if _, exists, err := c.present(ctx, renameBackupPath(opID), maxRenameBackup); err != nil {
		return err
	} else if exists {
		backup, _, _, err := c.readBackup(ctx, opID, old, next)
		if err != nil {
			return err
		}
		if backup.HasLog {
			got, _, err := c.safeFile(ctx, renameLogPath(next), maxRenameLog)
			if err != nil || !bytes.Equal(got, backup.Log) {
				return errors.Join(err, errors.New("renamed branch reflog changed"))
			}
		} else if err := c.absent(ctx, renameLogPath(next), "unexpected destination branch reflog"); err != nil {
			return err
		}
	}
	return nil
}

// removeBackup removes the rename's backup after issued, which records the
// removal for settleBackupRemoved.
func (c renameClone) removeBackup(ctx context.Context, opID string, issued runtimedriver.Gate) error {
	name := renameBackupPath(opID)
	meta, exists, err := c.present(ctx, name, maxRenameBackup)
	if err != nil || exists && (meta.Type != runtimedriver.FileRegular || meta.UID != renameUID || meta.GID != renameUID || meta.Mode != 0o600) {
		return errors.Join(err, errors.New("rename backup identity changed"))
	}
	if !exists {
		return nil
	}
	return c.remove(ctx, name, issued)
}

// settleBackupRemoved settles a recorded removal of the rename's backup,
// which is never sent again.
func (c renameClone) settleBackupRemoved(ctx context.Context, opID string) error {
	return c.settle(ctx, c.removeEffect(renameBackupPath(opID)), errors.New("rename backup removal outcome unresolved"))
}

var safeGitToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

type gitUpstreamConfigError struct{ branch, reason string }

func (e *gitUpstreamConfigError) Error() string { return e.reason }

// safeGitConfig accepts only inert facts needed to interpret an ordinary
// standalone worktree, and returns them without URLs or user identity.
// Includes, extensions, filters, monitors, external diff and credential
// helpers are unsupported regardless of where they appear.
func safeGitConfig(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxRenameConfig || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
		return nil, errors.New("Git config size or encoding unavailable")
	}
	section, subject := "", ""
	seenCore := map[string]bool{}
	coreValues := map[string]string{}
	branches := map[string]map[string]string{}
	remoteFetch := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, errors.New("Git config section malformed")
			}
			content := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			section, subject = "", ""
			if content == "core" || content == "user" {
				section = content
				continue
			}
			for _, prefix := range []string{"branch", "remote"} {
				if strings.HasPrefix(content, prefix+" \"") && strings.HasSuffix(content, "\"") {
					name := strings.TrimSuffix(strings.TrimPrefix(content, prefix+" \""), "\"")
					if !safeGitToken.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".") {
						return nil, errors.New("Git config subsection unavailable")
					}
					section, subject = prefix, name
					break
				}
			}
			if section == "" {
				return nil, errors.New("Git config section unsupported")
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return nil, errors.New("Git config entry malformed")
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if strings.ContainsAny(value, "\x00\n\r\\\"") || len(value) > 1024 {
			return nil, errors.New("Git config value unsupported")
		}
		switch section {
		case "core":
			if seenCore[key] {
				return nil, errors.New("Git core config repeated")
			}
			seenCore[key] = true
			switch key {
			case "repositoryformatversion":
				if value != "0" {
					return nil, errors.New("Git repository format unsupported")
				}
			case "bare":
				if value != "false" {
					return nil, errors.New("bare Git repository unavailable")
				}
			case "filemode", "logallrefupdates", "ignorecase", "precomposeunicode":
				if value != "true" && value != "false" {
					return nil, errors.New("Git core boolean unsupported")
				}
			default:
				return nil, errors.New("Git core behavior unsupported")
			}
			coreValues[key] = value
		case "user":
			if key != "name" && key != "email" {
				return nil, errors.New("Git user setting unsupported")
			}
			// Identity is intentionally omitted.
		case "remote":
			if key != "url" && key != "fetch" {
				return nil, errors.New("Git remote setting unsupported")
			}
			if key == "fetch" {
				want := "+refs/heads/*:refs/remotes/" + subject + "/*"
				if value != want || remoteFetch[subject] != "" {
					return nil, errors.New("Git remote refspec unsupported")
				}
				remoteFetch[subject] = value
			}
			// URLs are deliberately omitted.
		case "branch":
			if key != "remote" && key != "merge" {
				return nil, errors.New("Git branch setting unsupported")
			}
			if branches[subject] == nil {
				branches[subject] = map[string]string{}
			}
			if branches[subject][key] != "" {
				return nil, errors.New("Git branch setting repeated")
			}
			if key == "remote" && (!safeGitToken.MatchString(value) || strings.Contains(value, "..")) {
				return nil, errors.New("Git branch remote unsupported")
			}
			if key == "merge" && (!strings.HasPrefix(value, "refs/heads/") || !safeGitToken.MatchString(strings.TrimPrefix(value, "refs/heads/"))) {
				return nil, errors.New("Git branch merge ref unsupported")
			}
			branches[subject][key] = value
		}
	}
	if !seenCore["repositoryformatversion"] || !seenCore["bare"] {
		return nil, errors.New("Git core identity incomplete")
	}
	var out strings.Builder
	out.WriteString("[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
	for _, key := range []string{"filemode", "ignorecase", "precomposeunicode"} {
		if value := coreValues[key]; value != "" {
			fmt.Fprintf(&out, "\t%s = %s\n", key, value)
		}
	}
	out.WriteString("\tlogallrefupdates = false\n")
	remotes := make([]string, 0, len(remoteFetch))
	for name := range remoteFetch {
		remotes = append(remotes, name)
	}
	sort.Strings(remotes)
	for _, name := range remotes {
		fmt.Fprintf(&out, "[remote %q]\n\tfetch = %s\n", name, remoteFetch[name])
	}
	keys := make([]string, 0, len(branches))
	for key := range branches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		values := branches[name]
		if values["remote"] == "" || values["merge"] == "" {
			return nil, &gitUpstreamConfigError{branch: name, reason: "Git upstream setting incomplete"}
		}
		if remoteFetch[values["remote"]] == "" {
			return nil, &gitUpstreamConfigError{branch: name, reason: "Git upstream fetch mapping unavailable"}
		}
		fmt.Fprintf(&out, "[branch %q]\n\tremote = %s\n\tmerge = %s\n", name, values["remote"], values["merge"])
	}
	return []byte(out.String()), nil
}
