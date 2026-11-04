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
	// A git that is not the one on PATH runs: Check runs the configured
	// one.
	t.Setenv("PATH", "/nonexistent")
	fake := filepath.Join(t.TempDir(), "git")
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n: > '"+marker+"'\necho 'git version 2.0.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(filepath.Join(shortTempDir(t), "source"), driverConfig(fake, "/usr/bin/ssh"), testBinary())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Check(t.Context()); err != nil {
		t.Fatalf("Check with the configured git: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Check did not run the configured git: %v", err)
	}
	missing, err := New(filepath.Join(shortTempDir(t), "source"), driverConfig("/nonexistent/git", "/usr/bin/ssh"), testBinary())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := missing.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("Check with a git that is not there = %v, want a refusal naming git", err)
	}
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
