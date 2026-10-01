package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestServicesUseFixedUserCommandsAndDenyInfrastructure(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	executions := 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		a := argv[3:]
		if a[0] == "exec" {
			if slices.Contains(a, "show") {
				return []byte("ActiveState=active\nSubState=running\nResult=success\n"), nil
			}
			executions++
			prefix := []string{"exec", "p-" + testUUID, "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--env", "XDG_RUNTIME_DIR=/run/user/1000", "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--"}
			if len(a) <= len(prefix) || !slices.Equal(a[:len(prefix)], prefix) || (a[len(prefix)] != "/usr/libexec/p/systemctl" && a[len(prefix)] != "/usr/libexec/p/journalctl") {
				t.Fatalf("unexpected service exec prefix: %v", a)
			}
			if !slices.Contains(a, "--user") || !slices.Contains(a, "1000") || !slices.Contains(a, "--group") || !slices.Contains(a, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus") {
				t.Fatalf("not session user manager: %v", a)
			}
			if slices.Contains(a, "list-units") {
				return []byte(`[{"unit":"p-project-api.service","description":"API","active":"active","sub":"running"}]`), nil
			}
			if slices.Contains(a, "list-unit-files") {
				return []byte(`[{"unit_file":"p-project-api.service"},{"unit_file":"p-project-idle.service"}]`), nil
			}
			return []byte("log entry"), nil
		}
		return f.run(ctx, binary, argv, env)
	}
	for _, unit := range []string{"p-interactive.service", "p-session.target", "sshd.service", "../p-project-api.service", "--root", "p-project-a.service\nsshd.service"} {
		if _, err := b.Services(context.Background(), testSession(), unit, "restart"); err == nil {
			t.Fatalf("accepted infrastructure/injection %q", unit)
		}
	}
	if executions != 0 {
		t.Fatal("invalid request crossed native boundary")
	}
	r, err := b.Services(context.Background(), testSession(), "", "list")
	if err != nil || len(r.Services) != 2 || r.Services[1].ActiveState != "unknown" {
		t.Fatalf("inventory: %+v %v", r, err)
	}
	for _, action := range []string{"start", "stop", "restart", "journal"} {
		if _, err = b.Services(context.Background(), testSession(), "p-project-api.service", action); err != nil {
			t.Fatal(err)
		}
	}
	f.altered = true
	if _, err = b.Services(context.Background(), testSession(), "p-project-api.service", "stop"); err == nil {
		t.Fatal("changed ownership accepted")
	}
	f.altered = false
	f.status = "Stopped"
	if _, err = b.Services(context.Background(), testSession(), "", "list"); err == nil {
		t.Fatal("stopped runtime accepted")
	}
}

func TestServiceCommandOutputHelper(t *testing.T) {
	switch os.Getenv("P_SERVICE_OUTPUT_HELPER") {
	case "one":
		fmt.Print("[]")
		os.Exit(1)
	case "two":
		fmt.Print("[]")
		os.Exit(2)
	case "stdout-overflow":
		fmt.Print(strings.Repeat(" ", 33000) + "[]")
		os.Exit(1)
	case "stderr-overflow":
		fmt.Print("[]")
		fmt.Fprint(os.Stderr, strings.Repeat("x", 33000))
		os.Exit(1)
	}
}

func serviceCommandFixture(t *testing.T, mode string) ([]byte, error) {
	t.Helper()
	return runCommandBounded(context.Background(), os.Args[0], []string{"-test.run=^TestServiceCommandOutputHelper$"},
		[]string{"P_SERVICE_OUTPUT_HELPER=" + mode}, 32000, 32000, false)
}

