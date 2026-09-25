package runtimeincus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

const publicNICName = "p-public"

var nftStoreBinary = regexp.MustCompile(`^/nix/store/[a-z0-9]{32}-nftables-[^/]+/bin/nft$`)
var bridgeProofStoreBinary = regexp.MustCompile(`^/nix/store/[a-z0-9]{32}-[^/]+/bin/p-public-network-proof$`)
var sudoWrapperGeneration = regexp.MustCompile(`^/run/wrappers/wrappers\.[A-Za-z0-9]{10}$`)

func validatePublicEgressConfig(c *PublicEgressConfig) error {
	if c == nil {
		return nil
	}
	copy := control.PublicEgressConfig{Network: c.Network, ACL: c.ACL, BridgeIPv4: c.BridgeIPv4, DNS: c.DNS, SudoBinary: c.SudoBinary, NftBinary: c.NftBinary, BridgeProofBinary: c.BridgeProofBinary}
	if err := copy.Validate(); err != nil {
		return err
	}
	if c.SudoBinary != "/run/wrappers/bin/sudo" || !nftStoreBinary.MatchString(c.NftBinary) || !bridgeProofStoreBinary.MatchString(c.BridgeProofBinary) {
		return errors.New("public egress proof requires the fixed root sudo wrapper and pinned store executables")
	}
	for _, path := range []string{c.SudoBinary, c.NftBinary, c.BridgeProofBinary} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("public egress proof binary must be an absolute clean path")
		}
		var err error
		if path == c.SudoBinary {
			err = validateSudoWrapper(os.Lstat, os.Readlink)
		} else {
			err = validateRootProofBinary(path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// The proof command must not be replaceable by the daemon account. In
// particular, accepting an executable owned by that account would let it
// print fabricated nft JSON. The only writable ancestor exception is the
// root-owned sticky Nix store directory; immutable store items remain closed.
func validateRootProofBinary(path string) error {
	return validateRootProofBinaryWithLstat(path, os.Lstat)
}

// NixOS creates /run/wrappers/bin as an atomic symlink to a fresh, root-owned
// wrappers.<generation> directory on boot. Only this known link is followed;
// the complete resolved target path is separately LSTAT-checked before sudo.
func validateSudoWrapper(lstat func(string) (os.FileInfo, error), readlink func(string) (string, error)) error {
	link := "/run/wrappers/bin"
	for _, path := range []string{"/", "/run", "/run/wrappers", link} {
		info, err := lstat(path)
		if err != nil || fileUID(info) != 0 {
			return errors.New("public egress sudo wrapper ancestor is not root-owned")
		}
		if path == link {
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("public egress sudo wrapper generation link missing")
			}
			continue
		}
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return errors.New("public egress sudo wrapper ancestor is writable or symbolic")
		}
	}
	target, err := readlink(link)
	if err != nil || !sudoWrapperGeneration.MatchString(target) {
		return errors.New("public egress sudo wrapper target changed")
	}
	return validateRootProofBinaryWithLstat(target+"/sudo", lstat)
}

func validateRootProofBinaryWithLstat(path string, lstat func(string) (os.FileInfo, error)) error {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		current = filepath.Join(current, part)
		info, err := lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || fileUID(info) != 0 {
			return errors.New("public egress proof path is absent, symbolic, or not root-owned")
		}
		if current == path {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
				return errors.New("public egress proof executable is writable or not regular")
			}
			continue
		}
		if !info.IsDir() {
			return errors.New("public egress proof ancestor is not a directory")
		}
		storeSticky := current == "/nix/store" && info.Mode()&os.ModeSticky != 0 && info.Mode().Perm() == 0775
		if info.Mode().Perm()&0022 != 0 && !storeSticky {
			return errors.New("public egress proof ancestor is writable")
		}
	}
	return nil
}

