package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestTheSourceDriverStartsFromItsPinnedExecutableWithItsConfigAndStorage(t *testing.T) {
	config := json.RawMessage(`{"git":"/run/current-system/sw/bin/git","ssh":"/run/current-system/sw/bin/ssh"}`)
	digest := strings.Repeat("0f", 32)
	cfg := control.HostConfig{StateDir: "/var/lib/p", Git: &control.GitConfig{Listen: "127.0.0.1:0",
		Driver: control.DriverConfig{Path: "/opt/p/bin/p-source-git", SHA256: digest, Config: config}}}
	got := sourceDriverLaunch(cfg)
	if got.Path != "/opt/p/bin/p-source-git" || got.SHA256 != digest || string(got.Hello.Config) != string(config) {
		t.Fatalf("the driver's executable and own config: %+v", got)
	}
	if got.Hello.Core.Storage != "/var/lib/p" {
		t.Fatalf("the driver's storage is %q, want the state directory", got.Hello.Core.Storage)
	}
}
