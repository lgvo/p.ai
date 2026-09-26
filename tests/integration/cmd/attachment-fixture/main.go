// attachment-fixture exercises the actual host CLI, Incus terminal channel,
// and selected persistent tmux host inside the disposable product VM.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	initFrame  = 1
	readyFrame = 5
	errorFrame = 6
)

type fixture struct {
	config, socket, endpoints, dir string
	daemon                         *exec.Cmd
	log                            *os.File
	sessions                       []string
	clients                        []*terminal
	stage                          string
}

func main() {
	if len(os.Args) != 5 {
		fail(errors.New("usage: attachment-fixture HOST_JSON SOCKET ENDPOINT_PREFIX STEP_DIR"))
	}
	if err := runMain(); err != nil {
		fail(err)
	}
}

func runMain() error {
	f := &fixture{config: os.Args[1], socket: os.Args[2], endpoints: os.Args[3], dir: os.Args[4]}
	defer f.cleanup()
	if err := f.run(); err != nil {
		f.diagnostics(err)
		return err
	}
	return nil
}

func fail(err error) { fmt.Fprintln(os.Stderr, "P_ATTACHMENT_FAIL:", err); os.Exit(1) }

func command(timeout time.Duration, name string, args ...string) (string, error) {
	output, err := commandOutput(timeout, name, args...)
	if err != nil {
		return "", commandError(name, args, output, err)
	}
	return output, nil
}

func commandOutput(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return string(output), err
}

func commandError(name string, args []string, output string, err error) error {
	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, tail(output, 1200))
}

func (f *fixture) inc(args ...string) (string, error) {
	return command(45*time.Second, "incus", append([]string{"--force-local", "--project", "user-1000"}, args...)...)
}

func (f *fixture) rpc(method string, params any) (map[string]any, error) {
	var args = []string{"api", f.socket, method}
	if params != nil {
		payload, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		args = append(args, string(payload))
	}
	output, runErr := commandOutput(40*time.Second, "p", args...)
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 {
			return nil, commandError("p", args, output, runErr)
		}
	}
	reply, isError, err := parseRPCReply([]byte(output))
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%w; invalid %s response: %v", commandError("p", args, output, runErr), method, err)
		}
		return nil, fmt.Errorf("%s response: %w", method, err)
	}
	if isError != (runErr != nil) {
		return nil, fmt.Errorf("%s response and exit status disagree: %v", method, reply)
	}
	return reply, nil
}

func parseRPCReply(output []byte) (map[string]any, bool, error) {
	fields, err := jsonObjectFields(output)
	if err != nil {
		return nil, false, err
	}
	var version string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" {
		return nil, false, errors.New("invalid jsonrpc version")
	}
	var id int
	if json.Unmarshal(fields["id"], &id) != nil || id != 1 {
		return nil, false, errors.New("invalid response id")
	}
	result, hasResult := fields["result"]
	problem, hasError := fields["error"]
	if hasResult == hasError {
		return nil, false, errors.New("response must contain exactly one result or error")
	}
	if hasError {
		errorFields, err := jsonObjectFields(problem)
		if err != nil {
			return nil, false, fmt.Errorf("invalid error object: %w", err)
		}
		var code int
		var kind, message string
		if json.Unmarshal(errorFields["code"], &code) != nil || code == 0 ||
			json.Unmarshal(errorFields["kind"], &kind) != nil || kind == "" ||
			json.Unmarshal(errorFields["message"], &message) != nil || message == "" {
			return nil, false, errors.New("invalid error fields")
		}
	} else if !json.Valid(result) {
		return nil, false, errors.New("invalid result")
	}
	var reply map[string]any
	if err := json.Unmarshal(output, &reply); err != nil {
		return nil, false, err
	}
	return reply, hasError, nil
}

func jsonObjectFields(raw []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return fields, nil
}

func (f *fixture) result(method string, params any) (map[string]any, error) {
	reply, err := f.rpc(method, params)
	if err != nil {
		return nil, err
	}
	if problem, ok := reply["error"]; ok {
		return nil, fmt.Errorf("%s: %v", method, problem)
	}
	result, ok := reply["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: missing result: %v", method, reply)
	}
	return result, nil
}

