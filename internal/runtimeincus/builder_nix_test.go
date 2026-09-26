package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testNAR = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const testDrv = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-test-shell.drv"

func testShownDerivation(system string) []byte {
	raw, _ := json.Marshal(map[string]any{"version": 4, "derivations": map[string]any{filepath.Base(testDrv): map[string]any{"version": 4, "system": system, "outputs": map[string]any{"out": map[string]any{}}}}})
	return raw
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

func TestBuilderNixWrongSystemNeverSelectsBaseOrDefault(t *testing.T) {
	r := testBuilder()
	for _, flake := range [][]byte{nil, []byte(`{ outputs = _: { devShells.x86_64-linux.default = 42; }; }`)} {
		f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
		b := nixBuilderBackend(t, f, flake)
		calls := 0
		b.runBuilder = func(context.Context, string, []string, []string) ([]byte, error) { calls++; return nil, nil }
		if selected, err := b.ResolveBuilderNix(context.Background(), r, "aarch64-linux"); err == nil || selected.BaseOnly || calls != 0 {
			t.Fatalf("wrong-system source selected before architecture proof: %+v, calls=%d err=%v", selected, calls, err)
		}
	}
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	f.base.imageArchitecture = "aarch64"
	b := nixBuilderBackend(t, f, nil)
	if selected, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux"); err == nil || selected.BaseOnly {
		t.Fatalf("wrong pinned-base architecture selected base: %+v %v", selected, err)
	}
	f = &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true, hostArchitecture: "aarch64"}
	b = nixBuilderBackend(t, f, nil)
	if selected, err := b.ResolveBuilderNix(context.Background(), r, "aarch64-linux"); err == nil || selected.BaseOnly {
		t.Fatalf("host-matched request with wrong base architecture selected base: %+v %v", selected, err)
	}
	f = &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true, hostArchitecture: "mips64"}
	b = nixBuilderBackend(t, f, nil)
	if selected, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux"); err == nil || selected.BaseOnly {
		t.Fatalf("unsupported server architecture selected base: %+v %v", selected, err)
	}
}

