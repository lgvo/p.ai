package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// fakeImages is an in-memory image driver. It keeps images and the builders
// a build left behind, and records every build that passed its gate. It
// offers the image operations and finds no session runtime; any other call
// panics.
type fakeImages struct {
	runtimedriver.Driver
	mu         sync.Mutex
	images     map[runtimedriver.ImageRef]runtimedriver.Image
	builders   map[string]bool // build references whose builder remains
	builds     []runtimedriver.Build
	bundles    []bool // whether each build's bundle was a file when it ran
	shell      runtimedriver.Shell
	fail       error // a definite failure after the gate, leaving nothing
	unknown    bool  // publish the image, keep the builder, report an unknown outcome
	settlement runtimedriver.Settlement
	settles    int
	deletes    []string // build references whose leftovers Delete was asked to remove
	stopAfter  error    // returned once Delete removed a builder, as if P stopped there
	next       int
}

func newFakeImages() *fakeImages {
	return &fakeImages{images: map[runtimedriver.ImageRef]runtimedriver.Image{}, builders: map[string]bool{},
		shell: runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: strings.Repeat("d", 64)}}
}

func (f *fakeImages) Describe() runtimedriver.Capabilities {
	return runtimedriver.Capabilities{ImageBuilder: "fake-builder/1"}
}

func (f *fakeImages) BuildImage(_ context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	info, statErr := os.Stat(b.Source.Bundle)
	if err := g(); err != nil {
		return runtimedriver.Image{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, b)
	f.bundles = append(f.bundles, statErr == nil && info.Mode().IsRegular())
	if f.fail != nil {
		return runtimedriver.Image{}, f.fail
	}
	f.next++
	img := runtimedriver.Image{Ref: runtimedriver.ImageRef(fmt.Sprintf("%064x", f.next)), Labels: maps.Clone(b.Labels), Shell: f.shell, Size: 1 << 30}
	f.images[img.Ref] = img
	if f.unknown {
		f.builders[b.Ref.ID] = true
		return runtimedriver.Image{}, fmt.Errorf("publish reply lost: %w", runtimedriver.ErrUnknownOutcome)
	}
	return img, nil
}

func (f *fakeImages) Images(context.Context) ([]runtimedriver.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []runtimedriver.Image
	for _, img := range f.images {
		all = append(all, img)
	}
	return all, nil
}

func (f *fakeImages) InspectImage(_ context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	img, found := f.images[ref]
	return img, found, nil
}

func (f *fakeImages) DeleteImage(_ context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, found := f.images[img.Ref]
	if !found {
		return nil
	}
	if !maps.Equal(current.Labels, img.Labels) {
		return runtimedriver.ErrMismatch
	}
	if err := g(); err != nil {
		return err
	}
	delete(f.images, img.Ref)
	return nil
}

func (f *fakeImages) Settle(context.Context, runtimedriver.Effect) (runtimedriver.Settlement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settles++
	return f.settlement, nil
}

func (f *fakeImages) Delete(_ context.Context, r runtimedriver.Ref, _ runtimedriver.Incarnation, g runtimedriver.Gate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, r.ID)
	if !f.builders[r.ID] {
		return nil
	}
	if err := g(); err != nil {
		return err
	}
	delete(f.builders, r.ID)
	return f.stopAfter
}

func (f *fakeImages) projectImages(project string) []runtimedriver.Image {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []runtimedriver.Image
	for _, img := range f.images {
		if img.Labels[runtimedriver.LabelProject] == project {
			found = append(found, img)
		}
	}
	return found
}

func (f *fakeImages) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

func (f *fakeImages) Inspect(context.Context, runtimedriver.Session) (runtimedriver.Observation, error) {
	return runtimedriver.Observation{State: runtimedriver.StateAbsent}, nil
}

// imageFixture is a lifecycle with image builders configured, a real store
// and P repository for project "app", and the fake image driver. The
// repository's main branch holds commits[0]; its next branch holds
// commits[1], a child of commits[0].
type imageFixture struct {
	l       *lifecycle
	driver  *fakeImages
	raw     *sql.DB
	commits []string
}

func newImageFixture(t *testing.T) *imageFixture {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	store, err := control.OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Background work, such as image release, ends with the test.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	source, repo, tip := gitSource(t, store)
	// The image builder's bundle still comes from gitservice.
	backend := gitserviceFixture(t, store)
	dispatchGit(t, repo, "update-ref", "refs/heads/main", tip)
	next := dispatchGit(t, repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", dispatchGit(t, repo, "mktree"), "-p", tip, "-m", "next")
	dispatchGit(t, repo, "update-ref", "refs/heads/next", next)
	instance, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(state, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	driver := newFakeImages()
	l := &lifecycle{ctx: ctx, store: store, git: &gitCapability{backend: backend, driver: source, info: testGitCapability().info}, driver: driver, instanceID: instance,
		cfg: control.RuntimeConfig{BaseImageFingerprint: strings.Repeat("a", 64), EndpointPrefix: "/var/lib/p-endpoints",
			Environment: &control.EnvironmentConfig{System: "x86_64-linux", BuilderStoragePool: "builders"}}}
	return &imageFixture{l: l, driver: driver, raw: raw, commits: []string{tip, next}}
}

func imageTestSelection() control.CreationSelection {
	return control.CreationSelection{HostID: "host", HostSHA256: strings.Repeat("2", 64), SourceID: "source", SourceSHA256: strings.Repeat("3", 64)}
}

// create admits a creation of session branch from commit, assigned its
// branch, as image-ready finds it.
func (f *imageFixture) create(t *testing.T, branch, commit string) api.Operation {
	t.Helper()
	ctx := f.l.ctx
	op, _, err := f.l.store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: branch, Project: "app", Branch: branch, Choice: "existing"},
		f.l.cfg.BaseImageFingerprint, imageTestSelection(), func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return commit, true, nil
		}, f.l.environmentIntent())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.l.store.AdvanceOperation(ctx, op.ID, "running", "branch-assigned", true, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	return f.load(t, op.ID)
}

func (f *imageFixture) load(t *testing.T, id string) api.Operation {
	t.Helper()
	op, err := f.l.store.GetOperation(f.l.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (f *imageFixture) ensure(t *testing.T, op *api.Operation) (string, error) {
	t.Helper()
	ev, err := control.Evidence(*op)
	if err != nil {
		t.Fatal(err)
	}
	return f.l.ensureImage(f.l.ctx, op, ev)
}

func (f *imageFixture) evidence(t *testing.T, id string) control.CreationEvidence {
	t.Helper()
	ev, err := control.Evidence(f.load(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func (f *imageFixture) row(t *testing.T, commit string) (control.CommitImage, bool) {
	t.Helper()
	key := control.CommitImageKey{Project: "app", Commit: commit, BaseImage: f.l.cfg.BaseImageFingerprint, System: "x86_64-linux", BuilderVersion: "fake-builder/1"}
	row, found, err := f.l.store.GetCommitImage(f.l.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return row, found
}

// setRegistry moves a session's registry state the way its lifecycle does,
// or removes its row as the last step of a removal does.
func (f *imageFixture) setRegistry(t *testing.T, session, registry string) {
	t.Helper()
	var err error
	switch registry {
	case "established":
		err = f.l.store.AdvanceSessionRegistry(f.l.ctx, session, "creating", "established")
	case "removing":
		err = f.l.store.AdvanceSessionRegistry(f.l.ctx, session, "established", "removing")
	case "gone":
		_, err = f.raw.Exec(`DELETE FROM sessions WHERE uuid=?`, session)
	}
	if err != nil {
		t.Fatal(err)
	}
}
