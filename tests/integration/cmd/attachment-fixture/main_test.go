package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRPCReadsCLIErrorReplyFromExitOne(t *testing.T) {
	installFakeP(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32003,"kind":"busy","message":"attachment pending"}}`, 1)
	if output, err := command(time.Second, "p", "api", "/unused/control.sock", "session.stop"); err == nil || output != "" {
		t.Fatalf("generic command unexpectedly accepted exit one: %q, %v", output, err)
	}
	f := &fixture{socket: "/unused/control.sock"}
	if err := f.stopBusy("session-id"); err != nil {
		t.Fatalf("busy stop response: %v", err)
	}
	installFakeP(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"kind":"busy","message":"attachment pending"}}`, 1)
	if err := f.stopBusy("session-id"); err == nil {
		t.Fatal("accepted busy response with wrong code")
	}
	installFakeP(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"kind":"unavailable","message":"helper required"}}`, 1)
	reply, err := f.rpc("attachment.claim", map[string]any{"v": 1, "token": "invalid"})
	if err != nil {
		t.Fatalf("ordinary RPC error response: %v", err)
	}
	problem, ok := reply["error"].(map[string]any)
	if !ok || problem["kind"] != "unavailable" {
		t.Fatalf("missing structured error: %v", reply)
	}
}

func TestRPCRejectsInvalidCLIReplies(t *testing.T) {
	tests := []struct {
		name   string
		output string
		exit   int
	}{
		{"success", `{"jsonrpc":"2.0","id":1,"result":{"v":1}}`, 0},
		{"result with failure exit", `{"jsonrpc":"2.0","id":1,"result":{"v":1}}`, 1},
		{"error with success exit", `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 0},
		{"wrong id", `{"jsonrpc":"2.0","id":2,"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 1},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 1},
		{"both outcomes", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 1},
		{"missing error code", `{"jsonrpc":"2.0","id":1,"error":{"kind":"busy","message":"pending"}}`, 1},
		{"transport object", `{"error":{"kind":"transport","message":"dial failed"}}`, 1},
		{"duplicate field", `{"jsonrpc":"2.0","id":1,"id":1,"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 1},
		{"multiple replies", "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32001,\"kind\":\"busy\",\"message\":\"pending\"}}\n{}", 1},
		{"malformed", `{`, 1},
		{"empty", "", 1},
		{"other exit status", `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"kind":"busy","message":"pending"}}`, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			installFakeP(t, tc.output, tc.exit)
			f := &fixture{socket: "/unused/control.sock"}
			reply, err := f.rpc("session.stop", nil)
			if tc.name == "success" {
				if err != nil || reply["result"] == nil {
					t.Fatalf("success response = %v, %v", reply, err)
				}
			} else if err == nil || reply != nil {
				t.Fatalf("accepted invalid CLI response: %v, %v", reply, err)
			}
		})
	}
}

func TestRPCPreservesSpawnAndDeadlineFailures(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if reply, err := (&fixture{socket: "/unused/control.sock"}).rpc("session.stop", nil); err == nil || reply != nil {
		t.Fatalf("accepted missing CLI: %v, %v", reply, err)
	}
	_, err := commandOutput(20*time.Millisecond, "/bin/sh", "-c", "while :; do :; done")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func installFakeP(t *testing.T, output string, status int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'P_REPLY'\n" + output + "\nP_REPLY\nexit " + strconv.Itoa(status) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "p"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestParseHostIdentityRequiresOneLivePane(t *testing.T) {
	// /proc stat fields after comm begin at state (field 3); starttime is
	// field 22, index 19 here. The process name may contain parentheses.
	stat := "17 (tmux (server)) " + strings.Repeat("x ", 19) + "98765 0\n"
	identity, err := parseHostIdentity("17\n", "41\n", stat)
	if err != nil || identity != "17:41:98765" {
		t.Fatalf("identity = %q, %v", identity, err)
	}
	for _, panes := range []string{"", "0\n", "41\n42\n", "abc\n"} {
		if _, err := parseHostIdentity("17\n", panes, stat); err == nil {
			t.Errorf("accepted invalid pane listing %q", panes)
		}
	}
	if _, err := parseHostIdentity("", "41\n", stat); err == nil {
		t.Fatal("accepted absent server PID")
	}
}

func TestCarrierFrameOnInheritedSocket(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	left := os.NewFile(uintptr(fds[0]), "left")
	right := os.NewFile(uintptr(fds[1]), "right")
	defer left.Close()
	defer right.Close()
	want := []byte(`{"socket":"/tmp/control.sock","token":"secret","width":80,"height":24}`)
	if err := writeFrame(left, initFrame, want); err != nil {
		t.Fatal(err)
	}
	kind, data, err := readFrame(right, time.Second)
	if err != nil || kind != initFrame || !bytes.Equal(data, want) {
		t.Fatalf("frame = %d %q %v", kind, data, err)
	}
	if _, _, err := readFrame(right, 20*time.Millisecond); err == nil {
		t.Fatal("missing read deadline")
	}
}

func TestCarrierFrameLimit(t *testing.T) {
	if err := writeFrame(io.Discard, initFrame, make([]byte, 32769)); err == nil || err.Error() != "oversize carrier initiation" {
		t.Fatalf("oversize initiation: %v", err)
	}
}

func TestHelperCarrierCloseReachesInheritedSocket(t *testing.T) {
	if os.Getenv("P_FIXTURE_SOCKET_EOF_CHILD") == "1" {
		file := os.NewFile(3, "inherited-carrier")
		defer file.Close()
		if err := file.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err := file.Read(b[:]); err != io.EOF {
			t.Fatalf("closing parent carrier did not deliver EOF: %v", err)
		}
		return
	}
	parent, child, err := fixtureCarrierPair()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	defer child.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperCarrierCloseReachesInheritedSocket$")
	cmd.Env = append(os.Environ(), "P_FIXTURE_SOCKET_EOF_CHILD=1")
	cmd.ExtraFiles = []*os.File{child}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	parent.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child carrier EOF: %v: %s", err, output.String())
	}
}
