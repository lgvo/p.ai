package supervise

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/plugin"
)

// The names the tests start this test binary under: as noisyEntry, a
// handler that writes to its standard output and error for every event; as
// stuckEntry, one that answers the hello, then sends P a request whose
// answer cannot be written back, and reads nothing more.
const (
	noisyEntry = "noisy-handler"
	stuckEntry = "stuck-handler"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == stuckEntry {
		os.Exit(stuck())
	}
	if filepath.Base(os.Args[0]) == noisyEntry {
		conn, err := eventhandler.Inherited()
		if err != nil {
			os.Exit(2)
		}
		eventhandler.Serve(context.Background(), conn, eventhandler.Identity{ID: "example.noisy", Version: "1.0.0"},
			func(json.RawMessage) (eventhandler.Handler, error) {
				return func(context.Context, eventhandler.Event) (eventhandler.Status, error) {
					fmt.Println("noise on standard output")
					fmt.Fprintln(os.Stderr, "noise on standard error")
					return eventhandler.Handled, nil
				}, nil
			})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stuck is the stuck handler: it answers the hello, then calls a method of
// 4 MiB name, whose refusal P cannot write back to a handler that reads
// nothing, so that P's next writes to it block, and reads nothing more.
func stuck() int {
	conn, err := eventhandler.Inherited()
	if err != nil {
		return 2
	}
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		return 2
	}
	fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"result":{"protocol":"p.event-handler/v1","capabilities":{"id":"example.stuck","version":"1.0.0"}}}`)
	fmt.Fprintln(conn, `{"jsonrpc":"2.0","id":1,"method":"`+strings.Repeat("x", 4<<20)+`","params":{}}`)
	time.Sleep(time.Hour)
	return 0
}

// install writes a package of the entry name, this test binary, into a new
// directory and returns its path and digest.
func install(t *testing.T, id, entry string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "handler")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"p.plugin/v2","id":"` + id + `","version":"1.0.0","description":"Test handler","capability":"event-handler","entry":"` + entry + `"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entry), data, 0o700); err != nil {
		t.Fatal(err)
	}
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, pkg.SHA256
}

var event = eventhandler.Event{Schema: eventhandler.EventSchema, ID: "e-1", Kind: "operation.progress", OccurredAt: "2026-11-03T00:00:00Z", Instance: "p", Fields: map[string]string{"phase": "running"}}

// An event's deadline holds even while P cannot write to the handler: the
// handler that missed it is cut off at once, and is started again.
func TestTheDeadlineHoldsWhileWritesToTheHandlerBlock(t *testing.T) {
	dir, digest := install(t, "example.stuck", stuckEntry)
	s, err := Start(t.Context(), Config{Path: dir, SHA256: digest, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	// Let P's refusal of the handler's request take the connection's writes.
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.Handle(ctx, event)
	if took := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || took > 5*time.Second {
		t.Fatalf("Handle = %v after %v, want the deadline's error at once", err, took)
	}
}

// What a handler prints goes to the daemon's log, its standard error, and
// never to its standard output, which a CLI client such as `p plugins
// run-event` answers on.
func TestTheHandlersOutputGoesOnlyToTheLog(t *testing.T) {
	dir, digest := install(t, "example.noisy", noisyEntry)
	stdout, stderr := capture(t, &os.Stdout), capture(t, &os.Stderr)
	s, err := Start(t.Context(), Config{Path: dir, SHA256: digest, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if status, err := s.Handle(t.Context(), event); err != nil || status != eventhandler.Handled {
		t.Fatalf("Handle = %q, %v", status, err)
	}
	s.Close()
	out, log := stdout(), stderr()
	if out != "" {
		t.Fatalf("the handler wrote %q to the standard output", out)
	}
	if !strings.Contains(log, "noise on standard output") || !strings.Contains(log, "noise on standard error") {
		t.Fatalf("the log holds %q, want both of the handler's outputs", log)
	}
}

// capture replaces *f with a pipe until the test ends and returns a
// function that restores it and returns what was written to it.
func capture(t *testing.T, f **os.File) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := *f
	*f = w
	read := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		read <- string(data)
	}()
	restored := false
	restore := func() string {
		if !restored {
			restored = true
			*f = saved
			w.Close()
		}
		return <-read
	}
	t.Cleanup(func() {
		if !restored {
			restore()
		}
	})
	return restore
}
