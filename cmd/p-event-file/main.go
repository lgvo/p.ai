// Command p-event-file is P's bundled file-log event handler, the entry of
// the file-log plugin package. The daemon starts it with no arguments and
// no environment, its protocol connection on descriptor 3; it takes the
// file to write and its rotation size from the hello's config and appends
// each event P sends as one JSON line, until the connection ends.
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

// identity is the file-log package's ID and version, as its plugin.json
// names them: P starts the handler only when they agree.
var identity = eventhandler.Identity{ID: "org.p.filelog", Version: "0.2.0"}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "p-event-file: %v\n", err)
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
	return eventhandler.Serve(ctx, conn, identity, open)
}

// open makes the handler its config describes.
func open(raw json.RawMessage) (eventhandler.Handler, error) {
	config, err := eventfile.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return func(_ context.Context, e eventhandler.Event) (eventhandler.Status, error) {
		line, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		if err := eventfile.Append(config, append(line, '\n')); err != nil {
			return "", err
		}
		return eventhandler.Handled, nil
	}, nil
}
