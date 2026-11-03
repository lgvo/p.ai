package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// TestRemovalAuthorityFailuresKeepTheirMeaning checks how a removal answers a
// failed runtime or Git check, and how a confirmation answers a recheck that
// found the reviewed state changed.
func TestRemovalAuthorityFailuresKeepTheirMeaning(t *testing.T) {
	refusal := func(err error) (api.ErrorKind, string) {
		var r *control.Refusal
		if !errors.As(err, &r) {
			return "", ""
		}
		return r.Kind, r.Reason
	}
	present := &runtimeincus.RuntimePresentError{}
	for _, test := range []struct {
		name   string
		err    error
		kind   api.ErrorKind
		reason string
	}{
		{"a present runtime", runtimeFailure(present, "session/x"), api.KindConflict, api.ReasonRuntimePresent},
		{"an Incus failure", runtimeFailure(errors.New("incus down"), "session/x"), api.KindFailed, api.ReasonRuntime},
		{"refs moving during the read", gitFailure(gitservice.ErrRefsMoving), api.KindRetry, api.ReasonRefsMoved},
		{"a Git failure", gitFailure(errors.New("git down")), api.KindFailed, api.ReasonGit},
		{"a state changed since the review", staleIfConflict(runtimeFailure(present, "session/x")), api.KindPreconditionFailed, api.ReasonChanged},
		{"a holder at confirmation", staleIfConflict(control.Retry(api.ReasonSessionHeld, "")), api.KindRetry, api.ReasonSessionHeld},
		{"a runtime failure at confirmation", staleIfConflict(runtimeFailure(errors.New("incus down"), "")), api.KindFailed, api.ReasonRuntime},
	} {
		if kind, reason := refusal(test.err); kind != test.kind || reason != test.reason {
			t.Errorf("%s answered %s %s, want %s %s", test.name, kind, reason, test.kind, test.reason)
		}
	}
	if stale := staleIfConflict(control.Conflict(api.ReasonAttachmentActive, "")); !errors.Is(stale, control.ErrConflict) {
		t.Errorf("a stale confirmation lost its conflict cause: %v", stale)
	}
}

// TestRemovalRefusalsNameTheirNextStep checks the answers of removal reviews
// and confirmations that the session's state or a stale review refuses,
// before any runtime or Git effect.
func TestRemovalRefusalsNameTheirNextStep(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	instance, err := f.m.store.InstanceID(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	attached, reviewed, blocked := f.sessions[0].UUID, f.sessions[1].UUID, f.sessions[2].UUID
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	creation, creating, err := f.m.store.BeginSessionCreate(f.ctx, control.ReserveSessionRequest{Key: "session-creating", Project: "app", Branch: "creating", Choice: "existing"}, strings.Repeat("d", 64), selection,
		func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	removal := func(key, uuid, token string) (control.DiscardRequest, control.DiscardEvidence) {
		s, err := f.m.store.GetSession(f.ctx, uuid)
		if err != nil {
			t.Fatal(err)
		}
		ev := control.DiscardEvidence{Action: "discard", Project: s.Project, Branch: s.Branch, AssignedTip: strings.Repeat("e", 40), PolicySHA256: s.PolicySHA256,
			IncusProject: "user-1000", InstanceName: "p-" + uuid, InstanceUUID: instance, BaseFingerprint: strings.Repeat("d", 64), MissingRuntime: true,
			PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
		return control.DiscardRequest{Key: key, UUID: uuid, TokenSHA256: hexDigest([]byte(token))}, ev
	}
	req, ev := removal("blocked-discard", blocked, strings.Repeat("1", 32))
	op, err := f.m.store.BeginDiscard(f.ctx, req, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, op.ID, "blocked", op.Phase, false, op.Evidence, "internal", "blocked"); err != nil {
		t.Fatal(err)
	}
	// A recorded discard of another session under the key "reused".
	req, ev = removal("reused", f.sessions[3].UUID, strings.Repeat("2", 32))
	if _, err = f.m.store.BeginDiscard(f.ctx, req, ev); err != nil {
		t.Fatal(err)
	}
	discardToken, expiredToken := strings.Repeat("3", 32), strings.Repeat("4", 32)
	l := &lifecycle{ctx: f.ctx, store: f.m.store,
		attachments: map[string]*attachment{strings.Repeat("a", 64): {session: attached, expires: time.Now().Add(time.Minute)}},
		removalPreviews: map[string]removalPreviewState{
			discardToken: {Preview: api.RemovalPreview{Kind: "discard", SessionUUID: reviewed}, Expiry: time.Now().Add(time.Minute)},
			expiredToken: {Preview: api.RemovalPreview{Kind: "discard", SessionUUID: reviewed}, Expiry: time.Now().Add(-time.Second)},
		}}
	h := control.NewHandler(control.HandlerConfig{Lifecycle: l})
	preview := func(uuid string) string { return `{"uuid":"` + uuid + `","kind":"discard"}` }
	confirm := func(key, uuid, token string) string {
		return `{"key":"` + key + `","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`
	}
	absent := "99999999-9999-4999-8999-999999999999"
	for _, test := range []struct {
		name, method, params string
		kind                 api.ErrorKind
		reason, target       string
	}{
		{"an attached session's review", "session.removal.preview", preview(attached), api.KindConflict, api.ReasonAttachmentActive, "session/" + attached},
		// The journal's admission check names the creation that holds it.
		{"a session being created", "session.removal.preview", preview(creating.UUID), api.KindRetry, api.ReasonSessionHeld, "operation/" + creation.ID},
		{"a session held by a blocked removal", "session.removal.preview", preview(blocked), api.KindConflict, api.ReasonOperationBlocked, "operation/" + op.ID},
		{"an unknown session", "session.removal.preview", preview(absent), api.KindNotFound, api.ReasonSession, "session/" + absent},
		{"an attached session's confirmation", "session.discard", confirm("attached", attached, discardToken), api.KindConflict, api.ReasonAttachmentActive, "session/" + attached},
		{"a token never issued", "session.discard", confirm("unknown", reviewed, strings.Repeat("5", 32)), api.KindPreconditionFailed, api.ReasonExpired, ""},
		{"an expired token", "session.discard", confirm("expired", reviewed, expiredToken), api.KindPreconditionFailed, api.ReasonExpired, ""},
		{"a discard review used to delete", "session.delete", confirm("wrong-kind", reviewed, discardToken), api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"another session's review", "session.discard", confirm("wrong-session", f.sessions[4].UUID, discardToken), api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"a key of another request", "session.discard", confirm("reused", reviewed, discardToken), api.KindInvalid, api.ReasonKeyReused, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, answer := h(f.ctx, test.method, json.RawMessage(test.params))
			if answer == nil || answer.Kind() != test.kind || answer.Reason() != test.reason || answer.Target() != test.target {
				t.Fatalf("answered %v %v, want %s %s %s", result, answer, test.kind, test.reason, test.target)
			}
		})
	}
}
