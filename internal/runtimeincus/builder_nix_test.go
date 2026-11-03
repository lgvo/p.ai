package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

const testNAR = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA+="
const testDrv = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-test-shell.drv"
const testCapture = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env"

func testShownDerivation(system string) []byte {
	raw, _ := json.Marshal(map[string]any{"version": 4, "derivations": map[string]any{filepath.Base(testDrv): map[string]any{"version": 4, "system": system, "outputs": map[string]any{"out": map[string]any{}}}}})
	return raw
}

func testFlakeMetadata(commit string) []byte {
	raw, _ := json.Marshal(map[string]any{"locked": map[string]any{"narHash": testNAR, "rev": commit, "type": "git", "url": "file:///workspace"}, "path": "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-source"})
	return raw
}

func testFlakeEntry() []byte {
	return []byte("100644 blob " + strings.Repeat("b", 40) + "\tflake.nix\x00")
}

func TestBuilderNixPinnedDerivationShowShape(t *testing.T) {
	if err := checkBuilderDerivation(testShownDerivation("x86_64-linux"), testDrv, "x86_64-linux"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":4,"derivations":{"other.drv":{"version":4,"system":"x86_64-linux","outputs":{"out":{}}}}}`),
		[]byte(`{"version":4,"derivations":{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-test-shell.drv":{"version":4,"system":"aarch64-linux","outputs":{"out":{}}}}}`),
		[]byte(`{"version":4,"derivations":{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-test-shell.drv":{"version":4,"system":"x86_64-linux","outputs":{}}}}`),
	} {
		if err := checkBuilderDerivation(raw, testDrv, "x86_64-linux"); err == nil {
			t.Fatalf("accepted nonmatching store derivation: %s", raw)
		}
	}
}

func TestBuilderFlakeEntryFromTheCommitTree(t *testing.T) {
	oid := strings.Repeat("b", 40)
	for raw, want := range map[string]bool{
		"":                                       false,
		"100644 blob " + oid + "\tflake.nix\x00": true,
		"100755 blob " + oid + "\tflake.nix\x00": true,
		"120000 blob " + oid + "\tflake.nix\x00": true,
	} {
		if got, err := parseFlakeEntry([]byte(raw)); err != nil || got != want {
			t.Fatalf("entry %q: present=%t err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"040000 tree " + oid + "\tflake.nix\x00",
		"160000 commit " + oid + "\tflake.nix\x00",
		"100644 blob " + oid + "\tflake.nix",
		"100644 blob " + oid + "\tother.nix\x00",
		"100644 blob " + oid + "\tflake.nix\x00100644 blob " + oid + "\tflake.nix\x00",
		"100644 blob zz\tflake.nix\x00",
	} {
		if _, err := parseFlakeEntry([]byte(raw)); err == nil {
			t.Fatalf("accepted flake entry %q", raw)
		}
	}
}

func TestBuilderFlakeMetadataLocksTheCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	if got, err := parseFlakeNarHash(testFlakeMetadata(commit), commit); err != nil || got != testNAR {
		t.Fatalf("NAR hash: %q %v", got, err)
	}
	for name, raw := range map[string][]byte{
		"other commit": testFlakeMetadata(strings.Repeat("c", 40)),
		"no rev":       []byte(`{"locked":{"narHash":"` + testNAR + `"}}`),
		"not SRI":      []byte(`{"locked":{"narHash":"sha256:0000","rev":"` + commit + `"}}`),
		"not JSON":     []byte("warning: Git tree is dirty"),
	} {
		if _, err := parseFlakeNarHash(raw, commit); err == nil {
			t.Fatalf("%s: accepted flake metadata %s", name, raw)
		}
	}
}

func TestBuilderNixWrongSystemNeverSelects(t *testing.T) {
	r := testBuilder()
	for _, tc := range []struct {
		name, host, base, system string
	}{
		{"foreign request", "", "", "aarch64-linux"},
		{"wrong base image", "", "aarch64", "x86_64-linux"},
		{"host-matched request with wrong base", "aarch64", "", "aarch64-linux"},
		{"unsupported server", "mips64", "", "x86_64-linux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true, hostArchitecture: tc.host}
			f.base.imageArchitecture = tc.base
			b := guestBackend(t, f, sealedGuest())
			calls := 0
			b.runBuilder = func(context.Context, string, []string, []string) ([]byte, error) { calls++; return nil, nil }
			if selected, err := b.ResolveBuilderNix(context.Background(), r, tc.system); err == nil || selected.Selection != "" || calls != 0 {
				t.Fatalf("wrong-system source read before architecture proof: %+v, calls=%d err=%v", selected, calls, err)
			}
		})
	}
}

