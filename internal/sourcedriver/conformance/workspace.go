package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// The workspace cases rename a session's clone held as fixture files, the
// way core reads and writes it through the runtime driver: by default a
// stopped session's clone on branch main at cloneOID, whose upstream is
// origin's main, as the session user's Git leaves it.
const (
	renameID      = "550e8400-e29b-41d4-a716-446655440001"
	cloneUser     = 1000
	cloneOID      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cloneMovedOID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// cloneSecret is in the clone's origin URL; no diagnostic may repeat
	// it.
	cloneSecret = "secret@private.invalid"
)

// cloneConfig is a clone's Git config whose branch tracks origin's branch
// of the same name.
func cloneConfig(branch string) string {
	return "[core]\nrepositoryformatversion = 0\nbare = false\n" +
		"[remote \"origin\"]\nurl = ssh://" + cloneSecret + "/app\nfetch = +refs/heads/*:refs/remotes/origin/*\n" +
		"[branch \"" + branch + "\"]\nremote = origin\nmerge = refs/heads/" + branch + "\n"
}

func cloneRef(branch string) string { return ".git/refs/heads/" + branch }
func cloneLog(branch string) string { return ".git/logs/refs/heads/" + branch }

// cloneFiles is a session's clone as the runtime driver reads it: each
// path, relative to the workspace root, with its file. A path not in it is
// absent.
type cloneFiles map[string]sourcedriver.File

// newClone is a clone on branch at cloneOID, with a reflog, and a file of
// the session's own beside the Git directory.
func newClone(branch string) cloneFiles {
	c := cloneFiles{}
	for _, d := range []string{".", ".git", ".git/refs", ".git/refs/heads", ".git/logs", ".git/logs/refs", ".git/logs/refs/heads"} {
		c.dir(d)
	}
	c.parents(branch)
	c.put(".git/HEAD", "ref: refs/heads/"+branch+"\n")
	c.put(".git/config", cloneConfig(branch))
	c.put(cloneRef(branch), cloneOID+"\n")
	c.put(cloneLog(branch), "history\n")
	c.put("notes.txt", "the session's own work")
	return c
}

// parents makes the directories that branch's ref and reflog sit in.
func (c cloneFiles) parents(branch string) {
	for dir := path.Dir(branch); dir != "."; dir = path.Dir(dir) {
		c.dir(".git/refs/heads/" + dir)
		c.dir(".git/logs/refs/heads/" + dir)
	}
}

// dir makes p a directory of the session user.
func (c cloneFiles) dir(p string) {
	c[p] = sourcedriver.File{Type: sourcedriver.FileDirectory, UID: cloneUser, GID: cloneUser, Mode: 0o755}
}

// put makes p a regular file of the session user, as the session's Git
// writes it.
func (c cloneFiles) put(p, data string) {
	c[p] = cloneFile(data, 0o644)
}

// change sets p's owner and mode, keeping its type and bytes.
func (c cloneFiles) change(p string, uid int, mode uint32) {
	f := c[p]
	f.UID, f.GID, f.Mode = uid, uid, mode
	c[p] = f
}

func cloneFile(data string, mode uint32) sourcedriver.File {
	return sourcedriver.File{Type: sourcedriver.FileRegular, UID: cloneUser, GID: cloneUser, Mode: mode, Data: []byte(data)}
}

var cloneAbsent = sourcedriver.File{Type: sourcedriver.FileAbsent}

// read reads p within bound as the runtime driver does: it never follows a
// symlink, so one above p fails the read; a file above p leaves p absent;
// and a file larger than bound fails the read.
func (c cloneFiles) read(p string, bound int64) (sourcedriver.File, error) {
	if p != "." {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			above, ok := c[strings.Join(parts[:i], "/")]
			switch {
			case !ok:
				return cloneAbsent, nil
			case above.Type == sourcedriver.FileSymlink:
				return sourcedriver.File{}, fmt.Errorf("reading %s: a symlink is above it", p)
			case above.Type != sourcedriver.FileDirectory:
				return cloneAbsent, nil
			}
		}
	}
	f, ok := c[p]
	if !ok {
		return cloneAbsent, nil
	}
	if int64(len(f.Data)) > bound {
		return sourcedriver.File{}, fmt.Errorf("reading %s: it holds %d bytes, more than its bound of %d", p, len(f.Data), bound)
	}
	return f, nil
}

// inClone reports whether p is the workspace root or a clean path in its
// Git directory, the only paths RenameWorkspace may ask for.
func inClone(p string) bool {
	return p == "." || path.Clean(p) == p && (p == ".git" || strings.HasPrefix(p, ".git/"))
}

// maxCloneRounds bounds the calls of one answer, so that a driver that
// never stops asking fails its case instead of hanging the run.
const maxCloneRounds = 32

