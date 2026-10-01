package gitservice

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// RetainedLossRefs observes the complete bounded P-owned heads, then compares
// literal commit OIDs from the isolated helper against those captured tips.
// Runtime Git bytes and config are never opened by host Git.
func (b *Backend) RetainedLossRefs(ctx context.Context, project string, commits []string) ([]plugin.GitRef, []string, error) {
	if len(commits) > 256 {
		return nil, nil, errors.New("local commit inventory exceeds bound")
	}
	seen := map[string]bool{}
	for _, oid := range commits {
		if !validOID(oid) || seen[oid] {
			return nil, nil, errors.New("local commit inventory malformed")
		}
		seen[oid] = true
	}
	scoped, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	refs, err := b.observeLossHeads(scoped, project)
	if err != nil {
		return nil, nil, err
	}
	repo, err := b.checkRepository(scoped, project)
	if err != nil {
		return nil, nil, err
	}
	tips := make([]string, 0, len(refs))
	for _, ref := range refs {
		tips = append(tips, ref.OID)
	}
	types, err := b.lossPObjectTypes(scoped, repo, tips, commits)
	if err != nil {
		return nil, nil, err
	}
	for _, tip := range tips {
		if types[tip] != "commit" {
			return nil, nil, errors.New("P retained tip object unavailable")
		}
	}
	localOnly := make([]string, 0, len(commits))
	for _, oid := range commits {
		// Missing from P's object database means P has not retained these
		// runtime bytes, regardless of local ref names or origin claims.
		if types[oid] == "missing" {
			localOnly = append(localOnly, oid)
			continue
		}
		if types[oid] != "commit" {
			return nil, nil, errors.New("P local commit object type unavailable")
		}
		args := []string{"-C", repo, "rev-list", "--max-count=1", oid, "--not"}
		args = append(args, tips...)
		query := exec.CommandContext(scoped, b.gitPath, args...)
		query.Env = b.gitEnv()
		var output refOutput
		query.Stdout = &output
		if err := query.Run(); err != nil {
			return nil, nil, errors.New("P reachability observation unavailable")
		}
		if len(bytes.TrimSpace(output.buffer.Bytes())) != 0 {
			if !bytes.Equal(output.buffer.Bytes(), []byte(oid+"\n")) {
				return nil, nil, errors.New("P reachability output malformed")
			}
			localOnly = append(localOnly, oid)
		}
	}
	again, err := b.observeLossHeads(scoped, project)
	if err != nil || !reflect.DeepEqual(refs, again) {
		return nil, nil, errors.Join(err, errors.New("P retained refs changed during loss analysis"))
	}
	sort.Strings(localOnly)
	return refs, localOnly, nil
}

func (b *Backend) lossPObjectTypes(ctx context.Context, repo string, tips, commits []string) (map[string]string, error) {
	input := make([]string, 0, len(tips)+len(commits))
	seen := map[string]bool{}
	for _, oid := range append(append([]string(nil), tips...), commits...) {
		if !seen[oid] {
			seen[oid] = true
			input = append(input, oid)
		}
	}
	if len(input) == 0 {
		return map[string]string{}, nil
	}
	query := exec.CommandContext(ctx, b.gitPath, "-C", repo, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	query.Env = b.gitEnv()
	query.Stdin = strings.NewReader(strings.Join(input, "\n") + "\n")
	var output refOutput
	query.Stdout = &output
	if err := query.Run(); err != nil {
		return nil, errors.New("P object inventory unavailable")
	}
	lines := strings.Split(strings.TrimSuffix(output.buffer.String(), "\n"), "\n")
	if len(lines) != len(input) || !strings.HasSuffix(output.buffer.String(), "\n") {
		return nil, errors.New("P object inventory incomplete")
	}
	types := make(map[string]string, len(input))
	for i, line := range lines {
		oid, typ, ok := strings.Cut(line, " ")
		if !ok || oid != input[i] || typ != "commit" && typ != "missing" {
			return nil, errors.New("P object inventory malformed")
		}
		types[oid] = typ
	}
	return types, nil
}

func (b *Backend) observeLossHeads(ctx context.Context, project string) ([]plugin.GitRef, error) {
	const limit = 8
	refs := make([]plugin.GitRef, 0)
	cursor := ""
	for {
		page, next, err := b.ListRefsPage(ctx, project, cursor, limit)
		if err != nil {
			return nil, err
		}
		if len(page) > limit || len(refs)+len(page) > 1024 {
			return nil, errors.New("P retained ref inventory exceeds bound")
		}
		for _, ref := range page {
			if !strings.HasPrefix(ref.Ref, "refs/heads/") || !validBranch(strings.TrimPrefix(ref.Ref, "refs/heads/")) || !validOID(ref.OID) || len(refs) > 0 && ref.Ref <= refs[len(refs)-1].Ref {
				return nil, errors.New("P retained ref inventory malformed")
			}
			refs = append(refs, ref)
		}
		if next == "" {
			break
		}
		if len(page) == 0 || next != refs[len(refs)-1].Ref || next <= cursor {
			return nil, errors.New("P retained ref cursor unavailable")
		}
		cursor = next
	}
	return refs, nil
}
