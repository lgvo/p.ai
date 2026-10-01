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
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/active.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:0"}}`, true},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/active.json","source_plugin_id":"org.p.git","listen":"0.0.0.0:2222"}}`, false},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/active.json","source_plugin_id":"org.p.git","listen":"localhost:2222"}}`, false},
		{`{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/active.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:2222","secret":1}}`, false},
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
	prefix := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/git.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:0"},"runtime":{"activation_path":"` + dir + `/runtime.json","runtime_plugin_id":"org.p.runtime.incus","host_plugin_id":"org.p.tmux-host","incus_binary":"` + dir + `/incus","incus_user_socket":"` + dir + `/unix.socket.user","incus_project":"p-test","endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":`
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

func TestRuntimeAgentAdapterIsOnlyTrustedOptionalSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	check := privateFixtureChecker(dir)
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/git.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:0"},"runtime":{"activation_path":"` + dir + `/runtime.json","runtime_plugin_id":"org.p.runtime.incus","host_plugin_id":"org.p.tmux-host","incus_binary":"` + dir + `/incus","incus_user_socket":"` + dir + `/unix.socket.user","incus_project":"p-test","endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `","project_policy":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"absent", "", true},
		{"selected", `,"agent_adapter":{"activation_path":"` + dir + `/agent.json","plugin_id":"org.p.codex-adapter"}`, true},
		{"missing id", `,"agent_adapter":{"activation_path":"` + dir + `/agent.json"}`, false},
		{"relative activation", `,"agent_adapter":{"activation_path":"agent.json","plugin_id":"org.p.codex-adapter"}`, false},
		{"extra command", `,"agent_adapter":{"activation_path":"` + dir + `/agent.json","plugin_id":"org.p.codex-adapter","command":"/bin/sh"}`, false},
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