// call is the driver's answer to r on the clone, with every read it asks
// for answered from c, the way core answers through the runtime driver. A
// read that fails ends the call with the read's error, as it ends the
// rename. It fails the case when the driver asks for a path outside the
// workspace root and its Git directory, or again for one it was given, or
// answers in a shape the contract forbids.
func (c cloneFiles) call(t *testing.T, f Fixture, r sourcedriver.WorkspaceRename) (sourcedriver.WorkspaceEdits, error) {
	t.Helper()
	var given []sourcedriver.WorkspaceFile
	asked := map[string]bool{}
	for round := 1; ; round++ {
		answer, err := f.Driver().RenameWorkspace(t.Context(), r, given)
		if err != nil {
			return answer, err
		}
		if len(answer.Read) == 0 {
			checkCloneAnswer(t, r, answer)
			return answer, nil
		}
		if round == maxCloneRounds {
			t.Fatalf("RenameWorkspace still asks for reads after %d rounds", round)
		}
		if len(answer.Edits) != 0 || answer.Plan != "" {
			t.Fatalf("RenameWorkspace asked for reads with the edits %s and the plan %q, want neither", describeEdits(answer.Edits), answer.Plan)
		}
		for _, p := range slices.Sorted(maps.Keys(answer.Read)) {
			if !inClone(p) {
				t.Fatalf("RenameWorkspace asked for %q, outside the workspace root and its Git directory", p)
			}
			if asked[p] {
				t.Fatalf("RenameWorkspace asked again for %q, which it was given", p)
			}
			asked[p] = true
			file, err := c.read(p, answer.Read[p])
			if err != nil {
				return sourcedriver.WorkspaceEdits{}, err
			}
			given = append(given, sourcedriver.WorkspaceFile{Path: p, File: file})
		}
	}
}

// checkCloneAnswer fails the case when an answer's edits or plan break the
// contract: an edit outside the Git directory, an edit that is neither a
// regular file of the session user nor a removal, or a plan longer than
// MaxPlan, or missing toward prepared or renamed.
func checkCloneAnswer(t *testing.T, r sourcedriver.WorkspaceRename, answer sourcedriver.WorkspaceEdits) {
	t.Helper()
	for _, e := range answer.Edits {
		if path.Clean(e.Path) != e.Path || !strings.HasPrefix(e.Path, ".git/") {
			t.Fatalf("RenameWorkspace edits %q, outside the Git directory", e.Path)
		}
		switch e.File.Type {
		case sourcedriver.FileAbsent:
		case sourcedriver.FileRegular:
			if e.File.UID != r.User || e.File.GID != r.User {
				t.Fatalf("RenameWorkspace writes %s owned by %d:%d, want the session user %d", e.Path, e.File.UID, e.File.GID, r.User)
			}
		default:
			t.Fatalf("RenameWorkspace writes a %s at %s, want a regular file or a removal", e.File.Type, e.Path)
		}
	}
	if len(answer.Plan) > sourcedriver.MaxPlan || answer.Plan == "" && r.Goal != sourcedriver.GoalDone {
		t.Fatalf("RenameWorkspace toward %s returned the plan %q, want one of 1 to %d bytes", r.Goal, answer.Plan, sourcedriver.MaxPlan)
	}
}

// apply applies edits in order, as core does with the runtime driver: a
// write needs its parent directory and replaces only a regular file, and a
// removal removes a regular file.
func (c cloneFiles) apply(t *testing.T, edits []sourcedriver.WorkspaceFile) {
	t.Helper()
	for _, e := range edits {
		if parent := c[path.Dir(e.Path)]; parent.Type != sourcedriver.FileDirectory {
			t.Fatalf("the edit of %s has no parent directory", e.Path)
		}
		old, exists := c[e.Path]
		if exists && old.Type != sourcedriver.FileRegular {
			t.Fatalf("the edit of %s replaces a %s", e.Path, old.Type)
		}
		if e.File.Type != sourcedriver.FileAbsent {
			c[e.Path] = e.File
		} else if !exists {
			t.Fatalf("the edit of %s removes a path that is absent", e.Path)
		} else {
			delete(c, e.Path)
		}
	}
}

// reach takes the clone to r's goal: it applies the driver's edits, then
// asks again and fails the case unless no edits are left and the plan is
// the same. It returns the plan and the edits it applied, or the refusal.
func (c cloneFiles) reach(t *testing.T, f Fixture, r sourcedriver.WorkspaceRename) (string, []sourcedriver.WorkspaceFile, error) {
	t.Helper()
	answer, err := c.call(t, f, r)
	if err != nil {
		return "", nil, err
	}
	c.apply(t, answer.Edits)
	again, err := c.call(t, f, r)
	if err != nil || len(again.Edits) != 0 || again.Plan != answer.Plan {
		t.Fatalf("once its edits are applied, RenameWorkspace toward %s = %s, %v; want no edits and the plan %q", r.Goal, describeAnswer(again), err, answer.Plan)
	}
	return answer.Plan, answer.Edits, nil
}

