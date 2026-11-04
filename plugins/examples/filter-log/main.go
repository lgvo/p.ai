// Command filter-log is an example event handler, built apart from P: it
// appends to its configured file only the events that say a session became
// ready, and skips the rest.
//
// Build it with `go build -o filter-log ./plugins/examples/filter-log`,
// put filter-log beside this directory's plugin.json in a package directory
// of its own, and print its digest with `p plugins conformance`. Select the
// package in trusted host configuration's events block, by its path and
// that digest, with a config such as
// {"path": "/private/filter.ndjson", "max_bytes": 1048576}; then try it with
// `p plugins run-event HOST_JSON EVENT_JSON`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lgvo/p.ai/internal/eventfile"
	"github.com/lgvo/p.ai/internal/eventhandler"
)

// identity is the package's ID and version, as plugin.json names them.
var identity = eventhandler.Identity{ID: "org.p.example.filterlog", Version: "0.2.0"}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "filter-log: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("takes no arguments: the P daemon starts it with its connection on descriptor 3")
	}
	conn, err := eventhandler.Inherited()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return eventhandler.Serve(ctx, conn, identity, func(raw json.RawMessage) (eventhandler.Handler, error) {
		config, err := eventfile.ParseConfig(raw)
		if err != nil {
			return nil, err
		}
		return func(_ context.Context, e eventhandler.Event) (eventhandler.Status, error) {
			if !ready(e) {
				return eventhandler.Skipped, nil
			}
			line, err := json.Marshal(e)
			if err != nil {
				return "", err
			}
			if err := eventfile.Append(config, append(line, '\n')); err != nil {
				return "", err
			}
			return eventhandler.Handled, nil
		}, nil
	})
}

// ready reports whether e says a session became ready.
func ready(e eventhandler.Event) bool {
	return e.Kind == "session.condition_changed" && e.Fields["condition"] == "ready"
}
