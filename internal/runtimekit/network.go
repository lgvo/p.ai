package runtimekit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const ipBinary = "/run/current-system/sw/bin/ip"
const publicDNSService = "p-dns-over-https.service"
const publicResolverEndpoint = "127.0.0.1:53"
const publicResolverContent = "nameserver 127.0.0.1\n"
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
	return prepareGuestPublicNetwork(cfg.PublicNetwork, guestNetworkPreparation{
		none: validateNonePublicNetwork,
		ipv6: disablePublicIPv6,
		resolver: func(dns []string) error {
			return installPinnedResolver("/etc/resolv.conf", dns, validateResolverParent)
		},
		ip:            guestIP,
		startResolver: startPublicResolver,
		verify:        ValidatePublicNetwork,
	})
}

type guestNetworkPreparation struct {
	none          func() error
	ipv6          func() error
	resolver      func([]string) error
	ip            func(...string) ([]byte, error)
	startResolver func() error
	verify        func(*PublicNetwork) error
}

func validateNonePublicNetwork() error {
	if _, err := os.Lstat("/sys/class/net/eth0"); err == nil {
		return errors.New("none session unexpectedly has a public NIC")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func prepareGuestPublicNetwork(c *PublicNetwork, setup guestNetworkPreparation) error {
	// This branch never starts a unit, installs DNS, or invokes an IP command.
	if c == nil {
		return setup.none()
	}
	if err := validatePublicNetwork(*c); err != nil {
		return err
	}
	if err := setup.ipv6(); err != nil {
		return err
	}
	if err := setup.resolver(c.DNS); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"link", "set", "dev", "eth0", "up"},
		{"-4", "address", "replace", c.Address, "dev", "eth0"},
		{"-4", "route", "replace", "default", "via", c.Gateway, "dev", "eth0"},
	} {
		if _, err := setup.ip(args...); err != nil {
			return err
		}
	}
	// The image does not autostart this unit: no public DNS contact precedes the
	// trusted address/route setup, and old images without the unit fail closed.
	if err := setup.startResolver(); err != nil {
		return err
	}
	return setup.verify(c)
}

func publicResolverCommand(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/usr/libexec/p/systemctl", append([]string{"--system"}, args...)...)
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fixed DoH resolver unit unavailable or failed: %w", err)
	}
	return nil
}

func startPublicResolver() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := publicResolverCommand(ctx, "start", publicDNSService); err != nil {
		return err
	}
	resolver := net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(call context.Context, network, address string) (net.Conn, error) {
		endpoint, err := publicResolverDialAddress(network, address)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(call, network, endpoint)
	}}
	return waitPublicResolver(ctx, resolver.LookupIP)
}

func publicResolverDialAddress(network, _ string) (string, error) {
	if network != "udp" && network != "tcp" {
		return "", errors.New("public resolver transport changed")
	}
	return publicResolverEndpoint, nil
}

func waitPublicResolver(ctx context.Context, lookup func(context.Context, string, string) ([]net.IP, error)) error {
	for {
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := lookup(call, "ip4", "example.com.")
		cancel()
		if err == nil && len(ips) > 0 {
			for _, ip := range ips {
				if ip.To4() == nil {
					return errors.New("fixed loopback DoH resolver returned unexpected address family")
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("fixed loopback DoH resolver not ready within startup bound")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func validatePublicResolverService() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return publicResolverCommand(ctx, "is-active", "--quiet", publicDNSService)
}

func ValidatePublicNetwork(c *PublicNetwork) error {
	if c == nil {
		return validateNonePublicNetwork()
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
	if err := validatePinnedResolver(c.DNS); err != nil {
		return err
	}
	return validatePublicResolverService()
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
	content := []byte(publicResolverContent)
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
	if len(expected) != 2 || expected[0] != "1.1.1.1" || expected[1] != "9.9.9.9" {
		return errors.New("public guest DoH upstream selection changed")
	}
	return validateLocalResolverFile("/etc/resolv.conf", 0)
}

func validateLocalResolverFile(path string, expectedUID uint32) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("public guest local resolver is not a regular root-controlled file")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || owner(info) != expectedUID || info.Mode().Perm() != 0644 || info.Size() != int64(len(publicResolverContent)) {
		return errors.New("public guest local resolver metadata changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("public guest local resolver identity is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(publicResolverContent)+1)))
	if err != nil || string(data) != publicResolverContent {
		return errors.New("public guest local resolver selection changed")
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
