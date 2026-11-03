package gitservice

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/lgvo/p.ai/internal/plugin"
)

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
