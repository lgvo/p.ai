package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/eventhandler/conformance"
)

// entry is the name the tests start this test binary under: as the
// file-log package's entry, it runs main, the handler itself.
const entry = "p-event-file"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == entry {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Fixture{
		Install: func(t *testing.T, dir string) {
			manifest, err := os.ReadFile("../../plugins/bundled/file-log/plugin.json")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			conformance.CopyExecutable(t, filepath.Join(dir, entry))
		},
		Config: func(t *testing.T, private string) json.RawMessage {
			return json.RawMessage(`{"path":"` + filepath.Join(private, "events.ndjson") + `","max_bytes":1048576}`)
		},
		RefusedConfig: json.RawMessage(`{"path":"events.ndjson","max_bytes":1048576}`),
		Delivered:     delivered,
		Handles:       func(eventhandler.Event) bool { return true },
	})
}

// delivered are the IDs of the events in the log in private, in order.
func delivered(t *testing.T, private string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(private, "events.ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ids []string
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		e, err := eventhandler.ParseEvent(lines.Bytes())
		if err != nil {
			t.Fatalf("the log holds %q: %v", lines.Text(), err)
		}
		ids = append(ids, e.ID)
	}
	return ids
}

func TestTheExecutableTakesNoArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), entry)
	conformance.CopyExecutable(t, path)
	out, err := exec.Command(path, "--log", "/tmp/events.ndjson").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "takes no arguments") {
		t.Fatalf("started with arguments: %v, %q", err, out)
	}
}
