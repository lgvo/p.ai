package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/control"
)

func removalHelpFixture(t *testing.T, key string) (Model, *fakeClient, tea.Cmd) {
	t.Helper()
	kind := "discard"
	if key == "X" {
		kind = "delete"
	}
	f := &fakeClient{fn: func(method string, p params) (any, error) {
		switch method {
		case "workspace.loss.inspect":
			if p["uuid"] != "stopped" {
				t.Fatalf("inspection retargeted: %+v", p)
			}
			return map[string]any{"operation": control.Operation{ID: "loss", Kind: method, SessionUUID: "stopped", Status: "running"}}, nil
		case "operation.inspect":
			if p["id"] != "loss" {
				t.Fatalf("polled foreign operation: %+v", p)
			}
			return map[string]any{"operation": control.Operation{ID: "loss", Kind: "workspace.loss.inspect", SessionUUID: "stopped", Status: "completed"}}, nil
		case "session.removal.preview":
			if p["uuid"] != "stopped" || p["kind"] != kind || p["loss_operation_id"] != "loss" {
				t.Fatalf("preview changed captured removal: %+v", p)
			}
			return map[string]any{"preview": map[string]any{"session_uuid": "stopped", "kind": kind, "confirmation_token": "captured-token", "runtime_loss": map[string]bool{"dirty": true}}}, nil
		case "session." + kind:
			if p["uuid"] != "stopped" || p["confirmation_token"] != "captured-token" {
				t.Fatalf("mutation lost captured review: %+v", p)
			}
			return map[string]any{"operation": control.Operation{ID: "removal", Kind: method, SessionUUID: "stopped", Status: "running"}}, nil
		default:
			t.Fatalf("unexpected call %s", method)
			return nil, nil
		}
	}}
	m := fixture()
	m.client = f
	m.selected = "stopped"
	m.restoreSelection()
	m, cmd := press(m, key)
	if cmd == nil || m.page != "progress" || m.removal == nil {
		t.Fatal("removal did not begin through actual key")
	}
	return m, f, cmd
}
func removalHelpWindow(t *testing.T, key, window string) (Model, *fakeClient, actionDone) {
	t.Helper()
	m, f, cmd := removalHelpFixture(t, key)
	target := map[string]string{"initial": "workspace.loss.inspect", "poll": "operation.inspect", "preview": "session.removal.preview"}[window]
	for cmd != nil {
		reply := cmd().(actionDone)
		if reply.method == target {
			return m, f, reply
		}
		updated, next := m.Update(reply)
		m = updated.(Model)
		cmd = next
	}
	t.Fatal("workflow did not reach " + window)
	return m, f, actionDone{}
}
func finishRemovalHelp(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for step := 0; cmd != nil && step < 4; step++ {
		updated, next := m.Update(cmd())
		m = updated.(Model)
		cmd = next
	}
	if cmd != nil || m.page != "review" || m.confirmParams["uuid"] != "stopped" || m.confirmParams["confirmation_token"] != "captured-token" {
		t.Fatalf("workflow stranded: page%s working%v pending%s review%+v notice%s", m.page, m.working, m.pendingMethod, m.confirmParams, m.notice)
	}
	return m
}
func TestRemovalHelpPreservesPendingReadsAndAcceptedInspection(t *testing.T) {
	for _, key := range []string{"d", "X"} {
		for _, window := range []string{"initial", "poll", "preview"} {
			for _, arrival := range []string{"during-help", "after-return"} {
				t.Run(fmt.Sprintf("%s/%s/%s", key, window, arrival), func(t *testing.T) {
					m, f, reply := removalHelpWindow(t, key, window)
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
					if counts["workspace.loss.inspect"] != 1 || counts["session.removal.preview"] != 1 || counts["session.discard"] != 0 || counts["session.delete"] != 0 {
						t.Fatalf("Help replayed accepted work: %v", f.calls)
					}
					m, cmd = press(m, "enter")
					if cmd != nil || m.page != "review" {
						t.Fatal("Enter authorized removal")
					}
					m.selected = "running" // Review remains bound to the captured identity.
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
}
func TestRemovalHelpDefersErrorsAndProtectsEpoch(t *testing.T) {
	for _, window := range []string{"initial", "poll", "preview"} {
		t.Run(window, func(t *testing.T) {
			m, f, reply := removalHelpWindow(t, "X", window)
			m, _ = press(m, "?")
			stale := reply
			stale.epoch--
			updated, cmd := m.Update(stale)
			m = updated.(Model)
			if cmd != nil || m.helpRemovalRead.reply != nil {
				t.Fatal("stale reply consumed pending context")
			}
			reply.err = errors.New("inspection unavailable")
			updated, cmd = m.Update(reply)
			m = updated.(Model)
			if cmd != nil || m.page != "help" || m.notice != "" {
				t.Fatal("error escaped Help")
			}
			m, cmd = press(m, "esc")
			if cmd != nil || m.page != "progress" || m.working || m.pendingMethod != "" || m.notice != "inspection unavailable" {
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
		})
	}
}
func TestRemovalHelpDepartureAndContextChangeIgnoreLateReplies(t *testing.T) {
	for _, window := range []string{"initial", "preview"} {
		for _, change := range []string{"departure", "UUID", "kind"} {
			t.Run(window+"/"+change, func(t *testing.T) {
				m, f, reply := removalHelpWindow(t, "d", window)
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
}
func TestRemovalHelpRefusesForeignOperationAndPreview(t *testing.T) {
	for _, window := range []string{"initial", "poll", "preview"} {
		t.Run(window, func(t *testing.T) {
			m, _, reply := removalHelpWindow(t, "d", window)
			m, _ = press(m, "?")
			m, _ = press(m, "esc")
			if window == "preview" {
				reply.raw = json.RawMessage(`{"preview":{"session_uuid":"other","kind":"discard","confirmation_token":"token"}}`)
			} else {
				reply.raw = json.RawMessage(`{"operation":{"id":"foreign","kind":"workspace.loss.inspect","session_uuid":"other","status":"completed"}}`)
			}
			updated, cmd := m.Update(reply)
			m = updated.(Model)
			if cmd != nil || m.page != "progress" || m.confirmMethod != "" || m.operationID == "foreign" {
				t.Fatalf("foreign response adopted: page%s op%s confirmation%s", m.page, m.operationID, m.confirmMethod)
			}
			if m.notice == "" {
				t.Fatal("foreign identity refusal unexplained")
			}
		})
	}
}
