package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type proofInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i proofInfo) Name() string       { return "proof" }
func (i proofInfo) Size() int64        { return 0 }
func (i proofInfo) Mode() os.FileMode  { return i.mode }
func (i proofInfo) ModTime() time.Time { return time.Time{} }
func (i proofInfo) IsDir() bool        { return i.mode.IsDir() }
func (i proofInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestPublicProofCannotUseDaemonBinaryOrWritableAncestor(t *testing.T) {
	path := "/trusted/writable/proof"
	root := func(name string) (os.FileInfo, error) {
		if name == path {
			return proofInfo{mode: 0555}, nil
		}
		return proofInfo{mode: os.ModeDir | 0555}, nil
	}
	if err := validateRootProofBinaryWithLstat(path, root); err != nil {
		t.Fatal(err)
	}
	for name, modified := range map[string]func(string) (os.FileInfo, error){
		"daemon_leaf": func(name string) (os.FileInfo, error) {
			if name == path {
				return proofInfo{mode: 0555, uid: 1000}, nil
			}
			return root(name)
		},
		"writable_parent": func(name string) (os.FileInfo, error) {
			if name == "/trusted/writable" {
				return proofInfo{mode: os.ModeDir | 0775}, nil
			}
			return root(name)
		},
		"symbolic_parent": func(name string) (os.FileInfo, error) {
			if name == "/trusted/writable" {
				return proofInfo{mode: os.ModeSymlink | 0777}, nil
			}
			return root(name)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRootProofBinaryWithLstat(path, modified); err == nil {
				t.Fatal("replaceable firewall proof executable accepted")
			}
		})
	}
}

func TestSudoWrapperFollowsOnlyRootOwnedFixedGeneration(t *testing.T) {
	target := "/run/wrappers/wrappers.A1b2C3d4E5"
	base := func(path string) (os.FileInfo, error) {
		switch path {
		case "/run/wrappers/bin":
			return proofInfo{mode: os.ModeSymlink | 0777}, nil
		case target + "/sudo":
			return proofInfo{mode: 0555 | os.ModeSetuid}, nil
		default:
			return proofInfo{mode: os.ModeDir | 0755}, nil
		}
	}
	link := func(string) (string, error) { return target, nil }
	if err := validateSudoWrapper(base, link); err != nil {
		t.Fatal(err)
	}
	if err := validateSudoWrapper(base, func(string) (string, error) { return "/tmp/sudo", nil }); err == nil {
		t.Fatal("sudo link escaped fixed wrapper generation")
	}
	for name, altered := range map[string]func(string) (os.FileInfo, error){
		"daemon_owned_link": func(path string) (os.FileInfo, error) {
			if path == "/run/wrappers/bin" {
				return proofInfo{mode: os.ModeSymlink | 0777, uid: 1000}, nil
			}
			return base(path)
		},
		"writable_generation": func(path string) (os.FileInfo, error) {
			if path == target {
				return proofInfo{mode: os.ModeDir | 0775}, nil
			}
			return base(path)
		},
		"daemon_owned_sudo": func(path string) (os.FileInfo, error) {
			if path == target+"/sudo" {
				return proofInfo{mode: 0555, uid: 1000}, nil
			}
			return base(path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSudoWrapper(altered, link); err == nil {
				t.Fatal("unsafe sudo wrapper chain accepted")
			}
		})
	}
}

func testEgressSubstrate() *PublicEgressConfig {
	return &PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/nft/bin/nft", BridgeProofBinary: "/nix/store/proof/bin/p-public-network-proof"}
}

