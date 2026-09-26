package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type sourceFixture struct {
	inspection                      GitInspection
	refs                            []GitRef
	initCalls, headCalls, pageCalls int
	observedOID                     string
	observeCalls, createCalls       int
	createError                     error
	deleteCalls                     int
	deleteError                     error
}

type originFixture struct {
	*sourceFixture
	refs                     []GitOriginRef
	observeCalls, fetchCalls int
	fetched                  string
	publishCalls             int
	published                string
}

func (f *originFixture) ObserveOrigin(context.Context) ([]GitOriginRef, error) {
	f.observeCalls++
	return f.refs, nil
}
func (f *originFixture) FetchOrigin(context.Context) (string, error) {
	f.fetchCalls++
	return f.fetched, nil
}
func (f *originFixture) PublishOrigin(context.Context) (string, error) {
	f.publishCalls++
	return f.published, nil
}

func TestSourceGitOriginWASIBroker(t *testing.T) {
	active := buildSourcePackage(t, "../../plugins/bundled/source-git", true)
	alternate := buildSourcePackage(t, "testdata/git-alternate", false)
	ref := GitOriginRef{Ref: "refs/heads/main", OID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, selection := range []Active{active, alternate} {
		fixture := &originFixture{sourceFixture: &sourceFixture{}, refs: []GitOriginRef{ref}, fetched: ref.CommitOID}
		observed, err := RunSourceGit(context.Background(), selection, GitCommand{Kind: "git.origin.observe", Project: "team/app"}, fixture)
		if err != nil || fixture.observeCalls != 1 || !reflect.DeepEqual(observed.OriginRefs, fixture.refs) {
			t.Fatalf("%s observe: %+v %v", selection.Package.Manifest.ID, observed, err)
		}
		fetched, err := RunSourceGit(context.Background(), selection, GitCommand{Kind: "git.origin.fetch", Project: "team/app", OriginRef: ref.Ref, CommitOID: ref.CommitOID}, fixture)
		if err != nil || fixture.fetchCalls != 1 || fetched.CommitOID != ref.CommitOID {
			t.Fatalf("%s fetch: %+v %v", selection.Package.Manifest.ID, fetched, err)
		}
		fixture.fetched = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		if _, err := RunSourceGit(context.Background(), selection, GitCommand{Kind: "git.origin.fetch", Project: "team/app", OriginRef: ref.Ref, CommitOID: ref.CommitOID}, fixture); err == nil {
			t.Fatal("accepted changed fetch")
		}
		fixture.published = "advanced"
		publication, err := RunSourceGit(context.Background(), selection, GitCommand{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID}, fixture)
		if err != nil || publication.PublicationStatus != "advanced" || fixture.publishCalls != 1 {
			t.Fatalf("%s publish: %+v %v", selection.Package.Manifest.ID, publication, err)
		}
	}
	if _, err := RunSourceGit(context.Background(), active, GitCommand{Kind: "git.origin.observe", Project: "team/app"}, &sourceFixture{}); err == nil {
		t.Fatal("origin fell back to an unscoped broker")
	}
	if _, err := RunSourceGit(context.Background(), active, GitCommand{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID}, &sourceFixture{}); err == nil {
		t.Fatal("publication reached an unscoped broker")
	}
	for _, command := range []GitCommand{
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/tags/v1", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/tags/v1", CommitOID: ref.CommitOID},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: "HEAD"},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID, OriginRef: "refs/heads/other"},
	} {
		fixture := &originFixture{sourceFixture: &sourceFixture{}, published: "created"}
		if _, err := RunSourceGit(context.Background(), active, command, fixture); err == nil || fixture.publishCalls != 0 {
			t.Fatalf("invalid publication reached broker: %+v %v", command, err)
		}
	}
}

func (f *sourceFixture) ObserveSource(context.Context) (string, error) {
	f.observeCalls++
	return f.observedOID, nil
}
func (f *sourceFixture) CreateBranch(context.Context) error {
	f.createCalls++
	return f.createError
}
func (f *sourceFixture) DeleteBranch(context.Context) error {
	f.deleteCalls++
	return f.deleteError
}

