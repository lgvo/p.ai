package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// OriginPublicationPreview is a single-use comparison made under OriginScope's
// project origin/GC lock. Only the matching scope can act on this value.
type OriginPublicationPreview struct {
	Project        string `json:"project"`
	URL            string `json:"url"`
	SourceRef      string `json:"source_ref"`
	SourceOID      string `json:"source_oid"`
	DestinationRef string `json:"destination_ref"`
	DestinationOID string `json:"destination_oid,omitempty"`
	Relation       string `json:"relation"` // absent, equal, destination_contains, fast_forward, divergent
}

type OriginPublicationResult struct {
	Preview OriginPublicationPreview `json:"preview"`
	Status  string                   `json:"status"` // created, advanced, satisfied, refused, outcome_unknown
}

// PreviewPublication requires Observe of this URL in the same live scope. The
// source is an exact, current ordinary P branch tip supplied by core. An
// observed destination is fetched into the object cache without creating refs.
func (s *OriginScope) PreviewPublication(ctx context.Context, sourceRef, sourceOID, destinationRef string) (OriginPublicationPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	s.publication = nil
	if !s.active || s.url == "" || s.observed == nil || !validPublicationRef(sourceRef, true) || !validPublicationRef(destinationRef, false) || !validOID(sourceOID) || allZero(sourceOID) {
		return OriginPublicationPreview{}, control.ErrInvalid
	}
	repo, err := s.backend.checkRepository(ctx, s.project)
	if err != nil {
		return OriginPublicationPreview{}, err
	}
	tip, exists, err := s.backend.inspectBranchRef(ctx, repo, sourceRef)
	if err != nil {
		return OriginPublicationPreview{}, err
	}
	if !exists || tip != sourceOID {
		return OriginPublicationPreview{}, control.ErrConflict
	}
	typ, err := s.backend.originGit(ctx, repo, nil, "cat-file", "-t", sourceOID)
	if err != nil || strings.TrimSpace(typ) != "commit" {
		return OriginPublicationPreview{}, control.ErrInvalid
	}
	p := OriginPublicationPreview{Project: s.project, URL: s.url, SourceRef: sourceRef, SourceOID: sourceOID, DestinationRef: destinationRef}
	observed, exists := s.observed[destinationRef]
	if !exists {
		p.Relation = "absent"
	} else {
		if observed.OID != observed.CommitOID {
			return OriginPublicationPreview{}, errors.New("origin destination is not a commit")
		}
		p.DestinationOID = observed.OID
		if observed.OID == sourceOID {
			p.Relation = "equal"
		} else {
			if _, err := s.backend.fetchOrigin(ctx, repo, s.url, observed); err != nil {
				s.observed, s.url = nil, ""
				return OriginPublicationPreview{}, err
			}
			ancestor, err := s.backend.originAncestor(ctx, repo, sourceOID, observed.OID)
			if err != nil {
				return OriginPublicationPreview{}, err
			}
			if ancestor {
				p.Relation = "destination_contains"
			} else {
				ancestor, err = s.backend.originAncestor(ctx, repo, observed.OID, sourceOID)
				if err != nil {
					return OriginPublicationPreview{}, err
				}
				if ancestor {
					p.Relation = "fast_forward"
				} else {
					p.Relation = "divergent"
				}
			}
		}
	}
	s.publication = &p
	return p, nil
}

func validPublicationRef(ref string, source bool) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || !plugin.ValidGitOriginRef(ref) {
		return false
	}
	if source {
		return validBranch(strings.TrimPrefix(ref, "refs/heads/"))
	}
	return true
}

func (b *Backend) originAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	// Exit 1 is the documented negative answer. Other failures are unknown.
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, b.gitPath, "-c", "core.hooksPath=/dev/null", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir, cmd.Env = repo, b.originEnv(ssh)
	err = cmd.Run()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, errors.New("origin ancestry comparison failed")
}

