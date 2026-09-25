package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var safeGitToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// safeGitConfig accepts only inert facts needed to interpret an ordinary
// standalone worktree. The original file is never passed to Git. Includes,
// extensions, filters, monitors, external diff and credential helpers are
// unsupported regardless of where they appear.
func safeGitConfig(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 32<<10 || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
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
			// Identity is intentionally omitted from the helper.
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
			// URLs are deliberately omitted from the no-NIC helper.
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
			return nil, errors.New("Git upstream setting incomplete")
		}
		if remoteFetch[values["remote"]] == "" {
			return nil, errors.New("Git upstream fetch mapping unavailable")
		}
		fmt.Fprintf(&out, "[branch %q]\n\tremote = %s\n\tmerge = %s\n", name, values["remote"], values["merge"])
	}
	return []byte(out.String()), nil
}

func sanitizeWorkspaceSnapshot(snapshot WorkspaceSnapshot) (WorkspaceSnapshot, error) {
	return sanitizeWorkspaceMain(snapshot, false)
}

func sanitizeWorkspaceMain(snapshot WorkspaceSnapshot, allowLinked bool) (WorkspaceSnapshot, error) {
	if len(snapshot.Entries) < 2 || len(snapshot.Entries) > workspaceMaxEntries {
		return WorkspaceSnapshot{}, errors.New("workspace snapshot incomplete")
	}
	out := WorkspaceSnapshot{Entries: make([]WorkspaceEntry, len(snapshot.Entries))}
	copy(out.Entries, snapshot.Entries)
	var configFound, headFound bool
	for i, entry := range out.Entries {
		if entry.Path == ".git/config" {
			if entry.Type != "file" || configFound {
				return WorkspaceSnapshot{}, errors.New("Git config unavailable")
			}
			configFound = true
			data, err := safeGitConfig(entry.Data)
			if err != nil {
				return WorkspaceSnapshot{}, err
			}
			out.Entries[i].Data = data
			out.Entries[i].Mode = 0644
		}
		if entry.Path == ".git/HEAD" {
			headFound = entry.Type == "file"
		}
		if entry.Path == ".gitattributes" || entry.Path == ".git/info/attributes" || strings.HasSuffix(entry.Path, "/.gitattributes") {
			if entry.Type != "file" || len(bytes.TrimSpace(entry.Data)) != 0 {
				return WorkspaceSnapshot{}, errors.New("Git attributes semantics unavailable")
			}
		}
		if strings.HasPrefix(entry.Path, ".git/") && entry.Type == "symlink" {
			return WorkspaceSnapshot{}, errors.New("Git metadata symlink unavailable")
		}
		if entry.Path == ".git/index.lock" || entry.Path == ".git/shallow" || entry.Path == ".git/objects/info/alternates" || entry.Path == ".git/info/grafts" || entry.Path == ".git/commondir" || !allowLinked && entry.Path == ".git/worktrees" {
			return WorkspaceSnapshot{}, errors.New("Git worktree state unsupported")
		}
	}
	if !configFound || !headFound {
		return WorkspaceSnapshot{}, errors.New("Git metadata incomplete")
	}
	return out, nil
}

func sanitizeWorkspaceLinked(snapshot WorkspaceSnapshot, link workspaceLinkedTree) (WorkspaceSnapshot, error) {
	if len(snapshot.Entries) < 2 || len(snapshot.Entries) > workspaceMaxEntries || snapshot.Entries[0].Path != "" {
		return WorkspaceSnapshot{}, errors.New("linked workspace snapshot incomplete")
	}
	clean := WorkspaceSnapshot{Entries: make([]WorkspaceEntry, len(snapshot.Entries))}
	copy(clean.Entries, snapshot.Entries)
	found := false
	for _, entry := range clean.Entries[1:] {
		if entry.Path == ".git" {
			if found || verifyLinkedGitFile(link, entry) != nil {
				return WorkspaceSnapshot{}, errors.New("linked Git pointer changed")
			}
			found = true
		} else if strings.HasSuffix(entry.Path, "/.git") || strings.HasPrefix(entry.Path, ".git/") || entry.Path == ".gitattributes" || strings.HasSuffix(entry.Path, "/.gitattributes") {
			if entry.Path == ".gitattributes" || strings.HasSuffix(entry.Path, "/.gitattributes") {
				if entry.Type != "file" || len(bytes.TrimSpace(entry.Data)) != 0 {
					return WorkspaceSnapshot{}, errors.New("linked Git attributes semantics unavailable")
				}
			} else {
				return WorkspaceSnapshot{}, errors.New("nested Git metadata in linked tree unavailable")
			}
		}
	}
	if !found {
		return WorkspaceSnapshot{}, errors.New("linked Git pointer missing")
	}
	return clean, nil
}

