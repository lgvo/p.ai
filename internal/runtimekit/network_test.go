package runtimekit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPublicGuestConfigRequiresStaticBoundAddressAndDNS(t *testing.T) {
	base := `{"schema":"p.runtime-session/v5","activation":"base","command":["/bin/sh"],"public_network":{"address":"10.233.0.10/24","gateway":"10.233.0.1","dns":["1.1.1.1","9.9.9.9"]}}`
	if _, err := ParseConfig([]byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"schema":"p.runtime-session/v5","activation":"base","command":["/bin/sh"]}`,
		`{"schema":"p.runtime-session/v5","activation":"base","command":["/bin/sh"],"public_network":{"address":"10.233.0.14/24","gateway":"10.233.0.1","dns":["1.1.1.1","9.9.9.9"]}}`,
		`{"schema":"p.runtime-session/v5","activation":"base","command":["/bin/sh"],"public_network":{"address":"10.233.0.10/24","gateway":"10.234.0.1","dns":["1.1.1.1","9.9.9.9"]}}`,
		`{"schema":"p.runtime-session/v5","activation":"base","command":["/bin/sh"],"public_network":{"address":"10.233.0.10/24","gateway":"10.233.0.1","dns":["10.233.0.1","9.9.9.9"]}}`,
		`{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/sh"],"public_network":{"address":"10.233.0.10/24","gateway":"10.233.0.1","dns":["1.1.1.1","9.9.9.9"]}}`,
	} {
		if _, err := ParseConfig([]byte(input)); err == nil {
			t.Fatalf("unsafe guest network config accepted: %s", input)
		}
	}
}

