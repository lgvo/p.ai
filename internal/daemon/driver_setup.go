package daemon

import (
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
	"github.com/lgvo/p.ai/internal/runtimedriver/supervise"
)

// driverLaunch is how the daemon of P instance instanceID starts the runtime
// driver host configuration pins: its executable, and a hello with the
// driver's own config and the settings core owns that a driver needs.
func driverLaunch(cfg control.RuntimeConfig, instanceID string) supervise.Config {
	pool := ""
	if cfg.Environment != nil {
		pool = cfg.Environment.BuilderStoragePool
	}
	var egress *protocol.PublicEgress
	if p := cfg.PublicEgress; p != nil {
		egress = &protocol.PublicEgress{Network: p.Network, ACL: p.ACL, BridgeIPv4: p.BridgeIPv4, DNS: p.DNS, SudoBinary: p.SudoBinary, NftBinary: p.NftBinary, BridgeProofBinary: p.BridgeProofBinary}
	}
	return supervise.Config{Path: cfg.Driver.Path, SHA256: cfg.Driver.SHA256, Hello: protocol.Hello{Config: cfg.Driver.Config,
		Core: protocol.Core{InstanceID: instanceID, EndpointPrefix: cfg.EndpointPrefix, DiskSourceCeilings: cfg.DiskSourceCeilings, BuilderStoragePool: pool, PublicEgress: egress}}}
}
