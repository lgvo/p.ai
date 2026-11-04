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

func TestBuilderBorrowsItsSessionPublicAddressSlot(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	session := func(project, policy string) string {
		t.Helper()
		op, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: project, Project: project}, json.RawMessage(policy), strings.Repeat("d", 64), testSelection())
		if err != nil {
			t.Fatal(err)
		}
		created, err := s.CommitBlankProject(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return created.UUID
	}
	public := session("team/public", `{"network":"public-egress"}`)
	none := session("team/none", `{"network":"none"}`)
	if slot, err := s.ReserveBuilderPublicAddress(ctx, public); err != nil || slot != 0 {
		t.Fatalf("public session's builder did not use the session's slot: %d %v", slot, err)
	}
	for range 2 {
		if slot, err := s.ReserveBuilderPublicAddress(ctx, none); err != nil || slot != 1 {
			t.Fatalf("none session's builder slot: %d %v", slot, err)
		}
	}
	if err := s.ReleaseBuilderPublicAddress(ctx, none); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublicAddressSlot(ctx, none); !errors.Is(err, ErrNotFound) {
		t.Fatalf("none session kept its builder's address: %v", err)
	}
	if err := s.ReleaseBuilderPublicAddress(ctx, public); err != nil {
		t.Fatal(err)
	}
	if slot, err := s.PublicAddressSlot(ctx, public); err != nil || slot != 0 {
		t.Fatalf("public session lost its own address: %d %v", slot, err)
	}
	for i := range 3 {
		session(fmt.Sprintf("team/public-%d", i), `{"network":"public-egress"}`)
	}
	if _, err := s.ReserveBuilderPublicAddress(ctx, none); !errors.Is(err, ErrConflict) {
		t.Fatalf("builder took an address beyond the four slots: %v", err)
	}
}
