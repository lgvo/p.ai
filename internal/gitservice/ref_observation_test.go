package gitservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeRefObservationsAreFreshAndBounded(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	b := &Backend{stateDir: t.TempDir(), gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	env := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	tree := gitTest(t, nil, "-C", repo, "mktree")
	oid := gitTest(t, env, "-C", repo, "commit-tree", tree, "-m", "one")
	otherOID := gitTest(t, env, "-C", repo, "commit-tree", tree, "-m", "two")
	for _, name := range []string{"a", "c"} {
		gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/"+name, oid)
	}
	broker := &gitBroker{backend: b, project: "app"}
	first, done, err := broker.NextRefs(context.Background(), 0, 1)
	if err != nil || done || len(first) != 1 || first[0].Ref != "refs/heads/a" || broker.refQueries != 1 {
		t.Fatalf("first read: %v %+v exhausted=%v queries=%d", err, first, done, broker.refQueries)
	}
	// A cached prefetch cannot observe this insertion and changed OID.
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/b", otherOID)
	second, done, err := broker.NextRefs(context.Background(), 1, 1)
	if err != nil || done || len(second) != 1 || second[0].Ref != "refs/heads/b" || second[0].OID != otherOID || broker.refQueries != 2 {
		t.Fatalf("fresh read: %v %+v exhausted=%v queries=%d", err, second, done, broker.refQueries)
	}
	if _, _, err := broker.NextRefs(context.Background(), 1, 1); err == nil || broker.refQueries != 2 {
		t.Fatal("accepted replayed offset or queried Git before rejecting it")
	}
	last, done, err := broker.NextRefs(context.Background(), 2, 1)
	if err != nil || !done || len(last) != 1 || last[0].Ref != "refs/heads/c" {
		t.Fatalf("exact final read: %v %+v exhausted=%v", err, last, done)
	}
	if _, _, err := broker.NextRefs(context.Background(), 3, 1); err == nil || broker.refQueries != 3 {
		t.Fatal("accepted read after exhaustion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := &gitBroker{backend: b, project: "app"}
	if _, _, err := canceled.NextRefs(ctx, 0, 1); err == nil || canceled.refQueries != 1 {
		t.Fatalf("canceled native attempt: err=%v queries=%d", err, canceled.refQueries)
	}

	// Reach exactly the documented ceiling using a single real Git transaction.
	var updates strings.Builder
	for i := 3; i < maxObservedHeads; i++ {
		fmt.Fprintf(&updates, "update refs/heads/z%04d %s\n", i, oid)
	}
	cmd := exec.Command(gitPath, "-C", repo, "update-ref", "--stdin")
	cmd.Stdin = strings.NewReader(updates.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("populate observation ceiling: %v %s", err, out)
	}
	atLimit := &gitBroker{backend: b, project: "app", after: "refs/heads/z1022"}
	refs, done, err := atLimit.NextRefs(context.Background(), 0, 8)
	if err != nil || !done || len(refs) != 1 || refs[0].Ref != "refs/heads/z1023" {
		t.Fatalf("maximum supported observation: %v %+v exhausted=%v", err, refs, done)
	}
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/overflow", oid)
	overflow := &gitBroker{backend: b, project: "app"}
	refs, done, err = overflow.NextRefs(context.Background(), 0, 8)
	if err == nil || len(refs) != 0 || done || overflow.refQueries != 1 || overflow.refOffset != 0 || overflow.after != "" {
		t.Fatalf("overflow did not fail closed: %v %+v exhausted=%v broker=%+v", err, refs, done, overflow)
	}
}

func TestNativeRefOutputLimitIsEnforcedDuringCopy(t *testing.T) {
	// os/exec copies stdout through io.Copy. Exercise that path so an embedded
	// bytes.Buffer.ReadFrom cannot accidentally bypass the writer's bound.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("[p]\noutput="+strings.Repeat("x", maxRefObservationBytes+1)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A real Git config read exercises stdout copying with oversized output.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitPath, "config", "--file", config, "--get", "p.output")
	var output refOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err == nil {
		t.Fatal("native output exceeded bound without error")
	}
	if output.buffer.Len() == 0 || output.buffer.Len() > maxRefObservationBytes {
		t.Fatalf("retained %d bytes", output.buffer.Len())
	}
}
