package gitservice

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

type BranchRemovalLoss struct {
	AssignedRef                string          `json:"assigned_ref"`
	AssignedTip                string          `json:"assigned_tip,omitempty"`
	CommitsLosingPReachability []string        `json:"commits_losing_p_reachability"`
	PRefs                      []plugin.GitRef `json:"p_refs"`
}

// ErrRefsMoving reports that P's refs changed while a branch loss was read; a
// new read may succeed.
var ErrRefsMoving = errors.New("P refs changed during branch loss preview")

// BranchRemovalLoss compares one assigned ref with the complete freshly
// captured P head set. When allowMissing accepts an absent ref, nothing of
// that branch is left to lose in P's repository.
func (b *Backend) BranchRemovalLoss(ctx context.Context, project, branch string, allowMissing bool) (BranchRemovalLoss, error) {
	result, err := b.AssignedBranchSnapshot(ctx, project, branch, allowMissing)
	if err != nil || result.AssignedTip == "" {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return BranchRemovalLoss{}, err
	}
	others := make([]string, 0, len(result.PRefs))
	for _, ref := range result.PRefs {
		if ref.Ref != result.AssignedRef {
			others = append(others, ref.OID)
		}
	}
	args := []string{"-C", repo, "rev-list", "--max-count=257", result.AssignedTip, "--not"}
	args = append(args, others...)
	query := exec.CommandContext(ctx, b.gitPath, args...)
	query.Env = b.gitEnv()
	var output refOutput
	query.Stdout = &output
	if err := query.Run(); err != nil {
		return BranchRemovalLoss{}, errors.New("branch reachability unavailable")
	}
	if output.buffer.Len() != 0 {
		if !bytes.HasSuffix(output.buffer.Bytes(), []byte("\n")) {
			return BranchRemovalLoss{}, errors.New("branch reachability output malformed")
		}
		for _, oid := range strings.Split(strings.TrimSuffix(output.buffer.String(), "\n"), "\n") {
			if !validOID(oid) {
				return BranchRemovalLoss{}, errors.New("branch reachability output malformed")
			}
			result.CommitsLosingPReachability = append(result.CommitsLosingPReachability, oid)
		}
	}
	if len(result.CommitsLosingPReachability) > 256 {
		return BranchRemovalLoss{}, errors.New("branch loss exceeds complete bound")
	}
	again, err := b.observeLossHeads(ctx, project)
	if err != nil {
		return BranchRemovalLoss{}, err
	}
	if !equalLossRefs(result.PRefs, again) {
		return BranchRemovalLoss{}, ErrRefsMoving
	}
	return result, nil
}

// AssignedBranchSnapshot captures the assigned ref and every P head. Session
// removal sets allowMissing: a branch that was never born or was deleted
// outside P leaves AssignedTip empty instead of refusing the removal.
func (b *Backend) AssignedBranchSnapshot(ctx context.Context, project, branch string, allowMissing bool) (BranchRemovalLoss, error) {
	if !validProject(project) || !validBranch(branch) {
		return BranchRemovalLoss{}, errors.New("invalid branch loss request")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	refs, err := b.observeLossHeads(ctx, project)
	if err != nil {
		return BranchRemovalLoss{}, err
	}
	assigned := "refs/heads/" + branch
	result := BranchRemovalLoss{AssignedRef: assigned, CommitsLosingPReachability: []string{}, PRefs: refs}
	for _, ref := range refs {
		if ref.Ref == assigned {
			result.AssignedTip = ref.OID
		}
	}
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return BranchRemovalLoss{}, err
	}
	if result.AssignedTip == "" && !allowMissing {
		return BranchRemovalLoss{}, errors.New("assigned P ref unavailable")
	}
	if len(refs) != 0 {
		tips := make([]string, 0, len(refs))
		for _, ref := range refs {
			tips = append(tips, ref.OID)
		}
		types, err := b.lossPObjectTypes(ctx, repo, tips, nil)
		if err != nil {
			return BranchRemovalLoss{}, err
		}
		for _, tip := range tips {
			if types[tip] != "commit" {
				return BranchRemovalLoss{}, errors.New("P retained tip unavailable")
			}
		}
	}
	again, err := b.observeLossHeads(ctx, project)
	if err != nil {
		return BranchRemovalLoss{}, err
	}
	if !equalLossRefs(refs, again) {
		return BranchRemovalLoss{}, ErrRefsMoving
	}
	return result, nil
}

func equalLossRefs(a, b []plugin.GitRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
