package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
	bundledPlan, err := RunSourceGit(ctx, bundled, base)
	if err != nil || bundledPlan.Plan.MaxInputBytes != GitCoreInputCeiling {
		t.Fatalf("bundled plan: %v %+v", err, bundledPlan.Plan)
	}
	altPlan, err := RunSourceGit(ctx, alt, base)
	if err != nil || altPlan.Plan.MaxInputBytes != 65536 {
		t.Fatalf("alternate plan: %v %+v", err, altPlan.Plan)
	}
}

// TestSourceGitRefusesABrokerMethodItsCommandDoesNotAllow runs a package
// that asks for an origin push and then prepares its plan: the push request
// alone must fail the invocation.
func TestSourceGitRefusesABrokerMethodItsCommandDoesNotAllow(t *testing.T) {
	hostile := buildSourcePackage(t, "testdata/git-hostile", false)
	command := GitCommand{Kind: "git.transport.plan", Project: "team/app", Service: "receive", Ceilings: &GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}}
	if outcome, err := RunSourceGit(context.Background(), hostile, command); err == nil {
		t.Fatalf("a package that asked for an origin push planned %+v", outcome.Plan)
	}
}
