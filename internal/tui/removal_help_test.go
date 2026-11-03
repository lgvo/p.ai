package tui

import (
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/api"
)

// removalRequestOf is the session and token a discard or delete request sends.
func removalRequestOf(p any) (string, string) {
	switch r := p.(type) {
	case api.SessionDiscardRequest:
		return r.UUID, r.ConfirmationToken
	case api.SessionDeleteRequest:
		return r.UUID, r.ConfirmationToken
	}
	return "", ""
}

func removalHelpFixture(t *testing.T, key string) (Model, *fakeClient, tea.Cmd) {
	t.Helper()
	kind := "discard"
	if key == "X" {
		kind = "delete"
	}
	f := &fakeClient{fn: func(method string, p any) (any, error) {
		switch method {
		case "session.removal.preview":
			if p != (api.SessionRemovalPreviewRequest{UUID: stoppedID, Kind: kind}) {
				t.Fatalf("preview changed captured removal: %+v", p)
			}
			return map[string]any{"preview": map[string]any{"session_uuid": stoppedID, "kind": kind, "confirmation_token": reviewToken, "runtime": map[string]any{"condition": "present", "runtime_data_lost": true}}}, nil
		case "session." + kind:
			if uuid, token := removalRequestOf(p); uuid != stoppedID || token != reviewToken {
				t.Fatalf("mutation lost captured review: %+v", p)
			}
			return map[string]any{"operation": api.Operation{ID: "removal", Kind: method, SessionUUID: stoppedID, Status: "running"}}, nil
		default:
			t.Fatalf("unexpected call %s", method)
			return nil, nil
		}
	}}
	m := fixture()
	m.client = f
	m.selected = stoppedID
	m.restoreSelection()
	m, cmd := press(m, key)
	if cmd == nil || m.page != "progress" || m.removal == nil {
		t.Fatal("removal did not begin through actual key")
	}
	return m, f, cmd
}

