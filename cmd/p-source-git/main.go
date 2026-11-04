// Command p-source-git is P's bundled source driver. The daemon starts it
// with no arguments and only its HOME and SSH_AUTH_SOCK, its protocol
// connection on descriptor 3; it takes its configuration and its storage
// from the hello and serves the bundled Git driver until the connection
// ends.
//
// Started as `p-source-git git-hook`, it is the pre-receive hook of the
// driver's Git service instead: the hook script in the driver's storage
// executes the file the hello names as the driver's executable, the one the
// daemon checked and started, and the hook asks the driver over the push's
// own socket.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "p-source-git: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[1] == "git-hook" {
		return sourcegit.RunPreReceiveHook(context.Background(), os.Stdin)
	}
	if len(args) != 1 {
		return errors.New("takes no arguments: the P daemon starts it with its connection on descriptor 3, and its Git service runs it as `p-source-git git-hook`")
	}
	conn, err := protocol.Inherited()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var driver *sourcegit.Driver
	err = protocol.Serve(ctx, conn, func(_ context.Context, h protocol.Hello) (sourcedriver.Driver, error) {
		d, err := sourcegit.New(h.Core.Storage, h.Config, h.Core.Executable)
		driver = d
		return d, err
	})
	if driver != nil {
		// No Git service the driver started outlives it.
		driver.Close()
	}
	return err
}
