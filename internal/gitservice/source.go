package gitservice

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// ObserveSource captures a committed P source. A commit selector is eligible
// only while reachable from an ordinary P head; tags and hidden refs alone do
// not authorize it. The caller holds the project/ref lock when reserving it.
func (b *Backend) ObserveSource(ctx context.Context, project string, source plugin.GitSourceSelector) (string, error) {
	if !validSourceSelector(source) {
		return "", control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return "", err
	}
	if _, err := b.checkRepository(ctx, project); err != nil {
		return "", err
	}
	broker := &gitBroker{backend: b, project: project, source: source}
	outcome, err := plugin.RunSourceGit(ctx, b.selection, plugin.GitCommand{Kind: "git.source.observe", Project: project, Source: &source}, broker)
	return outcome.CommitOID, err
}

// CreateBranch attempts one absent-ref CAS at a previously captured commit.
// An existing destination is a conflict even when it already has that tip.
// Durable create intent, if any, decides whether a later replay may converge.
func (b *Backend) CreateBranch(ctx context.Context, project, branch, capturedOID string) error {
	return b.createBranch(ctx, project, branch, capturedOID, false)
}

// CreateCapturedOriginBranch uses only the exact source recorded by a pending
// session.create operation. The stored request and evidence, not the current
// origin or a caller-selected object, authorize this first ordinary P head.
func (b *Backend) CreateCapturedOriginBranch(ctx context.Context, operationID, project, branch, capturedOID string) error {
	if b.store == nil {
		return control.ErrInvalid
	}
	op, err := b.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	var req control.ReserveSessionRequest
	if err := json.Unmarshal(op.Request, &req); err != nil {
		return control.ErrInvalid
	}
	ev, err := control.Evidence(op)
	if err != nil {
		return err
	}
	if op.Kind != "session.create" || op.Phase != "source-ready" || op.Committed ||
		(op.Status != "running" && op.Status != "blocked") ||
		!control.ValidSessionCreateRequest(req) || req.Key != op.Key ||
		op.Project != project || req.Project != project || req.Branch != branch ||
		req.Choice != "new" || req.Source != "" || req.OriginRef == "" ||
		req.OriginRef != ev.OriginRef || req.ExpectedCommitOID != capturedOID ||
		ev.OriginURL == "" || ev.CapturedOID != capturedOID ||
		!ev.RefCASIntent || ev.BranchExisted || op.SessionUUID == "" {
		return control.ErrInvalid
	}
	session, err := b.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		return err
	}
	if session.Project != project || session.Branch != branch || session.Registry != "creating" {
		return control.ErrConflict
	}
	return b.createBranch(ctx, project, branch, capturedOID, true)
}

func (b *Backend) createBranch(ctx context.Context, project, branch, capturedOID string, capturedOrigin bool) error {
	if !validBranch(branch) || !validOID(capturedOID) || allZero(capturedOID) {
		return control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return err
	}
	if _, err := b.checkRepository(ctx, project); err != nil {
		return err
	}
	_, err := plugin.RunSourceGit(ctx, b.selection, plugin.GitCommand{Kind: "git.branch.create", Project: project, Branch: branch, CommitOID: capturedOID}, &gitBroker{backend: b, project: project, branch: branch, commitOID: capturedOID, capturedOrigin: capturedOrigin})
	return err
}

// InspectBranchRef reads one exact ordinary head. It is used for guarded
// create replay and never changes a ref or accepts an arbitrary Git argument.
func (b *Backend) InspectBranchRef(ctx context.Context, project, branch string) (string, bool, error) {
	if !validBranch(branch) {
		return "", false, control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return "", false, err
	}
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return "", false, err
	}
	return b.inspectBranchRef(ctx, repo, "refs/heads/"+branch)
}

// inspectBranchRef receives a checked repository and validated exact head.
// The public method performs package/repository validation before reaching it.
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

func validSourceSelector(source plugin.GitSourceSelector) bool {
	switch source.Kind {
	case "branch":
		return strings.HasPrefix(source.Value, "refs/heads/") && validBranch(strings.TrimPrefix(source.Value, "refs/heads/"))
	case "commit":
		return validOID(source.Value) && !allZero(source.Value)
	default:
		return false
	}
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
		return errors.New("source is not a commit")
	}
	return nil
}

func (g *gitBroker) reachableFromHead(ctx context.Context, oid string) error {
	ref, err := g.sourceGit(ctx, "for-each-ref", "--count=1", "--contains="+oid, "--format=%(refname)", "refs/heads/")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") || !validBranch(strings.TrimPrefix(ref, "refs/heads/")) {
		return errors.New("commit is not reachable from an ordinary P head")
	}
	return nil
}

func (g *gitBroker) ObserveSource(ctx context.Context) (string, error) {
	if !validSourceSelector(g.source) || g.branch != "" || g.commitOID != "" {
		return "", control.ErrInvalid
	}
	oid := g.source.Value
	if g.source.Kind == "branch" {
		var err error
		oid, err = g.sourceGit(ctx, "show-ref", "--verify", "--hash", g.source.Value)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil || !validOID(oid) || allZero(oid) {
			return "", errors.New("source branch is missing or unborn")
		}
	}
	if err := g.commitType(ctx, oid); err != nil {
		return "", err
	}
	if g.source.Kind == "commit" {
		if err := g.reachableFromHead(ctx, oid); err != nil {
			return "", err
		}
	}
	return oid, nil
}

func (g *gitBroker) CreateBranch(ctx context.Context) error {
	if !validBranch(g.branch) || !validOID(g.commitOID) || allZero(g.commitOID) || g.source.Kind != "" {
		return control.ErrInvalid
	}
	if err := g.commitType(ctx, g.commitOID); err != nil {
		return err
	}
	if !g.capturedOrigin {
		if err := g.reachableFromHead(ctx, g.commitOID); err != nil {
			return err
		}
	}
	// A dangling symbolic ref already occupies the destination. Without
	// --no-deref, update-ref would follow it and create another branch.
	_, err := g.sourceGit(ctx, "update-ref", "--no-deref", "refs/heads/"+g.branch, g.commitOID, strings.Repeat("0", len(g.commitOID)))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	// The native zero-old-OID guard is the authority. This second observation
	// only classifies the failed attempt for the caller; it cannot reset a ref.
	destination := "refs/heads/" + g.branch
	if current, readErr := g.sourceGit(ctx, "show-ref", "--verify", "--hash", destination); readErr == nil && validOID(current) {
		return control.ErrConflict
	}
	if symbolic, readErr := g.sourceGit(ctx, "symbolic-ref", "--quiet", destination); readErr == nil && symbolic != "" {
		return control.ErrConflict
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
