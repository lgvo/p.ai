package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

type WorkspaceIgnoredSummary struct {
	Count        int `json:"count"`
	LogicalBytes int `json:"logical_bytes"`
}

type WorkspaceLossTree struct {
	Path     string                  `json:"path"`
	Location string                  `json:"location"`
	Branch   string                  `json:"branch,omitempty"`
	HeadOID  string                  `json:"head_oid"`
	Changes  []WorkspaceChange       `json:"changes"`
	Ignored  WorkspaceIgnoredSummary `json:"ignored"`
}

type WorkspaceLossGitAnalysis struct {
	Worktrees []WorkspaceLossTree `json:"worktrees"`
	LocalRefs []WorkspaceRef      `json:"local_refs"`
	Commits   []string            `json:"-"`
}

// AnalyzeWorkspaceLossHelper runs only fixed Git reads over a sanitized copy
// in the no-NIC helper. Every commit object is enumerated, including detached
// HEAD and reflog-only objects, before P-owned refs determine retention.
func (b *Backend) AnalyzeWorkspaceLossHelper(ctx context.Context, helper Session, loss WorkspaceLossSnapshot) (WorkspaceLossGitAnalysis, error) {
	var out WorkspaceLossGitAnalysis
	if err := boundWorkspaceLossSnapshot(loss); err != nil {
		return out, err
	}
	all := []struct {
		root     string
		snapshot WorkspaceSnapshot
	}{{"/workspace", loss.Main}}
	for _, linked := range loss.Linked {
		all = append(all, struct {
			root     string
			snapshot WorkspaceSnapshot
		}{linked.Link.Root, linked.Snapshot})
	}
	out.Worktrees = make([]WorkspaceLossTree, 0, len(all))
	for _, tree := range all {
		if !supportedWorkspaceTreeRoot(tree.root) {
			return WorkspaceLossGitAnalysis{}, errors.New("loss worktree path unsupported")
		}
		branch, head, err := b.lossHeadAt(ctx, helper, tree.root)
		if err != nil {
			return WorkspaceLossGitAnalysis{}, err
		}
		index, err := b.helperGitAt(ctx, helper, tree.root, "ls-files", "--stage", "-z")
		if err != nil || !validWorkspaceIndex(index) {
			return WorkspaceLossGitAnalysis{}, errors.Join(err, errors.New("loss worktree index unavailable"))
		}
		status, err := b.helperGitAt(ctx, helper, tree.root, "status", "--porcelain=v1", "-z", "--no-renames", "--ignore-submodules=all", "--untracked-files=all", "--ignored=matching")
		if err != nil {
			return WorkspaceLossGitAnalysis{}, err
		}
		changes, err := parseWorkspaceChanges(status)
		if err != nil {
			return WorkspaceLossGitAnalysis{}, err
		}
		ignoredRaw, err := b.helperGitAt(ctx, helper, tree.root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
		if err != nil {
			return WorkspaceLossGitAnalysis{}, err
		}
		ignored, err := summarizeWorkspaceIgnored(ignoredRaw, tree.snapshot)
		if err != nil {
			return WorkspaceLossGitAnalysis{}, err
		}
		out.Worktrees = append(out.Worktrees, WorkspaceLossTree{Path: tree.root, Location: "runtime", Branch: branch, HeadOID: head, Changes: changes, Ignored: ignored})
	}
	refsRaw, err := b.helperGit(ctx, helper, "for-each-ref", "--format=%(objectname)%09%(refname)%09%(upstream)")
	if err != nil {
		return WorkspaceLossGitAnalysis{}, err
	}
	out.LocalRefs, err = parseWorkspaceRefs(refsRaw)
	if err != nil {
		return WorkspaceLossGitAnalysis{}, err
	}
	objectsRaw, err := b.helperGit(ctx, helper, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return WorkspaceLossGitAnalysis{}, err
	}
	out.Commits, err = parseAllCommitObjects(objectsRaw)
	if err != nil {
		return WorkspaceLossGitAnalysis{}, err
	}
	return out, b.verifyWorkspaceHelperBoot(ctx, helper)
}

func (b *Backend) lossHeadAt(ctx context.Context, helper Session, root string) (string, string, error) {
	return readLossHead(func(args ...string) ([]byte, error) { return b.helperGitAt(ctx, helper, root, args...) })
}

func readLossHead(git func(...string) ([]byte, error)) (string, string, error) {
	branch := ""
	// symbolic-ref works on an unborn branch; rev-parse --symbolic-full-name
	// does not. A detached HEAD has no symbolic ref but may have a commit.
	name, symbolicErr := git("symbolic-ref", "-q", "HEAD")
	if symbolicErr == nil {
		ref := strings.TrimSuffix(string(name), "\n")
		if !strings.HasPrefix(ref, "refs/heads/") || !safeGitToken.MatchString(strings.TrimPrefix(ref, "refs/heads/")) || ref == "refs/heads/" || string(name) != ref+"\n" {
			return "", "", errors.New("loss worktree branch unavailable")
		}
		branch = strings.TrimPrefix(ref, "refs/heads/")
	}
	raw, err := git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if branch == "" {
			return "", "", errors.New("detached worktree HEAD unavailable")
		}
		present, e := git("for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
		if e != nil || len(present) != 0 {
			return "", "", errors.Join(err, e, errors.New("worktree named HEAD object unavailable"))
		}
		return branch, "", nil // exact unborn branch; object scan still runs
	}
	oid := strings.TrimSuffix(string(raw), "\n")
	if !validBuilderOID(oid) || string(raw) != oid+"\n" {
		return "", "", errors.New("loss worktree head unavailable")
	}
	return branch, oid, nil
}

func validWorkspaceIndex(raw []byte) bool {
	if len(raw) > 256<<10 || len(raw) != 0 && raw[len(raw)-1] != 0 {
		return false
	}
	for _, entry := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, filename, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Split(meta, []byte{' '})
		if !ok || len(fields) != 3 || !validWorkspaceRelative(string(filename)) || !utf8.Valid(filename) ||
			!validBuilderOID(string(fields[1])) || len(fields[2]) != 1 || fields[2][0] < '0' || fields[2][0] > '3' ||
			!bytes.Equal(fields[0], []byte("100644")) && !bytes.Equal(fields[0], []byte("100755")) && !bytes.Equal(fields[0], []byte("120000")) {
			return false
		}
	}
	return true
}

