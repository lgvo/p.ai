package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testPublicEgress() PublicEgressConfig {
	return PublicEgressConfig{
		Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24",
		DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/example/bin/nft",
		BridgeProofBinary: "/nix/store/example/bin/p-public-network-proof",
	}
}

func TestPublicEgressAuthorityAndPolicySnapshot(t *testing.T) {
	c := testPublicEgress()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for slot, want := range []string{"10.233.0.10/24", "10.233.0.11/24", "10.233.0.12/24", "10.233.0.13/24"} {
		if got, err := c.PublicEgressGuestIPv4(slot); err != nil || got != want {
			t.Fatalf("slot %d: %q %v", slot, got, err)
		}
	}
	if _, err := c.PublicEgressGuestIPv4(4); err == nil {
		t.Fatal("fifth address admitted")
	}
	policy := ProjectPolicy{Network: "public-egress", FilesystemMounts: []FilesystemGrant{}, Command: []string{"/bin/sh"}, PublicEgressSHA256: c.SHA256()}
	raw, sha, err := ProjectPolicySnapshot(policy)
	if err != nil || sha == "" {
		t.Fatal(err)
	}
	stored, err := ParseStoredProjectPolicy(raw)
	if err != nil || stored.PublicEgressSHA256 != c.SHA256() {
		t.Fatalf("stored authority changed: %+v %v", stored, err)
	}
	c.DNS[0] = "8.8.8.8"
	if c.Validate() == nil || c.SHA256() == stored.PublicEgressSHA256 {
		t.Fatal("changed DNS retained trusted identity")
	}
	policy.Network = "none"
	if _, _, err := ProjectPolicySnapshot(policy); err == nil {
		t.Fatal("none policy accepted public authority")
	}
}

func TestPublicEgressSQLiteAddressReservation(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	policy := json.RawMessage(`{"network":"public-egress"}`)
	for slot := 0; slot < 4; slot++ {
		project := fmt.Sprintf("team/public-%d", slot)
		op, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: project, Project: project}, policy, strings.Repeat("d", 64), testSelection())
		if err != nil {
			t.Fatal(err)
		}
		created, err := s.CommitBlankProject(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.PublicAddressSlot(ctx, created.UUID)
		if err != nil || got != slot {
			t.Fatalf("slot %d: got %d, %v", slot, got, err)
		}
	}
	op, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "team/overflow", Project: "team/overflow"}, policy, strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitBlankProject(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("fifth public address admitted: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM session_public_addresses`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("address reservations changed across restart: %d %v", count, err)
	}
}