func (f *fixture) startDaemon() error {
	log, err := os.OpenFile(filepath.Join(f.dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	f.log = log
	cmd := exec.Command("p", "daemon", f.config)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		log.Close()
		f.log = nil
		return err
	}
	f.daemon = cmd
	return f.wait(30*time.Second, "daemon ready", func() (bool, error) {
		if _, err := os.Stat(f.socket); err != nil {
			return false, nil
		}
		health, err := f.result("system.health", nil)
		return err == nil && health["control_state"] == "ready", nil
	})
}

func (f *fixture) stopDaemon(signal syscall.Signal) {
	if f.daemon == nil {
		return
	}
	_ = f.daemon.Process.Signal(signal)
	done := make(chan struct{})
	go func() { _ = f.daemon.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = f.daemon.Process.Kill()
		<-done
	}
	f.daemon = nil
	if f.log != nil {
		_ = f.log.Close()
		f.log = nil
	}
}

func (f *fixture) wait(limit time.Duration, what string, condition func() (bool, error)) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		ok, err := condition()
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if ok {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func (f *fixture) create(key string) (string, error) {
	response, err := f.result("project.create", map[string]any{"v": 1, "key": key, "project": key})
	if err != nil {
		return "", err
	}
	op, ok := response["operation"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("missing create operation: %v", response)
	}
	uuid, _ := op["session_uuid"].(string)
	id, _ := op["id"].(string)
	if len(uuid) != 36 || id == "" {
		return "", fmt.Errorf("invalid create operation: %v", op)
	}
	f.sessions = append(f.sessions, uuid)
	err = f.wait(170*time.Second, "create "+uuid, func() (bool, error) {
		result, err := f.result("operation.inspect", map[string]any{"v": 1, "id": id})
		if err != nil {
			return false, err
		}
		operation, _ := result["operation"].(map[string]any)
		if operation["status"] == "blocked" {
			return false, fmt.Errorf("create blocked: %v", operation)
		}
		return operation["status"] == "completed", nil
	})
	if err != nil {
		return "", err
	}
	if err = f.waitReady(uuid); err != nil {
		return "", err
	}
	return uuid, nil
}

func (f *fixture) inspect(uuid string) (map[string]any, error) {
	result, err := f.result("session.inspect", map[string]any{"v": 1, "uuid": uuid})
	if err != nil {
		return nil, err
	}
	session, ok := result["session"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing session: %v", result)
	}
	return session, nil
}

func (f *fixture) waitReady(uuid string) error {
	return f.wait(80*time.Second, "session ready "+uuid, func() (bool, error) {
		s, err := f.inspect(uuid)
		if err != nil {
			return false, nil
		}
		if s["session_condition"] == "stopped" {
			return false, fmt.Errorf("session stopped: %v", s)
		}
		return s["session_condition"] == "ready", nil
	})
}

func (f *fixture) presence(uuid string, count int, latest any) error {
	return f.wait(15*time.Second, fmt.Sprintf("presence %s=%d latest=%v", uuid, count, latest), func() (bool, error) {
		s, err := f.inspect(uuid)
		if err != nil {
			return false, nil
		}
		if s["attached_count"] != float64(count) {
			return false, nil
		}
		value := s["latest_unattended_condition"]
		if latest == nil {
			return value == nil, nil
		}
		condition, ok := value.(map[string]any)
		return ok && condition["condition"] == latest, nil
	})
}

func (f *fixture) listPresence(uuid string, count int) error {
	// This fixture creates two sessions, so one allowed page covers both.
	result, err := f.result("session.list", map[string]any{"v": 1, "limit": 8})
	if err != nil {
		return err
	}
	items, _ := result["sessions"].([]any)
	for _, item := range items {
		s, _ := item.(map[string]any)
		if s["uuid"] == uuid && s["attached_count"] == float64(count) {
			return nil
		}
	}
	return fmt.Errorf("list missing %s with count %d: %v", uuid, count, result)
}

func (f *fixture) guest(uuid string, args ...string) (string, error) {
	return f.inc(append([]string{"exec", "p-" + uuid, "--user", "1000", "--group", "1000", "--cwd", "/workspace", "--env", "HOME=/home/p", "--"}, args...)...)
}

func (f *fixture) report(uuid, condition, reason string) error {
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "status.report", "params": map[string]any{"v": 1, "source": "codex/main", "condition": condition, "reason": reason, "adapter": "codex", "adapter_version": "1"}})
	line := base64.StdEncoding.EncodeToString(append(frame, '\n'))
	output, err := f.guest(uuid, "/home/p/session-fixture", "/run/p/session.sock", line)
	if err != nil {
		return err
	}
	if output != "" {
		return fmt.Errorf("status notification responded: %s", tail(output, 200))
	}
	return nil
}