func TestBuilderNixSelectionAbsentVsInvalid(t *testing.T) {
	r := testBuilder()
	for _, tc := range []struct {
		name, metadata, presence, drv string
		want                          runtimedriver.Selection
		wantErr                       bool
		calls                         int
	}{
		{name: "metadata fails", metadata: "error", wantErr: true, calls: 1},
		{name: "missing default", presence: "false", want: runtimedriver.SelectionNoDefault, calls: 2},
		{name: "malformed devShells", presence: "error", wantErr: true, calls: 2},
		{name: "present invalid default", presence: "true", drv: "error", wantErr: true, calls: 3},
		{name: "present valid default", presence: "true", drv: testDrv, want: runtimedriver.SelectionDevShell, calls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			run := func(args ...string) ([]byte, error) {
				calls = append(calls, slices.Clone(args))
				switch args[0] {
				case "flake":
					if !slices.Equal(args, []string{"flake", "metadata", "--json", "git+file:///workspace?rev=" + r.CommitOID}) {
						t.Fatalf("metadata of something other than the commit in the clone: %q", args)
					}
					if tc.metadata == "error" {
						return nil, errors.New("cannot update unlocked flake input")
					}
					return testFlakeMetadata(r.CommitOID), nil
				case "eval":
					expr := args[len(args)-1]
					escaped := strings.NewReplacer("+", "%2B", "=", "%3D").Replace(testNAR)
					if !strings.Contains(expr, `builtins.getFlake "git+file:///workspace?narHash=`+escaped+`&rev=`+r.CommitOID+`"`) {
						t.Fatalf("unlocked or wrong flake expression: %q", args)
					}
					if slices.Contains(args, "--json") {
						if tc.presence == "error" {
							return nil, errors.New("Nix type error")
						}
						return []byte(tc.presence), nil
					}
					if tc.drv == "error" {
						return nil, errors.New("Nix invalid default")
					}
					if !strings.Contains(expr, "shell.type or null") || !strings.Contains(expr, `shell.system or null`) {
						t.Fatalf("missing derivation/system predicate: %q", args)
					}
					return []byte(tc.drv), nil
				case "derivation":
					if !slices.Equal(args, []string{"derivation", "show", testDrv}) {
						t.Fatalf("unexpected derivation proof: %q", args)
					}
					return testShownDerivation("x86_64-linux"), nil
				}
				return nil, errors.New("unexpected command")
			}
			got, err := resolveBuilderNixInputs(r, "x86_64-linux", run)
			if (err != nil) != tc.wantErr || err == nil && got.Selection != tc.want || len(calls) != tc.calls {
				t.Fatalf("selection=%+v error=%v calls=%q", got, err, calls)
			}
			if tc.want == runtimedriver.SelectionNoDefault && (got.SourceNarHash != testNAR || got.DerivationPath != "") {
				t.Fatalf("no-default selection: %+v", got)
			}
			if tc.want == runtimedriver.SelectionDevShell && (got.SourceNarHash != testNAR || got.DerivationPath != testDrv || got.DevShellAttr != "devShells.x86_64-linux.default") {
				t.Fatalf("incomplete selection: %+v", got)
			}
		})
	}
	_, err := resolveBuilderNixInputs(r, "x86_64-linux", func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "flake":
			return testFlakeMetadata(r.CommitOID), nil
		case slices.Contains(args, "--json"):
			return []byte("true"), nil
		case slices.Contains(args, "--raw"):
			return []byte(testDrv), nil
		}
		return testShownDerivation("aarch64-linux"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "requested system") {
		t.Fatalf("accepted wrong-system derivation: %v", err)
	}
}

func TestBuilderNixCommandOutputBound(t *testing.T) {
	if out, err := runCommandBounded(context.Background(), "/bin/sh", []string{"-c", "head -c 2097153 /dev/zero"}, nil, 2<<20, 64<<10, true); err == nil || !strings.Contains(err.Error(), "exceeded bound") {
		t.Fatalf("accepted oversized Nix output: len=%d error=%v", len(out), err)
	}
	if _, err := runCommandBounded(context.Background(), "/bin/sh", []string{"-c", "head -c 65537 /dev/zero >&2"}, nil, 2<<20, 64<<10, true); err == nil || !strings.Contains(err.Error(), "exceeded bound") {
		t.Fatalf("accepted oversized Nix error: %v", err)
	}
}