func TestPublicEgressReadsExactNetworkACLAndHostFirewall(t *testing.T) {
	c := testEgressSubstrate()
	bridge := egressNetworkJSON{Name: c.Network, Project: "default", Type: "bridge", Status: "Created", Managed: true, Config: map[string]string{
		"ipv4.address": c.BridgeIPv4, "ipv4.dhcp": "false", "ipv4.nat": "true", "ipv4.routing": "true", "ipv4.firewall": "true",
		"ipv6.address": "none", "dns.mode": "none", "security.acls": c.ACL,
		"security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
	}}
	network := bridge
	network.Config = map[string]string{} // Incus redacts this for CanView.
	acl := egressACLJSON{Name: c.ACL, Config: map[string]string{}, Ingress: []egressACLRuleJSON{}, Egress: expectedEgressRules(c)}
	var calls []string
	b := &Backend{config: Config{Binary: "/bin/incus", UserSocket: "/confined/socket", Project: "user-1000", PublicEgress: c}, validate: func(Config) error { return nil }}
	b.run = func(_ context.Context, _ string, argv []string, _ []string) ([]byte, error) {
		if !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) {
			return nil, errors.New("unconfined Incus command")
		}
		switch command := strings.Join(argv[3:], " "); command {
		case "network list --format json":
			calls = append(calls, "network")
			return json.Marshal([]egressNetworkJSON{network})
		default:
			return nil, errors.New("unexpected Incus command: " + command)
		}
	}
	b.proofRun = func(_ context.Context, path string, argv []string, env []string) ([]byte, error) {
		if path != c.SudoBinary || !slices.Equal(env, []string{"LANG=C", "LC_ALL=C"}) {
			return nil, errors.New("unclosed root proof invocation")
		}
		if slices.Equal(argv, []string{"-n", "--", c.BridgeProofBinary}) {
			calls = append(calls, "network-proof")
			return json.Marshal(egressPublicProofJSON{Network: bridge, ACL: acl})
		}
		if slices.Equal(argv, []string{"-n", "--", c.NftBinary, "-j", "list", "table", "inet", "p_egress_p_public_v1"}) {
			calls = append(calls, "nft")
			return firewallFixture(t), nil
		}
		return nil, errors.New("root proof argv changed")
	}
	if err := b.checkPublicEgress(context.Background()); err != nil || !slices.Equal(calls, []string{"network", "network-proof", "nft"}) {
		t.Fatalf("closed network observation: %v %v", err, calls)
	}
	delete(bridge.Config, "ipv4.dhcp")
	if err := b.checkPublicEgress(context.Background()); err == nil {
		t.Fatal("bridge without explicit no-DHCP accepted")
	}
	bridge.Config["ipv4.dhcp"] = "false"
	network.Config = nil
	if err := b.checkPublicEgress(context.Background()); err == nil {
		t.Fatal("missing or null confined network config accepted as redaction")
	}
	network.Config = map[string]string{}
	network.Config["ipv4.dhcp"] = "false"
	if err := b.checkPublicEgress(context.Background()); err == nil {
		t.Fatal("confined principal gained network-edit visibility")
	}
	delete(network.Config, "ipv4.dhcp")
	acl.Egress[0].Destination = "10.0.0.0/8"
	if err := b.checkPublicEgress(context.Background()); err == nil {
		t.Fatal("changed private-drop ACL accepted")
	}
	acl.Egress = expectedEgressRules(c)
	acl.Egress[0], acl.Egress[len(acl.Egress)-1] = acl.Egress[len(acl.Egress)-1], acl.Egress[0]
	if err := b.checkPublicEgress(context.Background()); err == nil {
		t.Fatal("private-drop rule moved after public allow was accepted")
	}
}

