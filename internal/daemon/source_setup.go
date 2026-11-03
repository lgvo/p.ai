package daemon

import (
	"context"
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
)

// newSourceDriver starts the source driver host configuration selects: the
// bundled Git driver, in process, over the state directory. It returns the
// driver and the function that ends the Git services the driver runs and
// refuses later ones. It refuses to start a driver whose storage or Git tools
// fail its check, which also ends the Git services a driver that died left
// running.
//
// This file is the only daemon code that names the bundled driver's package,
// as internal/sourcedriver/git's boundary test checks; the rest of the daemon
// reaches the driver as a sourcedriver.Driver.
func newSourceDriver(ctx context.Context, cfg control.HostConfig) (sourcedriver.Driver, func() error, error) {
	driver, err := sourcegit.New(cfg.StateDir, cfg.Git.Driver.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("source driver: %w", err)
	}
	if err := driver.Check(ctx); err != nil {
		return nil, nil, fmt.Errorf("source driver: %w", err)
	}
	return driver, driver.Close, nil
}