func TestBuilderCommandCancellationBoundsPipeRetainingChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "detached-child-started")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runCommandBounded(ctx, os.Args[0], []string{"-test.run=^TestBuilderPipeHelper$"}, []string{"P_TEST_PIPE_HELPER=parent", "P_TEST_PIPE_MARKER=" + marker}, 2<<20, 64<<10, true)
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("detached pipe-retaining child never started: %v; runner error=%v", statErr, err)
	}
	if err == nil || time.Since(start) < 500*time.Millisecond || time.Since(start) > 2*time.Second {
		t.Fatalf("pipe-retaining child blocked cancellation: elapsed=%s err=%v", time.Since(start), err)
	}
}

func TestBuilderPipeHelper(t *testing.T) {
	switch os.Getenv("P_TEST_PIPE_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestBuilderPipeHelper$")
		child.Env = []string{"P_TEST_PIPE_HELPER=child"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(91)
		}
		if err := os.WriteFile(os.Getenv("P_TEST_PIPE_MARKER"), []byte("started"), 0600); err != nil {
			os.Exit(92)
		}
		time.Sleep(2 * time.Second)
		os.Exit(0)
	case "child":
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
}

// nixGuest answers a running builder's fixed commands as a clone at the
// commit with a valid default devShell would.
type nixGuest struct {
	t          *testing.T
	commit     string
	flake      bool
	failBuild  bool
	nixCalls   int
	captures   int
	gitCommits []string
}

func (g *nixGuest) run(_ context.Context, _ string, argv, env []string) ([]byte, error) {
	t := g.t
	if !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || argv[3] != "exec" || argv[slices.Index(argv, "--user")+1] != "1000" || argv[slices.Index(argv, "--cwd")+1] != "/workspace" ||
		!slices.Contains(argv, "NIX_CONFIG=") || !slices.Contains(argv, "NIX_PATH=") || !slices.Contains(argv, "LD_PRELOAD=") || !slices.Contains(argv, "BASH_ENV=") {
		t.Fatalf("unsafe builder exec argv: %q", argv)
	}
	if git := guestGit(t, argv); git != nil {
		if !slices.Equal(git, []string{"ls-tree", "-z", g.commit, "--", "flake.nix"}) {
			t.Fatalf("unexpected Git command: %q", git)
		}
		g.gitCommits = append(g.gitCommits, git[2])
		if !g.flake {
			return nil, nil
		}
		return testFlakeEntry(), nil
	}
	guest := argv[slices.Index(argv, "--")+1:]
	switch guest[0] {
	case "/bin/sh":
		return nil, nil
	case "/run/current-system/sw/bin/readlink":
		if !slices.Equal(guest[1:], []string{"-f", builderCaptureProfile}) {
			t.Fatalf("unexpected capture profile read: %q", guest)
		}
		return []byte(testCapture + "\n"), nil
	case builderNixBinary:
	default:
		t.Fatalf("wrong guest binary: %q", guest)
	}
	g.nixCalls++
	switch {
	case slices.Contains(guest, "--version"):
		return []byte("nix (Nix) 2.34.8\n"), nil
	case slices.Contains(guest, "info"):
		return []byte(`{"url":"daemon","version":"2.34.8","trusted":false}`), nil
	case slices.Contains(guest, "config"):
		return []byte(`{"sandbox":{"value":false}}`), nil
	case slices.Contains(guest, "metadata"):
		if !slices.Contains(guest, "--no-update-lock-file") || !slices.Contains(guest, "--no-write-lock-file") || !slices.Contains(guest, "--offline") {
			t.Fatalf("metadata may change or fetch the lock: %q", guest)
		}
		return testFlakeMetadata(g.commit), nil
	case slices.Contains(guest, "--json") && slices.Contains(guest, "eval"):
		return []byte("true"), nil
	case slices.Contains(guest, "--raw"):
		return []byte(testDrv), nil
	case slices.Contains(guest, "derivation"):
		return testShownDerivation("x86_64-linux"), nil
	case slices.Contains(guest, "build"):
		if g.failBuild {
			return nil, errors.New("offline derivation failed")
		}
		return nil, nil
	case slices.Contains(guest, "print-dev-env"):
		if !slices.Contains(guest, "--profile") || !slices.Contains(guest, builderCaptureProfile) || !slices.Contains(guest, builderLockedFlake(testNAR, g.commit)+"#devShells.x86_64-linux.default") {
			t.Fatalf("capture of something other than the locked commit: %q", guest)
		}
		g.captures++
		return []byte(`{"bashFunctions":{},"variables":{"outputs":{"type":"var","value":"out"},"out":{"type":"exported","value":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo"},"NATIVE_VALUE":{"type":"exported","value":"ok"}}}`), nil
	case slices.Contains(guest, "path-info"):
		if !slices.Contains(guest, testCapture) {
			t.Fatalf("wrong captured path verified: %q", guest)
		}
		return []byte(testCapture + "\n"), nil
	}
	return nil, errors.New("unexpected Nix command")
}