func (f *fixture) tmux(uuid string, args ...string) (string, error) {
	return f.guest(uuid, append([]string{"/usr/libexec/p/tmux", "-S", "/run/p-interactive/tmux.sock"}, args...)...)
}

func (f *fixture) tmuxClients(uuid string, count int) error {
	return f.wait(12*time.Second, fmt.Sprintf("tmux clients %s=%d", uuid, count), func() (bool, error) {
		output, err := f.tmux(uuid, "list-clients", "-F", "#{client_pid}")
		if err != nil {
			return false, err
		}
		return len(strings.Fields(output)) == count, nil
	})
}

func (f *fixture) hostIdentity(uuid string) (string, error) {
	server, err := f.tmux(uuid, "display-message", "-p", "-t", "=p", "#{pid}")
	if err != nil {
		return "", err
	}
	// display-message has session context here, so pane_pid is empty until a
	// client chooses a pane. list-panes supplies explicit pane context even
	// before the first attachment and must identify exactly one persistent pane.
	panes, err := f.tmux(uuid, "list-panes", "-s", "-t", "=p", "-F", "#{pane_pid}")
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(server))
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("invalid tmux server PID %q", server)
	}
	stat, err := f.guest(uuid, "cat", fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	return parseHostIdentity(server, panes, stat)
}

func parseHostIdentity(server, panes, stat string) (string, error) {
	server = strings.TrimSpace(server)
	pid, err := strconv.Atoi(server)
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("invalid tmux server PID %q", server)
	}
	paneLines := strings.Fields(panes)
	if len(paneLines) != 1 {
		return "", fmt.Errorf("expected one persistent tmux pane, got %d: %q", len(paneLines), tail(panes, 200))
	}
	panePID, err := strconv.Atoi(paneLines[0])
	if err != nil || panePID <= 0 {
		return "", fmt.Errorf("invalid tmux pane PID %q", paneLines[0])
	}
	// Container PIDs can repeat across Stop/Start. Linux starttime identifies
	// this particular tmux process within the host's running kernel.
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 {
		return "", fmt.Errorf("invalid tmux process stat %q", tail(stat, 200))
	}
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) < 20 || fields[19] == "" {
		return "", fmt.Errorf("invalid tmux process stat %q", tail(stat, 200))
	}
	return server + ":" + paneLines[0] + ":" + fields[19], nil
}

func (f *fixture) pending(uuid string) (string, error) {
	token, _, err := f.pendingWithExpiry(uuid)
	return token, err
}

func (f *fixture) pendingWithExpiry(uuid string) (string, time.Time, error) {
	result, err := f.result("session.attach", map[string]any{"v": 1, "uuid": uuid})
	if err != nil {
		return "", time.Time{}, err
	}
	token, _ := result["token"].(string)
	if len(token) != 64 {
		return "", time.Time{}, fmt.Errorf("invalid pending attachment: %v", result)
	}
	expiresRaw, _ := result["expires_at"].(string)
	expires, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil || !time.Now().Before(expires) {
		return "", time.Time{}, fmt.Errorf("invalid pending expiry: %q %v", expiresRaw, err)
	}
	return token, expires, nil
}

func (f *fixture) stopBusy(uuid string) error {
	reply, err := f.rpc("session.stop", map[string]any{"v": 1, "uuid": uuid})
	if err != nil {
		return err
	}
	problem, _ := reply["error"].(map[string]any)
	if problem["kind"] != "busy" || problem["code"] != float64(-32003) {
		return fmt.Errorf("Stop accepted despite attachment: %v", reply)
	}
	return nil
}

