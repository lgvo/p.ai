package gitservice

import (
	"context"
	"testing"
)

func TestSessionRemovalAcceptsAMissingAssignedRef(t *testing.T) {
	b, repo := selectedBackend(t)
	ctx := context.Background()
	tree := bareGit(t, repo, nil, "mktree")
	base := bareCommit(t, repo, tree, "")
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", base)
	work := bareCommit(t, repo, tree, base)
	bareGit(t, repo, nil, "update-ref", "refs/heads/work", work)
	// The session's branch ref is deleted outside P; its commit stays in the
	// repository unreferenced.
	bareGit(t, repo, nil, "update-ref", "-d", "refs/heads/work", work)

	snapshot, err := b.AssignedBranchSnapshot(ctx, "app", "work", true)
	if err != nil || snapshot.AssignedRef != "refs/heads/work" || snapshot.AssignedTip != "" ||
		len(snapshot.PRefs) != 1 || snapshot.PRefs[0].Ref != "refs/heads/main" || snapshot.PRefs[0].OID != base {
		t.Fatalf("discard snapshot of a missing branch: %+v %v", snapshot, err)
	}
	loss, err := b.BranchRemovalLoss(ctx, "app", "work", true)
	if err != nil || loss.AssignedTip != "" || len(loss.CommitsLosingPReachability) != 0 || len(loss.PRefs) != 1 {
		t.Fatalf("delete of a missing branch must remove nothing from P's repository: %+v %v", loss, err)
	}
	if _, err := b.BranchRemovalLoss(ctx, "app", "work", false); err == nil {
		t.Fatal("a review that needs the branch accepted its absence")
	}
}