func TestBuilderNixBackendResolveRealizeAndExecBoundary(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := guestBackend(t, f, sealedGuest())
	g := &nixGuest{t: t, commit: r.CommitOID, flake: true}
	b.runBuilder = g.run
	selected, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux")
	if err != nil || selected.Selection != runtimedriver.SelectionDevShell || selected.SourceNarHash != testNAR {
		t.Fatalf("resolve: %+v %v", selected, err)
	}
	got, err := b.RealizeBuilderNix(context.Background(), r, selected)
	if err != nil || got.MaterialDigest == "" || got.CaptureStorePath != testCapture || !strings.Contains(got.Material.Script, "NATIVE_VALUE='ok'") || got.Selection != selected {
		t.Fatalf("realize: %+v %v", got, err)
	}
	g.failBuild = true
	beforeCapture := g.captures
	failed, err := b.RealizeBuilderNix(context.Background(), r, selected)
	if err == nil || failed.MaterialDigest != "" || failed.Material.Schema != "" || g.captures != beforeCapture {
		t.Fatalf("failed derivation yielded capture: %+v err=%v captures=%d", failed, err, g.captures)
	}
	noDefault := selected
	noDefault.Selection = runtimedriver.SelectionNoDefault
	if _, err := b.RealizeBuilderNix(context.Background(), r, noDefault); err == nil {
		t.Fatal("realized a selection without a devShell")
	}
	f.foreign = true
	before := g.nixCalls
	if _, err := b.builderGuestExec(context.Background(), r, builderNixBinary, "--version"); err == nil || g.nixCalls != before {
		t.Fatalf("foreign identity reached guest exec: %v calls=%d", err, g.nixCalls)
	}
}

func TestBuilderNixNoFlakeRunsNoNix(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := guestBackend(t, f, sealedGuest())
	g := &nixGuest{t: t, commit: r.CommitOID}
	b.runBuilder = g.run
	selected, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux")
	if err != nil || selected.Selection != runtimedriver.SelectionNoFlake || selected.SourceNarHash != "" || g.nixCalls != 0 || !slices.Equal(g.gitCommits, []string{r.CommitOID}) {
		t.Fatalf("no-flake commit: %+v nix=%d git=%q err=%v", selected, g.nixCalls, g.gitCommits, err)
	}
}

func TestBuilderNixRefusesAChangedBundle(t *testing.T) {
	r := testBuilder()
	for name, edit := range map[string]func(*fakeBuilderFiles){
		"bundle missing":    func(g *fakeBuilderFiles) { delete(g.files, builderBundle) },
		"bundle writable":   func(g *fakeBuilderFiles) { f := g.files[builderBundle]; f.mode = 0666; g.files[builderBundle] = f },
		"bundle user-owned": func(g *fakeBuilderFiles) { f := g.files[builderBundle]; f.uid = 1000; g.files[builderBundle] = f },
		"directory open":    func(g *fakeBuilderFiles) { f := g.files[builderBuildDir]; f.mode = 0777; g.files[builderBuildDir] = f },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
			guest := sealedGuest()
			edit(guest)
			b := guestBackend(t, f, guest)
			calls := 0
			b.runBuilder = func(context.Context, string, []string, []string) ([]byte, error) { calls++; return nil, nil }
			if _, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux"); err == nil || calls != 0 {
				t.Fatalf("resolved from a changed bundle: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBuilderNixInterruptedExecStopsVerifiedBuilder(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := guestBackend(t, f, sealedGuest())
	ctx, cancel := context.WithCancel(context.Background())
	b.runBuilder = func(ctx context.Context, _ string, _, _ []string) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := b.builderGuestExec(ctx, r, builderNixBinary, "--version"); err == nil || !strings.Contains(err.Error(), "stopped after interrupted") || f.status != "Stopped" || !slices.Equal(f.mutations, []string{"stop"}) {
		t.Fatalf("cancel did not stop verified builder: %v status=%s mutations=%q", err, f.status, f.mutations)
	}
}

func TestBuilderNixReadyRequiresLiveDaemonRPC(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := guestBackend(t, f, sealedGuest())
	var probes int
	b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
		i := slices.Index(argv, "--")
		if i < 0 {
			t.Fatalf("missing guest command: %q", argv)
		}
		guest := argv[i+1:]
		if guest[0] == "/bin/sh" {
			return nil, nil // a stale socket path still exists
		}
		if !slices.Contains(guest, builderNixBinary) {
			t.Fatalf("unexpected readiness binary: %q", guest)
		}
		switch {
		case slices.Contains(guest, "info"):
			probes++
			if probes == 1 {
				return nil, errors.New("connection refused")
			}
			return []byte(`{"url":"daemon","version":"2.34.8"}`), nil
		case slices.Contains(guest, "--version"):
			return []byte("nix (Nix) 2.34.8\n"), nil
		case slices.Contains(guest, "config"):
			return []byte(`{"sandbox":{"value":false}}`), nil
		}
		t.Fatalf("unexpected readiness command: %q", guest)
		return nil, nil
	}
	if err := b.builderNixReady(context.Background(), r); err != nil || probes != 2 {
		t.Fatalf("did not retry a stale daemon socket: probes=%d err=%v", probes, err)
	}
	probes = 0
	b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
		i := slices.Index(argv, "--")
		guest := argv[i+1:]
		if guest[0] == "/bin/sh" {
			return nil, nil
		}
		if slices.Contains(guest, "info") {
			probes++
			return []byte(`{"url":"daemon","version":"9.99"}`), nil
		}
		t.Fatalf("continued after wrong daemon version: %q", guest)
		return nil, nil
	}
	if err := b.builderNixReady(context.Background(), r); err == nil || !strings.Contains(err.Error(), "identity changed") || probes != 1 {
		t.Fatalf("wrong daemon version was retried or accepted: probes=%d err=%v", probes, err)
	}
}