func TestBuilderNixSelectionAbsentVsInvalid(t *testing.T) {
	r := testBuilder()
	flake := guestFile{typ: "file", data: []byte("{ outputs = _: {}; }")}
	lock := guestFile{typ: "file", data: []byte(`{"version":7}`)}
	for _, tc := range []struct {
		name, presence, drv string
		wantBase, wantErr   bool
	}{
		{"missing devShells", "false", "", true, false},
		{"missing system", "false", "", true, false},
		{"missing default", "false", "", true, false},
		{"malformed devShells", "error", "", false, true},
		{"malformed system", "error", "", false, true},
		{"present invalid default", "true", "error", false, true},
		{"present valid default", "true", testDrv, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			run := func(args ...string) ([]byte, error) {
				calls = append(calls, slices.Clone(args))
				switch args[0] {
				case "hash":
					return []byte(testNAR + "\n"), nil
				case "eval":
					if !strings.Contains(args[len(args)-1], "narHash=sha256-") || !strings.Contains(args[len(args)-1], "&rev="+r.CommitOID) || !strings.Contains(args[len(args)-1], "builtins.getFlake") || strings.Contains(args[len(args)-1], ").outputs") {
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
					if !strings.Contains(args[len(args)-1], "shell.type or null") || !strings.Contains(args[len(args)-1], `shell.system or null`) {
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
			got, err := resolveBuilderNixInputs(r, "x86_64-linux", flake, lock, true, run)
			if (err != nil) != tc.wantErr || err == nil && got.BaseOnly != tc.wantBase {
				t.Fatalf("selection=%+v error=%v", got, err)
			}
			if tc.wantErr && len(calls) > 3 || tc.wantBase && len(calls) != 2 {
				t.Fatalf("evaluated after absent or invalid value: %q", calls)
			}
			if !tc.wantBase && !tc.wantErr && (got.SourceNarHash != testNAR || got.DerivationPath != testDrv || got.KeyDigest == "" || got.CommittedInputsDigest == "") {
				t.Fatalf("incomplete selection: %+v", got)
			}
		})
	}
}

func TestBuilderNixSourceOnlyCommitKeepsResolvedKey(t *testing.T) {
	flake := guestFile{typ: "file", mode: 0444, data: []byte(`{ outputs = { self }: { devShells.x86_64-linux.default = self; }; }`)}
	lock := guestFile{typ: "file", mode: 0444, data: []byte(`{"version":7}`)}
	var keys []string
	for _, commit := range []string{strings.Repeat("a", 40), strings.Repeat("c", 40)} {
		r := testBuilder()
		r.CommitOID = commit
		seenRev := false
		selection, err := resolveBuilderNixInputs(r, "x86_64-linux", flake, lock, true, func(args ...string) ([]byte, error) {
			switch args[0] {
			case "hash":
				return []byte(testNAR), nil
			case "eval":
				if !strings.Contains(args[len(args)-1], "&rev="+commit) {
					return nil, errors.New("captured commit missing from flake self.rev")
				}
				seenRev = true
				if slices.Contains(args, "--json") {
					return []byte("true"), nil
				}
				return []byte(testDrv), nil
			case "derivation":
				return testShownDerivation("x86_64-linux"), nil
			}
			return nil, errors.New("unexpected Nix command")
		})
		if err != nil || !seenRev || selection.BaseOnly {
			t.Fatalf("commit %s selection: %+v %v", commit, selection, err)
		}
		keys = append(keys, selection.KeyDigest)
	}
	if keys[0] != keys[1] {
		t.Fatalf("source-only commit invalidated identical resolved environment: %q", keys)
	}
}

func TestBuilderNixMissingLockAndSystemProof(t *testing.T) {
	r := testBuilder()
	flake := guestFile{typ: "file", data: []byte("flake")}
	var calls int
	run := func(args ...string) ([]byte, error) {
		calls++
		switch args[0] {
		case "hash":
			return []byte(testNAR), nil
		case "eval":
			if slices.Contains(args, "--json") {
				return nil, errors.New("missing locked input")
			}
		case "derivation":
			return testShownDerivation("aarch64-linux"), nil
		}
		return nil, errors.New("unexpected")
	}
	if got, err := resolveBuilderNixInputs(r, "x86_64-linux", flake, guestFile{}, false, run); err == nil || !strings.Contains(err.Error(), "presence") || got.BaseOnly || calls != 2 {
		t.Fatalf("missing lock selected fallback: %+v %v calls=%d", got, err, calls)
	}
	run = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "hash":
			return []byte(testNAR), nil
		case "eval":
			if slices.Contains(args, "--json") {
				return []byte("true"), nil
			}
			return []byte(testDrv), nil
		case "derivation":
			return testShownDerivation("aarch64-linux"), nil
		}
		return nil, errors.New("unexpected")
	}
	if _, err := resolveBuilderNixInputs(r, "x86_64-linux", flake, guestFile{}, false, run); err == nil || !strings.Contains(err.Error(), "requested system") {
		t.Fatalf("accepted wrong-system derivation: %v", err)
	}
	if digestBuilderInputs(flake, guestFile{}, false) == digestBuilderInputs(flake, guestFile{typ: "file"}, true) {
		t.Fatal("missing lock indistinguishable from present empty lock")
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

func nixBuilderBackend(t *testing.T, f *fakeBuilderIncus, flake []byte) *Backend {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/1.0" {
			if req.Method != http.MethodGet || req.URL.Query().Get("project") != "user-1000" {
				t.Errorf("unconfined server architecture request: %s", req.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			arch := f.hostArchitecture
			if arch == "" {
				arch = "x86_64"
			}
			_, _ = w.Write([]byte(`{"type":"sync","status_code":200,"metadata":{"environment":{"kernel_architecture":"` + arch + `"}}}`))
			return
		}
		if req.URL.Path != "/1.0/instances/"+builderName(f.request)+"/files" || req.URL.Query().Get("project") != "user-1000" {
			t.Errorf("unconfined file request: %s", req.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path := req.URL.Query().Get("path")
		if path == builderSource+"/flake.lock" || path == builderSource+"/flake.nix" && flake == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if path != builderSource && path != builderSource+"/flake.nix" {
			t.Errorf("unexpected source read %q", path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		typ, mode := "directory", "0555"
		if path != builderSource {
			typ, mode = "file", "0444"
			w.Header().Set("Content-Length", strconv.Itoa(len(flake)))
		}
		w.Header().Set("X-Incus-type", typ)
		w.Header().Set("X-Incus-uid", "0")
		w.Header().Set("X-Incus-gid", "0")
		w.Header().Set("X-Incus-mode", mode)
		if req.Method == http.MethodGet {
			_, _ = w.Write(flake)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	b := builderBackend(f)
	b.config.UserSocket = socket
	f.socket = socket
	f.base.socket = socket
	return b
}

func TestBuilderNixBackendResolveRealizeAndExecBoundary(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	flake := []byte("{ outputs = _: {}; }\n")
	b := nixBuilderBackend(t, f, flake)
	var execCalls, captureCalls int
	var failBuild bool
	b.runBuilder = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || argv[3] != "exec" || !slices.Contains(argv, "--user") || !slices.Contains(argv, "1000") || !slices.Contains(argv, "--group") || !slices.Contains(argv, "NIX_CONFIG=") || !slices.Contains(argv, "NIX_PATH=") || !slices.Contains(argv, "LD_PRELOAD=") || !slices.Contains(argv, "BASH_ENV=") {
			t.Fatalf("unsafe builder exec argv: %q", argv)
		}
		if !slices.Contains(env, "INCUS_SOCKET="+b.config.UserSocket) {
			t.Fatalf("wrong Incus socket: %q", env)
		}
		execCalls++
		i := slices.Index(argv, "--")
		if i < 0 || i+1 >= len(argv) {
			t.Fatalf("missing fixed guest command: %q", argv)
		}
		guest := argv[i+1:]
		if guest[0] == "/bin/sh" {
			return nil, nil
		}
		if guest[0] == "/run/current-system/sw/bin/readlink" {
			if !slices.Equal(guest[1:], []string{"-f", builderCaptureProfile}) {
				t.Fatalf("unexpected capture profile read: %q", guest)
			}
			return []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env\n"), nil
		}
		if guest[0] != builderNixBinary {
			t.Fatalf("wrong guest binary: %q", guest)
		}
		switch {
		case slices.Contains(guest, "--version"):
			return []byte("nix (Nix) 2.34.8\n"), nil
		case slices.Contains(guest, "info"):
			return []byte(`{"url":"daemon","version":"2.34.8","trusted":false}`), nil
		case slices.Contains(guest, "config"):
			return []byte(`{"sandbox":{"value":false}}`), nil
		case slices.Contains(guest, "hash"):
			return []byte(testNAR), nil
		case slices.Contains(guest, "--json") && slices.Contains(guest, "eval"):
			return []byte("true"), nil
		case slices.Contains(guest, "--raw"):
			return []byte(testDrv), nil
		case slices.Contains(guest, "derivation"):
			return testShownDerivation("x86_64-linux"), nil
		case slices.Contains(guest, "build"):
			if failBuild {
				return nil, errors.New("offline derivation failed")
			}
			return nil, nil
		case slices.Contains(guest, "print-dev-env"):
			if !slices.Contains(guest, "--profile") || !slices.Contains(guest, builderCaptureProfile) {
				t.Fatalf("capture did not install profile: %q", guest)
			}
			captureCalls++
			return []byte(`{"bashFunctions":{},"variables":{"outputs":{"type":"var","value":"out"},"out":{"type":"exported","value":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo"},"NATIVE_VALUE":{"type":"exported","value":"ok"}}}`), nil
		case slices.Contains(guest, "path-info"):
			if !slices.Contains(guest, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env") {
				t.Fatalf("wrong captured path verified: %q", guest)
			}
			return []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env\n"), nil
		}
		return nil, errors.New("unexpected Nix command")
	}
	selected, err := b.ResolveBuilderNix(context.Background(), r, "x86_64-linux")
	if err != nil || selected.BaseOnly || selected.SourceNarHash != testNAR {
		t.Fatalf("resolve: %+v %v", selected, err)
	}
	got, err := b.RealizeBuilderNix(context.Background(), r, selected)
	if err != nil || got.MaterialDigest == "" || got.CaptureStorePath != "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env" || !strings.Contains(got.Material.Script, "NATIVE_VALUE='ok'") {
		t.Fatalf("realize: %+v %v", got, err)
	}
	if execCalls < 13 {
		t.Fatalf("too few checked guest commands: %d", execCalls)
	}
	failBuild = true
	beforeCapture := captureCalls
	failed, err := b.RealizeBuilderNix(context.Background(), r, selected)
	if err == nil || failed.MaterialDigest != "" || failed.Material.Schema != "" || captureCalls != beforeCapture {
		t.Fatalf("failed derivation yielded capture: %+v err=%v captures=%d", failed, err, captureCalls)
	}
	f.foreign = true
	before := execCalls
	if _, err := b.builderGuestExec(context.Background(), r, builderNixBinary, "--version"); err == nil || execCalls != before {
		t.Fatalf("foreign identity reached guest exec: %v calls=%d", err, execCalls)
	}
}

func TestBuilderNixInterruptedExecStopsVerifiedBuilder(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := nixBuilderBackend(t, f, nil)
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
	b := nixBuilderBackend(t, f, nil)
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