// mustReach is reach toward a goal the clone must reach.
func (c cloneFiles) mustReach(t *testing.T, f Fixture, r sourcedriver.WorkspaceRename) (string, []sourcedriver.WorkspaceFile) {
	t.Helper()
	plan, edits, err := c.reach(t, f, r)
	if err != nil {
		t.Fatalf("RenameWorkspace toward %s: %v", r.Goal, err)
	}
	return plan, edits
}

// refused fails the case unless the driver refuses r on the clone, and
// returns the refusal.
func (c cloneFiles) refused(t *testing.T, f Fixture, r sourcedriver.WorkspaceRename) error {
	t.Helper()
	answer, err := c.call(t, f, r)
	if err == nil {
		t.Fatalf("RenameWorkspace toward %s = %s, want a refusal", r.Goal, describeAnswer(answer))
	}
	return err
}

// renaming is a call toward goal of the rename renameID of the clone's
// branch old to next.
func renaming(old, next string, goal sourcedriver.Goal) sourcedriver.WorkspaceRename {
	return sourcedriver.WorkspaceRename{ID: renameID, Old: old, New: next, Goal: goal, User: cloneUser}
}

// toward is a call toward goal of the rename of main to renamed.
func toward(goal sourcedriver.Goal) sourcedriver.WorkspaceRename {
	return renaming("main", "renamed", goal)
}

// planned is toward, passing the plan an earlier call returned.
func planned(goal sourcedriver.Goal, plan string) sourcedriver.WorkspaceRename {
	r := toward(goal)
	r.Plan = plan
	return r
}

var cloneGoals = []sourcedriver.Goal{sourcedriver.GoalPrepared, sourcedriver.GoalRenamed, sourcedriver.GoalDone}

// sameCloneFile reports whether two files are the same: of one type and,
// unless absent, with the same owner, mode and bytes.
func sameCloneFile(a, b sourcedriver.File) bool {
	return a.Type == b.Type && (a.Type == sourcedriver.FileAbsent ||
		a.UID == b.UID && a.GID == b.GID && a.Mode == b.Mode && bytes.Equal(a.Data, b.Data))
}

// sameEdits reports whether two lists of edits are the same, edit by edit.
func sameEdits(a, b []sourcedriver.WorkspaceFile) bool {
	return slices.EqualFunc(a, b, func(x, y sourcedriver.WorkspaceFile) bool { return x.Path == y.Path && sameCloneFile(x.File, y.File) })
}

// wantClone fails the case at each path where got and want differ.
func wantClone(t *testing.T, got, want cloneFiles) {
	t.Helper()
	paths := maps.Collect(maps.All(got))
	maps.Copy(paths, want)
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		g, ok := got[p]
		if !ok {
			g = cloneAbsent
		}
		w, ok := want[p]
		if !ok {
			w = cloneAbsent
		}
		if !sameCloneFile(g, w) {
			t.Errorf("the clone holds %s at %s, want %s", describeFile(g), p, describeFile(w))
		}
	}
}

// describeAnswer is an answer as a failing case reports it.
func describeAnswer(answer sourcedriver.WorkspaceEdits) string {
	if len(answer.Read) != 0 {
		return fmt.Sprintf("reads of %v", slices.Sorted(maps.Keys(answer.Read)))
	}
	return fmt.Sprintf("edits %s with the plan %q", describeEdits(answer.Edits), answer.Plan)
}

