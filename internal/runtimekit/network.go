package runtimekit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const ipBinary = "/run/current-system/sw/bin/ip"
const publicIPv6DisablePath = "/proc/sys/net/ipv6/conf/eth0/disable_ipv6"

func validatePublicNetwork(c PublicNetwork) error {
	address, err := netip.ParsePrefix(c.Address)
	gateway, gatewayErr := netip.ParseAddr(c.Gateway)
	if err != nil || gatewayErr != nil || !address.Addr().Is4() || address.Bits() != 24 ||
		address.Addr().As4()[3] < 10 || address.Addr().As4()[3] > 13 || !address.Contains(gateway) || gateway.As4()[3] != 1 {
		return errors.New("public guest address/gateway is outside the reserved static /24")
	}
	if len(c.DNS) != 2 || c.DNS[0] != "1.1.1.1" || c.DNS[1] != "9.9.9.9" {
		return errors.New("public guest DNS differs from pinned image")
	}
	return nil
}

func guestIP(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ipBinary, args...)
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	cmd.Stdin = nil
	data, err := cmd.Output()
	if len(data) > 32<<10 {
		return nil, errors.New("network observation exceeds bound")
	}
	if err != nil {
		return nil, fmt.Errorf("fixed ip observation failed: %w", err)
	}
	return data, nil
}

// PreparePublicNetwork runs only as the root-owned service pre-start. A none
// session must not acquire a NIC even if its project allows a public bridge.
func PreparePublicNetwork() error {
	if os.Geteuid() != 0 {
		return errors.New("public network preparation requires root service context")
	}
	cfg, err := ReadConfig()
	if err != nil {
		return err
	}
	if cfg.PublicNetwork == nil {
		if _, err := os.Lstat("/sys/class/net/eth0"); err == nil {
			return errors.New("none session unexpectedly has a public NIC")
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := validatePublicNetwork(*cfg.PublicNetwork); err != nil {
		return err
	}
	// Incus's ipv6.address=none prevents bridge IPv6 assignment, but the
	// kernel can still create an eth0 link-local address when the NIC appears.
	// Disable IPv6 on this exact interface before activating it; failure is a
	// hard refusal rather than a reason to tolerate an extra address.
	if err := disablePublicIPv6(); err != nil {
		return err
	}
	if err := installPinnedResolver("/etc/resolv.conf", cfg.PublicNetwork.DNS, validateResolverParent); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"link", "set", "dev", "eth0", "up"},
		{"-4", "address", "replace", cfg.PublicNetwork.Address, "dev", "eth0"},
		{"-4", "route", "replace", "default", "via", cfg.PublicNetwork.Gateway, "dev", "eth0"},
	} {
		if _, err := guestIP(args...); err != nil {
			return err
		}
	}
	return ValidatePublicNetwork(cfg.PublicNetwork)
}

func ValidatePublicNetwork(c *PublicNetwork) error {
	if c == nil {
		if _, err := os.Lstat("/sys/class/net/eth0"); err == nil {
			return errors.New("none session unexpectedly has a public NIC")
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := validatePublicNetwork(*c); err != nil {
		return err
	}
	if err := verifyPublicIPv6Disabled(publicIPv6DisablePath); err != nil {
		return err
	}
	var links []struct {
		IfName string `json:"ifname"`
	}
	if data, err := guestIP("-j", "link", "show"); err != nil || json.Unmarshal(data, &links) != nil || len(links) != 2 {
		return errors.New("public guest interface inventory unavailable")
	}
	seen := map[string]bool{}
	for _, link := range links {
		seen[link.IfName] = true
	}
	if !seen["lo"] || !seen["eth0"] || len(seen) != 2 {
		return errors.New("public guest has unexpected interface")
	}
	var addresses []guestAddressObservation
	data, err := guestIP("-j", "address", "show", "dev", "eth0")
	if err != nil {
		return fmt.Errorf("public guest address inventory unavailable: fixed ip command: %w", err)
	}
	if err := json.Unmarshal(data, &addresses); err != nil {
		return fmt.Errorf("public guest address inventory unavailable: malformed %d-byte JSON", len(data))
	}
	if len(addresses) != 1 || addresses[0].IfName != "eth0" || len(addresses[0].Info) != 1 {
		return fmt.Errorf("public guest address inventory unavailable: %s", describeGuestAddresses(addresses))
	}
	address, _ := netip.ParsePrefix(c.Address)
	if addresses[0].Info[0].Family != "inet" || addresses[0].Info[0].Local != address.Addr().String() || addresses[0].Info[0].Prefix != 24 {
		return errors.New("public guest address or IPv6 state changed")
	}
	var routes []struct {
		Destination string `json:"dst"`
		Gateway     string `json:"gateway"`
		Device      string `json:"dev"`
	}
	data, err = guestIP("-j", "route", "show", "default")
	if err != nil || json.Unmarshal(data, &routes) != nil || len(routes) != 1 || routes[0].Destination != "default" || routes[0].Gateway != c.Gateway || routes[0].Device != "eth0" {
		return errors.New("public guest default route changed")
	}
	if data, err = guestIP("-j", "-6", "route", "show", "default"); err != nil || len(bytes.TrimSpace(data)) != 2 || string(bytes.TrimSpace(data)) != "[]" {
		return errors.New("public guest IPv6 route present or unavailable")
	}
	return validatePinnedResolver(c.DNS)
}

func disablePublicIPv6() error {
	f, err := os.OpenFile(publicIPv6DisablePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("public guest cannot disable IPv6 on eth0: %w", err)
	}
	n, writeErr := f.Write([]byte("1\n"))
	closeErr := f.Close()
	if n != 2 || writeErr != nil || closeErr != nil {
		return errors.New("public guest IPv6 disable write failed")
	}
	return verifyPublicIPv6Disabled(publicIPv6DisablePath)
}

func verifyPublicIPv6Disabled(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("public guest IPv6 disable state unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 9))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || string(data) != "1\n" {
		return errors.New("public guest IPv6 on eth0 remains enabled or unverified")
	}
	return nil
}

func validateResolverParent(path string) error {
	if path != "/etc/resolv.conf" {
		return errors.New("public guest resolver path changed")
	}
	for _, ancestor := range []string{"/", "/etc"} {
		info, err := os.Lstat(ancestor)
		if err != nil || !info.IsDir() || owner(info) != 0 || info.Mode().Perm()&022 != 0 {
			return errors.New("public guest resolver parent is not trusted")
		}
	}
	return nil
}

// The image may initially provide NixOS's /etc/static/resolv.conf link. Its
// target is not an identity proof for P's selected DNS, so root replaces the
// link itself with a regular file built only from the validated session
// config. No existing target is followed or copied.
func installPinnedResolver(path string, dns []string, validateParent func(string) error) error {
	if err := validateParent(path); err != nil {
		return err
	}
	if len(dns) != 2 || dns[0] != "1.1.1.1" || dns[1] != "9.9.9.9" {
		return errors.New("public guest resolver selection changed")
	}
	info, err := os.Lstat(path)
	if err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(path)
			if readErr != nil || owner(info) != 0 || target != "/etc/static/resolv.conf" {
				return errors.New("public guest resolver link is unexpected")
			}
		case info.Mode().IsRegular():
			if owner(info) != 0 || info.Mode().Perm()&022 != 0 {
				return errors.New("public guest resolver file is not root-controlled")
			}
		default:
			return errors.New("public guest resolver path has an unsafe type")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("public guest resolver path unavailable")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".p-resolv-")
	if err != nil {
		return fmt.Errorf("public guest resolver creation failed: %w", err)
	}
	defer os.Remove(f.Name())
	content := []byte("nameserver " + dns[0] + "\nnameserver " + dns[1] + "\n")
	n, writeErr := f.Write(content)
	chmodErr := f.Chmod(0644)
	syncErr := f.Sync()
	closeErr := f.Close()
	if n != len(content) || writeErr != nil || chmodErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("public guest resolver installation failed")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("public guest resolver replacement failed: %w", err)
	}
	return nil
}

