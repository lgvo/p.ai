package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func init() { testBinaryRoles["publish-one"] = publishOneRole }

// publishOneRole is a driver's process publishing one commit of project app
// from its head work to the origin's head main: its arguments are the
// storage, the ssh the driver runs, the origin's URL and the commit. It
// prints "pushing" when Publish's gate is called, which admits it.
func publishOneRole(args []string) int {
	if len(args) != 4 {
		return 2
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return 2
	}
	d, err := New(args[0], driverConfig(git, args[1]), testBinary())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p := sourcedriver.Publication{URL: args[2], Branch: "work", Commit: sourcedriver.OID(args[3]), Destination: "main", Relation: sourcedriver.RelationAbsent}
	_, err = d.Publish(context.Background(), "app", p, func() error {
		fmt.Println("pushing")
		return nil
	})
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// blockingSSH stands in for OpenSSH as the fixture's does, but first writes
// its process ID to @DIR@/ssh-pid and waits until @DIR@/release exists.
const blockingSSH = `#!/bin/sh
for command; do :; done
echo $$ > @DIR@/ssh-pid
while [ ! -e @DIR@/release ]; do @SLEEP@ 0.05; done
path=${command#* \'}
exec @GIT@ receive-pack @ORIGINS@/"${path%\'}"
`

// A push against the origin runs in a process group of its own, whose ssh
// and pack-objects stream the pack by themselves. When the driver that
// started it dies, the next driver's Check ends it before that driver
// serves, so a Publish answered as unknown cannot land later.
func TestAfterTheDriversProcessDiesTheNextDriversCheckEndsTheOriginPushItLeftRunning(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("app")
	f.SetHead("app", "work", tip)
	url := f.NewOrigin()
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	script := strings.NewReplacer("@DIR@", shellQuote(dir), "@SLEEP@", shellQuote(sleep), "@GIT@", shellQuote(f.gitPath),
		"@ORIGINS@", shellQuote(f.origins.dir)).Replace(blockingSSH)
	if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })

	driver := exec.Command(testBinary(), "publish-one", f.storage, ssh, url, string(tip))
	driver.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	stdout, err := driver.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Process.Kill(); driver.Wait() })
	expectLine(t, bufio.NewScanner(stdout), "pushing")
	var pid int
	for limit := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		raw, _ := os.ReadFile(filepath.Join(dir, "ssh-pid"))
		if pid, err = strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("the push did not reach its ssh")
		}
	}
	group, _ := processGroupOf(pid)
	t.Cleanup(func() {
		if len(liveGroupMembers(group)) > 0 {
			syscall.Kill(-group, syscall.SIGKILL)
		}
	})

	if err := driver.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = driver.Wait()
	if len(liveGroupMembers(group)) == 0 {
		t.Fatal("the push died with its driver; the case needs it to outlive the driver")
	}
	if err := f.newDriver().Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if members := liveGroupMembers(group); len(members) > 0 {
		t.Errorf("processes %v of the dead driver's push outlived the next driver's Check", members)
	}

	// With its ssh released, a push still running would land now.
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := f.OriginRef(url, "refs/heads/main"); got != "" {
		t.Errorf("the origin's main moved to %s after the driver that pushed it died", got)
	}
}

// An origin command's record exists before its git starts, so a driver
// that dies at any point after the start leaves processes a later Check
// finds: every process that carries the record's token in its environment,
// with its process group, here a command and the child it started.
func TestCheckEndsEveryProcessOfAnEarlierDriversOriginCommand(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	f := newFixture(t).(*fixture)
	earlier := f.newDriver()
	token, _, err := earlier.recordOrigin()
	if err != nil {
		t.Fatalf("recordOrigin: %v", err)
	}
	command := exec.Command("/bin/sh", "-c", shellQuote(sleep)+" 60 & exec "+shellQuote(sleep)+" 60")
	command.Env = []string{originTokenVariable + "=" + token}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	group := command.Process.Pid
	t.Cleanup(func() {
		syscall.Kill(-group, syscall.SIGKILL)
		command.Wait()
	})
	for limit := time.Now().Add(10 * time.Second); len(liveGroupMembers(group)) < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(limit) {
			t.Fatalf("the command's group holds %v, want it and its child", liveGroupMembers(group))
		}
	}
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if members := liveGroupMembers(group); len(members) > 0 {
		t.Errorf("processes %v of the earlier driver's origin command outlived the next driver's Check", members)
	}
	if records := originRecordFiles(t, f); len(records) > 0 {
		t.Errorf("the earlier driver's origin records %q outlived the next driver's Check", records)
	}
}

// originRecordFiles are the origin records in the fixture's storage.
func originRecordFiles(t *testing.T, f *fixture) []string {
	t.Helper()
	entries, err := os.ReadDir(f.driver.recordsDirectory())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), originRecordPrefix) {
			names = append(names, e.Name())
		}
	}
	return names
}

// An origin command whose record cannot be written never starts: its ssh
// never runs and the origin does not move. A Publish then answers a plain
// error, which proves nothing was sent, and an observation a failure of
// the driver's storage, not an unreachable origin. The records directory
// here is private but takes no file.
func TestAnOriginCommandWhoseRecordCannotBeWrittenNeverStarts(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("app")
	f.SetHead("app", "work", tip)
	url := f.NewOrigin()
	records := f.driver.recordsDirectory()
	if err := os.MkdirAll(records, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(records, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(records, 0o700) })
	p := sourcedriver.Publication{URL: url, Branch: "work", Commit: tip, Destination: "main", Relation: sourcedriver.RelationAbsent}
	gates := 0
	if _, err := f.driver.Publish(t.Context(), "app", p, func() error { gates++; return nil }); err == nil || errors.Is(err, sourcedriver.ErrUnknownOutcome) || gates != 0 {
		t.Errorf("a Publish whose push could not be recorded = %v after %d gates, want a plain error before the gate", err, gates)
	}
	if _, err := f.driver.ObserveOrigin(t.Context(), url); err == nil || errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Errorf("an ObserveOrigin that could not be recorded = %v, want a failure that is not ErrUnavailable", err)
	}
	if _, err := os.Stat(filepath.Join(f.origins.dir, "ssh-env")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the origin's ssh ran for a command that could not be recorded: %v", err)
	}
	if got := f.OriginRef(url, "refs/heads/main"); got != "" {
		t.Errorf("the origin's main moved to %s", got)
	}
}
