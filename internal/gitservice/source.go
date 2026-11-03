package gitservice

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// inspectBranchRef reads one exact head of a checked repository; its caller
// validated the ref.
func (b *Backend) inspectBranchRef(ctx context.Context, repo, ref string) (string, bool, error) {
	exact := func(args ...string) (string, int, error) {
		cmd := exec.CommandContext(ctx, b.gitPath, append([]string{"-C", repo}, args...)...)
		cmd.Env = b.gitEnv()
		var out, stderr sourceGitOutput
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		e := cmd.Run()
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		if e == nil {
			return strings.TrimSpace(string(out.text)), 0, nil
		}
		var exit *exec.ExitError
		if errors.As(e, &exit) {
			return "", exit.ExitCode(), nil
		}
		return "", 0, errors.New("exact Git ref inspection failed")
	}
	// A symbolic destination occupies the ref even when its target is unborn.
	// Check it first: show-ref follows resolving symbolic refs and reports a
	// dangling one as missing.
	sym, symCode, err := exact("symbolic-ref", "--quiet", ref)
	if err != nil {
		return "", false, err
	}
	if symCode == 0 {
		if sym == "" {
			return "", false, errors.New("empty symbolic ref target")
		}
		return "", false, control.ErrConflict
	}
	if symCode != 1 {
		return "", false, errors.New("symbolic ref inspection failed")
	}
	// Git 2.55 returns 128, not 1, for a missing ref with --hash. Its quiet
	// verification has an unambiguous absence status and emits no native text.
	_, code, err := exact("show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return "", false, err
	}
	if code == 1 {
		return "", false, nil
	}
	if code != 0 {
		return "", false, errors.New("exact Git ref inspection failed")
	}
	oid, code, err := exact("show-ref", "--verify", "--hash", ref)
	if err != nil {
		return "", false, err
	}
	if code != 0 || !validOID(oid) || allZero(oid) {
		return "", false, errors.New("invalid branch tip")
	}
	return oid, true, nil
}

// sourceGitOutput bounds both native stdout and stderr; source methods emit
// only one OID, one type, or one ref. Git failures never expose native text.
type sourceGitOutput struct{ text []byte }

func (o *sourceGitOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-len(o.text) {
		return 0, errors.New("Git source output exceeded limit")
	}
	o.text = append(o.text, p...)
	return len(p), nil
}

func (g *gitBroker) sourceGit(ctx context.Context, args ...string) (string, error) {
	repo, err := g.backend.checkRepository(ctx, g.project)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, g.backend.gitPath, append([]string{"-C", repo}, args...)...)
	cmd.Env = g.backend.gitEnv()
	var stdout, stderr sourceGitOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("Git source operation failed")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(stdout.text)), nil
}

func (g *gitBroker) commitType(ctx context.Context, oid string) error {
	kind, err := g.sourceGit(ctx, "cat-file", "-t", oid)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || kind != "commit" {
		return sourceMissing("source is not a commit")
	}
	return nil
}

func (g *gitBroker) reachableFromHead(ctx context.Context, oid string) error {
	ref, err := g.sourceGit(ctx, "for-each-ref", "--count=1", "--contains="+oid, "--format=%(refname)", "refs/heads/")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") || !validBranch(strings.TrimPrefix(ref, "refs/heads/")) {
		return sourceMissing("commit is not reachable from an ordinary P head")
	}
	return nil
}

// sourceMissing answers that a commit to export does not exist as a source:
// it is missing, not a commit, or not on an ordinary P head.
func sourceMissing(detail string) error {
	return &control.Refusal{Kind: api.KindNotFound, Reason: api.ReasonSource, Cause: fmt.Errorf("%w: %s", control.ErrNotFound, detail)}
}
