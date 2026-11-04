package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// ComparePublication decides the relation with `git merge-base
// --is-ancestor` once the destination's objects are fetched.
func (d *Driver) ComparePublication(ctx context.Context, project, url string, source sourcedriver.OID, dest *sourcedriver.OriginRef) (sourcedriver.Relation, error) {
	if !gitnames.OriginURL(url) || !gitnames.OID(string(source)) {
		return "", fmt.Errorf("origin URL or source: %w", sourcedriver.ErrInvalid)
	}
	if dest != nil && (!validOriginRef(*dest) || !strings.HasPrefix(dest.Ref, "refs/heads/")) {
		return "", fmt.Errorf("publication destination: %w", sourcedriver.ErrInvalid)
	}
	repo, err := d.originRepository(ctx, project)
	if err != nil {
		return "", err
	}
	typ, err := d.objectType(ctx, repo, source)
	if err != nil {
		return "", err
	}
	if typ != "commit" {
		return "", fmt.Errorf("the source is not a commit of the project's repository: %w", sourcedriver.ErrInvalid)
	}
	switch {
	case dest == nil:
		return sourcedriver.RelationAbsent, nil
	case dest.OID != dest.Target:
		return "", fmt.Errorf("the origin destination is not a commit: %w", sourcedriver.ErrConflict)
	case dest.OID == source:
		return sourcedriver.RelationEqual, nil
	}
	if err := d.fetchObserved(ctx, repo, url, *dest); err != nil {
		return "", err
	}
	contains, err := d.isAncestor(ctx, repo, source, dest.OID)
	if err != nil {
		return "", err
	}
	if contains {
		return sourcedriver.RelationDestinationContains, nil
	}
	forward, err := d.isAncestor(ctx, repo, dest.OID, source)
	switch {
	case err != nil:
		return "", err
	case forward:
		return sourcedriver.RelationFastForward, nil
	}
	return sourcedriver.RelationDivergent, nil
}

// isAncestor reports whether commit ancestor is reachable from commit
// descendant, itself included.
func (d *Driver) isAncestor(ctx context.Context, repo string, ancestor, descendant sourcedriver.OID) (bool, error) {
	result, err := d.git(ctx, repo, 64, "merge-base", "--is-ancestor", string(ancestor), string(descendant))
	if err != nil {
		return false, err
	}
	// Exit 1 is Git's documented negative answer; any other failure decides
	// nothing.
	switch result.Exit {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, errors.New("comparing the ancestry failed")
}

// Publish pushes p.Commit to the origin's head with one ordinary `git push`
// and reads the origin's answer from its porcelain output.
func (d *Driver) Publish(ctx context.Context, project string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	if err := validPublication(p); err != nil {
		return "", err
	}
	// A publication is a mutation: it proves the storage private first.
	if err := d.prepareStorage(); err != nil {
		return "", err
	}
	tip, exists, err := d.InspectBranch(ctx, project, p.Branch)
	if err != nil {
		return "", err
	}
	if !exists || tip != p.Commit {
		return "", fmt.Errorf("the branch %q no longer names the published commit: %w", p.Branch, sourcedriver.ErrConflict)
	}
	repo, err := d.repository(project)
	if err != nil {
		return "", err
	}
	return d.push(ctx, repo, p, g)
}

// validPublication refuses a publication whose values CheckNames refuses,
// or whose relation is not one a push may follow.
func validPublication(p sourcedriver.Publication) error {
	if !gitnames.OriginURL(p.URL) || !gitnames.Branch(p.Branch) || !gitnames.OID(string(p.Commit)) || !gitnames.OriginRef("refs/heads/"+p.Destination) {
		return fmt.Errorf("publication values: %w", sourcedriver.ErrInvalid)
	}
	if p.Relation != sourcedriver.RelationAbsent && p.Relation != sourcedriver.RelationFastForward {
		return fmt.Errorf("a push follows only an absent or fast-forward relation, not %q: %w", p.Relation, sourcedriver.ErrInvalid)
	}
	return nil
}

// push sends the exact commit to the one destination head, from a scratch
// repository with no config, hooks or refs of the project's, which sees
// repo's objects only through GIT_OBJECT_DIRECTORY. The origin decides
// whether the ordinary push may update; no force and no tags.
func (d *Driver) push(ctx context.Context, repo string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	transport, err := d.scratchRepository(ctx, "origin-push-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(transport)
	dest := "refs/heads/" + p.Destination
	// Without its record, the push must not start: nothing is sent.
	token, remove, err := d.recordOrigin()
	if err != nil {
		return "", fmt.Errorf("the push could not be recorded: %w", err)
	}
	defer remove()
	cmd := d.originCommand(ctx, transport, token, []string{"GIT_OBJECT_DIRECTORY=" + filepath.Join(repo, "objects")},
		"-c", "protocol.allow=never", "-c", "protocol.ssh.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "push.followTags=false",
		"push", "--porcelain", "--no-verify", "--no-follow-tags", p.URL, string(p.Commit)+":"+dest)
	stdout, stderr := &boundedOutput{max: 4096}, &boundedOutput{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := g(); err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		// A push that did not start cannot have reached the origin.
		return "", fmt.Errorf("git push could not start: %w", err)
	}
	err = cmd.Wait()
	return pushAnswer(stdout.buf.String(), dest, p.Relation, err == nil && ctx.Err() == nil)
}

// pushAnswer reads the origin's answer from `git push --porcelain` for the
// one ref pushed: the status the relation leads to when the push succeeded,
// and Refused on a definite rejection. Anything else, from lost contact,
// cancellation or output that does not parse, may follow the origin's
// acceptance, so it is ErrUnknownOutcome.
func pushAnswer(out, dest string, relation sourcedriver.Relation, succeeded bool) (sourcedriver.PublishStatus, error) {
	for _, line := range strings.Split(out, "\n") {
		flag, rest, ok := strings.Cut(line, "\t")
		refs, summary, _ := strings.Cut(rest, "\t")
		if !ok || !strings.HasSuffix(refs, ":"+dest) {
			continue
		}
		switch {
		case succeeded && flag == "=":
			return sourcedriver.PublishSatisfied, nil
		case succeeded && flag == "*" && relation == sourcedriver.RelationAbsent:
			return sourcedriver.PublishCreated, nil
		case succeeded && flag == " " && relation == sourcedriver.RelationFastForward:
			return sourcedriver.PublishAdvanced, nil
		case !succeeded && flag == "!" && (strings.HasPrefix(summary, "[rejected]") || strings.HasPrefix(summary, "[remote rejected]")):
			return sourcedriver.PublishRefused, nil
		}
	}
	return "", fmt.Errorf("the origin's answer to the push is lost or unexpected: %w", sourcedriver.ErrUnknownOutcome)
}
