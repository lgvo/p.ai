package runtimekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err != nil || string(data) != "nameserver 1.1.1.1\nnameserver 9.9.9.9\n" {
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