func validatePublicSession(c *PublicEgressConfig, s Session) error {
	if s.PublicIPv4 == "" {
		return nil
	}
	if c == nil || s.WorkspaceOwner != "" {
		return errors.New("public NIC requires a selected ordinary session substrate")
	}
	guest, err := netip.ParsePrefix(s.PublicIPv4)
	bridge, bridgeErr := netip.ParsePrefix(c.BridgeIPv4)
	if err != nil || bridgeErr != nil || !guest.Addr().Is4() || guest.Bits() != 24 || guest.Addr().As4()[3] < 10 || guest.Addr().As4()[3] > 13 || !bridge.Contains(guest.Addr()) {
		return errors.New("public NIC address is outside reserved static slots")
	}
	return nil
}

func publicNICDevice(c *PublicEgressConfig, s Session) map[string]string {
	return map[string]string{
		"type": "nic", "name": "eth0", "network": c.Network,
		"ipv4.address": strings.TrimSuffix(s.PublicIPv4, "/24"), "ipv6.address": "none",
		"security.acls": c.ACL, "security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
		"security.mac_filtering": "true", "security.ipv4_filtering": "true", "security.ipv6_filtering": "true",
	}
}

func exactPublicNIC(observed map[string]string, expected map[string]string) bool {
	if len(observed) != len(expected) {
		return false
	}
	for key, value := range expected {
		if observed[key] != value {
			return false
		}
	}
	return true
}

type egressNetworkJSON struct {
	Name    string            `json:"name"`
	Project string            `json:"project"`
	Type    string            `json:"type"`
	Status  string            `json:"status"`
	Managed bool              `json:"managed"`
	Config  map[string]string `json:"config"`
}

type egressACLRuleJSON struct {
	Action          string `json:"action"`
	State           string `json:"state"`
	Source          string `json:"source"`
	Destination     string `json:"destination"`
	Protocol        string `json:"protocol"`
	SourcePort      string `json:"source_port"`
	DestinationPort string `json:"destination_port"`
	ICMPType        string `json:"icmp_type"`
	ICMPCode        string `json:"icmp_code"`
	Description     string `json:"description"`
}

type egressACLJSON struct {
	Name    string              `json:"name"`
	Config  map[string]string   `json:"config"`
	Ingress []egressACLRuleJSON `json:"ingress"`
	Egress  []egressACLRuleJSON `json:"egress"`
}

type egressPublicProofJSON struct {
	Network egressNetworkJSON `json:"network"`
	ACL     egressACLJSON     `json:"acl"`
}

func expectedEgressRules(c *PublicEgressConfig) []egressACLRuleJSON {
	rules := []egressACLRuleJSON{{Action: "drop", State: "enabled", Destination: strings.Join(control.PublicEgressDeniedCIDRs(), ",")}}
	for _, address := range c.DNS {
		for _, protocol := range []string{"udp", "tcp"} {
			rules = append(rules, egressACLRuleJSON{Action: "allow", State: "enabled", Destination: address, Protocol: protocol, DestinationPort: "53"})
		}
	}
	rules = append(rules, egressACLRuleJSON{Action: "allow", State: "enabled", Protocol: "tcp", DestinationPort: "80,443"})
	return rules
}

func (b *Backend) checkPublicEgress(ctx context.Context) error {
	c := b.config.PublicEgress
	raw, err := b.command(ctx, "network", "list", "--format", "json")
	if err != nil {
		return err
	}
	var networks []egressNetworkJSON
	if err := decode(raw, &networks); err != nil {
		return err
	}
	var actual *egressNetworkJSON
	for i := range networks {
		if networks[i].Name == c.Network {
			if actual != nil {
				return errors.New("ambiguous public egress network")
			}
			actual = &networks[i]
		}
	}
	if actual == nil || actual.Type != "bridge" || !actual.Managed || actual.Status != "Created" || actual.Project != "default" {
		return errors.New("dedicated managed public egress bridge unavailable")
	}
	// Incus deliberately redacts network config from CanView callers. If this
	// user socket can see it, it has acquired network-edit authority, which is
	// outside P's confined-project contract. A fixed root helper below proves
	// the complete live config without granting that edit entitlement.
	if actual.Config == nil || len(actual.Config) != 0 {
		return errors.New("confined Incus principal unexpectedly has network config authority")
	}
	if err := b.checkPublicBridge(ctx); err != nil {
		return err
	}
	return b.checkPublicFirewall(ctx)
}

