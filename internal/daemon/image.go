package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// createdRuntime reads whether a creating session's runtime exists.
type createdRuntime interface {
	InspectCreated(context.Context, runtimeincus.Session) (runtimeincus.Observation, error)
}

// ensureImage brings a creation with image builders to image-ready and
// returns the image its runtime is created from. It reads only the commit
// captured in the creation's evidence, never the branch's current tip.
func (l *lifecycle) ensureImage(ctx context.Context, op *api.Operation, ev control.CreationEvidence) (string, error) {
	return l.ensureImageWith(ctx, op, ev, l.runtime)
}

func (l *lifecycle) ensureImageWith(ctx context.Context, op *api.Operation, ev control.CreationEvidence, created createdRuntime) (string, error) {
	image, err := l.readyImage(ctx, op, ev, created)
	if err != nil {
		return "", err
	}
	// The builder is gone once the image is ready, so an address slot held
	// only for it goes back.
	return image, l.store.ReleaseBuilderPublicAddress(ctx, op.SessionUUID)
}

func (l *lifecycle) readyImage(ctx context.Context, op *api.Operation, ev control.CreationEvidence, created createdRuntime) (string, error) {
	current := l.environmentIntent()
	if current == nil || ev.Environment == nil || *current != *ev.Environment || ev.CapturedOID == "" {
		return "", refusalBecause(api.KindConflict, api.ReasonImageBuilderChanged, api.TargetSession(op.SessionUUID), control.ErrConflict, errors.New("pinned image builder selection unavailable"))
	}
	// An already-created runtime keeps its private root even if its image was
	// later removed.
	if ev.Image != nil && phaseRank(op.Phase) >= phaseRank("runtime-created") {
		return ev.ImageFingerprint, nil
	}
	key := commitImageKey(op.Project, ev)
	// The commit's lock keeps release from deleting the image this creation
	// finds, and keeps two creations from building the same commit at once.
	release, err := l.lockCommit(ctx, op.Project, ev.CapturedOID)
	if err != nil {
		return "", err
	}
	defer release()
	if ev.Image != nil {
		remains, err := l.acceptedImageRemains(ctx, op, ev, key, created)
		if err != nil {
			return "", err
		}
		if remains {
			return ev.ImageFingerprint, nil
		}
		// A verified external removal: forget the image and build one again
		// from the captured commit.
		if err = l.store.ForgetCommitImage(ctx, key, ev.Image.Ref); err != nil {
			return "", err
		}
		ev.Image, ev.ImageFingerprint = nil, ev.Environment.BaseFingerprint
		if err = l.persistImageEvidence(op, ev, op.Phase); err != nil {
			return "", err
		}
	}
	if b := ev.EnvironmentBuilder; b != nil && b.State == "attempted" {
		accepted, err := l.settleBuild(ctx, op, &ev, key)
		if err != nil {
			return "", err
		}
		if accepted {
			return ev.ImageFingerprint, nil
		}
	} else if b != nil && b.State == "abandoned" {
		if err = l.endAbandonedBuild(ctx, op, &ev, key); err != nil {
			return "", err
		}
	}
	reused, err := l.reuseIndexedImage(ctx, op, &ev, key)
	if err != nil {
		return "", err
	}
	if reused {
		return ev.ImageFingerprint, nil
	}
	return l.buildImage(ctx, op, &ev, key)
}

func commitImageKey(project string, ev control.CreationEvidence) control.CommitImageKey {
	return control.CommitImageKey{Project: project, Commit: ev.CapturedOID, BaseImage: ev.Environment.BaseFingerprint,
		System: ev.Environment.System, BuilderVersion: ev.Environment.ImageBuilder}
}

// imageBuild is a creation's build in one cycle, without its source and
// network: what settling and cleaning up the build need.
func imageBuild(operationID string, cycle uint64, key control.CommitImageKey) runtimedriver.Build {
	ref := control.BuildRef(operationID, cycle)
	return runtimedriver.Build{Ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: ref}, Labels: key.Labels(ref)}
}

// imageFailure answers a failure of the image driver: an effect whose outcome
// is unknown answers outcome_unknown, since P never issues it again, and any
// other failure is the runtime's.
func imageFailure(err error) error {
	if errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		return control.OutcomeUnknown(err)
	}
	return control.Failed(api.ReasonRuntime, err)
}

