package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
)

// The driver runs the git and ssh its config names: it inherits no PATH
// when it runs as its own executable.
func TestNewTakesTheGitAndSSHItRunsFromItsConfig(t *testing.T) {
	for _, config := range []string{
		`{}`,
		`{"git":"/usr/bin/git"}`,
		`{"ssh":"/usr/bin/ssh"}`,
		`{"git":"git","ssh":"/usr/bin/ssh"}`,
		`{"git":"/usr/bin/git","ssh":"bin/ssh"}`,
		`{"git":"/usr/bin/../bin/git","ssh":"/usr/bin/ssh"}`,
		`{"git":"/usr/bin/git","ssh":"/usr/bin/ssh","name":"git"}`,
		`{"git":"/usr/bin/git","git":"/usr/bin/git","ssh":"/usr/bin/ssh"}`,
		`[]`,
	} {
		if _, err := New(shortTempDir(t), []byte(config), testBinary()); err == nil {
			t.Errorf("New accepted config %s", config)
		}
	}
	// A git and an ssh that are not the ones on PATH run: Check runs the
	// configured ones.
	t.Setenv("PATH", "/nonexistent")
	git := fakeProgram(t, "git", "echo 'git version 2.0.0'")
	ssh := fakeProgram(t, "ssh", "echo 'OpenSSH_10.0p2, OpenSSL 3.0.0' >&2")
	d, err := New(filepath.Join(shortTempDir(t), "source"), driverConfig(git, ssh), testBinary())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Check(t.Context()); err != nil {
		t.Fatalf("Check with the configured git and ssh: %v", err)
	}
	for _, program := range []string{git, ssh} {
		if _, err := os.Stat(program + ".ran"); err != nil {
			t.Fatalf("Check did not run the configured %s: %v", filepath.Base(program), err)
		}
	}
}

func TestCheckRefusesAGitOrSSHThatDoesNotRun(t *testing.T) {
	git := fakeProgram(t, "git", "echo 'git version 2.0.0'")
	ssh := fakeProgram(t, "ssh", "echo 'OpenSSH_10.0p2, OpenSSL 3.0.0' >&2")
	for _, c := range []struct {
		name, git, ssh, refusal string
	}{
		{"a git that is not there", "/nonexistent/git", ssh, "git"},
		{"an ssh that is not there", git, "/nonexistent/ssh", "ssh"},
		{"an ssh that fails", git, fakeProgram(t, "failing-ssh", "echo 'No user exists for uid 1000' >&2\nexit 255"), "ssh"},
		{"an ssh that is not OpenSSH", git, fakeProgram(t, "other-ssh", "echo 'Dropbear v2025.88' >&2"), "ssh"},
	} {
		d, err := New(filepath.Join(shortTempDir(t), "source"), driverConfig(c.git, c.ssh), testBinary())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := d.Check(t.Context()); err == nil || !strings.Contains(err.Error(), c.refusal) {
			t.Errorf("Check with %s = %v, want a refusal naming %s", c.name, err, c.refusal)
		}
	}
}

// fakeProgram writes an executable shell script that runs body after it
// leaves a file at its own path plus ".ran", and returns its path.
func fakeProgram(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n: > '"+path+".ran'\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The Git service's pre-receive hook runs the executable New names, as
// `<executable> git-hook`: the driver never takes its own program for it.
func TestTheHookRunsTheExecutableNewNames(t *testing.T) {
	for _, hook := range []string{"", "p-source-git", "/opt/p/../p/bin/p-source-git", "/opt/p/bin/p-source-git'"} {
		if _, err := New(filepath.Join(shortTempDir(t), "source"), localConfig(), hook); err == nil {
			t.Errorf("New accepted the hook executable %q", hook)
		}
	}
	f := newFixture(t).(*fixture)
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	f.hook = filepath.Join(dir, "p-source-git")
	if err := os.WriteFile(f.hook, []byte("#!/bin/sh\necho \"$@\" > '"+ran+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.driver.Close()
	f.driver = f.newDriver()
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("app")
	f.SetHead("app", "work", tip)
	client := f.Client("app")
	serve := func(command string, stream sourcedriver.Stream) error {
		return f.driver.ServeGit(t.Context(), command, stream, openService("push"))
	}
	if _, err := client.Fetch(serve, "work"); err != nil {
		t.Fatal(err)
	}
	if err := client.Push(serve, conformance.Update{Ref: "refs/heads/work", New: client.Commit(tip)}); err == nil {
		t.Fatal("a push the hook refused succeeded")
	}
	if got, err := os.ReadFile(ran); err != nil || string(got) != "git-hook\n" {
		t.Fatalf("the hook executable ran with %q (%v), want git-hook", got, err)
	}
}