func TestPublicBridgeProofRejectsAmbiguousOrChangedAdminReadback(t *testing.T) {
	c := testEgressSubstrate()
	base := egressNetworkJSON{Name: c.Network, Project: "default", Type: "bridge", Status: "Created", Managed: true, Config: map[string]string{
		"ipv4.address": c.BridgeIPv4, "ipv4.dhcp": "false", "ipv4.nat": "true", "ipv4.routing": "true", "ipv4.firewall": "true",
		"ipv6.address": "none", "dns.mode": "none", "security.acls": c.ACL,
		"security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
	}}
	acl := egressACLJSON{Name: c.ACL, Config: map[string]string{}, Ingress: []egressACLRuleJSON{}, Egress: expectedEgressRules(c)}
	good, err := json.Marshal(egressPublicProofJSON{Network: base, ACL: acl})
	if err != nil {
		t.Fatal(err)
	}
	var returned []byte
	b := &Backend{config: Config{PublicEgress: c}}
	b.proofRun = func(_ context.Context, path string, args []string, env []string) ([]byte, error) {
		if path != c.SudoBinary || !slices.Equal(args, []string{"-n", "--", c.BridgeProofBinary}) || !slices.Equal(env, []string{"LANG=C", "LC_ALL=C"}) {
			return nil, errors.New("bridge helper invocation widened")
		}
		return returned, nil
	}
	returned = good
	if err := b.checkPublicBridge(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string][]byte{
		"wrong_project":        []byte(strings.Replace(string(good), `"project":"default"`, `"project":"other"`, 1)),
		"missing_config":       []byte(strings.Replace(string(good), `"config":{`, `"absent":{`, 1)),
		"extra_key":            []byte(strings.Replace(string(good), `"managed":true`, `"managed":true,"future_grant":true`, 1)),
		"duplicate_key":        []byte(strings.Replace(string(good), `"name":"p-public-v1"`, `"name":"p-public-v1","name":"p-public-v1"`, 1)),
		"dhcp_enabled":         []byte(strings.Replace(string(good), `"ipv4.dhcp":"false"`, `"ipv4.dhcp":"true"`, 1)),
		"wrong_acl":            []byte(strings.Replace(string(good), `"name":"p-public-v1-acl"`, `"name":"other"`, 1)),
		"missing_acl":          []byte(strings.Replace(string(good), `,"acl":{`, `,"missing_acl":{`, 1)),
		"null_acl_config":      []byte(strings.Replace(string(good), `"config":{},"ingress":[]`, `"config":null,"ingress":[]`, 1)),
		"null_acl_ingress":     []byte(strings.Replace(string(good), `"ingress":[]`, `"ingress":null`, 1)),
		"acl_allows_private":   []byte(strings.Replace(string(good), `"action":"drop"`, `"action":"allow"`, 1)),
		"acl_extra_rule_field": []byte(strings.Replace(string(good), `"action":"drop"`, `"action":"drop","future_grant":"unsafe"`, 1)),
		"duplicate_acl_key":    []byte(strings.Replace(string(good), `"acl":{`, `"acl":{},"acl":{`, 1)),
		"trailing":             append(slices.Clone(good), []byte(` {}`)...),
		"oversized":            []byte(strings.Repeat(" ", 64<<10+1)),
	} {
		t.Run(name, func(t *testing.T) {
			returned = changed
			if err := b.checkPublicBridge(context.Background()); err == nil {
				t.Fatal("unsafe admin bridge evidence accepted")
			}
		})
	}
}

