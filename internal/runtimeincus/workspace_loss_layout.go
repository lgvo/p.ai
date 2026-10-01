package runtimeincus

import (
	"bytes"
	"errors"
	"path"
	"sort"
	"strings"
)

// Git's common worktree directory is the only source of additional worktree
// paths. These are literal bytes from a quiescent source snapshot, never paths
// passed to Git or opened before separate ancestry and mount validation.
type workspaceLinkedTree struct {
	AdminName string
	Root      string
	GitFile   string
	AdminDir  string
}

type workspaceLinkedSnapshot struct {
	Link     workspaceLinkedTree
	Snapshot WorkspaceSnapshot
}

type WorkspaceLossSnapshot struct {
	Main   WorkspaceSnapshot
	Linked []workspaceLinkedSnapshot
}

func parseWorkspaceLinkedTrees(snapshot WorkspaceSnapshot) ([]workspaceLinkedTree, error) {
	entries := make(map[string]WorkspaceEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if _, exists := entries[entry.Path]; exists {
			return nil, errors.New("workspace snapshot contains duplicate path")
		}
		entries[entry.Path] = entry
	}
	root, exists := entries[".git/worktrees"]
	if !exists {
		for name := range entries {
			if strings.HasPrefix(name, ".git/worktrees/") {
				return nil, errors.New("Git worktree inventory parent missing")
			}
		}
		return nil, nil
	}
	if root.Type != "directory" {
		return nil, errors.New("Git worktree inventory is not a directory")
	}
	admin := make(map[string]bool)
	for name, entry := range entries {
		if !strings.HasPrefix(name, ".git/worktrees/") {
			continue
		}
		rel := strings.TrimPrefix(name, ".git/worktrees/")
		id, _, _ := strings.Cut(rel, "/")
		if !validWorkspaceComponent(id) || !safeGitToken.MatchString(id) || strings.Contains(id, "..") || strings.Contains(id, "/") {
			return nil, errors.New("Git worktree administration name unavailable")
		}
		if rel == id {
			if entry.Type != "directory" || admin[id] {
				return nil, errors.New("Git worktree administration directory invalid")
			}
			admin[id] = true
		}
	}
	if len(admin) > 8 {
		return nil, errors.New("Git worktree count exceeds bound")
	}
	names := make([]string, 0, len(admin))
	for id := range admin {
		names = append(names, id)
	}
	sort.Strings(names)
	links := make([]workspaceLinkedTree, 0, len(names))
	seenRoot := make(map[string]bool)
	for _, id := range names {
		prefix := ".git/worktrees/" + id
		gitdir, ok := entries[prefix+"/gitdir"]
		if !ok || gitdir.Type != "file" || len(gitdir.Data) > 4096 {
			return nil, errors.New("Git worktree gitdir unavailable")
		}
		common, ok := entries[prefix+"/commondir"]
		if !ok || common.Type != "file" || !bytes.Equal(common.Data, []byte("../..\n")) {
			return nil, errors.New("Git worktree common directory unavailable")
		}
		head, ok := entries[prefix+"/HEAD"]
		if !ok || head.Type != "file" || len(head.Data) == 0 || len(head.Data) > 256 {
			return nil, errors.New("Git worktree HEAD unavailable")
		}
		if bytes.IndexByte(gitdir.Data, 0) >= 0 || bytes.Count(gitdir.Data, []byte{'\n'}) != 1 || gitdir.Data[len(gitdir.Data)-1] != '\n' {
			return nil, errors.New("Git worktree gitdir pointer malformed")
		}
		pointer := strings.TrimSuffix(string(gitdir.Data), "\n")
		if !strings.HasSuffix(pointer, "/.git") || path.Clean(pointer) != pointer {
			return nil, errors.New("Git worktree gitdir pointer unavailable")
		}
		rootPath := strings.TrimSuffix(pointer, "/.git")
		if !supportedLinkedWorkspaceRoot(rootPath) || seenRoot[rootPath] {
			return nil, errors.New("Git worktree root outside closed runtime paths")
		}
		seenRoot[rootPath] = true
		links = append(links, workspaceLinkedTree{AdminName: id, Root: rootPath, GitFile: pointer,
			AdminDir: "/workspace/" + prefix})
	}
	for name := range entries {
		if !strings.HasPrefix(name, ".git/worktrees/") {
			continue
		}
		rel := strings.TrimPrefix(name, ".git/worktrees/")
		id, suffix, nested := strings.Cut(rel, "/")
		if !admin[id] {
			return nil, errors.New("Git worktree inventory contains unaccounted entry")
		}
		if nested && suffix != "gitdir" && suffix != "commondir" && suffix != "HEAD" && suffix != "ORIG_HEAD" && suffix != "index" && suffix != "COMMIT_EDITMSG" && suffix != "logs" && suffix != "logs/HEAD" && suffix != "refs" {
			return nil, errors.New("Git worktree administration entry unsupported")
		}
		if nested {
			kind := "file"
			if suffix == "logs" || suffix == "refs" {
				kind = "directory"
			}
			if entries[name].Type != kind {
				return nil, errors.New("Git worktree administration type unsupported")
			}
			if suffix == "logs/HEAD" {
				parent, exists := entries[".git/worktrees/"+id+"/logs"]
				if !exists || parent.Type != "directory" {
					return nil, errors.New("Git worktree log parent missing")
				}
			}
		}
	}
	return links, nil
}

// Only these narrow paths avoid the helper's root-owned assets and session
// credential directories. Other Git-known paths are explicit unavailable
// until a separate exact grant/mount classification is implemented.
func supportedLinkedWorkspaceRoot(root string) bool {
	if path.Clean(root) != root || strings.ContainsAny(root, "\x00\n\r\\") {
		return false
	}
	var name string
	if strings.HasPrefix(root, "/workspace/") {
		name = strings.TrimPrefix(root, "/workspace/")
	} else if strings.HasPrefix(root, "/home/p/worktrees/") {
		name = strings.TrimPrefix(root, "/home/p/worktrees/")
	} else {
		return false
	}
	return validWorkspaceComponent(name) && !strings.Contains(name, "/") && name != ".git"
}

func verifyLinkedGitFile(link workspaceLinkedTree, got WorkspaceEntry) error {
	if got.Type != "file" || len(got.Data) > 4096 || !bytes.Equal(got.Data, []byte("gitdir: "+link.AdminDir+"\n")) {
		return errors.New("linked Git file does not point back to exact common directory")
	}
	return nil
}

func verifyMainLinkedGitFiles(main WorkspaceSnapshot, links []workspaceLinkedTree) error {
	expected := make(map[string]workspaceLinkedTree)
	for _, link := range links {
		if strings.HasPrefix(link.GitFile, "/workspace/") {
			expected[strings.TrimPrefix(link.GitFile, "/workspace/")] = link
		}
	}
	seen := make(map[string]bool)
	for _, entry := range main.Entries {
		if entry.Path == ".git" || !strings.HasSuffix(entry.Path, "/.git") {
			continue
		}
		link, ok := expected[entry.Path]
		if !ok || seen[entry.Path] {
			return errors.New("unaccounted nested Git metadata in workspace")
		}
		if err := verifyLinkedGitFile(link, entry); err != nil {
			return err
		}
		seen[entry.Path] = true
	}
	if len(seen) != len(expected) {
		return errors.New("Git-known nested worktree missing from workspace")
	}
	return nil
}