func TestSourceGitDeleteUsesOnlySelectedEffect(t *testing.T) {
	active := buildSourcePackage(t, "../../plugins/bundled/source-git", true)
	for _, expected := range []string{"", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		fixture := &sourceFixture{}
		_, err := RunSourceGit(context.Background(), active, GitCommand{Kind: "git.branch.delete", Project: "app", Branch: "main", CommitOID: expected}, fixture)
		if err != nil || fixture.deleteCalls != 1 {
			t.Fatalf("selected delete skipped: expected=%q calls=%d err=%v", expected, fixture.deleteCalls, err)
		}
	}
	for _, bad := range []GitCommand{
		{Kind: "git.branch.delete", Project: "app", Branch: "../other", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Kind: "git.branch.delete", Project: "app", Branch: "main", CommitOID: "HEAD"},
		{Kind: "git.branch.delete", Project: "app", Branch: "main", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Source: &GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}},
	} {
		fixture := &sourceFixture{}
		if _, err := RunSourceGit(context.Background(), active, bad, fixture); err == nil || fixture.deleteCalls != 0 {
			t.Fatalf("malformed delete reached broker: %+v %v", bad, err)
		}
	}
}

func TestSourceGitDeleteCannotRepeatAfterUncertainBrokerEffect(t *testing.T) {
	adversarial := buildSourcePackage(t, "testdata/git-alternate", false)
	fixture := &sourceFixture{deleteError: errors.New("native CAS outcome unknown")}
	_, err := RunSourceGit(context.Background(), adversarial, GitCommand{Kind: "git.branch.delete", Project: "app", Branch: "main", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, fixture)
	if err == nil || fixture.deleteCalls != 1 {
		t.Fatalf("uncertain effect repeated: calls=%d err=%v", fixture.deleteCalls, err)
	}
}

func TestSourceGitCreateCannotRepeatAfterUncertainBrokerEffect(t *testing.T) {
	adversarial := buildSourcePackage(t, "testdata/git-alternate", false)
	fixture := &sourceFixture{createError: errors.New("native CAS outcome unknown")}
	_, err := RunSourceGit(context.Background(), adversarial, GitCommand{Kind: "git.branch.create", Project: "app", Branch: "main", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, fixture)
	if err == nil || fixture.createCalls != 1 {
		t.Fatalf("uncertain create repeated: calls=%d err=%v", fixture.createCalls, err)
	}
}

func (f *sourceFixture) Inspect(context.Context) (GitInspection, error) { return f.inspection, nil }
func (f *sourceFixture) Init(context.Context) error {
	f.initCalls++
	f.inspection.Exists = true
	return nil
}
func (f *sourceFixture) SetHead(context.Context) error {
	f.headCalls++
	f.inspection.Head = "refs/heads/main"
	return nil
}
func (f *sourceFixture) NextRefs(_ context.Context, offset, size int) ([]GitRef, bool, error) {
	f.pageCalls++
	end := offset + size
	if end > len(f.refs) {
		end = len(f.refs)
	}
	return f.refs[offset:end], end == len(f.refs), nil
}

func buildSourcePackage(t *testing.T, source string, asset bool) Active {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler is unavailable")
	}
	dir := t.TempDir()
	for _, name := range []string{"plugin.json"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if asset {
		data, err := os.ReadFile(filepath.Join(source, "p-git-ssh"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "p-git-ssh"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(goPath, "build", "-o", filepath.Join(dir, "git.wasm"), "./"+source)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build source-Git WASI: %v: %s", err, out)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Active{Package: pkg, Grants: []string{"git.project"}}
}

func TestSourceGitWASIOrchestration(t *testing.T) {
	bundled := buildSourcePackage(t, "../../plugins/bundled/source-git", true)
	alt := buildSourcePackage(t, "testdata/git-alternate", false)
	hostile := buildSourcePackage(t, "testdata/git-hostile", false)
	ctx := context.Background()
	fixture := &sourceFixture{}
	_, err := RunSourceGit(ctx, bundled, GitCommand{Kind: "git.project.ensure", Project: "team/app", InitialHead: "refs/heads/main"}, fixture)
	if err != nil || fixture.initCalls != 1 || fixture.headCalls != 1 {
		t.Fatalf("ensure: %v %+v", err, fixture)
	}
	_, err = RunSourceGit(ctx, bundled, GitCommand{Kind: "git.project.ensure", Project: "team/app", InitialHead: "refs/heads/main"}, fixture)
	if err != nil || fixture.initCalls != 1 || fixture.headCalls != 1 {
		t.Fatalf("idempotent ensure: %v %+v", err, fixture)
	}
	fixture.refs = []GitRef{{Ref: "refs/heads/a", OID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {Ref: "refs/heads/b", OID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, {Ref: "refs/heads/c", OID: "cccccccccccccccccccccccccccccccccccccccc"}}
	fixture.pageCalls = 0
	bundleRefs, err := RunSourceGit(ctx, bundled, GitCommand{Kind: "git.refs.list", Project: "team/app", Limit: 3}, fixture)
	if err != nil || !reflect.DeepEqual(bundleRefs.Refs, fixture.refs) || fixture.pageCalls != 1 {
		t.Fatalf("bundled refs: %v %+v pages=%d", err, bundleRefs, fixture.pageCalls)
	}
	fixture.pageCalls = 0
	altRefs, err := RunSourceGit(ctx, alt, GitCommand{Kind: "git.refs.list", Project: "team/app", Limit: 3}, fixture)
	if err != nil || !reflect.DeepEqual(altRefs.Refs, fixture.refs) || fixture.pageCalls != 3 {
		t.Fatalf("alternate refs: %v %+v pages=%d", err, altRefs, fixture.pageCalls)
	}
	base := GitCommand{Kind: "git.transport.plan", Project: "team/app", Service: "receive", Ceilings: &GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}}
	bundledPlan, err := RunSourceGit(ctx, bundled, base, fixture)
	if err != nil || bundledPlan.Plan.MaxInputBytes != GitCoreInputCeiling {
		t.Fatalf("bundled plan: %v %+v", err, bundledPlan.Plan)
	}
	altPlan, err := RunSourceGit(ctx, alt, base, fixture)
	if err != nil || altPlan.Plan.MaxInputBytes != 65536 {
		t.Fatalf("alternate plan: %v %+v", err, altPlan.Plan)
	}
	malicious := &sourceFixture{inspection: GitInspection{Exists: true, Head: "refs/heads/foreign"}}
	if _, err := RunSourceGit(ctx, hostile, GitCommand{Kind: "git.project.ensure", Project: "team/app", InitialHead: "refs/heads/main"}, malicious); err == nil || malicious.headCalls != 0 || malicious.inspection.Head != "refs/heads/foreign" {
		t.Fatalf("hostile existing-repo HEAD mutation: %v %+v", err, malicious)
	}
	fixture.observedOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observed, err := RunSourceGit(ctx, bundled, GitCommand{Kind: "git.source.observe", Project: "team/app", Source: &GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}}, fixture)
	if err != nil || observed.CommitOID != fixture.observedOID || fixture.observeCalls != 1 {
		t.Fatalf("observe source: %v %+v", err, observed)
	}
	_, err = RunSourceGit(ctx, bundled, GitCommand{Kind: "git.branch.create", Project: "team/app", Branch: "feature/new", CommitOID: fixture.observedOID}, fixture)
	if err != nil || fixture.createCalls != 1 {
		t.Fatalf("create branch: %v calls=%d", err, fixture.createCalls)
	}
	for _, command := range []GitCommand{
		{Kind: "git.source.observe", Project: "team/app", Source: &GitSourceSelector{Kind: "branch", Value: "refs/hidden/main"}},
		{Kind: "git.source.observe", Project: "team/app", Source: &GitSourceSelector{Kind: "commit", Value: "HEAD"}},
		{Kind: "git.branch.create", Project: "team/app", Branch: "../evil", CommitOID: fixture.observedOID},
		{Kind: "git.branch.create", Project: "team/app", Branch: "safe", CommitOID: "HEAD"},
	} {
		if _, err := RunSourceGit(ctx, bundled, command, fixture); err == nil {
			t.Fatalf("accepted invalid command: %+v", command)
		}
	}
	if fixture.observeCalls != 1 || fixture.createCalls != 1 {
		t.Fatalf("invalid command reached broker: %+v", fixture)
	}
	for _, command := range []GitCommand{
		{Kind: "git.source.observe", Project: "team/app", Source: &GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}},
		{Kind: "git.branch.create", Project: "team/app", Branch: "safe", CommitOID: fixture.observedOID},
	} {
		if _, err := RunSourceGit(ctx, hostile, command, fixture); err == nil {
			t.Fatalf("hostile wrong-operation request accepted for %s", command.Kind)
		}
	}
	if fixture.observeCalls != 1 || fixture.createCalls != 1 {
		t.Fatalf("hostile request reached source broker: %+v", fixture)
	}
}
