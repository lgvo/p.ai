package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	workspaceMaxEntries = 512
	workspaceMaxBytes   = 16 << 20
	workspaceMaxFile    = 2 << 20
	workspaceMaxDepth   = 24
)

type WorkspaceEntry struct {
	Path   string
	Type   string
	Mode   int
	Data   []byte
	Target string
}

type WorkspaceSnapshot struct{ Entries []WorkspaceEntry }

// ExportWorkspace sees only /workspace through the project-qualified file API.
// Every component is LSTATed before descent, and any unsupported layout is an
// error; callers must never turn a partial snapshot into a clean Git result.
func (b *Backend) ExportWorkspace(ctx context.Context, source Session) (WorkspaceSnapshot, error) {
	return b.exportWorkspace(ctx, source, false)
}

// ExportWorkspaceLoss captures only Git-known, narrowly located runtime-owned
// worktrees. Every discovered tree is read while the exact source remains
// stopped or frozen. A missing, unsafe, or over-bound tree makes the whole
// result unavailable; no partial tree list can become a clean loss report.
func (b *Backend) ExportWorkspaceLoss(ctx context.Context, source Session) (WorkspaceLossSnapshot, error) {
	var zero WorkspaceLossSnapshot
	if source.WorkspaceOwner != "" {
		return zero, errors.New("helper cannot be a workspace source")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.IncusUUID == "" || before.Generation == "" || before.Status != "Stopped" && before.Status != "Frozen" {
		return zero, errors.Join(err, errors.New("workspace source is not quiescent"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(source)}
	if before.Status == "Frozen" {
		if err := api.verifyFrozenWorkspaceMounts(ctx); err != nil {
			return zero, err
		}
	}
	main, err := exportWorkspaceFilesPolicy(ctx, api, true)
	if err != nil {
		return zero, err
	}
	var verifyMounts func([]string) error
	if before.Status == "Frozen" {
		verifyMounts = func(roots []string) error {
			return api.verifyFrozenWorkspaceMountsAt(ctx, roots)
		}
	}
	result, err := exportKnownLinkedTrees(ctx, api, main, verifyMounts)
	if err != nil {
		return zero, err
	}
	if err := boundWorkspaceLossSnapshot(result); err != nil {
		return zero, err
	}
	after, err := b.Inspect(ctx, source)
	if err != nil || !after.Exists || after.Status != before.Status || after.Fingerprint != before.Fingerprint || after.IncusUUID != before.IncusUUID || after.Generation != before.Generation {
		return zero, errors.Join(err, errors.New("workspace source changed during loss export"))
	}
	return result, nil
}

func exportKnownLinkedTrees(ctx context.Context, api workspaceReader, main WorkspaceSnapshot, verifyMounts func([]string) error) (WorkspaceLossSnapshot, error) {
	var zero WorkspaceLossSnapshot
	links, err := parseWorkspaceLinkedTrees(main)
	if err != nil {
		return zero, err
	}
	if err := verifyMainLinkedGitFiles(main, links); err != nil {
		return zero, err
	}
	roots := []string{"/workspace"}
	for _, link := range links {
		roots = append(roots, link.Root)
	}
	if verifyMounts != nil {
		if err := verifyMounts(roots); err != nil {
			return zero, err
		}
	}
	result := WorkspaceLossSnapshot{Main: main, Linked: make([]workspaceLinkedSnapshot, 0, len(links))}
	for _, link := range links {
		snapshot, err := exportWorkspaceTree(ctx, api, link.Root, true, "file")
		if err != nil {
			return zero, err
		}
		var gitFile WorkspaceEntry
		found := false
		for _, entry := range snapshot.Entries {
			if entry.Path == ".git" {
				gitFile, found = entry, true
			}
		}
		if !found || verifyLinkedGitFile(link, gitFile) != nil {
			return zero, errors.New("Git-known worktree reverse pointer unavailable")
		}
		result.Linked = append(result.Linked, workspaceLinkedSnapshot{Link: link, Snapshot: snapshot})
	}
	if verifyMounts != nil {
		if err := verifyMounts(roots); err != nil {
			return zero, err
		}
	}
	return result, nil
}

func boundWorkspaceLossSnapshot(result WorkspaceLossSnapshot) error {
	seen := make(map[string]WorkspaceEntry)
	count, total := 0, 0
	visit := func(root string, snapshot WorkspaceSnapshot) error {
		for _, entry := range snapshot.Entries {
			full := root
			if entry.Path != "" {
				full = path.Join(root, entry.Path)
			}
			if old, exists := seen[full]; exists {
				if old.Type != entry.Type || old.Mode != entry.Mode || old.Target != entry.Target || !bytes.Equal(old.Data, entry.Data) {
					return errors.New("overlapping worktree snapshot changed")
				}
				continue
			}
			seen[full] = entry
			count++
			total += len(entry.Data) + len(entry.Target)
			if count > workspaceMaxEntries || total > workspaceMaxBytes {
				return errors.New("combined worktree snapshot exceeds bound")
			}
		}
		return nil
	}
	if err := visit("/workspace", result.Main); err != nil {
		return err
	}
	for _, linked := range result.Linked {
		if strings.HasPrefix(linked.Link.Root, "/workspace/") {
			fromMain := make(map[string]WorkspaceEntry)
			for full, entry := range seen {
				if full == linked.Link.Root || strings.HasPrefix(full, linked.Link.Root+"/") {
					fromMain[full] = entry
				}
			}
			if len(fromMain) != len(linked.Snapshot.Entries) {
				return errors.New("nested linked worktree snapshot incomplete")
			}
			for _, entry := range linked.Snapshot.Entries {
				full := linked.Link.Root
				if entry.Path != "" {
					full = path.Join(full, entry.Path)
				}
				if _, exists := fromMain[full]; !exists {
					return errors.New("nested linked worktree differs from main snapshot")
				}
			}
		}
		if err := visit(linked.Link.Root, linked.Snapshot); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) exportWorkspace(ctx context.Context, source Session, allowLinked bool) (WorkspaceSnapshot, error) {
	if source.WorkspaceOwner != "" {
		return WorkspaceSnapshot{}, errors.New("helper cannot be a workspace source")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return WorkspaceSnapshot{}, err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.Status != "Stopped" && before.Status != "Frozen" {
		return WorkspaceSnapshot{}, errors.Join(err, errors.New("workspace source is not quiescent"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(source)}
	if before.Status == "Frozen" {
		if err := api.verifyFrozenWorkspaceMounts(ctx); err != nil {
			return WorkspaceSnapshot{}, err
		}
	}
	result, err := exportWorkspaceFilesPolicy(ctx, api, allowLinked)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	if before.Status == "Frozen" {
		if err := api.verifyFrozenWorkspaceMounts(ctx); err != nil {
			return WorkspaceSnapshot{}, err
		}
	}
	after, err := b.Inspect(ctx, source)
	if err != nil || !after.Exists || after.Status != before.Status || after.Fingerprint != before.Fingerprint {
		return WorkspaceSnapshot{}, errors.Join(err, errors.New("workspace source changed during export"))
	}
	return result, nil
}

type workspaceReader interface {
	lstat(context.Context, string) (guestFile, error)
	listDirectory(context.Context, string, int) ([]string, error)
	getBounded(context.Context, string, int64) (guestFile, bool, error)
	readlinkBounded(context.Context, string, int64) ([]byte, error)
}

func exportWorkspaceFiles(ctx context.Context, api workspaceReader) (WorkspaceSnapshot, error) {
	return exportWorkspaceFilesPolicy(ctx, api, false)
}

func exportWorkspaceFilesPolicy(ctx context.Context, api workspaceReader, allowLinked bool) (WorkspaceSnapshot, error) {
	return exportWorkspaceTree(ctx, api, "/workspace", allowLinked, "directory")
}

func exportWorkspaceTree(ctx context.Context, api workspaceReader, rootPath string, allowLinked bool, gitType string) (WorkspaceSnapshot, error) {
	if !supportedWorkspaceTreeRoot(rootPath) {
		return WorkspaceSnapshot{}, errors.New("workspace tree root outside closed paths")
	}
	if rootPath != "/workspace" {
		if err := verifyLinkedWorkspaceAncestors(ctx, api, rootPath); err != nil {
			return WorkspaceSnapshot{}, err
		}
	}
	root, err := api.lstat(ctx, rootPath)
	if err != nil || root.typ != "directory" || root.uid != 1000 || root.gid != 1000 || root.mode&07000 != 0 {
		return WorkspaceSnapshot{}, errors.Join(err, errors.New("workspace root unsafe"))
	}
	result := WorkspaceSnapshot{Entries: []WorkspaceEntry{{Path: "", Type: "directory", Mode: root.mode}}}
	var total int
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > workspaceMaxDepth {
			return errors.New("workspace depth exceeds bound")
		}
		meta, err := api.lstat(ctx, dir)
		if err != nil || meta.typ != "directory" || meta.uid != 1000 || meta.gid != 1000 || meta.mode&07000 != 0 {
			return errors.Join(err, errors.New("workspace directory changed before enumeration"))
		}
		names, err := api.listDirectory(ctx, dir, workspaceMaxEntries-len(result.Entries))
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			if !validWorkspaceComponent(name) {
				return errors.New("workspace child name unsupported")
			}
			if len(result.Entries) >= workspaceMaxEntries {
				return errors.New("workspace entry count exceeds bound")
			}
			full := path.Join(dir, name)
			if !strings.HasPrefix(full, rootPath+"/") {
				return errors.New("workspace path escaped root")
			}
			rel := strings.TrimPrefix(full, rootPath+"/")
			if rel == ".git/commondir" || rel == ".git/config.worktree" || rel == ".git/objects/info/alternates" ||
				!allowLinked && (rel == ".git/worktrees" || strings.HasPrefix(rel, ".git/worktrees/") || strings.HasSuffix(rel, "/.git")) {
				return errors.New("linked or external Git worktree layout unavailable")
			}
			f, err := api.lstat(ctx, full)
			if err != nil || f.uid != 1000 || f.gid != 1000 || f.mode&07000 != 0 || f.mode&0022 != 0 && strings.HasPrefix(rel, ".git/") {
				return errors.Join(err, errors.New("workspace entry metadata unsafe"))
			}
			e := WorkspaceEntry{Path: rel, Type: f.typ, Mode: f.mode}
			switch f.typ {
			case "directory":
				result.Entries = append(result.Entries, e)
				if err := walk(full, depth+1); err != nil {
					return err
				}
			case "file":
				if total >= workspaceMaxBytes {
					return errors.New("workspace byte count exceeds bound")
				}
				limit := workspaceMaxFile
				if workspaceMaxBytes-total < limit {
					limit = workspaceMaxBytes - total
				}
				got, exists, err := api.getBounded(ctx, full, int64(limit))
				if err != nil || !exists || got.typ != "file" || got.uid != f.uid || got.gid != f.gid || got.mode != f.mode {
					return errors.Join(err, errors.New("workspace file changed during read"))
				}
				total += len(got.data)
				e.Data = bytes.Clone(got.data)
				result.Entries = append(result.Entries, e)
			case "symlink":
				if strings.HasPrefix(rel, ".git/") || rel == ".git" {
					return errors.New("Git metadata symlink unavailable")
				}
				target, err := api.readlinkBounded(ctx, full, 4096)
				if err != nil || !validWorkspaceLinkAt(rootPath, dir, string(target)) {
					return errors.Join(err, errors.New("workspace symlink escapes root"))
				}
				e.Target = string(target)
				result.Entries = append(result.Entries, e)
			default:
				return errors.New("workspace special file unavailable")
			}
		}
		return nil
	}
	if err := walk(rootPath, 0); err != nil {
		return WorkspaceSnapshot{}, err
	}
	gitEntry := false
	for _, entry := range result.Entries {
		if entry.Path == ".git" && entry.Type == gitType {
			gitEntry = true
		}
	}
	if !gitEntry {
		return WorkspaceSnapshot{}, errors.New("workspace Git directory unavailable")
	}
	return result, nil
}

func supportedWorkspaceTreeRoot(root string) bool {
	return root == "/workspace" || supportedLinkedWorkspaceRoot(root)
}

func verifyLinkedWorkspaceAncestors(ctx context.Context, api workspaceReader, root string) error {
	ancestors := []string{"/workspace"}
	if strings.HasPrefix(root, "/home/p/worktrees/") {
		ancestors = []string{"/home", "/home/p", "/home/p/worktrees"}
	}
	for _, name := range ancestors {
		meta, err := api.lstat(ctx, name)
		uid, gid := 1000, 1000
		if name == "/home" {
			uid, gid = 0, 0
		}
		if err != nil || meta.typ != "directory" || meta.uid != uid || meta.gid != gid || meta.mode&07000 != 0 || meta.mode&0022 != 0 {
			return errors.Join(err, errors.New("linked workspace ancestor unsafe"))
		}
	}
	return nil
}

func validWorkspaceComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\x00\n\r") {
		return false
	}
	return true
}

func validWorkspaceLink(dir, target string) bool {
	return validWorkspaceLinkAt("/workspace", dir, target)
}

func validWorkspaceLinkAt(root, dir, target string) bool {
	if target == "" || len(target) > 4096 || !utf8.ValidString(target) || strings.ContainsAny(target, "\x00\n\r") || strings.HasPrefix(target, "/") {
		return false
	}
	resolved := path.Clean(path.Join(dir, target))
	return resolved == root || strings.HasPrefix(resolved, root+"/")
}