// PublishPublication consumes exactly the scoped preview once. A later attempt
// must observe and compare again. No action follows a divergent preview.
func (s *OriginScope) PublishPublication(ctx context.Context, p OriginPublicationPreview) (OriginPublicationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.publication == nil || *s.publication != p || s.url != p.URL || s.observed == nil {
		return OriginPublicationResult{}, control.ErrInvalid
	}
	s.publication = nil
	result := OriginPublicationResult{Preview: p}
	if p.Relation == "divergent" {
		result.Status = "refused"
		return result, nil
	}
	if p.Relation == "equal" || p.Relation == "destination_contains" {
		result.Status = "satisfied"
		return result, nil
	}
	if p.Relation != "absent" && p.Relation != "fast_forward" {
		return OriginPublicationResult{}, control.ErrInvalid
	}
	broker := &originBroker{scope: s, remote: s.url, publication: &p}
	out, err := plugin.RunSourceGit(ctx, s.backend.selection, plugin.GitCommand{Kind: "git.origin.publish", Project: s.project, SourceRef: p.SourceRef, DestinationRef: p.DestinationRef, CommitOID: p.SourceOID}, broker)
	if err != nil {
		if broker.pushAttempted {
			result.Status = "outcome_unknown"
			return result, nil
		}
		return OriginPublicationResult{}, err
	}
	result.Status = out.PublicationStatus
	return result, nil
}

func (o *originBroker) PublishOrigin(ctx context.Context) (string, error) {
	p := o.publication
	if p == nil {
		return "", control.ErrInvalid
	}
	tip, exists, err := o.scope.backend.inspectBranchRef(ctx, o.scope.repo, p.SourceRef)
	if err != nil {
		return "", err
	}
	if !exists || tip != p.SourceOID {
		return "", control.ErrConflict
	}
	return o.scope.backend.pushOrigin(ctx, o.scope.repo, o.remote, *p, &o.pushAttempted)
}

func (b *Backend) pushOrigin(ctx context.Context, repo, remote string, p OriginPublicationPreview, attempted *bool) (string, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil || !filepath.IsAbs(sshPath) {
		return "", errors.New("OpenSSH executable unavailable")
	}
	transport, err := os.MkdirTemp(b.stateDir, "origin-push-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(transport)
	// The clean repository has no project-controlled config, hooks, or refs.
	// It sees P's objects through the explicit object directory only.
	if _, err = b.originGit(ctx, "/", nil, "init", "--bare", "--template=/dev/null", transport); err != nil {
		return "", err
	}
	// The exact OID is the only source and one full heads ref is the only
	// destination. Git's server decides whether the ordinary push can update.
	args := []string{"-c", "protocol.allow=never", "-c", "protocol.ssh.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "push.followTags=false", "push", "--porcelain", "--no-verify", "--no-follow-tags", remote, p.SourceOID + ":" + p.DestinationRef}
	cmd := exec.CommandContext(ctx, b.gitPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Dir, cmd.Env = transport, b.originEnv(sshPath, "GIT_OBJECT_DIRECTORY="+filepath.Join(repo, "objects"))
	var stdout, stderr originOutput
	stdout.max, stderr.max = 4096, 4096
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	*attempted = true
	err = cmd.Run()
	if err != nil && cmd.Process == nil {
		// A failed process start cannot have contacted the origin. Keep this
		// distinct from a started push whose response may have been lost.
		*attempted = false
		return "", errors.New("origin Git push could not start")
	}
	if err == nil && ctx.Err() == nil {
		for _, line := range strings.Split(stdout.buf.String(), "\n") {
			if !strings.Contains(line, ":"+p.DestinationRef+"\t") {
				continue
			}
			switch {
			case strings.HasPrefix(line, "=\t"):
				return "satisfied", nil
			case strings.HasPrefix(line, "*\t") && p.Relation == "absent":
				return "created", nil
			case strings.HasPrefix(line, " \t") && p.Relation == "fast_forward":
				return "advanced", nil
			}
		}
		return "outcome_unknown", nil
	}
	// A porcelain ref rejection is a definite server answer. Lost contact,
	// cancellation, malformed output, and local execution failure may have
	// occurred after acceptance, so they all remain outcome unknown.
	for _, line := range strings.Split(stdout.buf.String(), "\n") {
		if strings.HasPrefix(line, "!\t") && strings.Contains(line, "\t[rejected]") || strings.HasPrefix(line, "!\t") && strings.Contains(line, "\t[remote rejected]") {
			return "refused", nil
		}
	}
	return "outcome_unknown", nil
}