type guestAddressObservation struct {
	IfName string `json:"ifname"`
	Info   []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
		Prefix int    `json:"prefixlen"`
	} `json:"addr_info"`
}

// Keep failure evidence limited to interface/address metadata. In particular,
// never emit the full ip JSON, which may contain guest-controlled fields.
func describeGuestAddresses(addresses []guestAddressObservation) string {
	if len(addresses) == 0 {
		return "interfaces=0"
	}
	first := addresses[0]
	name := first.IfName
	if len(name) > 32 {
		name = name[:32]
	}
	parts := []string{fmt.Sprintf("interfaces=%d first=%q addresses=%d", len(addresses), name, len(first.Info))}
	for i, info := range first.Info {
		if i == 4 {
			parts = append(parts, "more_addresses=true")
			break
		}
		family, local := info.Family, info.Local
		if len(family) > 16 {
			family = family[:16]
		}
		if len(local) > 64 {
			local = local[:64]
		}
		parts = append(parts, fmt.Sprintf("address%d=%q/%d family=%q", i, local, info.Prefix, family))
	}
	return strings.Join(parts, " ")
}

func validatePinnedResolver(expected []string) error {
	info, err := os.Stat("/etc/resolv.conf")
	if err != nil || !info.Mode().IsRegular() || owner(info) != 0 || info.Mode().Perm()&022 != 0 || info.Size() > 4096 {
		return fmt.Errorf("public guest resolver is not root-owned and bounded: %s", describeResolverPath("/etc/resolv.conf"))
	}
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil || len(data) > 4096 {
		return errors.New("public guest resolver unavailable")
	}
	var observed []string
	scan := bufio.NewScanner(bytes.NewReader(data))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "nameserver" {
			return errors.New("public guest resolver has an unexpected directive")
		}
		observed = append(observed, fields[1])
	}
	if scan.Err() != nil || len(observed) != len(expected) {
		return errors.New("public guest resolver list changed")
	}
	for i := range observed {
		if observed[i] != expected[i] {
			return errors.New("public guest resolver address changed")
		}
	}
	return nil
}

// Only fixed-path metadata is included in startup errors. Resolver bytes may
// contain machine-specific search domains, so the failure path never logs it.
func describeResolverPath(path string) string {
	metadata := func(info os.FileInfo, err error) string {
		if err != nil {
			return fmt.Sprintf("error=%q", err.Error())
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "stat_type=unknown"
		}
		return fmt.Sprintf("type=%s uid=%d gid=%d mode=%#o size=%d", info.Mode().Type(), stat.Uid, stat.Gid, info.Mode().Perm(), info.Size())
	}
	literal, literalErr := os.Lstat(path)
	resolved, resolvedErr := os.Stat(path)
	result := "lstat(" + metadata(literal, literalErr) + ") stat(" + metadata(resolved, resolvedErr) + ")"
	if literalErr == nil && literal.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			result += " link_error"
		} else {
			if len(target) > 128 {
				target = target[:128]
			}
			result += fmt.Sprintf(" link=%q", target)
		}
	}
	return result
}
