package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type originFixture struct {
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
		fixture := &originFixture{refs: []GitOriginRef{ref}, fetched: ref.CommitOID}
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
	if _, err := RunSourceGit(context.Background(), active, GitCommand{Kind: "git.origin.observe", Project: "team/app"}, nil); err == nil {
		t.Fatal("origin fell back to an unscoped broker")
	}
	if _, err := RunSourceGit(context.Background(), active, GitCommand{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID}, nil); err == nil {
		t.Fatal("publication reached an unscoped broker")
	}
	for _, command := range []GitCommand{
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/tags/v1", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/tags/v1", CommitOID: ref.CommitOID},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: "HEAD"},
		{Kind: "git.origin.publish", Project: "team/app", SourceRef: "refs/heads/main", DestinationRef: "refs/heads/release", CommitOID: ref.CommitOID, OriginRef: "refs/heads/other"},
	} {
		fixture := &originFixture{published: "created"}
		if _, err := RunSourceGit(context.Background(), active, command, fixture); err == nil || fixture.publishCalls != 0 {
			t.Fatalf("invalid publication reached broker: %+v %v", command, err)
		}
	}
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

func TestSourceGitPackageChoosesTransportPlanWithinCeilings(t *testing.T) {
	bundled := buildSourcePackage(t, "../../plugins/bundled/source-git", true)
	alt := buildSourcePackage(t, "testdata/git-alternate", false)
	ctx := context.Background()
	base := GitCommand{Kind: "git.transport.plan", Project: "team/app", Service: "receive", Ceilings: &GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}}
	bundledPlan, err := RunSourceGit(ctx, bundled, base, nil)
	if err != nil || bundledPlan.Plan.MaxInputBytes != GitCoreInputCeiling {
		t.Fatalf("bundled plan: %v %+v", err, bundledPlan.Plan)
	}
	altPlan, err := RunSourceGit(ctx, alt, base, nil)
	if err != nil || altPlan.Plan.MaxInputBytes != 65536 {
		t.Fatalf("alternate plan: %v %+v", err, altPlan.Plan)
	}
}

func TestSourceGitRefusesABrokerMethodOfAnotherCommand(t *testing.T) {
	hostile := buildSourcePackage(t, "testdata/git-hostile", false)
	ref := GitOriginRef{Ref: "refs/heads/main", OID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CommitOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, command := range []GitCommand{
		{Kind: "git.transport.plan", Project: "team/app", Service: "receive", Ceilings: &GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}},
		{Kind: "git.origin.observe", Project: "team/app"},
		{Kind: "git.origin.fetch", Project: "team/app", OriginRef: ref.Ref, CommitOID: ref.CommitOID},
	} {
		fixture := &originFixture{refs: []GitOriginRef{ref}, fetched: ref.CommitOID, published: "created"}
		if _, err := RunSourceGit(context.Background(), hostile, command, fixture); err == nil || fixture.publishCalls != 0 {
			t.Fatalf("%s let the package publish: %v, %d pushes", command.Kind, err, fixture.publishCalls)
		}
	}
}