func summarizeWorkspaceIgnored(raw []byte, snapshot WorkspaceSnapshot) (WorkspaceIgnoredSummary, error) {
	var summary WorkspaceIgnoredSummary
	if len(raw) > 256<<10 || len(raw) != 0 && raw[len(raw)-1] != 0 {
		return summary, errors.New("ignored file inventory exceeds bound")
	}
	entries := map[string]WorkspaceEntry{}
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	seen := map[string]bool{}
	for _, name := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
		if len(name) == 0 {
			continue
		}
		filename := string(name)
		if !utf8.Valid(name) || !validWorkspaceRelative(filename) || seen[filename] || summary.Count >= workspaceMaxEntries {
			return WorkspaceIgnoredSummary{}, errors.New("ignored path unavailable")
		}
		seen[filename] = true
		entry, ok := entries[filename]
		if !ok || entry.Type != "file" && entry.Type != "symlink" {
			return WorkspaceIgnoredSummary{}, errors.New("ignored file not in bounded source snapshot")
		}
		summary.Count++
		if entry.Type == "file" {
			summary.LogicalBytes += len(entry.Data)
		} else {
			summary.LogicalBytes += len(entry.Target)
		}
	}
	return summary, nil
}

func parseAllCommitObjects(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	if len(raw) > 256<<10 || raw[len(raw)-1] != '\n' || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("Git object inventory malformed or exceeds bound")
	}
	commits := make([]string, 0)
	seen := map[string]bool{}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) > 4096 {
		return nil, errors.New("Git object inventory exceeds bound")
	}
	for _, line := range lines {
		oid, kind, ok := strings.Cut(line, " ")
		if !ok || !validBuilderOID(oid) || seen[oid] || kind != "commit" && kind != "tree" && kind != "blob" && kind != "tag" {
			return nil, errors.New("Git object inventory contains unsupported record")
		}
		seen[oid] = true
		if kind == "commit" {
			if len(commits) >= 256 {
				return nil, errors.New("Git commit inventory exceeds bound")
			}
			commits = append(commits, oid)
		}
	}
	sort.Strings(commits)
	return commits, nil
}