func (b *Backend) InstallWorkspaceHelperCopy(ctx context.Context, helper Session, snapshot WorkspaceSnapshot) error {
	clean, err := sanitizeWorkspaceSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := b.verifyWorkspaceHelperBoot(ctx, helper); err != nil {
		return err
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(helper)}
	root, err := api.lstat(ctx, "/workspace")
	if err != nil || root.typ != "directory" || root.uid != 1000 || root.gid != 1000 || root.mode&0777 != 0755 {
		return errors.Join(err, errors.New("helper workspace root unsafe"))
	}
	names, err := api.listDirectory(ctx, "/workspace", 0)
	if err != nil || len(names) != 0 {
		return errors.Join(err, errors.New("helper workspace not empty"))
	}
	if err := installWorkspaceEntries(ctx, api, "/workspace", clean); err != nil {
		return err
	}
	return b.verifyWorkspaceHelperBoot(ctx, helper)
}

// The linked-tree path is accepted only after the quiescent source export has
// checked both Git pointers and every root ancestor. The copy stays under the
// same closed paths in a no-NIC helper; the original common config is replaced
// with inert fields before any Git command runs.
func (b *Backend) InstallWorkspaceLossCopy(ctx context.Context, helper Session, loss WorkspaceLossSnapshot) error {
	links, err := parseWorkspaceLinkedTrees(loss.Main)
	if err != nil || len(links) != len(loss.Linked) {
		return errors.Join(err, errors.New("loss worktree inventory changed"))
	}
	if err := verifyMainLinkedGitFiles(loss.Main, links); err != nil {
		return err
	}
	if err := boundWorkspaceLossSnapshot(loss); err != nil {
		return err
	}
	main, err := sanitizeWorkspaceMain(loss.Main, true)
	if err != nil {
		return err
	}
	cleanLinked := make([]WorkspaceSnapshot, len(loss.Linked))
	parentReady := false
	for i, linked := range loss.Linked {
		if linked.Link != links[i] {
			return errors.New("loss worktree order changed")
		}
		cleanLinked[i], err = sanitizeWorkspaceLinked(linked.Snapshot, linked.Link)
		if err != nil {
			return err
		}
	}
	if err := b.verifyWorkspaceHelperBoot(ctx, helper); err != nil {
		return err
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(helper)}
	root, err := api.lstat(ctx, "/workspace")
	if err != nil || root.typ != "directory" || root.uid != 1000 || root.gid != 1000 || root.mode&0777 != 0755 {
		return errors.Join(err, errors.New("helper workspace root unsafe"))
	}
	names, err := api.listDirectory(ctx, "/workspace", 0)
	if err != nil || len(names) != 0 {
		return errors.Join(err, errors.New("helper workspace not empty"))
	}
	if err := installWorkspaceEntries(ctx, api, "/workspace", main); err != nil {
		return err
	}
	for i, linked := range loss.Linked {
		if strings.HasPrefix(linked.Link.Root, "/workspace/") {
			continue // exact overlapping bytes were checked during source export
		}
		if !strings.HasPrefix(linked.Link.Root, "/home/p/worktrees/") {
			return errors.New("linked helper destination outside closed path")
		}
		for _, ancestor := range []string{"/home", "/home/p"} {
			f, err := api.lstat(ctx, ancestor)
			wantUID := 0
			if ancestor == "/home/p" {
				wantUID = 1000
			}
			if err != nil || f.typ != "directory" || f.uid != wantUID || f.mode&0022 != 0 {
				return errors.Join(err, errors.New("helper linked-tree ancestor unsafe"))
			}
		}
		if !parentReady {
			if _, exists, err := api.lstatOptional(ctx, "/home/p/worktrees"); err != nil || exists {
				return errors.Join(err, errors.New("helper linked-tree parent not empty"))
			}
			if err := api.put(ctx, "/home/p/worktrees", guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0700}); err != nil {
				return err
			}
			parentReady = true
		}
		parent, err := api.lstat(ctx, "/home/p/worktrees")
		if err != nil || parent.typ != "directory" || parent.uid != 1000 || parent.gid != 1000 || parent.mode != 0700 {
			return errors.Join(err, errors.New("helper linked-tree parent changed"))
		}
		if _, exists, err := api.lstatOptional(ctx, linked.Link.Root); err != nil || exists {
			return errors.Join(err, errors.New("helper linked-tree root already exists"))
		}
		if err := api.put(ctx, linked.Link.Root, guestFile{typ: "directory", uid: 1000, gid: 1000, mode: cleanLinked[i].Entries[0].Mode}); err != nil {
			return err
		}
		if err := installWorkspaceEntries(ctx, api, linked.Link.Root, cleanLinked[i]); err != nil {
			return err
		}
	}
	return b.verifyWorkspaceHelperBoot(ctx, helper)
}