func TestEmptyInstalledServiceInventoryExitConvention(t *testing.T) {
	empty, exitOne := serviceCommandFixture(t, "one")
	if string(empty) != "[]" || !emptyUnitFileInventory(context.Background(), empty, exitOne) {
		t.Fatalf("actual exit 1 empty inventory was lost: %q %v", empty, exitOne)
	}
	_, exitTwo := serviceCommandFixture(t, "two")
	for _, tc := range []struct {
		name string
		data []byte
		err  error
	}{
		{"exit-two", empty, exitTwo},
		{"transport", empty, errors.New("transport unavailable")},
		{"context-canceled", empty, errors.Join(exitOne, context.Canceled)},
		{"context-deadline", empty, errors.Join(exitOne, context.DeadlineExceeded)},
		{"malformed", []byte("["), exitOne},
		{"null", []byte("null"), exitOne},
		{"object", []byte("{}"), exitOne},
		{"nonempty", []byte(`[{"unit_file":"p-project-worker.service"}]`), exitOne},
		{"oversized", []byte(strings.Repeat(" ", 32001) + "[]"), exitOne},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if emptyUnitFileInventory(context.Background(), tc.data, tc.err) {
				t.Fatalf("failed inventory accepted: %q %v", tc.data, tc.err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if emptyUnitFileInventory(ctx, empty, exitOne) {
		t.Fatal("canceled caller accepted a stale inventory")
	}
	for _, mode := range []string{"stdout-overflow", "stderr-overflow"} {
		data, err := serviceCommandFixture(t, mode)
		if data != nil || err == nil || emptyUnitFileInventory(context.Background(), data, err) {
			t.Fatalf("overflow became an empty inventory: %s %q %v", mode, data, err)
		}
	}
}

func TestEmptyServiceInventoryStillChecksNativeIdentity(t *testing.T) {
	empty, exitOne := serviceCommandFixture(t, "one")
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	changeIdentity := false
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if slices.Contains(argv, "list-unit-files") {
			f.altered = changeIdentity
			return empty, exitOne
		}
		if slices.Contains(argv, "list-units") {
			return []byte(`[]`), nil
		}
		if slices.Contains(argv, "show") {
			return []byte("ActiveState=active\nSubState=running\nResult=success\n"), nil
		}
		return f.run(ctx, binary, argv, env)
	}
	result, err := b.Services(context.Background(), testSession(), "", "list")
	if err != nil || result.Services == nil || len(result.Services) != 0 {
		t.Fatalf("ready empty service list: %+v %v", result, err)
	}
	changeIdentity = true
	if _, err := b.Services(context.Background(), testSession(), "", "list"); err == nil {
		t.Fatal("empty inventory bypassed runtime identity proof")
	}
}

func TestServiceOutputBoundsAndFailures(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	raw := `[{"unit":"p-interactive.service","active":"active","sub":"running"}]`
	b.run = func(ctx context.Context, binary string, a, env []string) ([]byte, error) {
		if a[3] == "exec" {
			if slices.Contains(a, "list-unit-files") {
				return []byte(`[]`), nil
			}
			if slices.Contains(a, "show") {
				return []byte("ActiveState=active\nSubState=running\nResult=success\n"), nil
			}
			return []byte(raw), nil
		}
		return f.run(ctx, binary, a, env)
	}
	if _, err := b.Services(context.Background(), testSession(), "", "list"); err == nil {
		t.Fatal("internal unit leaked")
	}
	raw = strings.Repeat("x", 33000)
	if _, err := b.Services(context.Background(), testSession(), "", "list"); err == nil {
		t.Fatal("oversized inventory accepted")
	}
	r, err := b.Services(context.Background(), testSession(), "p-project-api.service", "journal")
	if err != nil || len(r.Journal) > 16000 {
		t.Fatalf("journal bound: %d %v", len(r.Journal), err)
	}
	b.run = func(context.Context, string, []string, []string) ([]byte, error) {
		return nil, errors.New("unavailable")
	}
	if _, err = b.Services(context.Background(), testSession(), "p-project-api.service", "stop"); err == nil {
		t.Fatal("unavailable authority claimed success")
	}
}