// removalHelpPreview returns the removal workflow with its preview read
// pending and that read's reply, not yet delivered.
func removalHelpPreview(t *testing.T, key string) (Model, *fakeClient, actionDone) {
	t.Helper()
	m, f, cmd := removalHelpFixture(t, key)
	reply := cmd().(actionDone)
	if reply.method != "session.removal.preview" {
		t.Fatalf("removal began with %s", reply.method)
	}
	return m, f, reply
}
func finishRemovalHelp(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for step := 0; cmd != nil && step < 4; step++ {
		updated, next := m.Update(cmd())
		m = updated.(Model)
		cmd = next
	}
	if cmd != nil || m.page != "review" {
		t.Fatalf("workflow stranded: page%s working%v pending%s notice%s", m.page, m.working, m.pendingMethod, m.notice)
	}
	if uuid, token := removalConfirmOf(t, m.confirm); uuid != stoppedID || token != reviewToken {
		t.Fatalf("review lost its capture: %s %s", uuid, token)
	}
	return m
}
func TestRemovalHelpPreservesThePendingPreviewRead(t *testing.T) {
	for _, key := range []string{"d", "X"} {
		for _, arrival := range []string{"during-help", "after-return"} {
			t.Run(fmt.Sprintf("%s/%s", key, arrival), func(t *testing.T) {
				m, f, reply := removalHelpPreview(t, key)
				startingEpoch := m.epoch
				var cmd tea.Cmd
				for cycle := 0; cycle < 3; cycle++ {
					m, cmd = press(m, "?")
					if cmd != nil || m.page != "help" {
						t.Fatal("Help did not suspend removal")
					}
					m, cmd = press(m, "?")
					if cmd != nil || m.page != "help" {
						t.Fatal("repeated Help changed caller")
					}
					if cycle == 2 && arrival == "during-help" {
						updated, next := m.Update(reply)
						m = updated.(Model)
						if next != nil || m.page != "help" {
							t.Fatal("reply hid Help or dispatched next workflow read")
						}
					}
					m, cmd = press(m, "esc")
					if cycle < 2 || arrival == "after-return" {
						if cmd != nil || m.page != "progress" || !m.working || m.pendingMethod != reply.method {
							t.Fatalf("pending original request lost: %+v", m.helpRemovalRead)
						}
					} else {
						m = finishRemovalHelp(t, m, cmd)
					}
				}
				if arrival == "after-return" {
					updated, cmd := m.Update(reply)
					m = finishRemovalHelp(t, updated.(Model), cmd)
				}
				if m.epoch <= startingEpoch {
					t.Fatal("Help reused an earlier navigation epoch")
				}
				// Consumed old replies must stay stale even after their original read
				// was deliberately accepted across Help's navigation epochs.
				updated, duplicate := m.Update(reply)
				m = updated.(Model)
				if duplicate != nil || m.page != "review" {
					t.Fatal("duplicate original reply restarted workflow")
				}
				counts := map[string]int{}
				for _, method := range f.calls {
					counts[method]++
				}
				if counts["session.removal.preview"] != 1 || counts["session.discard"] != 0 || counts["session.delete"] != 0 {
					t.Fatalf("Help replayed accepted work: %v", f.calls)
				}
				m, cmd = press(m, "enter")
				if cmd != nil || m.page != "review" {
					t.Fatal("Enter authorized removal")
				}
				m.selected = runningID // Review remains bound to the captured identity.
				m, cmd = press(m, "y")
				if cmd == nil {
					t.Fatal("explicit confirmation did not submit")
				}
				cmd()
				kind := "discard"
				if key == "X" {
					kind = "delete"
				}
				if f.calls[len(f.calls)-1] != "session."+kind {
					t.Fatalf("wrong confirmed action: %v", f.calls)
				}
			})
		}
	}
}
func TestRemovalHelpDefersErrorsAndProtectsEpoch(t *testing.T) {
	m, f, reply := removalHelpPreview(t, "X")
	m, _ = press(m, "?")
	stale := reply
	stale.epoch--
	updated, cmd := m.Update(stale)
	m = updated.(Model)
	if cmd != nil || m.helpRemovalRead.reply != nil {
		t.Fatal("stale reply consumed pending context")
	}
	reply.err = errors.New("preview unavailable")
	updated, cmd = m.Update(reply)
	m = updated.(Model)
	if cmd != nil || m.page != "help" || m.notice != "" {
		t.Fatal("error escaped Help")
	}
	m, cmd = press(m, "esc")
	if cmd != nil || m.page != "progress" || m.working || m.pendingMethod != "" || m.notice != "preview unavailable" {
		t.Fatalf("suspended error not surfaced: page%s notice%s pending%s", m.page, m.notice, m.pendingMethod)
	}
	before := len(f.calls)
	updated, cmd = m.Update(reply)
	m = updated.(Model)
	if cmd != nil || len(f.calls) != before {
		t.Fatal("duplicate error replayed API work")
	}
	m, cmd = press(m, "esc")
	if cmd != nil || m.removal != nil || m.helpRemovalRead != nil {
		t.Fatal("failed workflow prevented real departure")
	}
}
func TestRemovalHelpDepartureAndContextChangeIgnoreLateReplies(t *testing.T) {
	for _, change := range []string{"departure", "UUID", "kind"} {
		t.Run(change, func(t *testing.T) {
			m, f, reply := removalHelpPreview(t, "d")
			m.attachOnComplete = true
			m, _ = press(m, "?")
			switch change {
			case "departure":
				m, _ = press(m, "esc")
				m, _ = press(m, "esc")
			case "UUID":
				m.contextSession.UUID = "other"
			case "kind":
				copy := *m.removal
				copy.Kind = "delete"
				m.removal = &copy
			}
			before := len(f.calls)
			updated, cmd := m.Update(reply)
			m = updated.(Model)
			if cmd != nil || len(f.calls) != before {
				t.Fatal("late response advanced departed/retargeted workflow")
			}
			if change != "departure" {
				m, cmd = press(m, "esc")
				if cmd != nil || m.removal != nil || m.operationID != "" {
					t.Fatal("changed context retained actionable removal")
				}
			}
			if m.attachOnComplete || m.helpRemovalRead != nil {
				t.Fatal("departure retained pending attachment or read context")
			}
		})
	}
}
func TestRemovalHelpRefusesForeignPreview(t *testing.T) {
	m, _, reply := removalHelpPreview(t, "d")
	m, _ = press(m, "?")
	m, _ = press(m, "esc")
	reply.result = api.RemovalPreviewResult{Preview: api.RemovalPreview{SessionUUID: "other", Kind: "discard", ConfirmationToken: "token", Runtime: api.RemovalRuntimePreview{Condition: "present", RuntimeDataLost: true}}}
	updated, cmd := m.Update(reply)
	m = updated.(Model)
	if cmd != nil || m.page != "progress" || m.confirm.method != "" {
		t.Fatalf("foreign preview adopted: page%s confirmation%s", m.page, m.confirm.method)
	}
	if m.notice == "" {
		t.Fatal("foreign identity refusal unexplained")
	}
}