func TestPublicBuilderStartPreparesNetworkAsRootAfterBoot(t *testing.T) {
	for _, public := range []bool{false, true} {
		r := testBuilder()
		f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true}
		b := guestBackend(t, f, sealedGuest())
		if public {
			r.PublicIPv4 = "10.233.0.12/24"
			f.request = r
			publicBuilderBackend(t, b, f)
			f.publicNIC = publicNICDevice(b.config.PublicEgress, r.PublicIPv4)
		}
		var steps []string
		b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
			i := slices.Index(argv, "--")
			guest := argv[i+1:]
			user := argv[slices.Index(argv, "--user")+1]
			if f.status != "Running" {
				t.Fatalf("guest command before start: %q", guest)
			}
			if guest[0] == "/bin/sh" {
				// Only the manager's socket on /run is fresh each boot.
				if !strings.Contains(guest[len(guest)-1], "test -S /run/systemd/private") {
					t.Fatalf("boot wait does not prove the current boot: %q", guest)
				}
				steps = append(steps, "boot-wait as "+user)
				return nil, nil
			}
			steps = append(steps, strings.Join(guest, " ")+" as "+user)
			return nil, nil
		}
		got, err := b.StartBuilder(context.Background(), r)
		if err != nil || got.Status != "Running" {
			t.Fatalf("start public=%t: %+v %v", public, got, err)
		}
		var want []string
		if public {
			want = []string{"boot-wait as 1000", "/usr/libexec/p/runtime-kit prepare-builder-network 10.233.0.12/24 10.233.0.1 as 0"}
		}
		if !slices.Equal(steps, want) {
			t.Fatalf("public=%t builder start steps: %q", public, steps)
		}
	}
}

func TestPublicBuilderNixFetchesFromPublicCacheOnly(t *testing.T) {
	for _, public := range []bool{false, true} {
		r := testBuilder()
		f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
		b := guestBackend(t, f, sealedGuest())
		if public {
			r.PublicIPv4 = "10.233.0.12/24"
			f.request = r
			publicBuilderBackend(t, b, f)
			f.publicNIC = publicNICDevice(b.config.PublicEgress, r.PublicIPv4)
		}
		for _, command := range [][]string{{"build", "--no-link", testDrv + "^*"}, {"flake", "metadata", "--json", builderFlakeAt(r.CommitOID)}} {
			var guest []string
			b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
				guest = argv[slices.Index(argv, "--")+1:]
				return nil, nil
			}
			if _, err := b.builderNixExec(context.Background(), r, command...); err != nil {
				t.Fatal(err)
			}
			substituters := guest[slices.Index(guest, "substituters")+1]
			offline := slices.Contains(guest, "--offline")
			if public && (substituters != "https://cache.nixos.org" || offline) || !public && (substituters != "" || !offline) {
				t.Fatalf("public=%t Nix network policy: %q", public, guest)
			}
			if !slices.Contains(guest, "--no-update-lock-file") || !slices.Contains(guest, "--no-write-lock-file") {
				t.Fatalf("public=%t %s may change the committed lock: %q", public, command[0], guest)
			}
		}
	}
}
