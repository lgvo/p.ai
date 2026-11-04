package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHostConfigIsOptionalAndLoopbackOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	for _, test := range []struct {
		data  string
		valid bool
	}{
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state"}`, true},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}}}`, true},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"0.0.0.0:2222","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}}}`, false},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"localhost:2222","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}}}`, false},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:2222","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}},"secret":1}}`, false},
	} {
		if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadHostConfig(path, check)
		if (err == nil) != test.valid {
			t.Fatalf("config %s: err=%v", test.data, err)
		}
	}
}

func TestRuntimeConfigRequiresConfinedBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	prefix := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}},"runtime":{"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + strings.Repeat("0f", 32) + `","config":{"binary":"` + dir + `/incus","user_socket":"` + dir + `/unix.socket.user","project":"p-test"}},"endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":`
	for _, test := range []struct {
		policy string
		valid  bool
	}{
		{`{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`, true},
		{`{"network":"public-egress","filesystem_mounts":[],"command":["/bin/sh"]}`, false},
		{`{"network":"none","filesystem_mounts":["/host"],"command":["/bin/sh"]}`, false},
		{`{"network":"none","filesystem_mounts":[],"command":["sh"]}`, false},
		{`{"network":"none","filesystem_mounts":[],"command":["/bin/sh","` + strings.Repeat("x", 3900) + `","` + strings.Repeat("y", 3900) + `"]}`, true},
		{`{"network":"none","filesystem_mounts":[],"command":["/bin/sh","` + strings.Repeat("x", 4096) + `","` + strings.Repeat("y", 4096) + `"]}`, false},
	} {
		if err := os.WriteFile(path, []byte(prefix+test.policy+`}}`), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadHostConfig(path, check)
		if (err == nil) != test.valid {
			t.Fatalf("policy %s: %v", test.policy, err)
		}
	}
}

