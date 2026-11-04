package daemon

import (
	"context"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
	"github.com/lgvo/p.ai/internal/sourcedriver/supervise"
)

// newSourceDriver starts the source driver executable host configuration
// pins and supervises it. It returns the driver, which core reaches only as
// a sourcedriver.Driver, and the function that stops it. The daemon refuses
// to start when the first start fails its launch checks, its digest, its
// hello or the driver's Check; every restarted driver passes the same Check,
// which ends the Git services a driver that died left running.
func newSourceDriver(ctx context.Context, cfg control.HostConfig) (sourcedriver.Driver, func() error, error) {
	driver, err := supervise.Start(ctx, sourceDriverLaunch(cfg))
	if err != nil {
		return nil, nil, err
	}
	return driver, driver.Close, nil
}

// sourceDriverLaunch is how the daemon starts the source driver host
// configuration pins: its executable, and a hello with the driver's own
// config and its storage, which is P's state directory.
func sourceDriverLaunch(cfg control.HostConfig) supervise.Config {
	return supervise.Config{Path: cfg.Git.Driver.Path, SHA256: cfg.Git.Driver.SHA256,
		Hello: protocol.Hello{Config: cfg.Git.Driver.Config, Core: protocol.Core{Storage: cfg.StateDir}}}
}
