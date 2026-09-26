package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Rename changes only the ordinary Git administration files of an exact
// stopped/frozen runtime. The workspace files, object database, home, and
// process tree are never copied to the host or a helper for this mutation.
// Unsupported Git layouts are refused before the P ref commit point.
type WorkspaceRenamePlan struct {
	HeadOID           string
	ConfigAfterSHA256 string
}

type renameFileAPI interface {
	lstat(context.Context, string) (guestFile, error)
	lstatOptional(context.Context, string) (guestFile, bool, error)
	getBounded(context.Context, string, int64) (guestFile, bool, error)
	put(context.Context, string, guestFile) error
	deleteTree(context.Context, string) error
}

type workspaceRenameBackup struct {
	Schema string `json:"schema"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Head   []byte `json:"head"`
	Ref    []byte `json:"ref"`
	Config []byte `json:"config"`
	HasLog bool   `json:"has_log"`
	Log    []byte `json:"log,omitempty"`
}

func renameBackupPath(opID string) string { return "/workspace/.git/p-rename-" + opID + ".json" }
func renameRefPath(name string) string    { return "/workspace/.git/refs/heads/" + name }
func renameLogPath(name string) string    { return "/workspace/.git/logs/refs/heads/" + name }

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

func (b *Backend) renameSourceAPI(ctx context.Context, source Session, incusUUID, generation string) (*unixFileAPI, error) {
	if source.WorkspaceOwner != "" || !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) {
		return nil, errors.New("rename source identity unavailable")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return nil, err
	}
	observed, err := b.Inspect(ctx, source)
	if err != nil || !observed.Exists || observed.IncusUUID != incusUUID || observed.Generation != generation ||
		observed.Status != "Stopped" && observed.Status != "Frozen" {
		return nil, errors.Join(err, errors.New("rename source is not exact and quiescent"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(source)}
	if observed.Status == "Frozen" {
		if err := api.verifyFrozenWorkspaceMounts(ctx); err != nil {
			return nil, err
		}
	}
	return api, nil
}

func renameSafeFile(ctx context.Context, api renameFileAPI, name string, max int64) ([]byte, int, error) {
	f, err := api.lstat(ctx, name)
	if err != nil || f.typ != "file" || f.uid != 1000 || f.gid != 1000 || f.mode&07000 != 0 || f.mode&0022 != 0 {
		return nil, 0, errors.Join(err, fmt.Errorf("rename Git file metadata unavailable: %s", name))
	}
	got, exists, err := api.getBounded(ctx, name, max)
	if err != nil || !exists || got.typ != f.typ || got.uid != f.uid || got.gid != f.gid || got.mode != f.mode {
		return nil, 0, errors.Join(err, errors.New("rename Git file changed during read"))
	}
	return got.data, got.mode, nil
}

func renameSafeDirectory(ctx context.Context, api renameFileAPI, name string) error {
	f, err := api.lstat(ctx, name)
	if err != nil || f.typ != "directory" || f.uid != 1000 || f.gid != 1000 || f.mode&07000 != 0 || f.mode&0022 != 0 {
		return errors.Join(err, fmt.Errorf("rename Git directory unavailable: %s", name))
	}
	return nil
}

func renameLocksAbsent(ctx context.Context, api renameFileAPI, old, next string) error {
	for _, name := range []string{
		"/workspace/.git/index.lock", "/workspace/.git/HEAD.lock", "/workspace/.git/config.lock",
		"/workspace/.git/packed-refs.lock", renameRefPath(old) + ".lock", renameRefPath(next) + ".lock",
	} {
		if _, exists, err := api.lstatOptional(ctx, name); err != nil || exists {
			return errors.Join(err, errors.New("Git metadata lock makes rename unavailable"))
		}
	}
	return nil
}

func renameConfig(raw []byte, old, next string) ([]byte, error) {
	if _, err := safeGitConfig(raw); err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("rename Git config terminator unavailable")
	}
	oldHeader := "[branch \"" + old + "\"]"
	newHeader := "[branch \"" + next + "\"]"
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
				return nil, errors.New("source Git upstream does not match assigned branch")
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out.WriteString(indent + "merge = refs/heads/" + next + "\n")
		} else {
			out.WriteString(line)
		}
	}
	return []byte(out.String()), nil
}

func renameBackupDetails(v workspaceRenameBackup, old, next string) (WorkspaceRenamePlan, []byte, error) {
	if v.Schema != "p.workspace-rename/v1" || v.Old != old || v.New != next ||
		!bytes.Equal(v.Head, []byte("ref: refs/heads/"+old+"\n")) || len(v.Ref) < 41 || v.Ref[len(v.Ref)-1] != '\n' ||
		!validBuilderOID(strings.TrimSuffix(string(v.Ref), "\n")) {
		return WorkspaceRenamePlan{}, nil, errors.New("rename backup identity unavailable")
	}
	after, err := renameConfig(v.Config, old, next)
	if err != nil {
		return WorkspaceRenamePlan{}, nil, err
	}
	sum := sha256.Sum256(after)
	return WorkspaceRenamePlan{HeadOID: strings.TrimSuffix(string(v.Ref), "\n"), ConfigAfterSHA256: hex.EncodeToString(sum[:])}, after, nil
}

func readRenameBackup(ctx context.Context, api renameFileAPI, opID, old, next string) (workspaceRenameBackup, WorkspaceRenamePlan, []byte, error) {
	var backup workspaceRenameBackup
	if !uuidPattern.MatchString(opID) {
		return backup, WorkspaceRenamePlan{}, nil, errors.New("rename operation identity invalid")
	}
	raw, mode, err := renameSafeFile(ctx, api, renameBackupPath(opID), 128<<10)
	if err != nil || mode != 0600 {
		return backup, WorkspaceRenamePlan{}, nil, errors.Join(err, errors.New("rename backup unavailable"))
	}
	if err = json.Unmarshal(raw, &backup); err != nil {
		return backup, WorkspaceRenamePlan{}, nil, errors.New("rename backup malformed")
	}
	plan, after, err := renameBackupDetails(backup, old, next)
	return backup, plan, after, err
}

// PrepareWorkspaceRename persists a private runtime-local backup before the P
// ref commit point. A retry can verify the exact backup without reading or
// storing config contents in the control database.
func (b *Backend) PrepareWorkspaceRename(ctx context.Context, source Session, incusUUID, generation, opID, old, next string, beforePut func() error) (WorkspaceRenamePlan, error) {
	if !validWorkspaceRenameName(old) || !validWorkspaceRenameName(next) || old == next || !uuidPattern.MatchString(opID) {
		return WorkspaceRenamePlan{}, errors.New("rename names outside closed Git layout")
	}
	api, err := b.renameSourceAPI(ctx, source, incusUUID, generation)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	return prepareWorkspaceRenameFiles(ctx, api, opID, old, next, beforePut)
}

func prepareWorkspaceRenameFiles(ctx context.Context, api renameFileAPI, opID, old, next string, beforePut func() error) (WorkspaceRenamePlan, error) {
	if beforePut == nil {
		return WorkspaceRenamePlan{}, errors.New("rename backup effect marker missing")
	}
	for _, dir := range []string{"/workspace", "/workspace/.git", "/workspace/.git/refs", "/workspace/.git/refs/heads"} {
		if err := renameSafeDirectory(ctx, api, dir); err != nil {
			return WorkspaceRenamePlan{}, err
		}
	}
	for _, forbidden := range []string{"/workspace/.git/worktrees", "/workspace/.git/commondir", "/workspace/.git/config.worktree", "/workspace/.git/packed-refs"} {
		if _, exists, err := api.lstatOptional(ctx, forbidden); err != nil || exists {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("rename Git layout unsupported"))
		}
	}
	if err := renameLocksAbsent(ctx, api, old, next); err != nil {
		return WorkspaceRenamePlan{}, err
	}
	if _, exists, err := api.lstatOptional(ctx, renameBackupPath(opID)); err != nil {
		return WorkspaceRenamePlan{}, err
	} else if exists {
		plan, err := verifyPreparedWorkspaceRenameFiles(ctx, api, opID, old, next)
		return plan, err
	}
	for _, branch := range []string{old, next} {
		parent := path.Dir(renameRefPath(branch))
		for parent != "/workspace/.git/refs/heads" {
			if err := renameSafeDirectory(ctx, api, parent); err != nil {
				return WorkspaceRenamePlan{}, err
			}
			parent = path.Dir(parent)
		}
	}
	logRoot, logsPresent, err := api.lstatOptional(ctx, "/workspace/.git/logs")
	if err != nil || logsPresent && (logRoot.typ != "directory" || logRoot.uid != 1000 || logRoot.gid != 1000) {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("rename Git log root unsafe"))
	}
	if logsPresent {
		for _, dir := range []string{"/workspace/.git/logs", "/workspace/.git/logs/refs", "/workspace/.git/logs/refs/heads"} {
			if err := renameSafeDirectory(ctx, api, dir); err != nil {
				return WorkspaceRenamePlan{}, err
			}
		}
		for _, branch := range []string{old, next} {
			parent := path.Dir(renameLogPath(branch))
			for parent != "/workspace/.git/logs/refs/heads" {
				if err := renameSafeDirectory(ctx, api, parent); err != nil {
					return WorkspaceRenamePlan{}, err
				}
				parent = path.Dir(parent)
			}
		}
	}
	if _, exists, err := api.lstatOptional(ctx, renameLogPath(next)); err != nil || exists {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("destination branch reflog occupied"))
	}
	if _, exists, err := api.lstatOptional(ctx, renameRefPath(next)); err != nil || exists {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("destination local branch occupied"))
	}
	head, _, err := renameSafeFile(ctx, api, "/workspace/.git/HEAD", 256)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	ref, _, err := renameSafeFile(ctx, api, renameRefPath(old), 128)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	config, _, err := renameSafeFile(ctx, api, "/workspace/.git/config", 32<<10)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	backup := workspaceRenameBackup{Schema: "p.workspace-rename/v1", Old: old, New: next, Head: head, Ref: ref, Config: config}
	if _, exists, err := api.lstatOptional(ctx, renameLogPath(old)); err != nil {
		return WorkspaceRenamePlan{}, err
	} else if exists {
		backup.Log, _, err = renameSafeFile(ctx, api, renameLogPath(old), 32<<10)
		if err != nil {
			return WorkspaceRenamePlan{}, err
		}
		backup.HasLog = true
	}
	plan, _, err := renameBackupDetails(backup, old, next)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	raw, _ := json.Marshal(backup)
	if len(raw) > 128<<10 {
		return WorkspaceRenamePlan{}, errors.New("rename backup exceeds bound")
	}
	if err := beforePut(); err != nil {
		return WorkspaceRenamePlan{}, err
	}
	if err := api.put(ctx, renameBackupPath(opID), guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0600, data: raw}); err != nil {
		return WorkspaceRenamePlan{}, err
	}
	_, confirmed, _, err := readRenameBackup(ctx, api, opID, old, next)
	if err != nil || confirmed != plan {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("rename backup postcondition unavailable"))
	}
	return plan, nil
}

// VerifyPreparedWorkspaceRename rechecks the original local mapping and the
// exact runtime-local backup immediately before the irreversible P ref create.
func (b *Backend) VerifyPreparedWorkspaceRename(ctx context.Context, source Session, incusUUID, generation, opID, old, next string) (WorkspaceRenamePlan, error) {
	api, err := b.renameSourceAPI(ctx, source, incusUUID, generation)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	return verifyPreparedWorkspaceRenameFiles(ctx, api, opID, old, next)
}

func verifyPreparedWorkspaceRenameFiles(ctx context.Context, api renameFileAPI, opID, old, next string) (WorkspaceRenamePlan, error) {
	backup, plan, _, err := readRenameBackup(ctx, api, opID, old, next)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	for _, check := range []struct {
		name string
		want []byte
		max  int64
	}{{"/workspace/.git/HEAD", backup.Head, 256}, {renameRefPath(old), backup.Ref, 128}, {"/workspace/.git/config", backup.Config, 32 << 10}} {
		got, _, err := renameSafeFile(ctx, api, check.name, check.max)
		if err != nil || !bytes.Equal(got, check.want) {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("prepared rename source changed"))
		}
	}
	if backup.HasLog {
		got, _, err := renameSafeFile(ctx, api, renameLogPath(old), 32<<10)
		if err != nil || !bytes.Equal(got, backup.Log) {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("prepared branch reflog changed"))
		}
	} else if _, exists, err := api.lstatOptional(ctx, renameLogPath(old)); err != nil || exists {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("new source branch reflog appeared"))
	}
	if _, exists, err := api.lstatOptional(ctx, renameLogPath(next)); err != nil || exists {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("prepared destination reflog occupied"))
	}
	if _, exists, err := api.lstatOptional(ctx, renameRefPath(next)); err != nil || exists {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("prepared rename destination occupied"))
	}
	for _, forbidden := range []string{"/workspace/.git/worktrees", "/workspace/.git/commondir", "/workspace/.git/config.worktree", "/workspace/.git/packed-refs"} {
		if _, exists, err := api.lstatOptional(ctx, forbidden); err != nil || exists {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("prepared rename Git layout changed"))
		}
	}
	if err := renameLocksAbsent(ctx, api, old, next); err != nil {
		return WorkspaceRenamePlan{}, err
	}
	return plan, nil
}

func putRenameFile(ctx context.Context, api renameFileAPI, name string, content []byte, mode int) error {
	if err := api.put(ctx, name, guestFile{typ: "file", uid: 1000, gid: 1000, mode: mode, data: content}); err != nil {
		return err
	}
	got, gotMode, err := renameSafeFile(ctx, api, name, int64(len(content)))
	if err != nil || gotMode != mode || !bytes.Equal(got, content) {
		return errors.Join(err, errors.New("rename Git metadata write postcondition unavailable"))
	}
	return nil
}

// ApplyWorkspaceRename is called once after a durable effect marker. If the
// call is interrupted, the caller only verifies a complete positive outcome;
// it never reissues a possibly delayed SFTP write against a resumed session.
func (b *Backend) ApplyWorkspaceRename(ctx context.Context, source Session, incusUUID, generation, opID, old, next string, allowWrite bool) (WorkspaceRenamePlan, error) {
	api, err := b.renameSourceAPI(ctx, source, incusUUID, generation)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	return applyWorkspaceRenameFiles(ctx, api, opID, old, next, allowWrite)
}

func applyWorkspaceRenameFiles(ctx context.Context, api renameFileAPI, opID, old, next string, allowWrite bool) (WorkspaceRenamePlan, error) {
	backup, plan, configAfter, err := readRenameBackup(ctx, api, opID, old, next)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	newPath, oldPath := renameRefPath(next), renameRefPath(old)
	newMeta, newExists, err := api.lstatOptional(ctx, newPath)
	if err != nil || newExists && (newMeta.typ != "file" || newMeta.uid != 1000 || newMeta.gid != 1000) {
		return WorkspaceRenamePlan{}, errors.Join(err, errors.New("destination local ref metadata changed"))
	}
	if !newExists {
		if !allowWrite {
			return WorkspaceRenamePlan{}, errors.New("local ref creation outcome unresolved")
		}
		if err := putRenameFile(ctx, api, newPath, backup.Ref, 0644); err != nil {
			return WorkspaceRenamePlan{}, err
		}
	} else if current, _, e := renameSafeFile(ctx, api, newPath, 128); e != nil || !bytes.Equal(current, backup.Ref) {
		return WorkspaceRenamePlan{}, errors.Join(e, errors.New("destination local ref changed"))
	}
	newHead := []byte("ref: refs/heads/" + next + "\n")
	head, mode, err := renameSafeFile(ctx, api, "/workspace/.git/HEAD", 256)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	if bytes.Equal(head, backup.Head) {
		if !allowWrite {
			return WorkspaceRenamePlan{}, errors.New("local HEAD update outcome unresolved")
		}
		if err := putRenameFile(ctx, api, "/workspace/.git/HEAD", newHead, mode); err != nil {
			return WorkspaceRenamePlan{}, err
		}
	} else if !bytes.Equal(head, newHead) {
		return WorkspaceRenamePlan{}, errors.New("local HEAD changed outside rename")
	}
	config, mode, err := renameSafeFile(ctx, api, "/workspace/.git/config", 32<<10)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	if bytes.Equal(config, backup.Config) && !bytes.Equal(config, configAfter) {
		if !allowWrite {
			return WorkspaceRenamePlan{}, errors.New("local Git config update outcome unresolved")
		}
		if err := putRenameFile(ctx, api, "/workspace/.git/config", configAfter, mode); err != nil {
			return WorkspaceRenamePlan{}, err
		}
	} else if !bytes.Equal(config, configAfter) {
		return WorkspaceRenamePlan{}, errors.New("local Git config changed outside rename")
	}
	if backup.HasLog {
		newLog, exists, err := api.lstatOptional(ctx, renameLogPath(next))
		if err != nil || exists && (newLog.typ != "file" || newLog.uid != 1000 || newLog.gid != 1000) {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("destination branch reflog changed"))
		}
		if !exists {
			if !allowWrite {
				return WorkspaceRenamePlan{}, errors.New("branch reflog creation outcome unresolved")
			}
			if err := putRenameFile(ctx, api, renameLogPath(next), backup.Log, 0644); err != nil {
				return WorkspaceRenamePlan{}, err
			}
		} else if got, _, err := renameSafeFile(ctx, api, renameLogPath(next), 32<<10); err != nil || !bytes.Equal(got, backup.Log) {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("destination branch reflog differs"))
		}
		if _, exists, err := api.lstatOptional(ctx, renameLogPath(old)); err != nil {
			return WorkspaceRenamePlan{}, err
		} else if exists {
			got, _, err := renameSafeFile(ctx, api, renameLogPath(old), 32<<10)
			if err != nil || !bytes.Equal(got, backup.Log) {
				return WorkspaceRenamePlan{}, errors.Join(err, errors.New("source branch reflog changed"))
			}
			if !allowWrite {
				return WorkspaceRenamePlan{}, errors.New("source reflog removal outcome unresolved")
			}
			if err := api.deleteTree(ctx, renameLogPath(old)); err != nil {
				return WorkspaceRenamePlan{}, err
			}
		}
	}
	oldRef, exists, err := api.lstatOptional(ctx, oldPath)
	if err != nil {
		return WorkspaceRenamePlan{}, err
	}
	if exists {
		if oldRef.typ != "file" || oldRef.uid != 1000 || oldRef.gid != 1000 {
			return WorkspaceRenamePlan{}, errors.New("source local ref metadata changed")
		}
		content, _, e := renameSafeFile(ctx, api, oldPath, 128)
		if e != nil || !bytes.Equal(content, backup.Ref) {
			return WorkspaceRenamePlan{}, errors.Join(e, errors.New("source local ref changed"))
		}
		if !allowWrite {
			return WorkspaceRenamePlan{}, errors.New("source local ref removal outcome unresolved")
		}
		if err := api.deleteTree(ctx, oldPath); err != nil {
			return WorkspaceRenamePlan{}, err
		}
		if _, exists, err := api.lstatOptional(ctx, oldPath); err != nil || exists {
			return WorkspaceRenamePlan{}, errors.Join(err, errors.New("source local ref removal postcondition unavailable"))
		}
	}
	if err := verifyWorkspaceRenamedFiles(ctx, api, opID, old, next, plan); err != nil {
		return WorkspaceRenamePlan{}, err
	}
	return plan, nil
}

func (b *Backend) VerifyWorkspaceRenamed(ctx context.Context, source Session, incusUUID, generation, opID, old, next string, plan WorkspaceRenamePlan) error {
	api, err := b.renameSourceAPI(ctx, source, incusUUID, generation)
	if err != nil {
		return err
	}
	return verifyWorkspaceRenamedFiles(ctx, api, opID, old, next, plan)
}

func verifyWorkspaceRenamedFiles(ctx context.Context, api renameFileAPI, opID, old, next string, plan WorkspaceRenamePlan) error {
	if !validBuilderOID(plan.HeadOID) || len(plan.ConfigAfterSHA256) != 64 {
		return errors.New("rename workspace plan unavailable")
	}
	checks := []struct {
		path, value string
		max         int64
	}{
		{"/workspace/.git/HEAD", "ref: refs/heads/" + next + "\n", 256},
		{renameRefPath(next), plan.HeadOID + "\n", 128},
	}
	for _, check := range checks {
		got, _, err := renameSafeFile(ctx, api, check.path, check.max)
		if err != nil || string(got) != check.value {
			return errors.Join(err, errors.New("renamed workspace metadata mismatch"))
		}
	}
	if _, exists, err := api.lstatOptional(ctx, renameRefPath(old)); err != nil || exists {
		return errors.Join(err, errors.New("old workspace branch still present"))
	}
	config, _, err := renameSafeFile(ctx, api, "/workspace/.git/config", 32<<10)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(config)
	if hex.EncodeToString(sum[:]) != plan.ConfigAfterSHA256 {
		return errors.New("renamed workspace config changed")
	}
	// A missing source reflog is required even when logging was disabled. If a
	// runtime-local backup still exists, its exact content also proves that a
	// present new reflog retained the old history.
	if _, exists, err := api.lstatOptional(ctx, renameLogPath(old)); err != nil || exists {
		return errors.Join(err, errors.New("old branch reflog still present"))
	}
	if _, exists, err := api.lstatOptional(ctx, renameBackupPath(opID)); err != nil {
		return err
	} else if exists {
		backup, _, _, err := readRenameBackup(ctx, api, opID, old, next)
		if err != nil {
			return err
		}
		if backup.HasLog {
			got, _, err := renameSafeFile(ctx, api, renameLogPath(next), 32<<10)
			if err != nil || !bytes.Equal(got, backup.Log) {
				return errors.Join(err, errors.New("renamed branch reflog changed"))
			}
		} else if _, exists, err := api.lstatOptional(ctx, renameLogPath(next)); err != nil || exists {
			return errors.Join(err, errors.New("unexpected destination branch reflog"))
		}
	}
	return nil
}

func (b *Backend) RemoveWorkspaceRenameBackup(ctx context.Context, source Session, incusUUID, generation, opID string) error {
	api, err := b.renameSourceAPI(ctx, source, incusUUID, generation)
	if err != nil {
		return err
	}
	name := renameBackupPath(opID)
	meta, exists, err := api.lstatOptional(ctx, name)
	if err != nil || exists && (meta.typ != "file" || meta.uid != 1000 || meta.gid != 1000 || meta.mode != 0600) {
		return errors.Join(err, errors.New("rename backup identity changed"))
	}
	if !exists {
		return nil
	}
	if err := api.deleteTree(ctx, name); err != nil {
		return err
	}
	if _, exists, err := api.lstatOptional(ctx, name); err != nil || exists {
		return errors.Join(err, errors.New("rename backup removal unresolved"))
	}
	return nil
}
