// Command p-runtime-incus is P's Incus runtime driver. The daemon starts it
// with no arguments and no environment, its protocol connection on
// descriptor 3; it takes its configuration from the hello and serves the
// Incus driver until the connection ends.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/incus"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "p-runtime-incus: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("takes no arguments: the P daemon starts it with its connection on descriptor 3")
	}
	conn, err := protocol.Inherited()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return protocol.Serve(ctx, conn, open)
}

// open makes the Incus driver the hello configures.
func open(_ context.Context, h protocol.Hello) (runtimedriver.Driver, error) {
	config, err := driverConfig(h)
	if err != nil {
		return nil, err
	}
	backend, err := incus.New(config)
	if err != nil {
		return nil, err
	}
	return incus.NewDriver(backend), nil
}

// driverConfig is the Incus driver's configuration: its own settings from
// the hello's config, with the settings core owns.
func driverConfig(h protocol.Hello) (incus.Config, error) {
	settings, err := incus.ParseDriverConfig(h.Config)
	if err != nil {
		return incus.Config{}, err
	}
	var egress *incus.PublicEgressConfig
	if p := h.Core.PublicEgress; p != nil {
		egress = &incus.PublicEgressConfig{Network: p.Network, ACL: p.ACL, BridgeIPv4: p.BridgeIPv4, DNS: p.DNS,
			SudoBinary: p.SudoBinary, NftBinary: p.NftBinary, BridgeProofBinary: p.BridgeProofBinary}
	}
	return incus.Config{Binary: settings.Binary, UserSocket: settings.UserSocket, Project: settings.Project, JournalctlBinary: settings.Journalctl,
		EndpointPrefix: h.Core.EndpointPrefix, DiskSourceCeilings: h.Core.DiskSourceCeilings,
		BuilderStoragePool: h.Core.BuilderStoragePool, PInstanceID: h.Core.InstanceID, PublicEgress: egress}, nil
}