func (f *fixture) run() (runErr error) {
	f.stage = "setup"
	defer func() {
		if runErr != nil {
			runErr = fmt.Errorf("%s: %w", f.stage, runErr)
		}
	}()
	if err := f.startDaemon(); err != nil {
		return err
	}
	a, err := f.create("attach-a")
	if err != nil {
		return err
	}
	b, err := f.create("attach-b")
	if err != nil {
		return err
	}
	if a == b {
		return errors.New("duplicate session UUID")
	}
	for _, uuid := range []string{a, b} {
		fixturePath, e := exec.LookPath("session-fixture")
		if e != nil {
			return e
		}
		if _, err = f.inc("file", "push", "--uid", "1000", "--gid", "1000", "--mode", "0755", fixturePath, "p-"+uuid+"/home/p/session-fixture"); err != nil {
			return err
		}
		if _, err = f.guest(uuid, "sh", "-c", "touch /workspace/attachment-kept && test -S /run/p/session.sock"); err != nil {
			return err
		}
		if err = f.tmuxClients(uuid, 0); err != nil {
			return err
		}
	}
	identityA, err := f.hostIdentity(a)
	if err != nil || identityA == ":" {
		return fmt.Errorf("host identity A: %q %v", identityA, err)
	}
	identityB, err := f.hostIdentity(b)
	if err != nil || identityB == ":" {
		return fmt.Errorf("host identity B: %q %v", identityB, err)
	}
	if err = f.report(a, "attention", "before attach"); err != nil {
		return err
	}
	if err = f.presence(a, 0, "attention"); err != nil {
		return err
	}

	// A pending decision and a dead carrier have no presence effect. A normal
	// host API process cannot use the helper-only claim or confirm methods.
	f.stage = "pending and dead carrier"
	failedToken, err := f.pending(a)
	if err != nil {
		return err
	}
	if err = f.stopBusy(a); err != nil {
		return err
	}
	for _, method := range []string{"attachment.claim", "attachment.confirm"} {
		params := map[string]any{"v": 1, "token": failedToken}
		if method == "attachment.confirm" {
			params = map[string]any{"v": 1, "operation": "/1.0/operations/00000000-0000-0000-0000-000000000000"}
		}
		reply, e := f.rpc(method, params)
		if e != nil {
			return e
		}
		if reply["error"] == nil {
			return fmt.Errorf("ordinary RPC fabricated %s: %v", method, reply)
		}
	}
	if err = f.presence(a, 0, "attention"); err != nil {
		return err
	}
	if err = f.manual(failedToken, false); err != nil {
		return err
	}
	if err = f.presence(a, 0, "attention"); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}

	// The one-use helper handshake is real, including native PTY establishment.
	f.stage = "manual confirmation and replay"
	goodToken, err := f.pending(a)
	if err != nil {
		return err
	}
	manual, err := f.openHelper(goodToken)
	if err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		manual.close()
		return err
	}
	if err = f.stopBusy(a); err != nil {
		manual.close()
		return err
	}
	if err = manual.close(); err != nil {
		return err
	}
	if err = f.presence(a, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}
	if err = f.manual(goodToken, true); err != nil {
		return fmt.Errorf("replay: %w", err)
	}

	// A fresh unattended report permits a precise clear-on-confirm assertion.
	if err = f.report(a, "attention", "awaiting entry"); err != nil {
		return err
	}
	if err = f.presence(a, 0, "attention"); err != nil {
		return err
	}
	f.stage = "terminal bytes and resize"
	first, err := f.openTerminal(a)
	if err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	if err = f.listPresence(a, 1); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 1); err != nil {
		return err
	}
	if err = first.command("printf '%s%s\\n' P_ATTACH_ BYTES_OK", "P_ATTACH_BYTES_OK"); err != nil {
		return err
	}
	if err = first.resize(113, 37); err != nil {
		return err
	}
	// tmux reserves its default one-line status bar, so the pane is one row
	// shorter than the 113x37 client. Check both sizes before checking the PTY.
	var clientSize, paneSize string
	err = f.wait(12*time.Second, "resized tmux client and pane", func() (bool, error) {
		var e error
		clientSize, e = f.tmux(a, "list-clients", "-F", "#{client_width} #{client_height}")
		if e != nil {
			return false, e
		}
		paneSize, e = f.tmux(a, "list-panes", "-s", "-t", "=p", "-F", "#{pane_width} #{pane_height}")
		if e != nil {
			return false, e
		}
		return strings.TrimSpace(clientSize) == "113 37" && strings.TrimSpace(paneSize) == "113 36", nil
	})
	if err != nil {
		return fmt.Errorf("%w (client %q, pane %q)", err, strings.TrimSpace(clientSize), strings.TrimSpace(paneSize))
	}
	if err = first.command("stty size", "36 113"); err != nil {
		return err
	}
	if err = f.report(a, "failed", "suppressed while attached"); err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	if err = f.stopBusy(a); err != nil {
		return err
	}
	f.stage = "multiple attachments"
	second, err := f.openTerminal(a)
	if err != nil {
		return err
	}
	if err = f.presence(a, 2, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 2); err != nil {
		return err
	}
	other, err := f.openTerminal(b)
	if err != nil {
		return err
	}
	if err = f.presence(b, 1, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(b, 1); err != nil {
		return err
	}
	if err = f.stopBusy(b); err != nil {
		return err
	}
	f.stage = "client SIGKILL"
	first.kill()
	if err = first.waitExit(15 * time.Second); err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 1); err != nil {
		return err
	}
	if err = f.presence(b, 1, nil); err != nil {
		return err
	}
	f.stage = "helper SIGKILL"
	if err = second.killHelper(); err != nil {
		return err
	}
	if err = f.presence(a, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}
	f.stage = "terminal pipe closure"
	other.closePipe()
	if err = other.waitExit(15 * time.Second); err != nil {
		return err
	}
	if err = f.presence(b, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(b, 0); err != nil {
		return err
	}
	if err = f.listPresence(a, 0); err != nil {
		return err
	}
	if err = f.assertHost(a, identityA); err != nil {
		return err
	}
	if err = f.assertHost(b, identityB); err != nil {
		return err
	}

	// Expiry is checked through the actual helper, after the advertised TTL.
	if err = f.report(a, "idle", "pending expiry"); err != nil {
		return err
	}
	f.stage = "pending expiry"
	expired, expiresAt, err := f.pendingWithExpiry(a)
	if err != nil {
		return err
	}
	if err = f.stopBusy(a); err != nil {
		return err
	}
	time.Sleep(time.Until(expiresAt) + 100*time.Millisecond)
	if err = f.manual(expired, true); err != nil {
		return fmt.Errorf("expired token: %w", err)
	}
	if err = f.presence(a, 0, "idle"); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}

	// Restart invalidates an active lease. The helper begins native teardown;
	// it cannot renew the old channel on the recreated socket.
	f.stage = "graceful daemon restart"
	live, err := f.openTerminal(a)
	if err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	pendingBeforeRestart, expiresBeforeRestart, err := f.pendingWithExpiry(b)
	if err != nil {
		return err
	}
	f.stopDaemon(syscall.SIGTERM)
	if err = f.startDaemon(); err != nil {
		return err
	}
	if !time.Now().Before(expiresBeforeRestart) {
		return errors.New("pending token expired before restart invalidation check")
	}
	if err = f.manual(pendingBeforeRestart, true); err != nil {
		return fmt.Errorf("pending token survived restart: %w", err)
	}
	if err = f.presence(a, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}
	if err = live.waitExit(15 * time.Second); err != nil {
		return err
	}
	if err = f.assertHost(a, identityA); err != nil {
		return err
	}
	if err = f.assertHost(b, identityB); err != nil {
		return err
	}
	if err = f.manual(expired, true); err != nil {
		return err
	}
	f.stage = "daemon SIGKILL restart"
	if err = f.report(a, "attention", "before kill restart"); err != nil {
		return err
	}
	live, err = f.openTerminal(a)
	if err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	f.stopDaemon(syscall.SIGKILL)
	if err = f.startDaemon(); err != nil {
		return err
	}
	if err = f.presence(a, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}
	if err = live.waitExit(15 * time.Second); err != nil {
		return err
	}
	if err = f.assertHost(a, identityA); err != nil {
		return err
	}

	f.stage = "stop and start"
	// Ordinary Stop/Start changes the guest host, yet durable workspace remains.
	if _, err = f.result("session.stop", map[string]any{"v": 1, "uuid": a}); err != nil {
		return err
	}
	if _, err = f.result("session.start", map[string]any{"v": 1, "uuid": a}); err != nil {
		return err
	}
	if err = f.waitReady(a); err != nil {
		return err
	}
	if _, err = f.guest(a, "sh", "-c", "test -f /workspace/attachment-kept"); err != nil {
		return err
	}
	newHost, err := f.hostIdentity(a)
	if err != nil || newHost == identityA {
		return fmt.Errorf("host did not restart: before=%q after=%q err=%v", identityA, newHost, err)
	}
	live, err = f.openTerminal(a)
	if err != nil {
		return err
	}
	if err = f.presence(a, 1, nil); err != nil {
		return err
	}
	if err = live.command("printf '%s%s\\n' P_AFTER_ START_OK", "P_AFTER_START_OK"); err != nil {
		return err
	}
	live.closePipe()
	if err = live.waitExit(15 * time.Second); err != nil {
		return err
	}
	if err = f.presence(a, 0, nil); err != nil {
		return err
	}
	if err = f.tmuxClients(a, 0); err != nil {
		return err
	}
	return nil
}

