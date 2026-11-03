package git

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func init() { testBinaryRoles["serve-one-push"] = serveOnePushRole }

// serveOnePushRole is a driver's process serving one relayed push: its
// arguments are the storage and the socket the relay connects to. It prints
// "listening" once it accepts the connection and "approved" when core's
// Approve is called, which approves.
func serveOnePushRole(args []string) int {
	if len(args) != 2 {
		return 2
	}
	d, err := New(args[0], []byte(`{}`))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	listener, err := net.Listen("unix", args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("listening")
	conn, err := listener.Accept()
	if err != nil {
		return 1
	}
	serveRelayed(func(command string, stream sourcedriver.Stream) error {
		return d.ServeGit(context.Background(), command, stream, func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
			service := sourcedriver.Service{ID: "push", Project: project, Kind: kind, Branch: "work", MaxInput: 1 << 20, Deadline: time.Hour}
			return service, func(string, sourcedriver.OID, sourcedriver.OID) error {
				fmt.Println("approved")
				return nil
			}, nil
		})
	}, conn)
	return 0
}

func TestAfterTheDriversProcessDiesTheNextDriversCheckEndsTheGitItLeftRunning(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("app")
	f.SetHead("app", "work", tip)
	client := f.Client("app").(*clone)
	if _, err := client.Fetch(func(command string, stream sourcedriver.Stream) error {
		return f.driver.ServeGit(t.Context(), command, stream, openService("fetch"))
	}, "work"); err != nil {
		t.Fatal(err)
	}
	next := client.Commit(tip)
	// After its approval, receive-pack waits for the head's lock, which an
	// administrator holds: the approved update is in flight.
	repo, _ := f.driver.repository("app")
	f.git("app", "", "config", "core.filesRefLockTimeout", "60000")
	lock := filepath.Join(repo, "refs", "heads", "work.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(lock) })

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(filepath.Dir(f.storage), "driver.sock")
	driver := exec.Command(executable, "serve-one-push", f.storage, socket)
	driver.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	stdout, err := driver.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Process.Kill(); driver.Wait() })
	said := bufio.NewScanner(stdout)
	expectLine(t, said, "listening")
	push, err := client.relayed(socket, "push", "--quiet", client.url(), "+"+string(next)+":refs/heads/work")
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Start(); err != nil {
		t.Fatal(err)
	}
	pushed := make(chan error, 1)
	go func() { pushed <- push.Wait() }()
	expectLine(t, said, "approved")
	group := receivePackGroup(t, repo)
	t.Cleanup(func() {
		if len(liveGroupMembers(group)) > 0 {
			syscall.Kill(-group, syscall.SIGKILL)
		}
	})
	awaitHookEnd(t, group)

	if err := driver.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = driver.Wait()
	if len(liveGroupMembers(group)) == 0 {
		t.Fatal("receive-pack died with its driver; the case needs it to outlive the driver")
	}
	restarted := f.newDriver()
	if got := settleService(t, restarted, "push"); got != sourcedriver.Unknown {
		t.Fatalf("while the dead driver's receive-pack lives, its service settles %v, want unknown", got)
	}
	if err := restarted.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if members := liveGroupMembers(group); len(members) > 0 {
		t.Errorf("processes %v of the dead driver's service outlived the next driver's Check", members)
	}
	if got := settleService(t, restarted, "push"); got != sourcedriver.Applied {
		t.Errorf("after Check the dead driver's service settles %v, want applied", got)
	}

	// With the lock gone, a receive-pack still running would land the
	// update now.
	os.Remove(lock)
	time.Sleep(300 * time.Millisecond)
	if got := f.git("app", "", "rev-parse", "refs/heads/work"); got != string(tip) {
		t.Errorf("refs/heads/work moved to %s after the driver that approved it died", got)
	}
	if err := <-pushed; err == nil {
		t.Error("the dead driver's push succeeded")
	}
}

