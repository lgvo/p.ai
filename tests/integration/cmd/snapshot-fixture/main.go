// snapshot-fixture drives the production source-Git selection and committed
// tree capture in a disposable VM. Its hold mode keeps the staging tree alive
// for external inspection until SIGTERM, then closes it before exiting.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "snapshot-fixture:", err)
		os.Exit(1)
	}
}

// prepare STATE ACTIVATION PROJECT
// capture STATE ACTIVATION PROJECT SELECTOR-KIND SELECTOR-VALUE [hold]
func run(args []string) error {
	if len(args) != 4 && len(args) != 6 && len(args) != 7 {
		return errors.New("usage: snapshot-fixture prepare STATE ACTIVATION PROJECT | capture STATE ACTIVATION PROJECT KIND VALUE [hold]")
	}
	mode := args[0]
	if mode != "prepare" && mode != "capture" || mode == "prepare" && len(args) != 4 || mode == "capture" && len(args) != 6 && len(args) != 7 {
		return errors.New("invalid snapshot fixture arguments")
	}
	if len(args) == 7 && args[6] != "hold" {
		return errors.New("invalid snapshot fixture mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := control.OpenStore(args[1])
	if err != nil {
		return err
	}
	defer store.Close()
	active, err := plugin.LoadActivation(args[2])
	if err != nil {
		return err
	}
	backend, err := gitservice.New(store, args[1], active)
	if err != nil {
		return err
	}
	if mode == "prepare" {
		if err := backend.EnsureBare(ctx, args[3]); err != nil {
			return err
		}
		path, err := backend.RepositoryPath(args[3])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"repository_path": path})
	}
	snapshot, err := backend.CaptureCommittedSource(ctx, args[3], plugin.GitSourceSelector{Kind: args[4], Value: args[5]})
	if err != nil {
		return err
	}
	var signals chan os.Signal
	if len(args) == 7 {
		signals = make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"project": snapshot.Project(), "commit_oid": snapshot.CommitOID(),
		"tree_oid": snapshot.TreeOID(), "entries": snapshot.Entries(),
		"bytes": snapshot.Bytes(), "path": snapshot.Path(),
	}); err != nil {
		_ = snapshot.Close()
		return err
	}
	if len(args) == 7 {
		select {
		case <-signals:
		case <-time.After(60 * time.Second):
			_ = snapshot.Close()
			return errors.New("snapshot hold timed out")
		}
	}
	return snapshot.Close()
}
