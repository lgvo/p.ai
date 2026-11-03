package runtimeincus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHostReadinessRequiresActualSystemdState(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	unit := "ActiveState=activating\nSubState=start-post\nResult=success\n"
	commands := 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		args := argv[3:]
		if len(args) > 0 && args[0] == "exec" {
			commands++
			want := []string{"exec", "p-" + testUUID, "--mode", "non-interactive", "--", "/usr/libexec/p/systemctl", "show", "p-interactive.service", "--property=ActiveState,SubState,Result", "--no-pager"}
			if slices.Equal(args, want) {
				return []byte(unit), nil
			}
			return nil, errors.New("unexpected native exec")
		}
		return f.run(ctx, binary, argv, env)
	}
	h, err := b.ObserveHost(context.Background(), testSession())
	if err != nil || h.Ready || h.Unit != "activating/start-post" {
		t.Fatalf("Incus Running became ready: %+v %v", h, err)
	}
	unit = "ActiveState=active\nSubState=running\nResult=success\n"
	h, err = b.ObserveHost(context.Background(), testSession())
	if err != nil || !h.Ready || h.Unit != "active/running" || commands < 2 {
		t.Fatalf("systemd active not ready: %+v %v", h, err)
	}
}

// A long journal of multibyte characters is cut on a character boundary, so
// the host's diagnostic stays valid UTF-8.
func TestHostDiagnosticTailIsValidUTF8(t *testing.T) {
	journal := []byte(strings.Repeat("€", 1000) + "\xff end")
	got := boundedDiagnostic(journal)
	if len(got) > 2048 || !utf8.ValidString(got) || !strings.HasSuffix(got, "\uFFFD end") {
		t.Fatalf("diagnostic tail: %d bytes, valid=%t, %q", len(got), utf8.ValidString(got), got[len(got)-8:])
	}
}
