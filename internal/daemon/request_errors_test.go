package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// requireRefusal fails unless err is a refusal of kind with reason and
// target.
func requireRefusal(t *testing.T, err error, kind api.ErrorKind, reason, target string) {
	t.Helper()
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != kind || refusal.Reason != reason || refusal.Target != target {
		t.Fatalf("answer %v, want %s %s %q", err, kind, reason, target)
	}
}

// renameHolding records an unfinished rename of session, which holds it.
func renameHolding(t *testing.T, l *lifecycle, session control.Session) api.Operation {
	t.Helper()
	tip := strings.Repeat("e", 40)
	evidence := control.RenameEvidence{Project: session.Project, OldBranch: session.Branch, NewBranch: "renamed", OldTip: tip,
		PolicySHA256: session.PolicySHA256, InstanceUUID: "11111111-1111-1111-1111-111111111111", ImageFingerprint: strings.Repeat("d", 64),
		BaseFingerprint: strings.Repeat("d", 64), IncusUUID: "22222222-2222-2222-2222-222222222222", Generation: "33333333-3333-3333-3333-333333333333"}
	op, err := l.store.BeginRename(context.Background(), control.RenameRequest{Key: "rename", UUID: session.UUID, NewBranch: "renamed", ExpectedOldTip: tip},
		evidence, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestStartOfASessionHeldByAnOperationNamesIt(t *testing.T) {
	l, session := servicesLifecycle(t)
	ctx := context.Background()
	op := renameHolding(t, l, session)
	_, err := l.StartSession(ctx, session.UUID)
	requireRefusal(t, err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	if err = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, "internal", "blocked for the test"); err != nil {
		t.Fatal(err)
	}
	_, err = l.StartSession(ctx, session.UUID)
	requireRefusal(t, err, api.KindConflict, api.ReasonOperationBlocked, api.TargetOperation(op.ID))
	_, err = l.observeServices(ctx, session.UUID, "", "list")
	requireRefusal(t, err, api.KindConflict, api.ReasonOperationBlocked, api.TargetOperation(op.ID))
}

func TestStopWithAnAttachmentIsRefusedAsAttachmentActive(t *testing.T) {
	token := strings.Repeat("a", 64)
	l := &lifecycle{attachments: map[string]*attachment{token: {session: "session", expires: time.Now().Add(time.Minute)}}}
	_, err := l.StopSession(context.Background(), "session")
	requireRefusal(t, err, api.KindConflict, api.ReasonAttachmentActive, api.TargetSession("session"))
}

func TestStartOfASessionWithoutItsGitPrincipalIsRefused(t *testing.T) {
	l, session := servicesLifecycle(t)
	_, err := l.StartSession(context.Background(), session.UUID)
	requireRefusal(t, err, api.KindConflict, api.ReasonPrincipalMissing, api.TargetSession(session.UUID))
}

func TestRuntimeFailureOnStopAnswersFailedRuntime(t *testing.T) {
	l, session := servicesLifecycle(t)
	// The lifecycle has no runtime plugin, so the runtime call fails as an
	// unreachable runtime would.
	_, err := l.StopSession(context.Background(), session.UUID)
	requireRefusal(t, err, api.KindFailed, api.ReasonRuntime, "")
	_, err = l.observeServices(context.Background(), session.UUID, "", "list")
	requireRefusal(t, err, api.KindFailed, api.ReasonRuntime, "")
}

func TestSessionNotEstablishedOrInvalidPolicyRefusesStart(t *testing.T) {
	ctx := context.Background()
	session := control.Session{UUID: "s", Registry: "creating"}
	load := func(context.Context, string) (control.Session, error) { return session, nil }
	current := func(context.Context, control.Session) string { return "current" }
	_, err := startEligible(ctx, "s", load, current)
	requireRefusal(t, err, api.KindConflict, api.ReasonSessionNotEstablished, api.TargetSession("s"))
	session.Registry = "established"
	_, err = startEligible(ctx, "s", load, func(context.Context, control.Session) string { return "invalid" })
	requireRefusal(t, err, api.KindConflict, api.ReasonPolicyInvalid, api.TargetSession("s"))
}

func TestAttachmentRefusals(t *testing.T) {
	ctx := context.Background()
	l := &lifecycle{}
	_, err := l.ClaimAttachment(ctx, strings.Repeat("b", 64), &control.Connection{})
	requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
	err = l.ConfirmAttachment(ctx, &control.Connection{}, "/1.0/operations/11111111-1111-1111-1111-111111111111")
	requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
	for range maxSessionAttachments {
		if _, err = l.reserveAttachment("full", plugin.AttachSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = l.reserveAttachment("full", plugin.AttachSpec{})
	requireRefusal(t, err, api.KindCapacity, api.ReasonAttachmentCapacity, "")
	for condition, want := range map[string]api.ErrorKind{"stopped": api.KindConflict, "missing": api.KindConflict, "unreachable": api.KindFailed} {
		var refusal *control.Refusal
		if err := notReady("s", condition); !errors.As(err, &refusal) || refusal.Kind != want {
			t.Errorf("attach to a %s session answered %v, want %s", condition, err, want)
		}
	}
	if notReady("s", "ready") != nil {
		t.Error("attach to a ready session refused")
	}
}

func TestPublicEgressWithoutItsSubstrateIsNotConfigured(t *testing.T) {
	egress := &control.PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/nft/bin/nft", BridgeProofBinary: "/nix/store/proof/bin/p-public-network-proof"}
	policy := control.ProjectPolicy{Network: "public-egress", PublicEgressSHA256: egress.SHA256()}
	l := &lifecycle{}
	_, err := l.publicSessionIPv4(context.Background(), "s", policy)
	requireRefusal(t, err, api.KindNotConfigured, api.ReasonPublicEgress, "")
	l.cfg.PublicEgress = &control.PublicEgressConfig{Network: egress.Network, ACL: egress.ACL, BridgeIPv4: "10.234.0.1/24", DNS: egress.DNS, SudoBinary: egress.SudoBinary, NftBinary: egress.NftBinary, BridgeProofBinary: egress.BridgeProofBinary}
	_, err = l.publicSessionIPv4(context.Background(), "s", policy)
	requireRefusal(t, err, api.KindConflict, api.ReasonPolicyInvalid, api.TargetSession("s"))
}
