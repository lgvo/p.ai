package gitservice

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
)

// ProjectRemovalLoss captures every P head and every commit losing its final
// P reference when the whole bare repository is removed. Large histories fail
// closed, retaining the repository for explicit manual review.
func (b *Backend) ProjectRemovalLoss(ctx context.Context, project string) (BranchRemovalLoss, error) {
	loss := BranchRemovalLoss{AssignedRef: "refs/heads/*"}
	refs, err := b.observeLossHeads(ctx, project)
	if err != nil {
		return loss, err
	}
	loss.PRefs = refs
	loss.CommitsLosingPReachability = []string{}
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return loss, err
	}
	if len(refs) > 0 {
		args := []string{"-C", repo, "rev-list", "--max-count=257"}
		for _, r := range refs {
			args = append(args, r.OID)
		}
		cmd := exec.CommandContext(ctx, b.gitPath, args...)
		cmd.Env = b.gitEnv()
		var output refOutput
		cmd.Stdout = &output
		if err = cmd.Run(); err != nil {
			return loss, errors.New("project commit loss observation unavailable")
		}
		lines := strings.Split(strings.TrimSuffix(output.buffer.String(), "\n"), "\n")
		if len(lines) > 256 || !strings.HasSuffix(output.buffer.String(), "\n") {
			return loss, errors.New("project history exceeds bounded loss review; retain and investigate manually")
		}
		seen := map[string]bool{}
		for _, oid := range lines {
			if !validOID(oid) || seen[oid] {
				return loss, errors.New("project commit loss malformed")
			}
			seen[oid] = true
			loss.CommitsLosingPReachability = append(loss.CommitsLosingPReachability, oid)
		}
	}
	again, err := b.observeLossHeads(ctx, project)
	if err != nil || !reflect.DeepEqual(refs, again) {
		return loss, errors.Join(err, errors.New("project refs changed during loss observation"))
	}
	return loss, nil
}
