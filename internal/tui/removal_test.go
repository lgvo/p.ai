package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/api"
)

func TestRemovalKeysRequestPreviewForStoppedRunningAndStartingSessions(t *testing.T) {
	for _, condition := range []string{"stopped", "ready", "starting"} {
		for key, kind := range map[string]string{"d": "discard", "X": "delete"} {
			t.Run(condition+"/"+key, func(t *testing.T) {
				var requested any
				m := fixture()
				m.data.sessions = append(m.data.sessions, api.SessionView{UUID: targetID, Project: "b", Branch: "work", Condition: condition})
				m.client = &fakeClient{fn: func(method string, p any) (any, error) {
					if method != "session.removal.preview" {
						t.Fatalf("%s requested %s instead of its preview", key, method)
					}
					requested = p
					return map[string]any{"preview": map[string]any{"session_uuid": targetID, "kind": kind, "confirmation_token": reviewToken, "runtime": map[string]any{"condition": "present", "runtime_data_lost": true}}}, nil
				}}
				m.selected = targetID
				m.restoreSelection()
				m, cmd := press(m, key)
				if cmd == nil || m.page != "progress" {
					t.Fatalf("%s on a %s session did not request its review: page %s notice %q", key, condition, m.page, m.notice)
				}
				updated, _ := m.Update(cmd())
				m = updated.(Model)
				if want := (api.SessionRemovalPreviewRequest{UUID: targetID, Kind: kind}); !reflect.DeepEqual(requested, want) {
					t.Fatalf("preview params %v, want %v", requested, want)
				}
				if uuid, token := removalConfirmOf(t, m.confirm); m.page != "review" || m.confirm.method != "session."+kind || uuid != targetID || token != reviewToken {
					t.Fatalf("preview did not open the bound review: page %s %s %s %s", m.page, m.confirm.method, uuid, token)
				}
			})
		}
	}
}

// removalConfirmOf is the session and token that a removal confirmation
// sends.
func removalConfirmOf(t *testing.T, call daemonCall) (string, string) {
	t.Helper()
	switch call.method {
	case api.SessionDiscard.Name:
		r := requestOf[api.SessionDiscardRequest](t, call)
		return r.UUID, r.ConfirmationToken
	case api.SessionDelete.Name:
		r := requestOf[api.SessionDeleteRequest](t, call)
		return r.UUID, r.ConfirmationToken
	}
	t.Fatalf("confirmation calls %q", call.method)
	return "", ""
}

func TestRemovalKeysRefuseSessionsTheDaemonWouldRefuse(t *testing.T) {
	for _, s := range []api.SessionView{
		{UUID: targetID, Condition: "ready", AttachedCount: 1},
		{UUID: targetID, Condition: "starting", AttachedCount: 1},
		{UUID: targetID, Condition: "missing"},
		{UUID: targetID, Condition: "unreachable"},
		{UUID: targetID, Condition: "creating"},
		{UUID: targetID, Condition: "discarding"},
		{UUID: targetID, Condition: "deleting"},
	} {
		for _, key := range []string{"d", "X"} {
			t.Run(s.Condition+"/"+key, func(t *testing.T) {
				m := fixture()
				s.Project, s.Branch = "b", "work"
				m.data.sessions = append(m.data.sessions, s)
				m.client = &fakeClient{fn: func(method string, p any) (any, error) {
					t.Fatalf("refused removal requested %s", method)
					return nil, nil
				}}
				m.selected = targetID
				m.restoreSelection()
				m, cmd := press(m, key)
				if cmd != nil || m.page != "sessions" || m.removal != nil {
					t.Fatalf("%s on %+v began a removal", key, s)
				}
				if m.notice == "" {
					t.Fatal("refused removal was not explained")
				}
			})
		}
	}
}

func reviewOf(t *testing.T, kind, preview string) string {
	t.Helper()
	m := fixture()
	m.width, m.height = 120, 35
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: kind}
	m.page = "progress"
	updated, _ := m.Update(reply(t, m.epoch, "session.removal.preview", `{"preview":`+preview+`}`))
	m = updated.(Model)
	if m.page != "review" {
		t.Fatalf("preview did not open its review: %s", m.notice)
	}
	return strings.Join(m.reviewLines(), " ")
}

