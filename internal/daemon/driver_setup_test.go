package daemon

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestOnlyTheIncusDriverIsSelectedAndItGetsCoresSettings(t *testing.T) {
	config := json.RawMessage(`{"binary":"/run/current-system/sw/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"user-1000"}`)
	cfg := control.RuntimeConfig{Driver: control.RuntimeDriverConfig{Name: "incus", Config: config}, EndpointPrefix: "/var/lib/p/endpoints",
		DiskSourceCeilings: []string{"/srv/p"}, Environment: &control.EnvironmentConfig{System: "x86_64-linux", BuilderStoragePool: "builders"},
		PublicEgress: &control.PublicEgressConfig{Network: "p-public", ACL: "p-public-acl", DNS: []string{"1.1.1.1"}}}
	got, err := incusConfig(cfg, "11111111-1111-4111-8111-111111111111")
	if err != nil || got.Binary != "/run/current-system/sw/bin/incus" || got.UserSocket != "/var/lib/incus/unix.socket.user" || got.Project != "user-1000" {
		t.Fatalf("Incus driver settings %+v %v", got, err)
	}
	if got.PInstanceID != "11111111-1111-4111-8111-111111111111" || got.EndpointPrefix != cfg.EndpointPrefix || !slices.Equal(got.DiskSourceCeilings, cfg.DiskSourceCeilings) ||
		got.BuilderStoragePool != "builders" || got.PublicEgress == nil || got.PublicEgress.Network != "p-public" || got.PublicEgress.ACL != "p-public-acl" {
		t.Fatalf("the Incus driver lacks core's settings: %+v", got)
	}
	cfg.Driver.Name = "kubernetes"
	if got, err := incusConfig(cfg, "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatalf("unknown runtime driver selected with settings %+v", got)
	}
}