// buildFailure answers a failed image build. An unknown outcome and an
// object that is not the build's answer as imageFailure does; any other
// failure is the build's own, such as a default devShell the commit declares
// invalid, and its diagnostic says why.
func buildFailure(err error) error {
	if errors.Is(err, runtimedriver.ErrUnknownOutcome) || errors.Is(err, runtimedriver.ErrMismatch) {
		return imageFailure(err)
	}
	return control.Failed(api.ReasonImageBuildFailed, err)
}

func indexedImage(row control.CommitImage) runtimedriver.Image {
	return runtimedriver.Image{Ref: runtimedriver.ImageRef(row.Ref), Labels: row.Labels, Size: row.Size,
		Shell: runtimedriver.Shell{Selection: runtimedriver.Selection(row.Selection), MaterialSHA256: row.MaterialSHA256}}
}

// noIntent is the gate of an effect whose durable intent is already recorded.
func noIntent() error { return nil }

// acceptedImageRemains reports whether the image a creation accepted can
// still create its runtime: the image exists, or the runtime already does.
func (l *lifecycle) acceptedImageRemains(ctx context.Context, op *api.Operation, ev control.CreationEvidence, key control.CommitImageKey, created createdRuntime) (bool, error) {
	img, found, err := l.images.InspectImage(ctx, runtimedriver.ImageRef(ev.Image.Ref))
	if err != nil {
		return false, imageFailure(err)
	}
	if found {
		if !maps.Equal(img.Labels, key.Labels(img.Labels[runtimedriver.LabelBuild])) || img.Shell.Selection != runtimedriver.Selection(ev.Image.Selection) || img.Shell.MaterialSHA256 != ev.Image.MaterialSHA256 {
			return false, imageFailure(fmt.Errorf("%w: accepted image %s changed", runtimedriver.ErrMismatch, ev.Image.Ref))
		}
		return true, nil
	}
	// Incus init can succeed before runtime-created is persisted. Its private
	// root remains valid after the image is removed, so inspect the exact
	// durable instance identity before forgetting the image.
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || session.Project != op.Project || session.Registry != "creating" || session.PolicySHA256 != ev.PolicySHA256 {
		return false, errors.Join(err, errors.New("creating session identity unavailable"))
	}
	owned, err := l.runtimeSession(ctx, session)
	if err != nil {
		return false, err
	}
	// Creation recovery cannot substitute another image or source. The
	// stored session policy and reserved address supply every device.
	if owned.ImageFingerprint != ev.ImageFingerprint || owned.InitialOID != ev.CapturedOID {
		return false, errors.New("creating runtime image or source differs from captured request")
	}
	observed, err := created.InspectCreated(ctx, owned)
	if err != nil {
		return false, control.Failed(api.ReasonRuntime, err)
	}
	return observed.Exists, nil
}

// settleBuild settles the build of a cycle whose outcome is unknown. A build
// that applied is accepted once its leftovers are deleted; any other is
// abandoned. It reports whether the creation accepted the build's image.
func (l *lifecycle) settleBuild(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, key control.CommitImageKey) (bool, error) {
	cycle := ev.EnvironmentBuilder.Cycle
	build := imageBuild(op.ID, cycle, key)
	settled, err := l.images.Settle(ctx, runtimedriver.Effect{Kind: runtimedriver.EffectBuild, Build: build})
	if err != nil {
		return false, imageFailure(err)
	}
	if settled != runtimedriver.Applied {
		// Recorded before the leftovers go: without its builder, the driver
		// could settle the build as Applied with an image whose publication it
		// never verified, so an abandoned build is never settled again.
		ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "abandoned"}
		if err = l.persistImageEvidence(op, *ev, op.Phase); err != nil {
			return false, err
		}
		return false, l.endAbandonedBuild(ctx, op, ev, key)
	}
	img, err := l.imageOfBuild(ctx, build)
	if err != nil {
		return false, err
	}
	if err = l.images.Delete(ctx, build.Ref, noIntent); err != nil {
		return false, imageFailure(err)
	}
	return l.acceptBuiltImage(ctx, op, ev, key, build, cycle, img)
}

// endAbandonedBuild deletes the leftovers of a build settled as not accepted
// and ends its cycle absent. Its image, if it was published, is release's to
// delete.
func (l *lifecycle) endAbandonedBuild(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, key control.CommitImageKey) error {
	cycle := ev.EnvironmentBuilder.Cycle
	if err := l.images.Delete(ctx, imageBuild(op.ID, cycle, key).Ref, noIntent); err != nil {
		return imageFailure(err)
	}
	ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "absent"}
	return l.persistImageEvidence(op, *ev, op.Phase)
}

