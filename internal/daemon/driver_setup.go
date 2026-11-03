package daemon

import (
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/incus"
)

// newRuntimeDriver is the runtime driver host configuration selects, given
// the settings core owns that the driver needs. It is the only daemon code
// that knows a driver's package, until drivers run as their own executables.
func newRuntimeDriver(cfg control.RuntimeConfig, instanceID string) (runtimedriver.Driver, error) {
	config, err := incusConfig(cfg, instanceID)
	if err != nil {
		return nil, err
	}
	backend, err := incus.New(config)
	if err != nil {
		return nil, err
	}
	return incus.NewDriver(backend), nil
}

// incusConfig is the Incus driver's configuration: its own settings from
// cfg.Driver, the only driver P knows, with core's.
func incusConfig(cfg control.RuntimeConfig, instanceID string) (incus.Config, error) {
	if cfg.Driver.Name != control.IncusDriver {
		return incus.Config{}, fmt.Errorf("runtime driver %q unavailable", cfg.Driver.Name)
	}
	settings, err := incus.ParseDriverConfig(cfg.Driver.Config)
	if err != nil {
		return incus.Config{}, err
	}
	pool := ""
	if cfg.Environment != nil {
		pool = cfg.Environment.BuilderStoragePool
	}
	var egress *incus.PublicEgressConfig
	if p := cfg.PublicEgress; p != nil {
		egress = &incus.PublicEgressConfig{Network: p.Network, ACL: p.ACL, BridgeIPv4: p.BridgeIPv4, DNS: p.DNS, SudoBinary: p.SudoBinary, NftBinary: p.NftBinary, BridgeProofBinary: p.BridgeProofBinary}
	}
	return incus.Config{Binary: settings.Binary, UserSocket: settings.UserSocket, Project: settings.Project, EndpointPrefix: cfg.EndpointPrefix,
		DiskSourceCeilings: cfg.DiskSourceCeilings, BuilderStoragePool: pool, PInstanceID: instanceID, PublicEgress: egress}, nil
}