func TestRuntimeDriverIsAnExecutablePinnedByPathAndDigestWithItsOwnConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	prefix := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}},"runtime":{"endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	digest := strings.Repeat("0f", 32)
	executable := `"path":"/opt/p/bin/p-runtime-incus","sha256":"` + digest + `"`
	incusConfig := `{"binary":"` + dir + `/incus","user_socket":"` + dir + `/unix.socket.user","project":"p-test"}`
	for _, tc := range []struct {
		name, driver string
		valid        bool
	}{
		{"pinned executable", `,"driver":{` + executable + `,"config":` + incusConfig + `}`, true},
		{"config left to the driver", `,"driver":{` + executable + `,"config":{"anything":1}}`, true},
		{"absent", ``, false},
		{"missing path", `,"driver":{"sha256":"` + digest + `","config":` + incusConfig + `}`, false},
		{"relative path", `,"driver":{"path":"bin/p-runtime-incus","sha256":"` + digest + `","config":` + incusConfig + `}`, false},
		{"unclean path", `,"driver":{"path":"/opt/p/../p/bin/p-runtime-incus","sha256":"` + digest + `","config":` + incusConfig + `}`, false},
		{"missing digest", `,"driver":{"path":"/opt/p/bin/p-runtime-incus","config":` + incusConfig + `}`, false},
		{"short digest", `,"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + digest[2:] + `","config":` + incusConfig + `}`, false},
		{"uppercase digest", `,"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + strings.ToUpper(digest) + `","config":` + incusConfig + `}`, false},
		{"missing config", `,"driver":{` + executable + `}`, false},
		{"null config", `,"driver":{` + executable + `,"config":null}`, false},
		{"config not an object", `,"driver":{` + executable + `,"config":["` + dir + `/incus"]}`, false},
		{"driver chosen by name", `,"driver":{"name":"incus",` + executable + `,"config":` + incusConfig + `}`, false},
		{"Incus settings outside the driver", `,"driver":{` + executable + `,"config":` + incusConfig + `},"incus_project":"p-test"`, false},
		{"runtime plugin selection", `,"driver":{` + executable + `,"config":` + incusConfig + `},"runtime_plugin_id":"org.p.runtime.incus"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(prefix+tc.driver+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("driver selection valid=%v err=%v", tc.valid, err)
			}
			if d := cfg.Runtime; tc.name == "pinned executable" && (d.Driver.Path != "/opt/p/bin/p-runtime-incus" || d.Driver.SHA256 != digest || string(d.Driver.Config) != incusConfig) {
				t.Fatalf("driver selection %+v", d.Driver)
			}
		})
	}
}

func TestGitDriverIsAnExecutablePinnedByPathAndDigestWithItsOwnConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	prefix := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0"`
	digest := strings.Repeat("0f", 32)
	executable := `"path":"/opt/p/bin/p-source-git","sha256":"` + digest + `"`
	for _, tc := range []struct {
		name, driver string
		valid        bool
	}{
		{"pinned executable", `,"driver":{` + executable + `,"config":{}}`, true},
		{"config left to the driver", `,"driver":{` + executable + `,"config":{"anything":1}}`, true},
		{"absent", ``, false},
		{"missing path", `,"driver":{"sha256":"` + digest + `","config":{}}`, false},
		{"relative path", `,"driver":{"path":"bin/p-source-git","sha256":"` + digest + `","config":{}}`, false},
		{"unclean path", `,"driver":{"path":"/opt/p/../p/bin/p-source-git","sha256":"` + digest + `","config":{}}`, false},
		{"missing digest", `,"driver":{"path":"/opt/p/bin/p-source-git","config":{}}`, false},
		{"short digest", `,"driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + digest[2:] + `","config":{}}`, false},
		{"uppercase digest", `,"driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.ToUpper(digest) + `","config":{}}`, false},
		{"missing config", `,"driver":{` + executable + `}`, false},
		{"null config", `,"driver":{` + executable + `,"config":null}`, false},
		{"config not an object", `,"driver":{` + executable + `,"config":[]}`, false},
		{"driver chosen by name", `,"driver":{"name":"git",` + executable + `,"config":{}}`, false},
		{"source package selection", `,"driver":{` + executable + `,"config":{}},"activation_path":"` + dir + `/git.json","source_plugin_id":"org.p.git"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(prefix+tc.driver+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("driver selection valid=%v err=%v", tc.valid, err)
			}
			if d := cfg.Git; tc.name == "pinned executable" && (d.Driver.Path != "/opt/p/bin/p-source-git" || d.Driver.SHA256 != digest || string(d.Driver.Config) != `{}`) {
				t.Fatalf("driver selection %+v", d.Driver)
			}
		})
	}
}

// The runtime driver runs the session host itself: host configuration
// selects no host package, and a runtime block that names one is invalid.
func TestRuntimeSelectsNoSessionHostPackage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}},"runtime":{"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + strings.Repeat("0f", 32) + `","config":{"binary":"` + dir + `/incus","user_socket":"` + dir + `/unix.socket.user","project":"p-test"}},"endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"none", "", true},
		{"host package and its activation", `,"activation_path":"` + dir + `/runtime.json","host_plugin_id":"org.p.tmux-host"`, false},
		{"host package", `,"host_plugin_id":"org.p.tmux-host"`, false},
		{"activation", `,"activation_path":"` + dir + `/runtime.json"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(base+tc.extra+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("runtime config valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestRuntimeAgentAdapterIsOnlyTrustedOptionalSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}},"runtime":{"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + strings.Repeat("0f", 32) + `","config":{"binary":"` + dir + `/incus","user_socket":"` + dir + `/unix.socket.user","project":"p-test"}},"endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"absent", "", true},
		{"selected", `,"agent_adapter":{"path":"` + dir + `/codex-adapter","sha256":"` + strings.Repeat("ab", 32) + `"}`, true},
		{"missing digest", `,"agent_adapter":{"path":"` + dir + `/codex-adapter"}`, false},
		{"an uppercase digest", `,"agent_adapter":{"path":"` + dir + `/codex-adapter","sha256":"` + strings.Repeat("AB", 32) + `"}`, false},
		{"relative path", `,"agent_adapter":{"path":"codex-adapter","sha256":"` + strings.Repeat("ab", 32) + `"}`, false},
		{"config", `,"agent_adapter":{"path":"` + dir + `/codex-adapter","sha256":"` + strings.Repeat("ab", 32) + `","config":{}}`, false},
		{"an activation file", `,"agent_adapter":{"activation_path":"` + dir + `/agent.json","plugin_id":"org.p.codex-adapter"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(base+tc.extra+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("trusted adapter config valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

// Host configuration selects the event handler by its package's path and
// digest, with the handler's config; an activation file selects nothing.
func TestEventsSelectAPackageByPathAndDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state"`
	digest := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, events string
		valid        bool
	}{
		{"absent", ``, true},
		{"selected", `,"events":{"path":"` + dir + `/file-log","sha256":"` + digest + `","config":{"path":"` + dir + `/events.ndjson","max_bytes":4096}}`, true},
		{"missing config", `,"events":{"path":"` + dir + `/file-log","sha256":"` + digest + `"}`, false},
		{"a config that is no object", `,"events":{"path":"` + dir + `/file-log","sha256":"` + digest + `","config":"x"}`, false},
		{"missing digest", `,"events":{"path":"` + dir + `/file-log","config":{}}`, false},
		{"a short digest", `,"events":{"path":"` + dir + `/file-log","sha256":"abc","config":{}}`, false},
		{"relative path", `,"events":{"path":"file-log","sha256":"` + digest + `","config":{}}`, false},
		{"unclean path", `,"events":{"path":"` + dir + `/x/../file-log","sha256":"` + digest + `","config":{}}`, false},
		{"an activation file", `,"events":{"activation_path":"` + dir + `/events.json","plugin_id":"org.p.filelog"}`, false},
		{"an activation file beside the selection", `,"events":{"path":"` + dir + `/file-log","sha256":"` + digest + `","config":{},"activation_path":"` + dir + `/events.json"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(base+tc.events+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("events config valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestRuntimeEnvironmentSelectsOnlySystemAndBuilderPool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"listen":"127.0.0.1:0","driver":{"path":"/opt/p/bin/p-source-git","sha256":"` + strings.Repeat("0f", 32) + `","config":{}}},"runtime":{"driver":{"path":"/opt/p/bin/p-runtime-incus","sha256":"` + strings.Repeat("0f", 32) + `","config":{"binary":"` + dir + `/incus","user_socket":"` + dir + `/unix.socket.user","project":"p-test"}},"endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"selected", `,"environment":{"system":"x86_64-linux","builder_storage_pool":"builders"}`, true},
		{"plugin selection", `,"environment":{"activation_path":"` + dir + `/environment.json","plugin_id":"org.p.environment.nix","system":"x86_64-linux","builder_storage_pool":"builders"}`, false},
		{"unknown system", `,"environment":{"system":"riscv64-linux","builder_storage_pool":"builders"}`, false},
		{"missing pool", `,"environment":{"system":"x86_64-linux"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(base+tc.extra+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadHostConfig(path, check)
			if (err == nil) != tc.valid {
				t.Fatalf("trusted environment config valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