func installWorkspaceEntries(ctx context.Context, api *unixFileAPI, rootPath string, clean WorkspaceSnapshot) error {
	// Install real directories/files first, symlinks last, so no later POST
	// can traverse a copied link.
	for _, typ := range []string{"directory", "file", "symlink"} {
		for _, entry := range clean.Entries[1:] {
			if entry.Type != typ {
				continue
			}
			full := path.Join(rootPath, entry.Path)
			if !strings.HasPrefix(full, rootPath+"/") {
				return errors.New("helper copy path escaped root")
			}
			file := guestFile{typ: entry.Type, uid: 1000, gid: 1000, mode: entry.Mode}
			if typ == "file" {
				file.data = entry.Data
			}
			if typ == "symlink" {
				file.data = []byte(entry.Target)
			}
			if err := api.put(ctx, full, file); err != nil {
				return err
			}
			actual, err := api.lstat(ctx, full)
			if err != nil || actual.typ != typ || typ != "symlink" && (actual.uid != 1000 || actual.gid != 1000 || actual.mode != entry.Mode) {
				return errors.Join(err, errors.New("helper copy metadata changed"))
			}
			if typ == "file" {
				got, found, err := api.getBounded(ctx, full, int64(len(entry.Data)))
				if err != nil || !found || !bytes.Equal(got.data, entry.Data) {
					return errors.Join(err, errors.New("helper copy bytes changed"))
				}
			} else if typ == "symlink" {
				got, err := api.readlinkBounded(ctx, full, 4096)
				if err != nil || string(got) != entry.Target {
					return errors.Join(err, errors.New("helper copy symlink changed"))
				}
			}
		}
	}
	return nil
}

type WorkspaceChange struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

type WorkspaceRef struct {
	Name     string `json:"name"`
	OID      string `json:"oid"`
	Upstream string `json:"upstream,omitempty"`
}

type WorkspaceAnalysis struct {
	Branch  string            `json:"branch"`
	HeadOID string            `json:"head_oid,omitempty"`
	Changes []WorkspaceChange `json:"changes"`
	Refs    []WorkspaceRef    `json:"refs"`
}

func (b *Backend) helperGit(ctx context.Context, helper Session, args ...string) ([]byte, error) {
	return b.helperGitAt(ctx, helper, "/workspace", args...)
}

func (b *Backend) helperGitAt(ctx context.Context, helper Session, cwd string, args ...string) ([]byte, error) {
	if err := b.verifyWorkspaceHelperBoot(ctx, helper); err != nil {
		return nil, err
	}
	if len(args) == 0 || !supportedWorkspaceTreeRoot(cwd) {
		return nil, errors.New("missing fixed Git operation")
	}
	argv := []string{"exec", b.name(helper), "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--cwd", cwd,
		"--env", "HOME=/var/empty", "--env", "XDG_CONFIG_HOME=/var/empty", "--env", "USER=p", "--env", "LOGNAME=p",
		"--env", "PATH=/run/current-system/sw/bin", "--env", "LANG=C", "--env", "LC_ALL=C",
		"--env", "GIT_CONFIG_NOSYSTEM=1", "--env", "GIT_CONFIG_SYSTEM=/dev/null", "--env", "GIT_CONFIG_GLOBAL=/dev/null",
		"--env", "GIT_CONFIG_COUNT=0", "--env", "GIT_CONFIG_PARAMETERS=", "--env", "GIT_NO_REPLACE_OBJECTS=1",
		"--env", "GIT_ATTR_NOSYSTEM=1",
		"--env", "GIT_OPTIONAL_LOCKS=0", "--env", "GIT_TERMINAL_PROMPT=0", "--env", "GIT_PROTOCOL_FROM_USER=0",
		"--env", "GIT_SSH_COMMAND=/bin/false", "--env", "LD_PRELOAD=", "--env", "LD_LIBRARY_PATH=", "--env", "BASH_ENV=",
		"--", "/run/current-system/sw/bin/git", "--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper="}
	argv = append(argv, args...)
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := b.command(commandCtx, argv...)
	if err != nil && commandCtx.Err() != nil {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		_, stopErr := b.Stop(cleanupCtx, helper)
		return nil, errors.Join(err, commandCtx.Err(), stopErr)
	}
	return out, err
}

