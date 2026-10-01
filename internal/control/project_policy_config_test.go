package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectPolicyDriftKeepsExistingSessionSnapshot(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	first := ProjectPolicy{Network: "none", FilesystemMounts: []FilesystemGrant{}, Command: []string{"/bin/sh"}}
	second := ProjectPolicy{Network: "none", FilesystemMounts: []FilesystemGrant{}, Command: []string{"/bin/bash"}}
	initial, firstSHA, err := ProjectPolicySnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	changed, secondSHA, err := ProjectPolicySnapshot(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSHA == secondSHA {
		t.Fatal("distinct trusted commands have one digest")
	}
	if err := store.CreateProject(ctx, "team/a", initial); err != nil {
		t.Fatal(err)
	}
	_, a, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "a", Project: "team/a", Branch: "main", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectPolicy(ctx, "team/a", changed); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetSession(ctx, a.UUID)
	if err != nil || stored.PolicySHA256 != firstSHA || string(stored.Policy) != string(initial) {
		t.Fatalf("old snapshot changed: %+v %v", stored, err)
	}
	_, b, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "b", Project: "team/a", Branch: "side", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if b.PolicySHA256 != secondSHA || string(b.Policy) != string(changed) {
		t.Fatalf("new session did not capture current policy: %+v", b)
	}
}

func TestOmittedMountPolicyIsCurrentAcrossRestart(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	store, err := testScopedOpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policy := ProjectPolicy{Network: "none", Command: []string{"/bin/sh"}}
	snapshot, expected, err := ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject(ctx, "team/legacy", snapshot); err != nil {
		t.Fatal(err)
	}
	_, session, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "legacy-create", Project: "team/legacy", Branch: "main", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if session.PolicySHA256 != expected {
		t.Fatal("new session immediately outdated")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := testScopedOpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.GetSession(ctx, session.UUID)
	if err != nil || got.PolicySHA256 != expected || string(got.Policy) != string(snapshot) {
		t.Fatalf("normalized snapshot changed after restart: %+v %v", got, err)
	}
	_, current, err := ProjectPolicySnapshot(policy)
	if err != nil || current != got.PolicySHA256 {
		t.Fatalf("same trusted policy became outdated: %s %v", current, err)
	}
}

func TestExactProjectPolicyMapAndCompatibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	base := `{"schema":"p.host/v1","state_dir":"` + dir + `/state","git":{"activation_path":"` + dir + `/git.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:0"},"runtime":{"activation_path":"` + dir + `/runtime.json","runtime_plugin_id":"org.p.runtime.incus","host_plugin_id":"org.p.tmux-host","incus_binary":"` + dir + `/incus","incus_user_socket":"` + dir + `/unix.socket.user","incus_project":"p-test","endpoint_prefix":"` + dir + `/endpoints","disk_source_ceilings":["` + dir + `/endpoints"],"base_image_fingerprint":"` + strings.Repeat("a", 64) + `",`
	policy := `{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"singleton compatibility", `"project_policy":` + policy, true},
		{"exact map", `"project_policies":{"team/a":` + policy + `,"team/b":{"network":"none","filesystem_mounts":[],"command":["/bin/bash"]}}`, true},
		{"map omitted empty mounts", `"project_policies":{"team/a":{"network":"none","command":["/bin/sh"]}}`, true},
		{"map null empty mounts", `"project_policies":{"team/a":{"network":"none","filesystem_mounts":null,"command":["/bin/sh"]}}`, true},
		{"empty explicit map", `"project_policies":{}`, true},
		{"both modes", `"project_policy":` + policy + `,"project_policies":{"team/a":` + policy + `}`, false},
		{"both even empty singleton", `"project_policy":{},"project_policies":{}`, false},
		{"missing mode", `"agent_adapter":null`, false},
		{"null map", `"project_policies":null`, false},
		{"wildcard", `"project_policies":{"team/*":` + policy + `}`, false},
		{"prefix alias", `"project_policies":{"team/../a":` + policy + `}`, false},
		{"unsafe map value", `"project_policies":{"team/a":{"network":"public-egress","filesystem_mounts":[],"command":["/bin/sh"]}}`, false},
		{"unknown map grant", `"project_policies":{"team/a":{"network":"none","filesystem_mounts":[],"command":["/bin/sh"],"privileged":true}}`, false},
		{"duplicate exact key", `"project_policies":{"team/a":` + policy + `,"team/a":` + policy + `}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(base+tc.fields+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadHostConfig(path, privateFixtureChecker(dir))
			if (err == nil) != tc.valid {
				t.Fatalf("accepted=%v, want %v: %v", err == nil, tc.valid, err)
			}
			if err != nil {
				return
			}
			if tc.name == "exact map" {
				a, ok := cfg.Runtime.PolicyForProject("team/a")
				b, okB := cfg.Runtime.PolicyForProject("team/b")
				if !ok || !okB || a.Command[0] != "/bin/sh" || b.Command[0] != "/bin/bash" {
					t.Fatalf("cross-project selection: %+v %+v", a, b)
				}
				for _, missing := range []string{"team", "team/a/child", "team/c"} {
					if _, ok := cfg.Runtime.PolicyForProject(missing); ok {
						t.Fatalf("implicit fallback for %q", missing)
					}
				}
			}
			if tc.name == "singleton compatibility" {
				if _, ok := cfg.Runtime.PolicyForProject("any/exact/path"); !ok {
					t.Fatal("legacy policy lost")
				}
			}
		})
	}
	var p ProjectPolicy
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		t.Fatal(err)
	}
	normalized, sha, err := ProjectPolicySnapshot(p)
	if err != nil || len(sha) != 64 || string(normalized) != `{"command":["/bin/sh"],"filesystem_mounts":[],"network":"none"}` {
		t.Fatalf("snapshot %s %s %v", normalized, sha, err)
	}
	for _, form := range []string{
		`{"network":"none","command":["/bin/sh"]}`,
		`{"network":"none","filesystem_mounts":null,"command":["/bin/sh"]}`,
	} {
		var equivalent ProjectPolicy
		if err := json.Unmarshal([]byte(form), &equivalent); err != nil {
			t.Fatal(err)
		}
		other, otherSHA, err := ProjectPolicySnapshot(equivalent)
		if err != nil || otherSHA != sha || string(other) != string(normalized) {
			t.Fatalf("equivalent policy not canonical: %s %s %v", other, otherSHA, err)
		}
	}
}
