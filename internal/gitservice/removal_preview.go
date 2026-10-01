package gitservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type OriginRemovalLoss struct {
	Status             string   `json:"status"` // local_only, observed, or unknown
	Reason             string   `json:"reason,omitempty"`
	ContainingBranches []string `json:"containing_branches"`
	ObservedRefsDigest string   `json:"observed_refs_digest,omitempty"`
	UnresolvedRefs     []string `json:"unresolved_refs"`
}

// CompareOriginRemoval uses only freshly advertised literal branch tips. An
// object not already present in P's bare repository is unknown, never proof
// that the origin does not preserve a local commit. No origin object is fetched.
func (b *Backend) CompareOriginRemoval(ctx context.Context, project string, advertised []plugin.GitOriginRef, lost []string) (OriginRemovalLoss, error) {
	if len(advertised) > 1024 || len(lost) > 256 {
		return OriginRemovalLoss{}, errors.New("origin removal comparison exceeds bound")
	}
	encoded, err := json.Marshal(advertised)
	if err != nil {
		return OriginRemovalLoss{}, err
	}
	sum := sha256.Sum256(encoded)
	result := OriginRemovalLoss{Status: "observed", ContainingBranches: []string{},
		ObservedRefsDigest: hex.EncodeToString(sum[:]), UnresolvedRefs: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return OriginRemovalLoss{}, err
	}
	tips := make([]string, 0, len(advertised))
	seen := map[string]bool{}
	for _, ref := range advertised {
		if !validOID(ref.CommitOID) || seen[ref.Ref] ||
			!strings.HasPrefix(ref.Ref, "refs/heads/") && !strings.HasPrefix(ref.Ref, "refs/tags/") {
			return OriginRemovalLoss{}, errors.New("fresh origin branch inventory malformed")
		}
		seen[ref.Ref] = true
		if strings.HasPrefix(ref.Ref, "refs/heads/") {
			tips = append(tips, ref.CommitOID)
		}
	}
	if len(lost) == 0 {
		return result, nil
	}
	for _, oid := range lost {
		if !validOID(oid) {
			return OriginRemovalLoss{}, errors.New("branch loss commit malformed")
		}
	}
	types, err := b.lossPObjectTypes(ctx, repo, tips, lost)
	if err != nil {
		return OriginRemovalLoss{}, err
	}
	for _, ref := range advertised {
		if !strings.HasPrefix(ref.Ref, "refs/heads/") {
			continue
		}
		if types[ref.CommitOID] != "commit" || len(advertised)*len(lost) > 4096 {
			result.Status = "unknown"
			result.Reason = "origin_object_or_comparison_unavailable"
			result.UnresolvedRefs = append(result.UnresolvedRefs, ref.Ref)
			continue
		}
		contained := false
		unknown := false
		for _, candidate := range lost {
			query := exec.CommandContext(ctx, b.gitPath, "-C", repo, "merge-base", "--is-ancestor", candidate, ref.CommitOID)
			query.Env = b.gitEnv()
			err := query.Run()
			if err == nil {
				contained = true
				break
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				unknown = true
				break
			}
		}
		if contained {
			result.ContainingBranches = append(result.ContainingBranches, ref.Ref)
		} else if unknown {
			result.Status = "unknown"
			result.Reason = "origin_object_or_comparison_unavailable"
			result.UnresolvedRefs = append(result.UnresolvedRefs, ref.Ref)
		}
	}
	return result, nil
}

// BranchRemovalLoss compares one assigned ref with the complete freshly
// captured P head set. An eligible unborn branch may coexist with unreferenced
// P objects; absence of retained heads is what matters for branch loss.
func (b *Backend) BranchRemovalLoss(ctx context.Context, project, branch string, allowUnborn bool) (BranchRemovalLoss, error) {
	result, err := b.AssignedBranchSnapshot(ctx, project, branch, allowUnborn)
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
	if err != nil || !equalLossRefs(result.PRefs, again) {
		return BranchRemovalLoss{}, errors.Join(err, errors.New("P refs changed during branch loss preview"))
	}
	return result, nil
}

func (b *Backend) AssignedBranchSnapshot(ctx context.Context, project, branch string, allowUnborn bool) (BranchRemovalLoss, error) {
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
	if result.AssignedTip == "" {
		if !allowUnborn || len(refs) != 0 {
			return BranchRemovalLoss{}, errors.New("assigned P ref unavailable")
		}
		again, err := b.observeLossHeads(ctx, project)
		if err != nil || len(again) != 0 {
			return BranchRemovalLoss{}, errors.Join(err, errors.New("unborn P refs changed during preview"))
		}
		return result, nil
	}
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
	again, err := b.observeLossHeads(ctx, project)
	if err != nil || !equalLossRefs(refs, again) {
		return BranchRemovalLoss{}, errors.Join(err, errors.New("P refs changed during branch loss preview"))
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