// describeEdits is a list of edits as a failing case reports it.
func describeEdits(edits []sourcedriver.WorkspaceFile) string {
	var out []string
	for _, e := range edits {
		if e.File.Type == sourcedriver.FileAbsent {
			out = append(out, "remove "+e.Path)
		} else {
			out = append(out, "write "+e.Path+" "+describeFile(e.File))
		}
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// describeFile is a file as a failing case reports it, its bytes cut
// short.
func describeFile(f sourcedriver.File) string {
	if f.Type == sourcedriver.FileAbsent {
		return "nothing"
	}
	data := f.Data
	if len(data) > 48 {
		data = append(slices.Clip(data[:48]), "..."...)
	}
	return fmt.Sprintf("a %s of %d:%d, %#o, %q", f.Type, f.UID, f.GID, f.Mode, data)
}

// cloneBackup is the path of the backup that preparing a copy of c writes.
func cloneBackup(t *testing.T, f Fixture, c cloneFiles) string {
	t.Helper()
	answer, err := maps.Clone(c).call(t, f, toward(sourcedriver.GoalPrepared))
	if err != nil || len(answer.Edits) != 1 {
		t.Fatalf("RenameWorkspace toward prepared = %s, %v; want one edit, the backup", describeAnswer(answer), err)
	}
	return answer.Edits[0].Path
}

var workspaceCases = []testCase{
	{"prepared, renamed, then done renames the clone and keeps its history", func(t *testing.T, f Fixture) {
		for _, tc := range []struct {
			name, old, next string
			change          func(cloneFiles)
			reflog          bool
		}{
			{"with a reflog", "main", "renamed", func(cloneFiles) {}, true},
			{"without a reflog", "main", "renamed", func(c cloneFiles) { delete(c, cloneLog("main")) }, false},
			{"without a logs directory", "main", "renamed", func(c cloneFiles) {
				for p := range c {
					if p == ".git/logs" || strings.HasPrefix(p, ".git/logs/") {
						delete(c, p)
					}
				}
			}, false},
			{"with nested branch names", "feature/login", "team/login-v2", func(c cloneFiles) { c.parents("team/login-v2") }, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := newClone(tc.old)
				tc.change(c)
				want := maps.Clone(c)
				delete(want, cloneRef(tc.old))
				delete(want, cloneLog(tc.old))
				want[".git/HEAD"] = cloneFile("ref: refs/heads/"+tc.next+"\n", 0o644)
				want[".git/config"] = cloneFile(cloneConfig(tc.next), 0o644)
				want[cloneRef(tc.next)] = cloneFile(cloneOID+"\n", 0o644)
				if tc.reflog {
					want[cloneLog(tc.next)] = cloneFile("history\n", 0o644)
				}
				for _, goal := range cloneGoals {
					c.mustReach(t, f, renaming(tc.old, tc.next, goal))
				}
				wantClone(t, c, want)
			})
		}
	}},
	{"each goal's edits come in order, as the session user's, with the modes the clone had", func(t *testing.T, f Fixture) {
		c := newClone("main")
		c.change(".git/HEAD", cloneUser, 0o640)
		c.change(".git/config", cloneUser, 0o600)
		_, prepared := c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
		if len(prepared) != 1 || prepared[0].File.Type != sourcedriver.FileRegular || prepared[0].File.Mode != 0o600 {
			t.Fatalf("toward prepared the edits are %s, want only the backup, a private file", describeEdits(prepared))
		}
		backup := prepared[0].Path
		if slices.Contains([]string{".git/HEAD", ".git/config", ".git/index", ".git/packed-refs"}, backup) ||
			strings.HasPrefix(backup, ".git/refs/") || strings.HasPrefix(backup, ".git/logs/") {
			t.Fatalf("the backup is written at %s, one of Git's own files", backup)
		}
		_, renamed := c.mustReach(t, f, toward(sourcedriver.GoalRenamed))
		want := []sourcedriver.WorkspaceFile{
			{Path: cloneRef("renamed"), File: cloneFile(cloneOID+"\n", 0o644)},
			{Path: ".git/HEAD", File: cloneFile("ref: refs/heads/renamed\n", 0o640)},
			{Path: ".git/config", File: cloneFile(cloneConfig("renamed"), 0o600)},
			{Path: cloneLog("renamed"), File: cloneFile("history\n", 0o644)},
			{Path: cloneLog("main"), File: cloneAbsent},
			{Path: cloneRef("main"), File: cloneAbsent},
		}
		if !sameEdits(renamed, want) {
			t.Fatalf("toward renamed the edits are %s, want %s", describeEdits(renamed), describeEdits(want))
		}
		if _, done := c.mustReach(t, f, toward(sourcedriver.GoalDone)); !sameEdits(done, []sourcedriver.WorkspaceFile{{Path: backup, File: cloneAbsent}}) {
			t.Fatalf("toward done the edits are %s, want only the removal of the backup %s", describeEdits(done), backup)
		}
	}},
	{"no edits once a goal holds, and the same Plan on every call", func(t *testing.T, f Fixture) {
		c := newClone("main")
		first, err := c.call(t, f, toward(sourcedriver.GoalPrepared))
		if err != nil {
			t.Fatal(err)
		}
		for _, goal := range cloneGoals {
			if plan, _ := c.mustReach(t, f, planned(goal, first.Plan)); plan != first.Plan {
				t.Fatalf("toward %s the plan is %q, want %q as on the first call", goal, plan, first.Plan)
			}
			if goal == sourcedriver.GoalDone {
				continue
			}
			if again, err := c.call(t, f, toward(goal)); err != nil || len(again.Edits) != 0 || again.Plan != first.Plan {
				t.Fatalf("toward %s, without the plan, = %s, %v; want no edits and the plan %q", goal, describeAnswer(again), err, first.Plan)
			}
		}
		// The clone stays renamed once the backup is gone.
		if again, err := c.call(t, f, planned(sourcedriver.GoalRenamed, first.Plan)); err != nil || len(again.Edits) != 0 || again.Plan != first.Plan {
			t.Fatalf("toward renamed after done = %s, %v; want no edits and the plan %q", describeAnswer(again), err, first.Plan)
		}
	}},
	{"after each edit toward renamed, the next call returns the edits still to make", func(t *testing.T, f Fixture) {
		c := newClone("main")
		c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
		all, err := c.call(t, f, toward(sourcedriver.GoalRenamed))
		if err != nil {
			t.Fatal(err)
		}
		for i := range all.Edits {
			c.apply(t, all.Edits[i:i+1])
			left, err := c.call(t, f, toward(sourcedriver.GoalRenamed))
			if err != nil || !sameEdits(left.Edits, all.Edits[i+1:]) || left.Plan != all.Plan {
				t.Fatalf("after %d edits RenameWorkspace = %s, %v; want the edits %s and the plan %q", i+1, describeAnswer(left), err, describeEdits(all.Edits[i+1:]), all.Plan)
			}
		}
	}},
	{"a clone on another branch or upstream is a WorkspaceMismatch naming only safe values", func(t *testing.T, f Fixture) {
		for _, tc := range []struct {
			name, head, config, field, expected, actual string
		}{
			{"another branch", "ref: refs/heads/other\n", cloneConfig("main"), "branch", "refs/heads/main", "refs/heads/other"},
			{"a detached HEAD", cloneOID + "\n", cloneConfig("main"), "branch", "refs/heads/main", "<detached> " + cloneOID},
			{"an unsafe branch name", "ref: refs/heads/" + cloneSecret + "\n", cloneConfig("main"), "branch", "refs/heads/main", "<unavailable>"},
			{"another upstream branch", "ref: refs/heads/main\n", strings.ReplaceAll(cloneConfig("main"), "merge = refs/heads/main", "merge = refs/heads/other"),
				"upstream", "origin:refs/heads/main", "origin:refs/heads/other"},
			{"another remote", "ref: refs/heads/main\n", strings.ReplaceAll(cloneConfig("main"), "origin", "other"),
				"upstream", "origin:refs/heads/main", "other:refs/heads/main"},
			{"no upstream", "ref: refs/heads/main\n", "[core]\nrepositoryformatversion = 0\nbare = false\n", "upstream", "origin:refs/heads/main", "<unset>"},
			{"an incomplete upstream", "ref: refs/heads/main\n", strings.ReplaceAll(cloneConfig("main"), "merge = refs/heads/main\n", ""),
				"upstream", "origin:refs/heads/main", "<incomplete>"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c := newClone("main")
				c.put(".git/HEAD", tc.head)
				c.put(".git/config", tc.config)
				err := c.refused(t, f, toward(sourcedriver.GoalPrepared))
				var mismatch *sourcedriver.WorkspaceMismatch
				if !errors.As(err, &mismatch) || *mismatch != (sourcedriver.WorkspaceMismatch{Field: tc.field, Expected: tc.expected, Actual: tc.actual}) {
					t.Fatalf("the refusal is %v, want a %s mismatch from %q to %q", err, tc.field, tc.expected, tc.actual)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private.invalid") || len(err.Error()) > 512 {
					t.Fatalf("the refusal %q repeats the clone's secrets or is too long", err)
				}
			})
		}
	}},
	{"an unsafe or unsupported clone is refused toward prepared, and not as a mismatch", func(t *testing.T, f Fixture) {
		config := cloneConfig("main")
		for name, change := range map[string]func(cloneFiles){
			"packed refs":                     func(c cloneFiles) { c.put(".git/packed-refs", "") },
			"a worktree":                      func(c cloneFiles) { c.dir(".git/worktrees") },
			"a common directory":              func(c cloneFiles) { c.put(".git/commondir", "") },
			"a worktree config":               func(c cloneFiles) { c.put(".git/config.worktree", "") },
			"an index lock":                   func(c cloneFiles) { c.put(".git/index.lock", "") },
			"a HEAD lock":                     func(c cloneFiles) { c.put(".git/HEAD.lock", "") },
			"a config lock":                   func(c cloneFiles) { c.put(".git/config.lock", "") },
			"a packed refs lock":              func(c cloneFiles) { c.put(".git/packed-refs.lock", "") },
			"a lock on the source ref":        func(c cloneFiles) { c.put(cloneRef("main")+".lock", "") },
			"a lock on the destination ref":   func(c cloneFiles) { c.put(cloneRef("renamed")+".lock", "") },
			"the destination ref":             func(c cloneFiles) { c.put(cloneRef("renamed"), cloneOID+"\n") },
			"an empty destination ref":        func(c cloneFiles) { c.put(cloneRef("renamed"), "") },
			"the destination reflog":          func(c cloneFiles) { c.put(cloneLog("renamed"), "") },
			"a HEAD of another user":          func(c cloneFiles) { c.change(".git/HEAD", 0, 0o644) },
			"a config others can write":       func(c cloneFiles) { c.change(".git/config", cloneUser, 0o666) },
			"a setuid source ref":             func(c cloneFiles) { c.change(cloneRef("main"), cloneUser, 0o4644) },
			"a source reflog of another user": func(c cloneFiles) { c.change(cloneLog("main"), 0, 0o644) },
			"a symlink for the source ref": func(c cloneFiles) {
				c[cloneRef("main")] = sourcedriver.File{Type: sourcedriver.FileSymlink, UID: cloneUser, GID: cloneUser, Mode: 0o777}
			},
			"a symlink for the Git directory": func(c cloneFiles) {
				c[".git"] = sourcedriver.File{Type: sourcedriver.FileSymlink, UID: cloneUser, GID: cloneUser, Mode: 0o777}
			},
			"a workspace others can write":      func(c cloneFiles) { c.change(".", cloneUser, 0o777) },
			"a workspace of another user":       func(c cloneFiles) { c.change(".", 0, 0o755) },
			"a heads directory of another user": func(c cloneFiles) { c.change(".git/refs/heads", 0, 0o755) },
			"a logs directory others can write": func(c cloneFiles) { c.change(".git/logs", cloneUser, 0o777) },
			"a logs root that is a file":        func(c cloneFiles) { c.put(".git/logs", "") },
			"a config beyond its bound":         func(c cloneFiles) { c.put(".git/config", config+strings.Repeat("# padding\n", 4<<10)) },
			"a config with CRLF line ends":      func(c cloneFiles) { c.put(".git/config", strings.ReplaceAll(config, "\n", "\r\n")) },
			"a bare repository":                 func(c cloneFiles) { c.put(".git/config", strings.Replace(config, "bare = false", "bare = true", 1)) },
			"an include":                        func(c cloneFiles) { c.put(".git/config", config+"[include]\npath = /tmp/evil\n") },
			"a filter":                          func(c cloneFiles) { c.put(".git/config", config+"[filter \"evil\"]\nclean = /tmp/evil\n") },
			"an fsmonitor":                      func(c cloneFiles) { c.put(".git/config", config+"[core]\nfsmonitor = /tmp/evil\n") },
			"an external diff":                  func(c cloneFiles) { c.put(".git/config", config+"[diff]\nexternal = /tmp/evil\n") },
			"a core worktree":                   func(c cloneFiles) { c.put(".git/config", config+"[core]\nworktree = /tmp/evil\n") },
			"the source branch's config twice": func(c cloneFiles) {
				c.put(".git/config", config+"[branch \"main\"]\nremote = origin\nmerge = refs/heads/main\n")
			},
			"the destination branch's config": func(c cloneFiles) {
				c.put(".git/config", config+"[branch \"renamed\"]\nremote = origin\nmerge = refs/heads/renamed\n")
			},
			"another branch's incomplete upstream": func(c cloneFiles) { c.put(".git/config", config+"[branch \"other\"]\nremote = origin\n") },
			"another branch's upstream on an unknown remote": func(c cloneFiles) {
				c.put(".git/config", config+"[branch \"other\"]\nremote = missing\nmerge = refs/heads/other\n")
			},
		} {
			t.Run(name, func(t *testing.T) {
				c := newClone("main")
				change(c)
				err := c.refused(t, f, toward(sourcedriver.GoalPrepared))
				if mismatch := (*sourcedriver.WorkspaceMismatch)(nil); errors.As(err, &mismatch) {
					t.Fatalf("the refusal is a mismatch, %v; want another refusal", err)
				}
			})
		}
	}},
	{"toward renamed, a file neither as the backup holds it nor as the goal needs it is refused", func(t *testing.T, f Fixture) {
		for name, tc := range map[string]struct{ before, after func(cloneFiles) }{
			"HEAD on another branch":                {after: func(c cloneFiles) { c.put(".git/HEAD", "ref: refs/heads/other\n") }},
			"a HEAD of another user":                {after: func(c cloneFiles) { c.change(".git/HEAD", 0, 0o644) }},
			"a moved source ref":                    {after: func(c cloneFiles) { c.put(cloneRef("main"), cloneMovedOID+"\n") }},
			"a destination ref elsewhere":           {after: func(c cloneFiles) { c.put(cloneRef("renamed"), cloneMovedOID+"\n") }},
			"a changed config":                      {after: func(c cloneFiles) { c.put(".git/config", cloneConfig("main")+"# changed\n") }},
			"a changed source reflog":               {after: func(c cloneFiles) { c.put(cloneLog("main"), "other history\n") }},
			"a destination reflog of other history": {after: func(c cloneFiles) { c.put(cloneLog("renamed"), "other history\n") }},
			"a source reflog that appeared": {
				before: func(c cloneFiles) { delete(c, cloneLog("main")) },
				after:  func(c cloneFiles) { c.put(cloneLog("main"), "history\n") },
			},
			"a destination reflog that appeared": {
				before: func(c cloneFiles) { delete(c, cloneLog("main")) },
				after:  func(c cloneFiles) { c.put(cloneLog("renamed"), "history\n") },
			},
		} {
			t.Run(name, func(t *testing.T) {
				c := newClone("main")
				if tc.before != nil {
					tc.before(c)
				}
				c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
				tc.after(c)
				c.refused(t, f, toward(sourcedriver.GoalRenamed))
			})
		}
	}},
	{"a source changed after prepared is refused, and so is another plan than the one passed", func(t *testing.T, f Fixture) {
		c := newClone("main")
		plan, _ := c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
		c.put(cloneRef("main"), cloneMovedOID+"\n")
		c.refused(t, f, planned(sourcedriver.GoalPrepared, plan))
		c.refused(t, f, planned(sourcedriver.GoalRenamed, plan))
		// The rename rolls back; a backup made again of the changed clone
		// fixes another plan than the one recorded.
		c.mustReach(t, f, planned(sourcedriver.GoalDone, plan))
		c.refused(t, f, planned(sourcedriver.GoalPrepared, plan))
		if answer, err := c.call(t, f, toward(sourcedriver.GoalPrepared)); err != nil || len(answer.Edits) != 1 || answer.Plan == plan {
			t.Fatalf("a new rename of the changed clone = %s, %v; want its backup and another plan than %q", describeAnswer(answer), err, plan)
		}
	}},
	{"done removes only the backup, from a clone in any state", func(t *testing.T, f Fixture) {
		for name, state := range map[string]func(*testing.T, cloneFiles){
			"prepared": func(*testing.T, cloneFiles) {},
			"partly renamed": func(t *testing.T, c cloneFiles) {
				answer, err := c.call(t, f, toward(sourcedriver.GoalRenamed))
				if err != nil || len(answer.Edits) < 3 {
					t.Fatalf("toward renamed = %s, %v; want its edits", describeAnswer(answer), err)
				}
				c.apply(t, answer.Edits[:3])
			},
			"renamed": func(t *testing.T, c cloneFiles) { c.mustReach(t, f, toward(sourcedriver.GoalRenamed)) },
			"changed outside the rename": func(t *testing.T, c cloneFiles) {
				c.put(".git/HEAD", cloneMovedOID+"\n")
				c.put(".git/config", "not a Git config")
				delete(c, cloneRef("main"))
				c.put(".git/index.lock", "")
			},
		} {
			t.Run(name, func(t *testing.T) {
				c := newClone("main")
				plan, prepared := c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
				backup := prepared[0].Path
				state(t, c)
				want := maps.Clone(c)
				delete(want, backup)
				if _, done := c.mustReach(t, f, planned(sourcedriver.GoalDone, plan)); !sameEdits(done, []sourcedriver.WorkspaceFile{{Path: backup, File: cloneAbsent}}) {
					t.Fatalf("toward done the edits are %s, want only the removal of the backup %s", describeEdits(done), backup)
				}
				wantClone(t, c, want)
			})
		}
		t.Run("never prepared", func(t *testing.T) {
			if _, done := newClone("main").mustReach(t, f, toward(sourcedriver.GoalDone)); len(done) != 0 {
				t.Fatalf("toward done a clone without a backup gets the edits %s, want none", describeEdits(done))
			}
		})
	}},
	{"a backup that is not private to the session user is refused toward every goal", func(t *testing.T, f Fixture) {
		for name, change := range map[string]func(cloneFiles, string){
			"readable by others": func(c cloneFiles, backup string) { c.change(backup, cloneUser, 0o644) },
			"of another user":    func(c cloneFiles, backup string) { c.change(backup, 0, 0o600) },
			"a directory":        func(c cloneFiles, backup string) { c.dir(backup); c.change(backup, cloneUser, 0o700) },
		} {
			t.Run(name, func(t *testing.T) {
				for _, goal := range cloneGoals {
					c := newClone("main")
					_, prepared := c.mustReach(t, f, toward(sourcedriver.GoalPrepared))
					change(c, prepared[0].Path)
					c.refused(t, f, toward(goal))
				}
			})
		}
	}},
	{"a backup the rename did not write is refused toward prepared and renamed", func(t *testing.T, f Fixture) {
		for _, goal := range []sourcedriver.Goal{sourcedriver.GoalPrepared, sourcedriver.GoalRenamed} {
			c := newClone("main")
			c[cloneBackup(t, f, c)] = cloneFile("not a backup", 0o600)
			c.refused(t, f, toward(goal))
		}
	}},
	{"renamed without a backup holds only by the plan passed", func(t *testing.T, f Fixture) {
		moved := newClone("main")
		moved.put(cloneRef("main"), cloneMovedOID+"\n")
		other, err := moved.call(t, f, toward(sourcedriver.GoalPrepared))
		if err != nil {
			t.Fatal(err)
		}
		c := newClone("main")
		c.refused(t, f, toward(sourcedriver.GoalRenamed))
		var plan string
		for _, goal := range cloneGoals {
			plan, _ = c.mustReach(t, f, planned(goal, plan))
		}
		c.refused(t, f, toward(sourcedriver.GoalRenamed))
		c.refused(t, f, planned(sourcedriver.GoalRenamed, other.Plan))
		for name, change := range map[string]func(cloneFiles){
			"HEAD back on the source branch": func(c cloneFiles) { c.put(".git/HEAD", "ref: refs/heads/main\n") },
			"a moved destination ref":        func(c cloneFiles) { c.put(cloneRef("renamed"), cloneMovedOID+"\n") },
			"the source ref back":            func(c cloneFiles) { c.put(cloneRef("main"), cloneOID+"\n") },
			"a changed config":               func(c cloneFiles) { c.put(".git/config", cloneConfig("renamed")+"# changed\n") },
			"the source reflog back":         func(c cloneFiles) { c.put(cloneLog("main"), "history\n") },
		} {
			t.Run(name, func(t *testing.T) {
				changed := maps.Clone(c)
				change(changed)
				changed.refused(t, f, planned(sourcedriver.GoalRenamed, plan))
			})
		}
	}},
	{"a file given beyond the bound asked for, or a path given twice, is refused", func(t *testing.T, f Fixture) {
		// A caller that reads without the bounds: the source reflog is
		// larger than a reflog the rename copies may be.
		c := newClone("main")
		c.put(cloneLog("main"), strings.Repeat("history\n", 5<<10))
		r := toward(sourcedriver.GoalPrepared)
		var given []sourcedriver.WorkspaceFile
		for round := 0; ; round++ {
			answer, err := f.Driver().RenameWorkspace(t.Context(), r, given)
			if err != nil {
				break
			}
			if len(answer.Read) == 0 || round == maxCloneRounds {
				t.Fatalf("RenameWorkspace given a reflog of %d bytes = %s, want a refusal", len(c[cloneLog("main")].Data), describeAnswer(answer))
			}
			for _, p := range slices.Sorted(maps.Keys(answer.Read)) {
				file, err := c.read(p, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				given = append(given, sourcedriver.WorkspaceFile{Path: p, File: file})
			}
			if round == 0 {
				if answer, err := f.Driver().RenameWorkspace(t.Context(), r, append(slices.Clip(given), given[0])); err == nil {
					t.Fatalf("RenameWorkspace given %s twice = %s, want a refusal", given[0].Path, describeAnswer(answer))
				}
			}
		}
	}},
	{"nothing is asked for or edited outside the workspace root and its Git directory", func(t *testing.T, f Fixture) {
		c := newClone("main")
		c.put(".gitignore", "*.log\n")
		c.put(".git-rewrite", "")
		c.dir("src")
		c.dir("src/.git")
		c.put("src/.git/HEAD", "ref: refs/heads/main\n")
		outside := func(c cloneFiles) cloneFiles {
			out := cloneFiles{}
			for p, file := range c {
				if !inClone(p) {
					out[p] = file
				}
			}
			return out
		}
		before := outside(c)
		// call fails the case at the first path asked for or edited outside.
		for _, goal := range cloneGoals {
			c.mustReach(t, f, toward(goal))
		}
		wantClone(t, outside(c), before)
	}},
	{"a name Git refuses is ErrInvalid, and a malformed rename refused, before any read", func(t *testing.T, f Fixture) {
		upper := toward(sourcedriver.GoalPrepared)
		upper.ID = strings.ToUpper(renameID)
		pathID := toward(sourcedriver.GoalPrepared)
		pathID.ID = "../config"
		goal := toward("elsewhere")
		plan := planned(sourcedriver.GoalPrepared, "not a plan")
		user := toward(sourcedriver.GoalPrepared)
		user.User = -1
		for name, tc := range map[string]struct {
			r       sourcedriver.WorkspaceRename
			invalid bool
		}{
			"an old branch Git refuses":            {renaming("a..b", "renamed", sourcedriver.GoalPrepared), true},
			"a new branch a rename may not create": {renaming("main", "-renamed", sourcedriver.GoalPrepared), true},
			"an empty new branch":                  {renaming("main", "", sourcedriver.GoalDone), true},
			"an uppercase rename ID":               {upper, false},
			"a rename ID that is a path":           {pathID, false},
			"an unknown goal":                      {goal, false},
			"a plan no driver returned":            {plan, false},
			"a negative user":                      {user, false},
		} {
			t.Run(name, func(t *testing.T) {
				answer, err := f.Driver().RenameWorkspace(t.Context(), tc.r, nil)
				if err == nil || tc.invalid && !errors.Is(err, sourcedriver.ErrInvalid) {
					t.Fatalf("RenameWorkspace = %s, %v; want a refusal before any read (ErrInvalid: %v)", describeAnswer(answer), err, tc.invalid)
				}
			})
		}
	}},
	{"names outside the clone's closed layout are refused", func(t *testing.T, f Fixture) {
		long := strings.Repeat("x", 101)
		for name, r := range map[string]sourcedriver.WorkspaceRename{
			"the same branch":                      renaming("main", "main", sourcedriver.GoalPrepared),
			"an old branch of more than 100 bytes": renaming(long, "renamed", sourcedriver.GoalPrepared),
			"an old branch led by an underscore":   renaming("_main", "renamed", sourcedriver.GoalPrepared),
			"a new branch led by an underscore":    renaming("main", "_renamed", sourcedriver.GoalPrepared),
		} {
			t.Run(name, func(t *testing.T) {
				newClone(r.Old).refused(t, f, r)
			})
		}
	}},
}
