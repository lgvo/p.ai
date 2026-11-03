// bundle-fixture drives the production Git service's commit bundle export in
// a disposable VM, from a repository the bundled source driver makes. The
// caller makes the private staging directory and inspects the bundle
// afterwards; the fixture never removes it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/tests/integration/internal/bundledgit"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bundle-fixture:", err)
		os.Exit(1)
	}
}

// prepare STATE PROJECT
// export STATE ACTIVATION PROJECT COMMIT STAGING
func run(args []string) error {
	prepare := len(args) == 3 && args[0] == "prepare"
	if !prepare && !(len(args) == 6 && args[0] == "export") {
		return errors.New("usage: bundle-fixture prepare STATE PROJECT | export STATE ACTIVATION PROJECT COMMIT STAGING")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if prepare {
		driver, err := bundledgit.Open(ctx, args[1])
		if err != nil {
			return err
		}
		if _, err := driver.CreateRepository(ctx, args[2]); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"repository_path": bundledgit.RepositoryPath(args[1], args[2])})
	}
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
	bundle, err := backend.ExportBundle(ctx, args[3], args[4], args[5])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"path": bundle.Path, "ref": bundle.Ref, "commit": bundle.Commit})
}