func (b *Backend) checkPublicBridge(ctx context.Context) error {
	c := b.config.PublicEgress
	checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	run := b.proofRun
	if run == nil {
		run = func(ctx context.Context, binary string, args, env []string) ([]byte, error) {
			return runCommandBounded(ctx, binary, args, env, 64<<10, 8<<10, false)
		}
	}
	raw, err := run(checkCtx, c.SudoBinary, []string{"-n", "--", c.BridgeProofBinary}, []string{"LANG=C", "LC_ALL=C"})
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return errors.New("public egress live network and ACL proof unavailable")
	}
	if err := control.RejectDuplicateKeys(raw); err != nil {
		return errors.New("public egress network and ACL proof has ambiguous JSON")
	}
	var proof egressPublicProofJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&proof); err != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("public egress network proof malformed or trailing")
	}
	network := proof.Network
	if network.Name != c.Network || network.Project != "default" || network.Type != "bridge" || !network.Managed || network.Status != "Created" {
		return errors.New("public egress bridge identity changed")
	}
	expected := map[string]string{
		"ipv4.address": c.BridgeIPv4, "ipv4.dhcp": "false", "ipv4.nat": "true", "ipv4.routing": "true", "ipv4.firewall": "true",
		"ipv6.address": "none", "dns.mode": "none", "security.acls": c.ACL,
		"security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
	}
	if len(network.Config) != len(expected) {
		return errors.New("public egress bridge config differs from trusted closed network")
	}
	for key, want := range expected {
		if network.Config[key] != want {
			return fmt.Errorf("public egress bridge %s differs from trusted value", key)
		}
	}
	acl := proof.ACL
	if acl.Name != c.ACL || acl.Config == nil || len(acl.Config) != 0 || acl.Ingress == nil || len(acl.Ingress) != 0 || acl.Egress == nil {
		return errors.New("public egress ACL missing or has unexpected ingress/config")
	}
	if !slices.Equal(acl.Egress, expectedEgressRules(c)) {
		return errors.New("public egress ACL rule order or content changed")
	}
	return nil
}

// SQLite reserves distinct P-session addresses. This native observation also
// refuses a conflicting or opaque managed NIC already present in the confined
// project. A trusted host owner can still change the bridge after this read;
// that actor owns the network substrate and is outside the guest threat model.
func (b *Backend) checkPublicAddressCollision(ctx context.Context, s Session) error {
	if s.PublicIPv4 == "" {
		return nil
	}
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) > 4 {
		return errors.New("public egress address inventory unavailable")
	}
	want := strings.TrimSuffix(s.PublicIPv4, "/24")
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] || in.Type != "container" {
			return errors.New("public egress address inventory ambiguous")
		}
		seen[in.Name] = true
		if in.Name == b.name(s) {
			continue // exact identity is separately checked by inspect()
		}
		if len(in.ExpandedDevices) == 0 {
			return errors.New("public egress peer device inventory unavailable")
		}
		for _, device := range in.ExpandedDevices {
			if device["type"] == "" {
				return errors.New("public egress peer device type unavailable")
			}
			if device["type"] != "nic" || device["network"] != b.config.PublicEgress.Network && device["parent"] != b.config.PublicEgress.Network {
				continue
			}
			address := device["ipv4.address"]
			if address == "" || address == "none" || strings.Contains(address, "/") {
				return errors.New("public egress peer NIC address is not an exact static IPv4")
			}
			parsed, err := netip.ParseAddr(address)
			if err != nil || !parsed.Is4() || parsed.String() != address {
				return errors.New("public egress peer NIC address is malformed")
			}
			if address == want {
				return errors.New("public egress static IPv4 address is already occupied")
			}
		}
	}
	return nil
}
