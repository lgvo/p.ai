package gitservice

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestProjectRemovalLossAllHeadsAndBoundedHistory(t *testing.T) {
	b, repo := selectedDeleteBackend(t)
	tree := strings.TrimSpace(gitTest(t, nil, "-C", repo, "mktree"))
	// mktree with no stdin yields the ordinary empty tree. Each separate head's
	// history disappears when deleting the entire project, including siblings.
	identity := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	first := gitTest(t, identity, "-C", repo, "commit-tree", tree, "-m", "first")
	second := gitTest(t, identity, "-C", repo, "commit-tree", tree, "-p", first, "-m", "second")
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", first)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/retained", second)
	loss, err := b.ProjectRemovalLoss(context.Background(), "app")
	if err != nil || len(loss.PRefs) != 2 || len(loss.CommitsLosingPReachability) != 2 || !slices.Contains(loss.CommitsLosingPReachability, first) || !slices.Contains(loss.CommitsLosingPReachability, second) {
		t.Fatal("aggregate head reachability incomplete", loss, err)
	}
	for i := 0; i < 255; i++ {
		second = gitTest(t, identity, "-C", repo, "commit-tree", tree, "-p", second, "-m", "bounded")
	}
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/retained", second)
	if _, err = b.ProjectRemovalLoss(context.Background(), "app"); err == nil {
		t.Fatal("oversized history produced incomplete confirmation report")
	}
	if refs, err := b.observeLossHeads(context.Background(), "app"); err != nil || len(refs) != 2 {
		t.Fatal("loss inspection changed refs", err)
	}
}
