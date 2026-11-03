package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// TestRemovalAuthorityFailuresKeepTheirMeaning checks how a removal answers a
// failed runtime or Git check.
func TestRemovalAuthorityFailuresKeepTheirMeaning(t *testing.T) {
	refusal := func(err error) (api.ErrorKind, string) {
		var r *control.Refusal
		if !errors.As(err, &r) {
			return "", ""
		}
		return r.Kind, r.Reason
	}
	present := &runtimePresentError{}
	for _, test := range []struct {
		name   string
		err    error
		kind   api.ErrorKind
		reason string
	}{
		{"a present runtime", runtimeFailure(present, "session/x"), api.KindConflict, api.ReasonRuntimePresent},
		{"an Incus failure", runtimeFailure(errors.New("incus down"), "session/x"), api.KindFailed, api.ReasonRuntime},
		{"heads moving during the read", control.HeadsFailure(fmt.Errorf("the heads moved: %w", sourcedriver.ErrConflict)), api.KindRetry, api.ReasonRefsMoved},
		{"a Git failure", control.HeadsFailure(fmt.Errorf("git down: %w", sourcedriver.ErrUnavailable)), api.KindFailed, api.ReasonGit},
	} {
		if kind, reason := refusal(test.err); kind != test.kind || reason != test.reason {
			t.Errorf("%s answered %s %s, want %s %s", test.name, kind, reason, test.kind, test.reason)
		}
	}
}

// TestRemovalRefusalsNameTheirNextStep checks the answers of removals that
// the session's state refuses before any runtime or Git read: a state that
// forbids the removal is its plan's refusal, a held or unknown session an
// error, and a run that is stale, expired, untagged or reuses a key is
// refused without recording an operation.
func TestRemovalRefusalsNameTheirNextStep(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	instance, err := f.m.store.InstanceID(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	attached, blocked := f.sessions[0].UUID, f.sessions[2].UUID
	selection := control.CreationSelection{HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	creation, creating, err := f.m.store.BeginSessionCreate(f.ctx, control.ReserveSessionRequest{Key: "session-creating", Project: "app", Branch: "creating", Choice: "existing"}, strings.Repeat("d", 64), selection,
		func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	removal := func(key, uuid string) (control.DiscardRequest, control.DiscardEvidence) {
		s, err := f.m.store.GetSession(f.ctx, uuid)
		if err != nil {
			t.Fatal(err)
		}
		ev := control.DiscardEvidence{Action: "discard", Project: s.Project, Branch: s.Branch, AssignedTip: strings.Repeat("e", 40), PolicySHA256: s.PolicySHA256,
			IncusProject: "user-1000", InstanceName: "p-" + uuid, InstanceUUID: instance, BaseFingerprint: strings.Repeat("d", 64), MissingRuntime: true}
		return control.DiscardRequest{Key: key, UUID: uuid, IfMatch: futureTag(time.Minute)}, ev
	}
	req, ev := removal("blocked-discard", blocked)
	op, err := f.m.store.BeginDiscard(f.ctx, req, ev, strings.Repeat("c", 64), noRecheck)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, op.ID, "blocked", op.Phase, false, op.Evidence, "internal", "blocked"); err != nil {
		t.Fatal(err)
	}
	// A recorded discard of another session under the key "reused".
	req, ev = removal("reused", f.sessions[3].UUID)
	if _, err = f.m.store.BeginDiscard(f.ctx, req, ev, strings.Repeat("c", 64), noRecheck); err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{ctx: f.ctx, store: f.m.store, instanceID: instance, driver: memory.NewDriver(memory.NewWorld(memory.Config{})),
		attachments: map[string]*attachment{strings.Repeat("a", 64): {session: attached, expires: time.Now().Add(time.Minute)}}}
	h := control.NewHandler(control.HandlerConfig{Lifecycle: l})
	call := func(method, uuid, extra string) (any, *api.Error) {
		return h(f.ctx, method, json.RawMessage(`{"uuid":"`+uuid+`"`+extra+`}`))
	}
	run := func(key string, d time.Duration) string {
		return `,"key":"` + key + `","if_match":"` + futureTag(d) + `"`
	}
	for _, test := range []struct {
		name, method, uuid string
		refusal            api.Refusal
	}{
		{"an attached session", "session.discard", attached, api.Refusal{Code: api.ReasonAttachmentActive}},
		{"a session held by a blocked removal", "session.delete", blocked, api.Refusal{Code: api.ReasonOperationBlocked, Target: "operation/" + op.ID}},
	} {
		result, answer := call(test.method, test.uuid, "")
		plan, _ := result.(api.ChangeResult[api.RemovalDetails])
		if answer != nil || plan.Plan == nil || plan.Plan.Allowed || plan.ChangeTag != "" || !reflect.DeepEqual(plan.Plan.Refusals, []api.Refusal{test.refusal}) ||
			plan.Plan.Action != test.method || plan.Plan.Target != "session/"+test.uuid {
			t.Errorf("%s planned %+v %v, want refusal %+v", test.name, result, answer, test.refusal)
		}
	}
	absent := "99999999-9999-4999-8999-999999999999"
	for _, test := range []struct {
		name, method, uuid, extra string
		kind                      api.ErrorKind
		reason, target            string
	}{
		// The journal's admission check names the creation that holds it.
		{"a session being created", "session.discard", creating.UUID, "", api.KindRetry, api.ReasonSessionHeld, "operation/" + creation.ID},
		{"an unknown session", "session.delete", absent, "", api.KindNotFound, api.ReasonSession, "session/" + absent},
		{"an attached session's run", "session.discard", attached, run("attached", time.Minute), api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"a held session's run", "session.discard", creating.UUID, run("held", time.Minute), api.KindRetry, api.ReasonSessionHeld, "operation/" + creation.ID},
		{"an expired tag", "session.discard", attached, run("expired", -time.Second), api.KindPreconditionFailed, api.ReasonExpired, ""},
		{"a key of another request", "session.discard", attached, run("reused", time.Minute), api.KindInvalid, api.ReasonKeyReused, ""},
		{"a run without its tag", "session.delete", attached, `,"key":"untagged"`, api.KindPreconditionRequired, api.ReasonIfMatchMissing, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, answer := call(test.method, test.uuid, test.extra)
			if answer == nil || answer.Kind() != test.kind || answer.Reason() != test.reason || answer.Target() != test.target {
				t.Fatalf("answered %v %v, want %s %s %s", result, answer, test.kind, test.reason, test.target)
			}
		})
	}
	for _, key := range []string{"attached", "held", "expired", "untagged"} {
		if _, err := f.m.store.GetOperationByKey(f.ctx, key); !errors.Is(err, control.ErrNotFound) {
			t.Errorf("a refused run recorded %s: %v", key, err)
		}
	}
}