func (f *fixture) assertHost(uuid, identity string) error {
	current, err := f.hostIdentity(uuid)
	if err != nil {
		return err
	}
	if current != identity {
		return fmt.Errorf("persistent tmux host changed %s: %s -> %s", uuid, identity, current)
	}
	_, err = f.guest(uuid, "sh", "-c", "test -f /workspace/attachment-kept && systemctl is-active --quiet p-interactive.service")
	return err
}

func (f *fixture) cleanup() {
	for _, c := range f.clients {
		c.closePipe()
		c.kill()
	}
	f.stopDaemon(syscall.SIGTERM)
	for _, uuid := range f.sessions {
		_, _ = f.inc("delete", "--force", "p-"+uuid)
		_ = os.RemoveAll(filepath.Join(f.endpoints, uuid))
	}
}

func (f *fixture) diagnostics(cause error) {
	fmt.Fprintln(os.Stderr, "attachment fixture diagnostic:", cause)
	if data, err := os.ReadFile(filepath.Join(f.dir, "daemon.log")); err == nil {
		fmt.Fprintln(os.Stderr, "daemon:", tail(string(data), 3000))
	}
	for _, uuid := range f.sessions {
		if s, err := f.inspect(uuid); err == nil {
			fmt.Fprintln(os.Stderr, "session:", uuid, s)
		}
		if output, err := f.tmux(uuid, "list-clients", "-F", "#{client_pid}"); err == nil {
			fmt.Fprintln(os.Stderr, "tmux clients:", uuid, strings.TrimSpace(output))
		}
		if output, err := f.inc("info", "p-"+uuid, "--show-log"); err == nil {
			fmt.Fprintln(os.Stderr, "incus:", uuid, tail(output, 1000))
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type terminal struct {
	cmd    *exec.Cmd
	pty    *os.File
	closed sync.Once
	mu     sync.Mutex
	text   string
	done   chan struct{}
}

func (f *fixture) openTerminal(uuid string) (*terminal, error) {
	cmd := exec.Command("p", "attach", f.socket, uuid)
	t, err := startTerminal(cmd)
	if err == nil {
		f.clients = append(f.clients, t)
	}
	return t, err
}

func startTerminal(cmd *exec.Cmd) (*terminal, error) {
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, err
	}
	// creack/pty uses File.Fd for its ioctls, which switches the master to
	// blocking mode. Restore nonblocking I/O before starting the reader so
	// Close can interrupt it and actually hang up the terminal carrier.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		_ = master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	t := &terminal{cmd: cmd, pty: master, done: make(chan struct{})}
	go t.read()
	go func() { _ = cmd.Wait(); close(t.done) }()
	return t, nil
}

func (t *terminal) read() {
	buffer := make([]byte, 4096)
	for {
		n, err := t.pty.Read(buffer)
		if n > 0 {
			t.mu.Lock()
			t.text += string(buffer[:n])
			if len(t.text) > 65536 {
				t.text = tail(t.text, 65536)
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *terminal) command(line, marker string) error {
	// Wait for the tmux client to be connected before writing. The caller uses
	// authoritative presence to establish that barrier.
	t.mu.Lock()
	t.text = ""
	t.mu.Unlock()
	if _, err := io.WriteString(t.pty, line+"\r"); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		output := t.text
		t.mu.Unlock()
		if strings.Contains(output, marker) {
			return nil
		}
		select {
		case <-t.done:
			return fmt.Errorf("terminal exited waiting for %q: %s", marker, tail(output, 600))
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.mu.Lock()
	output := tail(t.text, 600)
	t.mu.Unlock()
	return fmt.Errorf("terminal output missing %q: %s", marker, output)
}

func (t *terminal) resize(columns, rows uint16) error {
	// Avoid File.Fd here: it would put the concurrent reader back in blocking
	// mode and prevent closePipe from promptly closing the carrier.
	raw, err := t.pty.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: columns})
	})
	if err != nil {
		return err
	}
	if ioctlErr != nil {
		return ioctlErr
	}
	return t.cmd.Process.Signal(syscall.SIGWINCH)
}

func (t *terminal) closePipe() { t.closed.Do(func() { _ = t.pty.Close() }) }
func (t *terminal) kill() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}
func (t *terminal) waitExit(limit time.Duration) error {
	select {
	case <-t.done:
		t.closePipe()
		return nil
	case <-time.After(limit):
		return errors.New("attachment client did not exit")
	}
}

func (t *terminal) killHelper() error {
	executable, err := os.Stat(t.cmd.Path)
	if err != nil {
		return fmt.Errorf("attachment executable: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found, err := killAttachmentHelper(t.cmd.Process, executable)
		if err != nil {
			return err
		}
		if found {
			return t.waitExit(15 * time.Second)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("trusted attachment helper child not found across client tasks")
}

type helper struct {
	conn *os.File
	cmd  *exec.Cmd
	done chan struct{}
}

func (h *helper) close() error {
	_ = h.conn.Close()
	select {
	case <-h.done:
		return nil
	case <-time.After(15 * time.Second):
		_ = h.cmd.Process.Kill()
		<-h.done
		return errors.New("attachment helper did not finish native teardown")
	}
}

func (f *fixture) startHelper(token string) (*helper, error) {
	parent, child, err := fixtureCarrierPair()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("p", "attach-helper")
	cmd.ExtraFiles = []*os.File{child}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	log, _ := os.OpenFile(filepath.Join(f.dir, "helper.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	err = cmd.Start()
	_ = child.Close()
	if log != nil {
		_ = log.Close()
	}
	if err != nil {
		parent.Close()
		return nil, err
	}
	h := &helper{conn: parent, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(h.done) }()
	init, _ := json.Marshal(map[string]any{"socket": f.socket, "token": token, "width": 80, "height": 24})
	if err = writeFrame(parent, initFrame, init); err != nil {
		_ = h.close()
		return nil, err
	}
	return h, nil
}

func fixtureCarrierPair() (*os.File, *os.File, error) {
	// Only the child endpoint is passed through ExtraFiles. The parent endpoint
	// must close on exec so closing the fixture carrier delivers EOF to helper.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "fixture-carrier")
	child := os.NewFile(uintptr(fds[1]), "helper-fd3")
	return parent, child, nil
}

func (f *fixture) openHelper(token string) (*helper, error) {
	h, err := f.startHelper(token)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(35 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = h.close()
			return nil, errors.New("timed out waiting for attachment helper readiness")
		}
		kind, detail, err := readFrame(h.conn, remaining)
		if err != nil {
			_ = h.close()
			return nil, err
		}
		if kind == readyFrame {
			return h, nil
		}
		if kind == errorFrame {
			_ = h.close()
			return nil, fmt.Errorf("helper rejected valid token: %s", tail(string(detail), 300))
		}
		if kind != 3 {
			_ = h.close()
			return nil, fmt.Errorf("unexpected helper frame: %d", kind)
		}
	}
}

func (f *fixture) manual(token string, expectFailure bool) error {
	h, err := f.startHelper(token)
	if err != nil {
		return err
	}
	if !expectFailure {
		return h.close()
	}
	kind, detail, err := readFrame(h.conn, 10*time.Second)
	if closeErr := h.close(); closeErr != nil {
		return closeErr
	}
	if err != nil {
		return err
	}
	if kind != errorFrame {
		return fmt.Errorf("invalid/expired token produced frame %d", kind)
	}
	if !strings.Contains(string(detail), "lifecycle request conflicts with current authority") {
		return fmt.Errorf("token rejection was not a claim conflict: %s", tail(string(detail), 300))
	}
	return nil
}

func writeFrame(w io.Writer, kind byte, data []byte) error {
	if len(data) > 32768 {
		return errors.New("oversize carrier initiation")
	}
	frame := make([]byte, 5+len(data))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(data)))
	copy(frame[5:], data)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func readFrame(file *os.File, limit time.Duration) (byte, []byte, error) {
	if err := file.SetReadDeadline(time.Now().Add(limit)); err != nil {
		return 0, nil, err
	}
	var header [5]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > 32768 {
		return 0, nil, errors.New("oversize carrier response")
	}
	data := make([]byte, length)
	_, err := io.ReadFull(file, data)
	return header[0], data, err
}
