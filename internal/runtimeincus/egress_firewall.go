package runtimeincus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

func publicFirewallTable(network string) string {
	return "p_egress_" + strings.ReplaceAll(network, "-", "_")
}

func (b *Backend) checkPublicFirewall(ctx context.Context) error {
	c := b.config.PublicEgress
	if c == nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	run := b.proofRun
	if run == nil {
		run = runCommand
	}
	table := publicFirewallTable(c.Network)
	// The host owner grants this one read-only sudoers argv. P never receives a
	// command that can alter nftables or inspect an arbitrary table.
	raw, err := run(checkCtx, c.SudoBinary, []string{"-n", "--", c.NftBinary, "-j", "list", "table", "inet", table}, []string{"LANG=C", "LC_ALL=C"})
	if err != nil {
		return fmt.Errorf("public egress live packet filter unavailable: %w", err)
	}
	return verifyPublicFirewall(raw, c.Network)
}

func nftMatch(left any, op string, right any) any {
	return map[string]any{"match": map[string]any{"left": left, "op": op, "right": right}}
}

func nftInterface(network string) any {
	return nftMatch(map[string]any{"meta": map[string]any{"key": "iifname"}}, "==", network)
}

func nftDrop() any { return map[string]any{"drop": nil} }

func nftPrefix(prefix netip.Prefix) any {
	return map[string]any{"prefix": map[string]any{"addr": prefix.Addr().String(), "len": float64(prefix.Bits())}}
}

func expectedFirewallRules(network string) map[string][][]any {
	forward := [][]any{{nftInterface(network), nftMatch(map[string]any{"ct": map[string]any{"key": "status"}}, "in", "dnat"), map[string]any{"counter": map[string]any{"packets": float64(0), "bytes": float64(0)}}, nftDrop()}}
	for _, raw := range control.PublicEgressDeniedCIDRs() {
		prefix := netip.MustParsePrefix(raw)
		forward = append(forward, []any{nftInterface(network), nftMatch(map[string]any{"payload": map[string]any{"protocol": "ip", "field": "daddr"}}, "==", nftPrefix(prefix)), nftDrop()})
	}
	forward = append(forward, []any{nftInterface(network), nftMatch(map[string]any{"meta": map[string]any{"key": "nfproto"}}, "==", "ipv6"), nftDrop()})
	return map[string][][]any{"input": {{nftInterface(network), nftDrop()}}, "forward": forward}
}

func verifyPublicFirewall(raw []byte, network string) error {
	if len(raw) == 0 || len(raw) > maxOutput {
		return errors.New("public egress firewall proof size unavailable")
	}
	var document struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&document); err != nil || len(document.Nftables) == 0 {
		return errors.New("public egress firewall proof malformed")
	}
	if dec.Decode(new(any)) != io.EOF {
		// A second JSON object is not an Incus/nft observation.
		return errors.New("public egress firewall proof has trailing content")
	}
	table := publicFirewallTable(network)
	seenTable := false
	chains := map[string]bool{}
	actualRules := map[string][][]any{"input": {}, "forward": {}}
	for _, entry := range document.Nftables {
		if len(entry) != 1 {
			return errors.New("public egress firewall proof has ambiguous entries")
		}
		for kind, data := range entry {
			switch kind {
			case "metainfo":
				continue
			case "table":
				var value struct{ Family, Name string }
				if json.Unmarshal(data, &value) != nil || value.Family != "inet" || value.Name != table || seenTable {
					return errors.New("public egress firewall table changed")
				}
				seenTable = true
			case "chain":
				var value struct {
					Family string `json:"family"`
					Table  string `json:"table"`
					Name   string `json:"name"`
					Type   string `json:"type"`
					Hook   string `json:"hook"`
					Prio   int    `json:"prio"`
					Policy string `json:"policy"`
				}
				if json.Unmarshal(data, &value) != nil || value.Family != "inet" || value.Table != table ||
					(value.Name != "input" && value.Name != "forward") || value.Type != "filter" || value.Hook != value.Name || value.Prio != 0 || value.Policy != "accept" || chains[value.Name] {
					return errors.New("public egress firewall base chain changed")
				}
				chains[value.Name] = true
			case "rule":
				var value struct {
					Family string `json:"family"`
					Table  string `json:"table"`
					Chain  string `json:"chain"`
					Expr   []any  `json:"expr"`
				}
				if json.Unmarshal(data, &value) != nil || value.Family != "inet" || value.Table != table ||
					(value.Chain != "input" && value.Chain != "forward") {
					return errors.New("public egress firewall rule changed")
				}
				if value.Chain == "forward" && len(actualRules["forward"]) == 0 {
					if len(value.Expr) != 4 || !normalizeDNATCounter(value.Expr[2]) {
						return errors.New("public egress DNAT drop counter missing or malformed")
					}
					value.Expr[2] = map[string]any{"counter": map[string]any{"packets": float64(0), "bytes": float64(0)}}
				}
				actualRules[value.Chain] = append(actualRules[value.Chain], value.Expr)
			default:
				return errors.New("public egress firewall has unexpected object")
			}
		}
	}
	if !seenTable || !chains["input"] || !chains["forward"] || len(chains) != 2 || !reflect.DeepEqual(actualRules, expectedFirewallRules(network)) {
		return errors.New("public egress firewall drop proof changed")
	}
	return nil
}

// nft's anonymous counter values change as packets arrive. Only the exact
// counter between the DNAT match and terminal drop is normalized; every other
// expression and its order still has to match the trusted closed rule set.
func normalizeDNATCounter(expr any) bool {
	outer, ok := expr.(map[string]any)
	if !ok || len(outer) != 1 {
		return false
	}
	inner, ok := outer["counter"].(map[string]any)
	if !ok || len(inner) != 2 {
		return false
	}
	for _, key := range []string{"packets", "bytes"} {
		value, ok := inner[key].(float64)
		if !ok || value < 0 || value > 9007199254740991 || math.Trunc(value) != value {
			return false
		}
	}
	return true
}