func (b *Backend) AnalyzeWorkspaceHelper(ctx context.Context, helper Session) (WorkspaceAnalysis, error) {
	var analysis WorkspaceAnalysis
	index, err := b.helperGit(ctx, helper, "ls-files", "--stage", "-z")
	if err != nil {
		return analysis, err
	}
	if len(index) != 0 && index[len(index)-1] != 0 {
		return WorkspaceAnalysis{}, errors.New("Git index output malformed")
	}
	for _, entry := range bytes.Split(bytes.TrimSuffix(index, []byte{0}), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, filename, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Split(meta, []byte{' '})
		if !ok || len(fields) != 3 || !validWorkspaceRelative(string(filename)) || !utf8.Valid(filename) ||
			!validBuilderOID(string(fields[1])) || len(fields[2]) != 1 || fields[2][0] < '0' || fields[2][0] > '3' ||
			!bytes.Equal(fields[0], []byte("100644")) && !bytes.Equal(fields[0], []byte("100755")) && !bytes.Equal(fields[0], []byte("120000")) {
			return WorkspaceAnalysis{}, errors.New("Git index has unsupported gitlink or entry")
		}
	}
	head, err := b.helperGit(ctx, helper, "symbolic-ref", "HEAD")
	if err != nil {
		return analysis, errors.New("detached or unsafe Git HEAD unavailable")
	}
	branchRef := strings.TrimSpace(string(head))
	if !strings.HasPrefix(branchRef, "refs/heads/") || !safeGitToken.MatchString(strings.TrimPrefix(branchRef, "refs/heads/")) {
		return analysis, errors.New("Git HEAD ref unsupported")
	}
	analysis.Branch = strings.TrimPrefix(branchRef, "refs/heads/")
	refs, err := b.helperGit(ctx, helper, "for-each-ref", "--format=%(objectname)%09%(refname)%09%(upstream)")
	if err != nil {
		return analysis, err
	}
	analysis.Refs, err = parseWorkspaceRefs(refs)
	if err != nil {
		return WorkspaceAnalysis{}, err
	}
	for _, ref := range analysis.Refs {
		if ref.Name == branchRef {
			analysis.HeadOID = ref.OID
		}
	}
	status, err := b.helperGit(ctx, helper, "status", "--porcelain=v1", "-z", "--no-renames", "--ignore-submodules=all", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return WorkspaceAnalysis{}, err
	}
	analysis.Changes, err = parseWorkspaceChanges(status)
	if err != nil {
		return WorkspaceAnalysis{}, err
	}
	if err := b.verifyWorkspaceHelperBoot(ctx, helper); err != nil {
		return WorkspaceAnalysis{}, err
	}
	return analysis, nil
}

func parseWorkspaceRefs(raw []byte) ([]WorkspaceRef, error) {
	if len(raw) > 256<<10 || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("Git refs output unsupported")
	}
	refs := []WorkspaceRef{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || len(refs) >= 256 || !validBuilderOID(fields[0]) || !strings.HasPrefix(fields[1], "refs/") || !safeGitToken.MatchString(strings.TrimPrefix(fields[1], "refs/")) ||
			fields[2] != "" && (!strings.HasPrefix(fields[2], "refs/") || !safeGitToken.MatchString(strings.TrimPrefix(fields[2], "refs/"))) {
			return nil, errors.New("Git ref output malformed or exceeds bound")
		}
		refs = append(refs, WorkspaceRef{Name: fields[1], OID: fields[0], Upstream: fields[2]})
	}
	return refs, nil
}

func parseWorkspaceChanges(raw []byte) ([]WorkspaceChange, error) {
	if len(raw) > 256<<10 || len(raw) != 0 && raw[len(raw)-1] != 0 {
		return nil, errors.New("Git status output malformed or exceeds bound")
	}
	changes := []WorkspaceChange{}
	for _, entry := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' || len(changes) >= 256 || !utf8.Valid(entry[3:]) || bytes.IndexAny(entry[3:], "\n\r") >= 0 {
			return nil, errors.New("Git status entry unsupported")
		}
		code, name := string(entry[:2]), string(entry[3:])
		if !validWorkspaceRelative(name) {
			return nil, errors.New("Git status path escaped workspace")
		}
		changes = append(changes, WorkspaceChange{Code: code, Path: name})
	}
	return changes, nil
}

func validWorkspaceRelative(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\x00\n\r") {
		return false
	}
	clean := path.Clean(name)
	return clean == name && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