func (l *lifecycle) imageOfBuild(ctx context.Context, build runtimedriver.Build) (runtimedriver.Image, error) {
	images, err := l.images.Images(ctx)
	if err != nil {
		return runtimedriver.Image{}, imageFailure(err)
	}
	var found []runtimedriver.Image
	for _, img := range images {
		if img.Labels[runtimedriver.LabelBuild] == build.Ref.ID {
			found = append(found, img)
		}
	}
	if len(found) != 1 || !maps.Equal(found[0].Labels, build.Labels) {
		return runtimedriver.Image{}, control.Failed(api.ReasonRuntime, fmt.Errorf("the image of applied build %s is unavailable", build.Ref.ID))
	}
	return found[0], nil
}

// acceptBuiltImage indexes the image of a creation's build and records it as
// the creation's image. An image another creation indexed for the commit
// first wins; this build's image is then deleted, and the cycle ends absent.
func (l *lifecycle) acceptBuiltImage(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, key control.CommitImageKey, build runtimedriver.Build, cycle uint64, img runtimedriver.Image) (bool, error) {
	if !maps.Equal(img.Labels, build.Labels) {
		return false, imageFailure(fmt.Errorf("%w: built image %s labels differ from its build", runtimedriver.ErrMismatch, img.Ref))
	}
	row := control.CommitImage{Key: key, Ref: string(img.Ref), Build: build.Ref.ID, Labels: img.Labels,
		Selection: string(img.Shell.Selection), MaterialSHA256: img.Shell.MaterialSHA256, Size: img.Size}
	err := l.store.PutCommitImage(ctx, row)
	if errors.Is(err, control.ErrConflict) {
		indexed, found, e := l.store.GetCommitImage(ctx, key)
		switch {
		case e != nil:
			return false, e
		case found && indexed.Ref == row.Ref:
			err = nil // indexed before a restart
		case found:
			if e = l.images.DeleteImage(ctx, img, noIntent); e != nil {
				return false, imageFailure(e)
			}
			ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "absent"}
			return false, l.persistImageEvidence(op, *ev, op.Phase)
		}
	}
	if err != nil {
		return false, err
	}
	ev.Image = &control.CreationImage{Ref: row.Ref, Built: true, Selection: row.Selection, MaterialSHA256: row.MaterialSHA256}
	ev.ImageFingerprint = row.Ref
	ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "absent"}
	return true, l.persistImageEvidence(op, *ev, "image-ready")
}

// reuseIndexedImage records the commit's indexed image as the creation's,
// when the image is exactly the one the index names. It forgets a row whose
// image is gone, and finishes the delete of a releasing row, which is never
// reused.
func (l *lifecycle) reuseIndexedImage(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, key control.CommitImageKey) (bool, error) {
	row, found, err := l.store.GetCommitImage(ctx, key)
	if err != nil || !found {
		return false, err
	}
	indexed := indexedImage(row)
	if row.State == "releasing" {
		if err = l.images.DeleteImage(ctx, indexed, noIntent); err != nil {
			return false, imageFailure(err)
		}
		return false, l.store.ForgetCommitImage(ctx, key, row.Ref)
	}
	img, present, err := l.images.InspectImage(ctx, indexed.Ref)
	if err != nil {
		return false, imageFailure(err)
	}
	if !present {
		return false, l.store.ForgetCommitImage(ctx, key, row.Ref)
	}
	if !maps.Equal(img.Labels, row.Labels) || img.Shell != indexed.Shell {
		return false, imageFailure(fmt.Errorf("%w: image %s differs from its index row", runtimedriver.ErrMismatch, row.Ref))
	}
	ev.Image = &control.CreationImage{Ref: row.Ref, Selection: row.Selection, MaterialSHA256: row.MaterialSHA256}
	ev.ImageFingerprint = row.Ref
	return true, l.persistImageEvidence(op, *ev, "image-ready")
}

