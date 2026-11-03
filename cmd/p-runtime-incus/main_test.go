package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// executableName is the name the tests start this test binary under, which
// makes it run main: the driver itself, without needing a separate build.
const executableName = "p-runtime-incus"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == executableName {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestTheDriverTakesItsSettingsFromTheHello(t *testing.T) {
	config := json.RawMessage(`{"binary":"/run/current-system/sw/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"user-1000"}`)
	core := protocol.Core{InstanceID: "11111111-1111-4111-8111-111111111111", EndpointPrefix: "/var/lib/p/endpoints",
		DiskSourceCeilings: []string{"/srv/p"}, BuilderStoragePool: "builders",
		PublicEgress: &protocol.PublicEgress{Network: "p-public", ACL: "p-public-acl", BridgeIPv4: "198.18.0.1/24", DNS: []string{"1.1.1.1"},
			SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/run/current-system/sw/bin/nft", BridgeProofBinary: "/run/current-system/sw/bin/p-bridge-proof"}}
	got, err := driverConfig(protocol.Hello{Config: config, Core: core})
	if err != nil || got.Binary != "/run/current-system/sw/bin/incus" || got.UserSocket != "/var/lib/incus/unix.socket.user" || got.Project != "user-1000" {
		t.Fatalf("the driver's own settings: %+v %v", got, err)
	}
	if got.PInstanceID != core.InstanceID || got.EndpointPrefix != core.EndpointPrefix || !slices.Equal(got.DiskSourceCeilings, core.DiskSourceCeilings) ||
		got.BuilderStoragePool != core.BuilderStoragePool || got.JournalctlBinary != "" {
		t.Fatalf("core's settings: %+v", got)
	}
	e := got.PublicEgress
	if e == nil || e.Network != "p-public" || e.ACL != "p-public-acl" || e.BridgeIPv4 != "198.18.0.1/24" || !slices.Equal(e.DNS, []string{"1.1.1.1"}) ||
		e.SudoBinary != "/run/wrappers/bin/sudo" || e.NftBinary != "/run/current-system/sw/bin/nft" || e.BridgeProofBinary != "/run/current-system/sw/bin/p-bridge-proof" {
		t.Fatalf("the public egress substrate: %+v", e)
	}
	core.PublicEgress = nil
	if got, err := driverConfig(protocol.Hello{Config: config, Core: core}); err != nil || got.PublicEgress != nil {
		t.Fatalf("without a substrate: %+v %v", got.PublicEgress, err)
	}
	withJournal := json.RawMessage(`{"binary":"/run/current-system/sw/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"user-1000","journalctl":"/run/current-system/sw/bin/journalctl"}`)
	if got, err := driverConfig(protocol.Hello{Config: withJournal, Core: core}); err != nil || got.JournalctlBinary != "/run/current-system/sw/bin/journalctl" {
		t.Fatalf("the host journal: %q %v", got.JournalctlBinary, err)
	}
	if _, err := driverConfig(protocol.Hello{Config: json.RawMessage(`{"binary":"/bin/incus","user_socket":"/s","project":"p","name":"incus"}`), Core: core}); err == nil {
		t.Fatal("a config with a field the driver does not know was accepted")
	}
}

// The built driver, started as the daemon starts it, answers the hello with
// the Incus driver's own refusal of a host without Incus: its
// configuration came from the hello.
func TestTheExecutableServesTheIncusDriverConfiguredByTheHello(t *testing.T) {
	core, proc := start(t)
	hello := protocol.Hello{Config: json.RawMessage(`{"binary":"/nonexistent/bin/incus","user_socket":"/nonexistent/unix.socket.user","project":"user-1000"}`)}
	_, refusal := open(t.Context(), hello)
	if refusal == nil {
		t.Fatal("the Incus driver accepted a host without Incus")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if client, err := protocol.Connect(ctx, core, hello); err == nil || !strings.Contains(err.Error(), refusal.Error()) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("hello on a host without Incus = %v, want the Incus driver's refusal %q", err, refusal)
	}
	if code := exitCode(t, proc); code != 1 {
		t.Fatalf("the driver exited %d after a refused hello, want 1", code)
	}
}

func TestTheExecutableRefusesAnotherProtocolVersion(t *testing.T) {
	core, proc := start(t)
	core.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"p.runtime-driver/v0","config":{},"core":{"instance_id":"","endpoint_prefix":"","disk_source_ceilings":null,"builder_storage_pool":"","public_egress":null}}}` + "\n"))
	core.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(core).ReadBytes('\n')
	var res struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err != nil || json.Unmarshal(line, &res) != nil || res.Error == nil || res.Error.Code != -32001 {
		t.Fatalf("a hello of another version answered %q, %v; want a version refusal", line, err)
	}
	if code := exitCode(t, proc); code != 1 {
		t.Fatalf("the driver exited %d, want 1", code)
	}
}

func TestTheExecutableTakesNoArguments(t *testing.T) {
	path := executable(t)
	out, err := exec.Command(path, "--project", "default").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "takes no arguments") {
		t.Fatalf("started with arguments: %v, %q", err, out)
	}
}

// executable is this test binary, copied under the driver's name.
func executable(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), executableName)
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// start starts the driver as the daemon does: no arguments, no
// environment, its connection on descriptor 3. It returns core's end.
func start(t *testing.T) (*net.UnixConn, *os.Process) {
	t.Helper()
	path := executable(t)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	coreFile, driverFile := os.NewFile(uintptr(fds[0]), "core"), os.NewFile(uintptr(fds[1]), "driver")
	defer driverFile.Close()
	proc, err := os.StartProcess(path, []string{path}, &os.ProcAttr{Dir: "/", Env: []string{}, Files: []*os.File{nil, os.Stderr, os.Stderr, driverFile}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Kill() })
	c, err := net.FileConn(coreFile)
	coreFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*net.UnixConn), proc
}

func exitCode(t *testing.T, proc *os.Process) int {
	t.Helper()
	exited := make(chan *os.ProcessState, 1)
	go func() {
		state, _ := proc.Wait()
		exited <- state
	}()
	select {
	case state := <-exited:
		return state.ExitCode()
	case <-time.After(10 * time.Second):
		t.Fatal("the driver did not exit")
	}
	return -1
}