func TestRemovalReviewStatesRuntimeLossByCategory(t *testing.T) {
	for _, kind := range []string{"discard", "delete"} {
		review := reviewOf(t, kind, `{"kind":"`+kind+`","session_uuid":"`+stoppedID+`","project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"1111111111111111111111111111111111111111","runtime":{"condition":"present","incus_project":"p-a","instance_name":"p-stopped","original_status":"Stopped","runtime_data_lost":true},"branch_loss":{"assigned_ref":"refs/heads/main","commits_losing_p_reachability":[],"p_refs":[]},"confirmation_token":"token","expires_at":"2026-11-02T12:02:00Z"}`)
		for _, category := range []string{"uncommitted changes", "files outside Git", "commits not pushed to P", "home directory", "runtime-local credentials", "P does not look inside"} {
			if !strings.Contains(review, category) {
				t.Errorf("%s review omits %q:\n%s", kind, category, review)
			}
		}
		for _, identity := range []string{"a / main", stoppedID, "p-stopped", "refs/heads/main", "2026-11-02T12:02:00Z"} {
			if !strings.Contains(review, identity) {
				t.Errorf("%s review omits %q:\n%s", kind, identity, review)
			}
		}
		if strings.Contains(review, "stops the running container") {
			t.Errorf("%s review of a stopped container says P stops it:\n%s", kind, review)
		}
	}
}

func TestRemovalReviewSaysPStopsARunningContainer(t *testing.T) {
	review := reviewOf(t, "discard", `{"kind":"discard","session_uuid":"`+stoppedID+`","project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"1111111111111111111111111111111111111111","runtime":{"condition":"present","instance_name":"p-stopped","original_status":"Running","runtime_data_lost":true},"confirmation_token":"token"}`)
	if !strings.Contains(review, "stops the running container") {
		t.Fatalf("review of a running container does not say P stops it:\n%s", review)
	}
}

func TestRemovalReviewStatesWhatHappensToTheBranch(t *testing.T) {
	const tip = "1111111111111111111111111111111111111111"
	const lost = "2222222222222222222222222222222222222222"
	for _, item := range []struct{ name, kind, preview, want, absent string }{
		{"discard keeps the branch", "discard", `{"kind":"discard","session_uuid":"` + stoppedID + `","assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","runtime":{"condition":"present","runtime_data_lost":true},"confirmation_token":"token"}`, "Discard keeps this branch in P", "keeps no branch"},
		{"delete lists commits leaving P", "delete", `{"kind":"delete","session_uuid":"` + stoppedID + `","assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","runtime":{"condition":"present","runtime_data_lost":true},"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","commits_losing_p_reachability":["` + lost + `"],"p_refs":[]},"confirmation_token":"token"}`, lost, "removes nothing"},
		{"delete says P does not check the origin", "delete", `{"kind":"delete","session_uuid":"` + stoppedID + `","assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","runtime":{"condition":"present","runtime_data_lost":true},"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","commits_losing_p_reachability":["` + lost + `"],"p_refs":[]},"confirmation_token":"token"}`, "does not check whether the external origin", "keeps this branch"},
		{"missing branch: discard keeps none", "discard", `{"kind":"discard","session_uuid":"` + stoppedID + `","assigned_ref":"refs/heads/main","assigned_ref_missing":true,"runtime":{"condition":"present","runtime_data_lost":true},"confirmation_token":"token"}`, "keeps no branch", "keeps this branch"},
		{"missing branch: delete removes nothing", "delete", `{"kind":"delete","session_uuid":"` + stoppedID + `","assigned_ref":"refs/heads/main","assigned_ref_missing":true,"runtime":{"condition":"present","runtime_data_lost":true},"branch_loss":{"assigned_ref":"refs/heads/main","commits_losing_p_reachability":[],"p_refs":[]},"confirmation_token":"token"}`, "removes nothing from P's repository", "removes this branch"},
	} {
		t.Run(item.name, func(t *testing.T) {
			review := reviewOf(t, item.kind, item.preview)
			if !strings.Contains(review, item.want) || strings.Contains(review, item.absent) {
				t.Fatalf("review should contain %q and not %q:\n%s", item.want, item.absent, review)
			}
		})
	}
}

