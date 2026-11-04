package daemon

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestTheDriverStartsFromItsPinnedExecutableWithItsConfigAndCoresSettings(t *testing.T) {
	config := json.RawMessage(`{"binary":"/run/current-system/sw/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"user-1000"}`)
	digest := strings.Repeat("0f", 32)
	cfg := control.RuntimeConfig{Driver: control.DriverConfig{Path: "/opt/p/bin/p-runtime-incus", SHA256: digest, Config: config},
		EndpointPrefix: "/var/lib/p/endpoints", DiskSourceCeilings: []string{"/srv/p"},
		Environment: &control.EnvironmentConfig{System: "x86_64-linux", BuilderStoragePool: "builders"},
		PublicEgress: &control.PublicEgressConfig{Network: "p-public", ACL: "p-public-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1"},
			SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/run/current-system/sw/bin/nft", BridgeProofBinary: "/run/current-system/sw/bin/p-public-network-proof"}}
	got := driverLaunch(cfg, "11111111-1111-4111-8111-111111111111")
	if got.Path != "/opt/p/bin/p-runtime-incus" || got.SHA256 != digest || string(got.Hello.Config) != string(config) {
		t.Fatalf("the driver's executable and own config: %+v", got)
	}
	core := got.Hello.Core
	if core.InstanceID != "11111111-1111-4111-8111-111111111111" || core.EndpointPrefix != "/var/lib/p/endpoints" ||
		!slices.Equal(core.DiskSourceCeilings, []string{"/srv/p"}) || core.BuilderStoragePool != "builders" {
		t.Fatalf("core's settings: %+v", core)
	}
	e := core.PublicEgress
	if e == nil || e.Network != "p-public" || e.ACL != "p-public-acl" || e.BridgeIPv4 != "10.233.0.1/24" || !slices.Equal(e.DNS, []string{"1.1.1.1"}) ||
		e.SudoBinary != "/run/wrappers/bin/sudo" || e.NftBinary != "/run/current-system/sw/bin/nft" || e.BridgeProofBinary != "/run/current-system/sw/bin/p-public-network-proof" {
		t.Fatalf("the public egress substrate: %+v", e)
	}
	cfg.Environment, cfg.PublicEgress = nil, nil
	if core := driverLaunch(cfg, "11111111-1111-4111-8111-111111111111").Hello.Core; core.BuilderStoragePool != "" || core.PublicEgress != nil {
		t.Fatalf("without an environment or a substrate: %+v", core)
	}
}
