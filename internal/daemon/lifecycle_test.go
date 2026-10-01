package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestRegisteredSessionKeyCannotRotateOnRetry(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	id := "session-1"
	registered := ""
	principal := func(_ context.Context, gotID string) (string, bool, error) {
		if gotID != id {
			t.Fatalf("unexpected session ID: %q", gotID)
		}
		return registered, registered != "", nil
	}
	key, pub, err := sessionKeyAt(ctx, id, state, principal)
	if err != nil || len(key) == 0 {
		t.Fatal(err)
	}
	registered = gitservice.Fingerprint(pub)
	again, same, err := sessionKeyAt(ctx, id, state, principal)
	if err != nil || string(again) != string(key) || string(same.Marshal()) != string(pub.Marshal()) {
		t.Fatal("retry rotated registered identity", err)
	}
	if err = os.Remove(filepath.Join(state, "session_keys", id)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sessionKeyAt(ctx, id, state, principal); err == nil || !strings.Contains(err.Error(), "repair required") {
		t.Fatalf("missing registered key silently rotated: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(state, "session_keys", id)); !os.IsNotExist(err) {
		t.Fatal("missing key was recreated")
	}
}

func TestPhaseRankPreventsRetryRegression(t *testing.T) {
	if phaseRank("workspace-ready") <= phaseRank("principals-ready") || phaseRank("established") <= phaseRank("workspace-ready") {
		t.Fatal("creation phase order regressed")
	}
}

func TestReadinessRetriesBootObservationAndStopsFailure(t *testing.T) {
	attempts, stops := 0, 0
	err := awaitHostReady(context.Background(), time.Second, func(context.Context) (plugin.RuntimeState, error) {
		attempts++
		if attempts == 1 {
			return plugin.RuntimeState{}, errors.New("guest systemd not yet accepting exec")
		}
		return plugin.RuntimeState{Exists: true, Status: "Running", HostReady: true}, nil
	}, func(context.Context) error { stops++; return nil })
	if err != nil || attempts != 2 || stops != 0 {
		t.Fatalf("transient boot observation: attempts=%d stops=%d err=%v", attempts, stops, err)
	}
	err = awaitHostReady(context.Background(), time.Second, func(context.Context) (plugin.RuntimeState, error) {
		return plugin.RuntimeState{Exists: true, Status: "Running", HostUnit: "failed/failed", DiagnosticAvailable: true, Diagnostic: "host failed"}, nil
	}, func(context.Context) error { stops++; return nil })
	if err == nil || !strings.Contains(err.Error(), "host failed") || stops != 1 {
		t.Fatalf("failed host left running: stops=%d err=%v", stops, err)
	}
	err = awaitHostReady(context.Background(), 25*time.Millisecond, func(context.Context) (plugin.RuntimeState, error) {
		return plugin.RuntimeState{Exists: true, Status: "Running", HostUnit: "activating/start-post"}, nil
	}, func(context.Context) error { stops++; return nil })
	if err == nil || stops != 2 {
		t.Fatalf("readiness timeout left running: stops=%d err=%v", stops, err)
	}
}

func TestSessionMutationLockSerializesStartStop(t *testing.T) {
	l := &lifecycle{}
	release, err := l.lockSession(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.lockSession(context.Background(), "session"); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("concurrent lifecycle mutation accepted: %v", err)
	}
	release()
	again, err := l.lockSession(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestConfinementDriftInvalidatesPolicyAndBlocksStart(t *testing.T) {
	ctx := context.Background()
	snapshot, sha, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	session := control.Session{UUID: "session-1", Project: "app", Registry: "established", Policy: snapshot, PolicySHA256: sha}
	currentPolicy := func(context.Context, string) (string, error) { return sha, nil }
	drifted := false
	confinementCheck := func(context.Context) error {
		if drifted {
			return errors.New("Incus project ceiling changed")
		}
		return nil
	}
	condition := func(ctx context.Context, s control.Session) string {
		return policyCondition(ctx, s, currentPolicy, confinementCheck)
	}
	loadSession := func(context.Context, string) (control.Session, error) { return session, nil }
	if got := condition(ctx, session); got != "current" {
		t.Fatalf("safe policy condition = %q", got)
	}
	if _, err := startEligible(ctx, session.UUID, loadSession, condition); err != nil {
		t.Fatalf("Start rejected safe confinement: %v", err)
	}
	drifted = true
	if got := condition(ctx, session); got != "invalid" {
		t.Fatalf("unsafe policy condition = %q", got)
	}
	if _, err := startEligible(ctx, session.UUID, loadSession, condition); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("Start accepted invalid confinement: %v", err)
	}
}

func TestExactProjectPolicyDriftAndMissingAuthority(t *testing.T) {
	ctx := context.Background()
	a := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	b := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/bash"}}
	aPolicy, aSHA, err := control.ProjectPolicySnapshot(a)
	if err != nil {
		t.Fatal(err)
	}
	bPolicy, bSHA, err := control.ProjectPolicySnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{"team/a": a, "team/b": b}}}
	first := control.Session{UUID: "first", Project: "team/a", Registry: "established", Policy: aPolicy, PolicySHA256: aSHA}
	sibling := control.Session{UUID: "sibling", Project: "team/b", Registry: "established", Policy: bPolicy, PolicySHA256: bSHA}
	if got := l.policyCondition(ctx, first); got != "current" {
		t.Fatalf("first=%s", got)
	}
	missingBytes := first
	missingBytes.Policy = nil
	if got := l.policyCondition(ctx, missingBytes); got != "invalid" {
		t.Fatalf("missing immutable snapshot=%s", got)
	}
	futureGrant := first
	futureGrant.Policy = []byte(`{"command":["/bin/sh"],"filesystem_mounts":[],"future_host_grant":true,"network":"none"}`)
	futureDigest := sha256.Sum256(futureGrant.Policy)
	futureGrant.PolicySHA256 = hex.EncodeToString(futureDigest[:])
	if got := l.policyCondition(ctx, futureGrant); got != "invalid" {
		t.Fatalf("future grant silently dropped=%s", got)
	}
	legacy := first
	legacy.Policy = []byte(`{"command":["/bin/sh"],"filesystem_mounts":null,"network":"none"}`)
	legacyDigest := sha256.Sum256(legacy.Policy)
	legacy.PolicySHA256 = hex.EncodeToString(legacyDigest[:])
	if got := l.policyCondition(ctx, legacy); got != "current" {
		t.Fatalf("equivalent old zero-mount snapshot=%s", got)
	}
	if got := l.policyCondition(ctx, sibling); got != "current" {
		t.Fatalf("sibling=%s", got)
	}
	l.cfg.ProjectPolicies["team/a"] = b
	if got := l.policyCondition(ctx, first); got != "outdated" {
		t.Fatalf("changed A=%s", got)
	}
	if got := l.policyCondition(ctx, sibling); got != "current" {
		t.Fatalf("changed sibling=%s", got)
	}
	load := func(context.Context, string) (control.Session, error) { return first, nil }
	if _, err := startEligible(ctx, first.UUID, load, l.policyCondition); err != nil {
		t.Fatalf("outdated snapshot blocked Start: %v", err)
	}
	delete(l.cfg.ProjectPolicies, "team/a")
	if got := l.policyCondition(ctx, first); got != "invalid" {
		t.Fatalf("missing A=%s", got)
	}
	if _, err := startEligible(ctx, first.UUID, load, l.policyCondition); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("missing authority permitted Start: %v", err)
	}
	if got := l.policyCondition(ctx, sibling); got != "current" {
		t.Fatalf("missing A affected sibling=%s", got)
	}
	if _, configured := l.cfg.PolicyForProject("team/a"); configured {
		t.Fatal("missing map key admitted new creation")
	}
	l.cfg.ProjectPolicies["team/a"] = control.ProjectPolicy{Network: "public-egress", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	if got := l.policyCondition(ctx, first); got != "invalid" {
		t.Fatalf("unsafe current grant=%s", got)
	}
}

func TestPublicEgressStoredSubstrateDriftBlocksStart(t *testing.T) {
	ctx := context.Background()
	egress := &control.PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/nft/bin/nft", BridgeProofBinary: "/nix/store/proof/bin/p-public-network-proof"}
	policy := control.ProjectPolicy{Network: "public-egress", PublicEgressSHA256: egress.SHA256(), FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	raw, sha, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{cfg: control.RuntimeConfig{PublicEgress: egress, ProjectPolicies: map[string]control.ProjectPolicy{"team/public": policy}}}
	session := control.Session{UUID: "public", Project: "team/public", Registry: "established", Policy: raw, PolicySHA256: sha}
	if got := l.policyCondition(ctx, session); got != "current" {
		t.Fatalf("unchanged public authority = %s", got)
	}
	l.cfg.PublicEgress = &control.PublicEgressConfig{Network: egress.Network, ACL: egress.ACL, BridgeIPv4: "10.234.0.1/24", DNS: egress.DNS, SudoBinary: egress.SudoBinary, NftBinary: egress.NftBinary, BridgeProofBinary: egress.BridgeProofBinary}
	if got := l.policyCondition(ctx, session); got != "invalid" {
		t.Fatalf("changed public bridge = %s", got)
	}
	load := func(context.Context, string) (control.Session, error) { return session, nil }
	if _, err := startEligible(ctx, session.UUID, load, l.policyCondition); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("unsafe public Start accepted: %v", err)
	}
	l.cfg.PublicEgress = nil
	l.cfg.ProjectPolicies["team/public"] = control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	if got := l.policyCondition(ctx, session); got != "invalid" {
		t.Fatalf("removed public authority = %s", got)
	}
}

func TestProjectPolicyChangeEventUsesEffectivePolicy(t *testing.T) {
	selected := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	_, selectedSHA, err := control.ProjectPolicySnapshot(selected)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"command":["/bin/sh"],"filesystem_mounts":null,"network":"none"}`)
	legacyDigest := sha256.Sum256(legacy)
	if projectPolicyRecordChanged(legacy, hex.EncodeToString(legacyDigest[:]), selectedSHA) {
		t.Fatal("null-to-empty canonicalization emitted false policy change")
	}
	if !projectPolicyRecordChanged(legacy, strings.Repeat("0", 64), selectedSHA) {
		t.Fatal("corrupt stored policy digest hidden")
	}
	other := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/bash"}}
	_, otherSHA, err := control.ProjectPolicySnapshot(other)
	if err != nil {
		t.Fatal(err)
	}
	if !projectPolicyRecordChanged(legacy, hex.EncodeToString(legacyDigest[:]), otherSHA) {
		t.Fatal("changed command omitted policy event")
	}
}