// expectLine reads the helper's next line, which must be want.
func expectLine(t *testing.T, said *bufio.Scanner, want string) {
	t.Helper()
	if !said.Scan() || said.Text() != want {
		t.Fatalf("the driver's process said %q (%v), want %q", said.Text(), said.Err(), want)
	}
}

// awaitHookEnd waits until the group's pre-receive hook has ended with the
// approval, while receive-pack goes on to its update.
func awaitHookEnd(t *testing.T, group int) {
	t.Helper()
	for limit := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		hook := false
		for _, pid := range liveGroupMembers(group) {
			cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			hook = hook || slices.Contains(strings.Split(string(cmdline), "\x00"), "git-hook")
		}
		if !hook {
			return
		}
		if time.Now().After(limit) {
			t.Fatal("the pre-receive hook did not end")
		}
	}
}

func TestCheckLeavesTheDriversOwnServicesRunning(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := stallReceive(t, ctx, f.driver, "own")
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("the driver's own service ended at its Check: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if got := settleService(t, f.newDriver(), "own"); got != sourcedriver.Unknown {
		t.Errorf("another driver settles the running service %v, want unknown", got)
	}
	cancel()
	<-result
	if got := settleService(t, f.newDriver(), "own"); got != sourcedriver.Applied {
		t.Errorf("another driver settles the ended service %v, want applied", got)
	}
}

func TestAStorageWhosePathHoldsGlobCharactersFindsItsServicesRecords(t *testing.T) {
	parent := filepath.Join(shortTempDir(t), "p[1]")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(parent, "source")
	d, err := New(storage, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := stallReceive(t, ctx, d, "own")
	other, err := New(storage, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := settleService(t, other, "own"); got != sourcedriver.Unknown {
		t.Errorf("another driver settles the running service %v, want unknown", got)
	}
	cancel()
	<-result
}

func TestAnUnreadableRecordsDirectoryFailsSettleAndCheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	f := newFixture(t).(*fixture)
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	dir := f.driver.recordsDirectory()
	if err := os.Mkdir(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if got, err := f.driver.Settle(t.Context(), sourcedriver.Effect{Kind: sourcedriver.EffectService, Service: "any"}); err == nil {
		t.Errorf("Settle over an unreadable records directory = %v, want an error", got)
	}
	if err := f.driver.Check(t.Context()); err == nil {
		t.Error("Check over an unreadable records directory succeeded")
	}
}

func TestARecordOfAnotherBootIsRemovedByCheckWithoutSignallingItsProcess(t *testing.T) {
	f := newFixture(t).(*fixture)
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A process of this boot now holds the process ID the record names.
	sleeper := exec.Command("sleep", "30")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sleeper.Process.Kill(); sleeper.Wait() })
	pid := sleeper.Process.Pid
	start, alive, err := leaderStart(pid)
	if err != nil || !alive {
		t.Fatalf("the sleeper's start time: %v", err)
	}
	record, err := json.Marshal(map[string]any{
		"service": "before-reboot", "owner": "another driver", "leader": pid, "start": start,
		"boot": "00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := f.driver.recordsDirectory()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, record, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settleService(t, f.driver, "before-reboot"); got != sourcedriver.Applied {
		t.Errorf("a service of another boot settles %v, want applied", got)
	}
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("Check kept the record of another boot")
	}
	if _, alive, _ := leaderStart(pid); !alive {
		t.Error("Check signalled the process that now holds the record's process ID")
	}
}

// stallReceive starts a receive service on d, as id, whose client never
// answers, and returns once Git has started.
func stallReceive(t *testing.T, ctx context.Context, d *Driver, id string) <-chan error {
	t.Helper()
	never, _ := net.Pipe()
	t.Cleanup(func() { never.Close() })
	started := &firstWrite{started: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		result <- d.ServeGit(ctx, "git-receive-pack '/app'", sourcedriver.Stream{In: never, Out: started}, openService(id))
	}()
	<-started.started
	return result
}
