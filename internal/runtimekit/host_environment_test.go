package runtimekit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInteractiveHostInheritsFixedSessionUserBus(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/untrusted/owner-runtime")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/untrusted/owner-bus")
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", `printf '%s\n' "$HOME" "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS"`)
	cmd.Env = interactiveHostEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "/home/p\n/run/user/1000\nunix:path=/run/user/1000/bus\n" {
		t.Fatalf("closed host environment: %q %v", output, err)
	}
}

func TestDevShellHostRestoresBusAndPreservesActivation(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	activation, probe := filepath.Join(root, "activate with spaces.sh"), filepath.Join(root, "host probe")
	if err := os.WriteFile(activation, []byte(`export XDG_RUNTIME_DIR=/builder/runtime DBUS_SESSION_BUS_ADDRESS=unix:path=/builder/bus
export P_TEST_ACTIVATION=retained
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s\\n' \"$XDG_RUNTIME_DIR\" \"$DBUS_SESSION_BUS_ADDRESS\" \"$P_TEST_ACTIVATION\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Run the actual production Bash argv/script with fixture material and a
	// probe in place of tmux. Paths remain positional argv, including spaces.
	argv := devShellHostArgs()
	argv[6], argv[7] = activation, probe
	cmd := exec.Command(bash, argv[1:]...)
	cmd.Env = interactiveHostEnvironment()
	output, err := cmd.CombinedOutput()
	want := "/run/user/1000\nunix:path=/run/user/1000/bus\nretained\n-D\n-S\n" + SocketPath + "\n-f\n/opt/p/tmux.conf\n"
	if err != nil || string(output) != want {
		t.Fatalf("activated host inheritance: %q %v", output, err)
	}
	if err := os.WriteFile(activation, []byte("return 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(bash, argv[1:]...)
	cmd.Env = interactiveHostEnvironment()
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || len(output) != 0 {
		t.Fatalf("failed activation reached host: %q %v", output, err)
	}
	if err := os.WriteFile(activation, []byte("readonly XDG_RUNTIME_DIR=/builder/runtime\nreadonly DBUS_SESSION_BUS_ADDRESS=unix:path=/builder/bus\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(bash, argv[1:]...)
	cmd.Env = interactiveHostEnvironment()
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "readonly") || strings.Contains(string(output), "\n-D\n") {
		t.Fatalf("readonly wrong bus endpoints reached host: %q %v", output, err)
	}
}