// buildImage builds the commit's image in a new cycle. The gate records the
// attempt right before the build's first effect: a definite failure ends the
// cycle absent, and an unknown outcome keeps it attempted until settled.
func (l *lifecycle) buildImage(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, key control.CommitImageKey) (string, error) {
	if ev.EnvironmentBuilder == nil {
		return "", errors.New("image build state unavailable")
	}
	cycle := ev.EnvironmentBuilder.Cycle + 1
	dir, err := l.imageBuildDir()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	bundle, err := l.git.backend.ExportBundle(ctx, op.Project, ev.CapturedOID, dir)
	if err != nil {
		return "", control.Failed(api.ReasonGit, err)
	}
	if bundle.Commit != ev.CapturedOID {
		return "", control.Failed(api.ReasonGit, errors.New("exported source differs from the captured commit"))
	}
	address, err := l.reserveBuilderPublicIPv4(ctx, op.SessionUUID)
	if err != nil {
		return "", err
	}
	build := imageBuild(op.ID, cycle, key)
	build.Source = runtimedriver.SourceExport{Bundle: bundle.Path, Ref: bundle.Ref, Commit: bundle.Commit}
	build.Network = runtimedriver.Network{PublicIPv4: address}
	attempted := false
	var gateErr error
	img, err := l.images.BuildImage(ctx, build, func() error {
		next := *ev
		next.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "attempted"}
		if gateErr = l.persistImageEvidence(op, next, op.Phase); gateErr != nil {
			return gateErr
		}
		*ev, attempted = next, true
		return nil
	})
	if err != nil {
		if gateErr != nil {
			// The build never started: the failure is recording its attempt.
			return "", err
		}
		if attempted && !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
			ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: cycle, State: "absent"}
			err = errors.Join(err, l.persistImageEvidence(op, *ev, op.Phase))
		}
		return "", buildFailure(err)
	}
	accepted, err := l.acceptBuiltImage(ctx, op, ev, key, build, cycle, img)
	if err != nil {
		return "", err
	}
	if !accepted {
		return "", errors.New("another image was indexed for the commit during the build")
	}
	return ev.ImageFingerprint, nil
}

// imageBuildDir makes a private directory under P's state for one build's
// source bundle. The caller removes it.
func (l *lifecycle) imageBuildDir() (string, error) {
	parent := filepath.Join(l.store.StateDir(), "image-builds")
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !ownedPrivateDirectory(info) {
		return "", errors.New("image build directory is unsafe")
	}
	return os.MkdirTemp(parent, "build-")
}

func (l *lifecycle) commitLockSlot(project, commit string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.commitLocks == nil {
		l.commitLocks = map[string]chan struct{}{}
	}
	id := project + "\x00" + commit
	slot := l.commitLocks[id]
	if slot == nil {
		slot = make(chan struct{}, 1)
		slot <- struct{}{}
		l.commitLocks[id] = slot
	}
	return slot
}

// lockCommit serializes everything that starts using or deletes the images
// of one source commit. It waits for the holder.
func (l *lifecycle) lockCommit(ctx context.Context, project, commit string) (func(), error) {
	slot := l.commitLockSlot(project, commit)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-slot:
		return func() { slot <- struct{}{} }, nil
	}
}

// tryLockCommit takes a commit's lock only if no one holds it.
func (l *lifecycle) tryLockCommit(project, commit string) (func(), bool) {
	slot := l.commitLockSlot(project, commit)
	select {
	case <-slot:
		return func() { slot <- struct{}{} }, true
	default:
		return nil, false
	}
}

// Builders reach the public internet whenever the host provides public
// egress. A builder uses its session's address slot, reserved for it when the
// session's own policy holds none. An empty address means no NIC.
func (l *lifecycle) reserveBuilderPublicIPv4(ctx context.Context, sessionID string) (string, error) {
	if l.cfg.PublicEgress == nil {
		return "", nil
	}
	slot, err := l.store.ReserveBuilderPublicAddress(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("no public address for the image builder: %w", err)
	}
	return l.cfg.PublicEgress.PublicEgressGuestIPv4(slot)
}

// builderPublicIPv4 is the address a session's builder holds, if any.
func (l *lifecycle) builderPublicIPv4(ctx context.Context, sessionID string) (string, error) {
	if l.cfg.PublicEgress == nil {
		return "", nil
	}
	slot, err := l.store.PublicAddressSlot(ctx, sessionID)
	if errors.Is(err, control.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return l.cfg.PublicEgress.PublicEgressGuestIPv4(slot)
}

func (l *lifecycle) persistImageEvidence(op *api.Operation, ev control.CreationEvidence, phase string) error {
	data, err := json.Marshal(ev)
	if err != nil || len(data) > 16384 {
		return errors.New("image creation evidence exceeds durable bound")
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, true, data, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed, op.Status = phase, data, true, "running"
	l.recordProgress(*op, "running")
	return nil
}