func firewallFixture(t *testing.T) []byte {
	t.Helper()
	items := []map[string]any{
		{"table": map[string]any{"family": "inet", "name": "p_egress_p_public_v1"}},
		{"chain": map[string]any{"family": "inet", "table": "p_egress_p_public_v1", "name": "input", "type": "filter", "hook": "input", "prio": 0, "policy": "accept"}},
		{"chain": map[string]any{"family": "inet", "table": "p_egress_p_public_v1", "name": "forward", "type": "filter", "hook": "forward", "prio": 0, "policy": "accept"}},
	}
	for chain, rules := range expectedFirewallRules("p-public-v1") {
		for _, expr := range rules {
			items = append(items, map[string]any{"rule": map[string]any{"family": "inet", "table": "p_egress_p_public_v1", "chain": chain, "expr": expr}})
		}
	}
	raw, err := json.Marshal(map[string]any{"nftables": items})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPublicFirewallRequiresLiveInputForwardAndPostDNATDrops(t *testing.T) {
	valid := firewallFixture(t)
	if err := verifyPublicFirewall(valid, "p-public-v1"); err != nil {
		t.Fatalf("closed firewall refused: %v", err)
	}
	counted := []byte(strings.Replace(string(valid), `"counter":{"bytes":0,"packets":0}`, `"counter":{"bytes":480,"packets":8}`, 1))
	if slices.Equal(counted, valid) {
		t.Fatal("test did not change the dynamic DNAT counter")
	}
	if err := verifyPublicFirewall(counted, "p-public-v1"); err != nil {
		t.Fatalf("live DNAT counter was not normalized: %v", err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"missing_dnat": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"right":"dnat"`, `"right":"snat"`, 1))
		},
		"wrong_bridge": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"right":"p-public-v1"`, `"right":"other"`, 1))
		},
		"missing_private_drop": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"addr":"10.0.0.0"`, `"addr":"11.0.0.0"`, 1))
		},
		"wrong_hook_priority": func(raw []byte) []byte { return []byte(strings.Replace(string(raw), `"prio":0`, `"prio":1`, 1)) },
		"missing_terminal_drop": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"drop":null`, `"accept":null`, 1))
		},
		"missing_dnat_counter": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"counter":{"bytes":0,"packets":0}`, `"counter":{}`, 1))
		},
		"counter_after_drop": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `{"counter":{"bytes":0,"packets":0}},{"drop":null}`, `{"drop":null},{"counter":{"bytes":0,"packets":0}}`, 1))
		},
		"counter_extra_field": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"counter":{"bytes":0,"packets":0}`, `"counter":{"bytes":0,"packets":0,"future":1}`, 1))
		},
		"counter_fractional": func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"packets":0`, `"packets":0.5`, 1))
		},
		"trailing_json": func(raw []byte) []byte { return append(raw, []byte(` {}`)...) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyPublicFirewall(mutate(valid), "p-public-v1"); err == nil {
				t.Fatal("changed live firewall was accepted")
			}
		})
	}
}

func TestPublicNICIsExactAndNoneHasNoNIC(t *testing.T) {
	c := &PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24"}
	s := Session{PublicIPv4: "10.233.0.10/24"}
	if err := validatePublicSession(c, s); err != nil {
		t.Fatal(err)
	}
	want := publicNICDevice(c, s)
	if !exactPublicNIC(want, want) {
		t.Fatal("fixed public NIC refused")
	}
	for key, value := range map[string]string{"network": "other", "security.acls": "", "security.mac_filtering": "false", "security.ipv4_filtering": "false", "ipv4.address": "10.233.0.11"} {
		changed := make(map[string]string, len(want))
		for k, v := range want {
			changed[k] = v
		}
		changed[key] = value
		if exactPublicNIC(changed, want) {
			t.Fatalf("NIC mutation %s accepted", key)
		}
	}
	if err := validatePublicSession(c, Session{PublicIPv4: "10.233.0.14/24"}); err == nil {
		t.Fatal("unreserved public address accepted")
	}
	if err := validatePublicSession(nil, s); err == nil {
		t.Fatal("public NIC without substrate accepted")
	}
}

func TestPublicAddressPreflightRejectsForeignAndOpaqueNIC(t *testing.T) {
	c := testEgressSubstrate()
	s := Session{SessionUUID: "550e8400-e29b-41d4-a716-446655440000", PublicIPv4: "10.233.0.10/24"}
	peer := instanceJSON{Name: "foreign", Type: "container", ExpandedDevices: map[string]map[string]string{
		"network": {"type": "nic", "network": c.Network, "ipv4.address": "10.233.0.11"},
	}}
	b := &Backend{config: Config{Binary: "/bin/incus", UserSocket: "/confined/socket", Project: "user-1000", PublicEgress: c}, validate: func(Config) error { return nil }}
	b.run = func(_ context.Context, _ string, argv []string, _ []string) ([]byte, error) {
		if !slices.Equal(argv, []string{"--force-local", "--project", "user-1000", "list", "--format", "json"}) {
			return nil, errors.New("address preflight left confined project")
		}
		return json.Marshal([]instanceJSON{peer})
	}
	if err := b.checkPublicAddressCollision(context.Background(), s); err != nil {
		t.Fatalf("distinct exact address refused: %v", err)
	}
	peer.ExpandedDevices["network"]["ipv4.address"] = "10.233.0.10"
	if err := b.checkPublicAddressCollision(context.Background(), s); err == nil {
		t.Fatal("foreign peer with selected address accepted")
	}
	peer.ExpandedDevices["network"]["ipv4.address"] = "2001:db8::1"
	if err := b.checkPublicAddressCollision(context.Background(), s); err == nil {
		t.Fatal("IPv6 literal in peer IPv4 assignment accepted")
	}
	delete(peer.ExpandedDevices["network"], "ipv4.address")
	if err := b.checkPublicAddressCollision(context.Background(), s); err == nil {
		t.Fatal("opaque peer NIC accepted as collision-free")
	}
	peer.ExpandedDevices = nil
	if err := b.checkPublicAddressCollision(context.Background(), s); err == nil {
		t.Fatal("missing peer device inventory accepted")
	}
	peer.ExpandedDevices = map[string]map[string]string{"root": {"path": "/"}}
	if err := b.checkPublicAddressCollision(context.Background(), s); err == nil {
		t.Fatal("peer device without type accepted")
	}
	peer.Name = "p-" + s.SessionUUID
	if err := b.checkPublicAddressCollision(context.Background(), s); err != nil {
		t.Fatalf("own NIC is separately inspected: %v", err)
	}
}