func TestPublicIPv6InterfaceStateMustBeExplicitlyDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disable_ipv6")
	for _, test := range []struct {
		value string
		valid bool
	}{
		{value: "1\n", valid: true},
		{value: "0\n"},
		{value: "1\n0\n"},
		{value: strings.Repeat("1", 64)},
	} {
		if err := os.WriteFile(path, []byte(test.value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := verifyPublicIPv6Disabled(path); (err == nil) != test.valid {
			t.Fatalf("IPv6 state %q accepted=%v: %v", test.value, err == nil, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicIPv6Disabled(path); err == nil {
		t.Fatal("missing IPv6 state accepted")
	}
}

func TestResolverFailureEvidenceContainsMetadataButNoContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("nameserver 192.0.2.1\nprivate-search-domain\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "resolv.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	observed := describeResolverPath(link)
	if !strings.Contains(observed, "lstat(") || !strings.Contains(observed, "stat(") || !strings.Contains(observed, "link=") {
		t.Fatalf("resolver path evidence incomplete: %q", observed)
	}
	if strings.Contains(observed, "192.0.2.1") || strings.Contains(observed, "private-search-domain") {
		t.Fatalf("resolver content leaked in diagnostic: %q", observed)
	}
}

func TestPinnedResolverInstallWritesOnlyValidatedSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	allowFixtureParent := func(string) error { return nil }
	dns := []string{"1.1.1.1", "9.9.9.9"}
	if err := installPinnedResolver(path, dns, allowFixtureParent); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "nameserver 127.0.0.1\n" {
		t.Fatalf("resolver installation differs from the bound selection: %q %v", data, err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 {
		t.Fatalf("resolver was not installed as a regular 0644 file: %v %v", info, err)
	}
	if err := installPinnedResolver(path, []string{"10.233.0.1", "9.9.9.9"}, allowFixtureParent); err == nil {
		t.Fatal("unbound gateway resolver accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "private")
	if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := installPinnedResolver(path, dns, allowFixtureParent); err == nil {
		t.Fatal("unexpected resolver symlink accepted")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "sentinel" {
		t.Fatal("hostile symlink target changed")
	}
}

func TestPublicAddressFailureDiagnosticIsBoundedMetadata(t *testing.T) {
	var observed []guestAddressObservation
	input := `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"10.233.0.10","prefixlen":24,"private":"do-not-log"},{"family":"inet6","local":"fe80::1","prefixlen":64}]}]`
	if err := json.Unmarshal([]byte(input), &observed); err != nil {
		t.Fatal(err)
	}
	diagnostic := describeGuestAddresses(observed)
	if !strings.Contains(diagnostic, `addresses=2`) || !strings.Contains(diagnostic, `"fe80::1"/64`) || strings.Contains(diagnostic, "do-not-log") {
		t.Fatalf("address evidence missing or unsafe: %q", diagnostic)
	}
	observed[0].IfName = strings.Repeat("n", 1000)
	observed[0].Info[0].Local = strings.Repeat("x", 1000)
	if len(describeGuestAddresses(observed)) > 500 {
		t.Fatal("guest-controlled address metadata escaped the diagnostic bound")
	}
}

func TestPublicNetworkStartsDoHOnlyAfterTrustedAddressAndRoute(t *testing.T) {
	for _, failure := range []string{"none", "none-nic", "invalid-selection", "ipv6", "resolver", "route", "start", "verify", "success"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			refused := errors.New("fixture refusal")
			step := func(name string) error {
				events = append(events, name)
				if failure == name {
					return refused
				}
				return nil
			}
			c := &PublicNetwork{Address: "10.233.0.10/24", Gateway: "10.233.0.1", DNS: []string{"1.1.1.1", "9.9.9.9"}}
			if failure == "none" || failure == "none-nic" {
				c = nil
			}
			if failure == "invalid-selection" {
				c.DNS = []string{"127.0.0.1", "9.9.9.9"}
			}
			setup := guestNetworkPreparation{
				none: func() error {
					events = append(events, "none")
					if failure == "none-nic" {
						return refused
					}
					return nil
				},
				ipv6: func() error { return step("ipv6") },
				resolver: func(dns []string) error {
					if !reflect.DeepEqual(dns, []string{"1.1.1.1", "9.9.9.9"}) {
						t.Fatal("unbound bootstrap selection")
					}
					return step("resolver")
				},
				ip: func(args ...string) ([]byte, error) {
					name := "link"
					switch {
					case slices.Equal(args, []string{"link", "set", "dev", "eth0", "up"}):
					case slices.Equal(args, []string{"-4", "address", "replace", c.Address, "dev", "eth0"}):
						name = "address"
					case slices.Equal(args, []string{"-4", "route", "replace", "default", "via", c.Gateway, "dev", "eth0"}):
						name = "route"
					default:
						t.Fatal("network preparation issued unrelated command")
					}
					return nil, step(name)
				},
				startResolver: func() error { return step("start") },
				verify: func(observed *PublicNetwork) error {
					if observed != c {
						t.Fatal("selection changed before verification")
					}
					return step("verify")
				},
			}
			err := prepareGuestPublicNetwork(c, setup)
			expected := map[string][]string{
				"none": {"none"}, "none-nic": {"none"}, "invalid-selection": nil,
				"ipv6": {"ipv6"}, "resolver": {"ipv6", "resolver"},
				"route":   {"ipv6", "resolver", "link", "address", "route"},
				"start":   {"ipv6", "resolver", "link", "address", "route", "start"},
				"verify":  {"ipv6", "resolver", "link", "address", "route", "start", "verify"},
				"success": {"ipv6", "resolver", "link", "address", "route", "start", "verify"},
			}[failure]
			if !reflect.DeepEqual(events, expected) {
				t.Fatalf("public/none authority or ordering changed: %v", events)
			}
			if (err == nil) != (failure == "none" || failure == "success") {
				t.Fatalf("preparation ignored refusal: %v", err)
			}
		})
	}
}

func TestLocalResolverFileRequiresExactSafeLoopbackIdentity(t *testing.T) {
	for _, name := range []string{"safe", "plaintext", "extra-server", "search", "writable", "hardlink", "symlink", "fifo", "directory", "wrong-owner", "missing"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "resolv.conf")
			content := publicResolverContent
			switch name {
			case "plaintext":
				content = "nameserver 1.1.1.1\nnameserver 9.9.9.9\n"
			case "extra-server":
				content += "nameserver 9.9.9.9\n"
			case "search":
				content += "search private.example\n"
			}
			if name != "missing" {
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			uid := uint32(os.Geteuid())
			switch name {
			case "writable":
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(dir, "other")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, filepath.Join(dir, "target")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				os.Remove(path)
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Remove(path)
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				uid++
			}
			if err := validateLocalResolverFile(path, uid); (err == nil) != (name == "safe") {
				t.Fatalf("unsafe resolver accepted=%v: %v", err == nil, err)
			}
		})
	}
}

func TestDoHReadinessUsesOnlyLoopbackAndBoundedFixedQuery(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		target, err := publicResolverDialAddress(network, "192.0.2.53:53")
		if err != nil || target != "127.0.0.1:53" {
			t.Fatal("resolver dial honored ambient DNS", target, err)
		}
	}
	for _, network := range []string{"udp6", "tcp6", "unix", ""} {
		if _, err := publicResolverDialAddress(network, "127.0.0.1:53"); err == nil {
			t.Fatal("unexpected resolver transport accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	if err := waitPublicResolver(ctx, func(ctx context.Context, network, host string) ([]net.IP, error) {
		calls++
		if network != "ip4" || host != "example.com." {
			t.Fatal("workspace/ambient query entered readiness")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded resolver query")
		}
		if calls == 1 {
			return nil, errors.New("fixture listener still starting")
		}
		return []net.IP{net.ParseIP("93.184.215.14")}, nil
	}); err != nil || calls != 2 {
		t.Fatal("resolver start readiness was not retried", err, calls)
	}
	bounded, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if err := waitPublicResolver(bounded, func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("fixture upstream unavailable; private diagnostic must not leak")
	}); err == nil || strings.Contains(err.Error(), "private diagnostic") {
		t.Fatal("unavailable DoH escaped bound or leaked output", err)
	}
	if err := waitPublicResolver(ctx, func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP("::1")}, nil }); err == nil {
		t.Fatal("unexpected query family accepted")
	}
}