func TestRemovalReviewRefusesAPreviewThatDoesNotStateRuntimeLoss(t *testing.T) {
	m := fixture()
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: "discard"}
	m.page = "progress"
	updated, _ := m.Update(reply(t, m.epoch, "session.removal.preview", `{"preview":{"kind":"discard","session_uuid":"`+stoppedID+`","runtime":{"condition":"present"},"confirmation_token":"token"}}`))
	m = updated.(Model)
	if m.page == "review" || m.confirm.method != "" || m.notice == "" {
		t.Fatalf("a review without its runtime loss became authorizable: page %s", m.page)
	}
}

func TestRenameRefusesASessionThatIsNotStoppedBeforeTheForm(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		t.Fatalf("refused Rename requested %s", method)
		return nil, nil
	}}
	m.selected = runningID
	m.restoreSelection()
	m, cmd := press(m, "R")
	if cmd != nil || m.page != "sessions" || m.notice == "" {
		t.Fatalf("Rename of a running session opened page %s with notice %q", m.page, m.notice)
	}
	m.notice = ""
	m.selected = stoppedID
	m.restoreSelection()
	m, _ = press(m, "R")
	if m.page != "form" || m.form != "rename" {
		t.Fatalf("Rename of a stopped session did not open its form: page %s", m.page)
	}
}

func TestFailedRemovalPreviewKeepsItsErrorWithoutPollingTheSession(t *testing.T) {
	m := fixture()
	m.data.sessions = append(m.data.sessions, api.SessionView{UUID: targetID, Project: "b", Branch: "work", Condition: "starting"})
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		if method != "session.removal.preview" {
			t.Fatalf("pending removal review requested %s", method)
		}
		return nil, errors.New("busy: runtime is Frozen")
	}}
	m.selected = targetID
	m.restoreSelection()
	m, cmd := press(m, "d")
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.working || m.page != "progress" || m.notice != "busy: runtime is Frozen" {
		t.Fatalf("tick replaced the failed review: working %v page %s notice %q", m.working, m.page, m.notice)
	}
	if _, lines, _ := m.inspectionContent(); !strings.Contains(strings.Join(lines, "\n"), "Nothing was removed") {
		t.Fatalf("failed review still reads as pending:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRemovalProgressNamesTheRequestedReview(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.selected = runningID
	m.restoreSelection()
	m, _ = press(m, "X")
	title, lines, _ := m.inspectionContent()
	text := title + "\n" + strings.Join(lines, "\n")
	if !strings.Contains(text, "Delete review") || strings.Contains(text, "startup") {
		t.Fatalf("removal progress does not name its review:\n%s", text)
	}
}

func TestRemovalReviewUsesTheTextPageHeightAndKeepsItsPromptReadable(t *testing.T) {
	for _, size := range []struct{ width, height, rows int }{{80, 24, 16}, {48, 16, 8}} {
		m := fixture()
		m.width, m.height = size.width, size.height
		m.page = "review"
		rows := []string{}
		for i := 1; i <= 40; i++ {
			rows = append(rows, fmt.Sprintf("row %02d", i))
		}
		m.review = strings.Join(rows, "\n")
		frame := ansi.Strip(m.View().Content)
		if last := fmt.Sprintf("row %02d", size.rows); !strings.Contains(frame, last) || strings.Contains(frame, fmt.Sprintf("row %02d", size.rows+1)) {
			t.Errorf("%dx%d review does not show rows 1-%d:\n%s", size.width, size.height, size.rows, frame)
		}
		if !strings.Contains(frame, "ownership") {
			t.Errorf("%dx%d review clips its confirmation prompt:\n%s", size.width, size.height, frame)
		}
	}
}

func TestRemovalReviewScrollStopsAtItsLastPageAcrossResize(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.page = "review"
	rows := []string{}
	for i := 1; i <= 20; i++ {
		rows = append(rows, fmt.Sprintf("row %02d", i))
	}
	m.review = strings.Join(rows, "\n")
	m, _ = press(m, "G")
	m, _ = press(m, "j")
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "row 13") {
		t.Fatalf("scrolling past the end hid the last full page:\n%s", frame)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	m = updated.(Model)
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "row 01") || !strings.Contains(frame, "row 20") {
		t.Fatalf("enlarged review does not show its whole text:\n%s", frame)
	}
}
