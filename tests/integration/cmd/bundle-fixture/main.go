// bundle-fixture drives the bundled source driver's commit export in a
// disposable VM, from a repository the driver makes. The caller makes the
// private staging directory and inspects the bundle afterwards; the fixture
// never removes it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/tests/integration/internal/bundledgit"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bundle-fixture:", err)
		os.Exit(1)
	}
}

// prepare STATE PROJECT
// export STATE PROJECT COMMIT STAGING
func run(args []string) error {
	prepare := len(args) == 3 && args[0] == "prepare"
	if !prepare && !(len(args) == 5 && args[0] == "export") {
		return errors.New("usage: bundle-fixture prepare STATE PROJECT | export STATE PROJECT COMMIT STAGING")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	driver, err := bundledgit.Open(ctx, args[1])
	if err != nil {
		return err
	}
	if prepare {
		if _, err := driver.CreateRepository(ctx, args[2]); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"repository_path": bundledgit.RepositoryPath(args[1], args[2])})
	}
	exported, err := driver.Export(ctx, args[2], sourcedriver.OID(args[3]), args[4])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"path": exported.Bundle, "ref": exported.Ref, "commit": string(exported.Commit)})
}
