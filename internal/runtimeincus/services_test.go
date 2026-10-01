package runtimeincus

import (
	"context"
	"errors"
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
