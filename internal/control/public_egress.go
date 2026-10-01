package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// SHA256 binds the selected policy to the complete trusted public-egress
// substrate, including the exact read-only firewall proof commands.
func (c PublicEgressConfig) SHA256() string {
	data, _ := json.Marshal(c)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// PublicEgressConfig names a host-provisioned network and the read-only
// firewall observation tools. It cannot be supplied by a repository or a
// session request. The four static addresses are reserved by SQLite.
type PublicEgressConfig struct {
	Network           string   `json:"network"`
	ACL               string   `json:"acl"`
	BridgeIPv4        string   `json:"bridge_ipv4"`
	DNS               []string `json:"dns"`
	SudoBinary        string   `json:"sudo_binary"`
	NftBinary         string   `json:"nft_binary"`
	BridgeProofBinary string   `json:"bridge_proof_binary"`
}

var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
}

func PublicIPv4(address netip.Addr) bool {
	if !address.Is4() || !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicIPv4 {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func PublicEgressDeniedCIDRs() []string {
	out := make([]string, len(nonPublicIPv4))
	for i, prefix := range nonPublicIPv4 {
		out[i] = prefix.String()
	}
	return out
}

func validIncusName(name string) bool {
	if len(name) < 1 || len(name) > 63 || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '-' {
		return false
	}
	for _, c := range name {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' {
					return false
				}
			}
		}
	}
	return true
}

func (c PublicEgressConfig) Validate() error {
	if !validIncusName(c.Network) || !validIncusName(c.ACL) || c.Network == c.ACL {
		return errors.New("public egress requires distinct exact network and ACL names")
	}
	prefix, err := netip.ParsePrefix(c.BridgeIPv4)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 24 || prefix.Addr().As4()[3] != 1 ||
		!prefix.Contains(prefix.Addr()) || !prefix.Addr().IsPrivate() {
		return errors.New("public egress requires a private /24 bridge gateway ending in .1")
	}
	// This MVP image has a fixed immutable resolver file. A different resolver
	// requires a separately validated image/contract revision.
	if len(c.DNS) != 2 || c.DNS[0] != "1.1.1.1" || c.DNS[1] != "9.9.9.9" {
		return errors.New("public egress DNS differs from the pinned guest resolver")
	}
	seen := map[string]bool{}
	for _, raw := range c.DNS {
		addr, err := netip.ParseAddr(raw)
		if err != nil || !PublicIPv4(addr) || addr.String() != raw || seen[raw] {
			return fmt.Errorf("public egress DNS %q is not a distinct public IPv4 literal", raw)
		}
		seen[raw] = true
	}
	if c.SudoBinary == "" || c.NftBinary == "" || c.BridgeProofBinary == "" || strings.ContainsAny(c.SudoBinary+c.NftBinary+c.BridgeProofBinary, "\x00\n\r") {
		return errors.New("public egress firewall proof commands are required")
	}
	return nil
}

// PublicEgressGuestIPv4 returns one of four collision-free, fixed host slots
// within the trusted /24. SQLite reserves the slot before native creation.
func (c PublicEgressConfig) PublicEgressGuestIPv4(slot int) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if slot < 0 || slot > 3 {
		return "", errors.New("public egress address slot unavailable")
	}
	gateway, _ := netip.ParsePrefix(c.BridgeIPv4)
	address := gateway.Addr().As4()
	address[3] = byte(10 + slot)
	return netip.AddrFrom4(address).String() + "/24", nil
}
