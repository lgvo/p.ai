package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/api"
)

// removalPlan is the JSON of an allowed plan of a removal (kind) of session
// uuid with details, and its change tag.
func removalPlan(kind, uuid, details string) string {
	return `{"plan":{"action":"session.` + kind + `","target":"session/` + uuid + `","allowed":true,"refusals":[],` +
		`"loss":[{"kind":"runtime_data","target":"session/` + uuid + `","certain":true}],"gone":[],"details":` + details + `},"change_tag":"` + reviewTag + `"}`
}

func TestRemovalKeysRequestThePlanOfStoppedRunningStartingAndMissingSessions(t *testing.T) {
	for _, condition := range []string{"stopped", "ready", "starting", "missing"} {
		for key, kind := range map[string]string{"d": "discard", "X": "delete"} {
			t.Run(condition+"/"+key, func(t *testing.T) {
				var requested any
				m := fixture()
				m.data.sessions = append(m.data.sessions, api.SessionView{UUID: targetID, Project: "b", Branch: "work", Condition: condition})
				m.client = &fakeClient{fn: func(method string, p any) (any, error) {
					if method != "session."+kind {
						t.Fatalf("%s requested %s instead of its plan", key, method)
					}
					requested = p
					var plan any
					err := json.Unmarshal([]byte(removalPlan(kind, targetID, `{"runtime":{"condition":"present"}}`)), &plan)
					return plan, err
				}}
				m.selected = targetID
				m.restoreSelection()
				m, cmd := press(m, key)
				if cmd == nil || m.page != "progress" {
					t.Fatalf("%s on a %s session did not request its plan: page %s notice %q", key, condition, m.page, m.notice)
				}
				updated, _ := m.Update(cmd())
				m = updated.(Model)
				if uuid, change := removalRequestOf(t, kind, requested); uuid != targetID || !change.DryRun() {
					t.Fatalf("plan params %v, want a dry run of %s", requested, targetID)
				}
				if uuid, change := removalConfirmOf(t, m.confirm); m.page != "review" || m.confirm.method != "session."+kind || uuid != targetID || change.IfMatch != reviewTag || change.Key == "" {
					t.Fatalf("the plan did not open its bound review: page %s %s %s %+v", m.page, m.confirm.method, uuid, change)
				}
			})
		}
	}
}

// removalRequestOf is the session and change of a removal request p.
func removalRequestOf(t *testing.T, kind string, p any) (string, api.ChangeRequest) {
	t.Helper()
	switch r := p.(type) {
	case api.SessionDiscardRequest:
		if kind == "discard" {
			return r.UUID, r.ChangeRequest
		}
	case api.SessionDeleteRequest:
		if kind == "delete" {
			return r.UUID, r.ChangeRequest
		}
	}
	t.Fatalf("%s sent %T", kind, p)
	return "", api.ChangeRequest{}
}

// removalConfirmOf is the session and change that a removal's run sends.
func removalConfirmOf(t *testing.T, call daemonCall) (string, api.ChangeRequest) {
	t.Helper()
	switch call.method {
	case api.SessionDiscard.Name:
		r := requestOf[api.SessionDiscardRequest](t, call)
		return r.UUID, r.ChangeRequest
	case api.SessionDelete.Name:
		r := requestOf[api.SessionDeleteRequest](t, call)
		return r.UUID, r.ChangeRequest
	}
	t.Fatalf("confirmation calls %q", call.method)
	return "", api.ChangeRequest{}
}

func TestRemovalKeysRefuseSessionsTheDaemonWouldRefuse(t *testing.T) {
	for _, s := range []api.SessionView{
		{UUID: targetID, Condition: "ready", AttachedCount: 1},
		{UUID: targetID, Condition: "starting", AttachedCount: 1},
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

// reviewOf is the review that the plan raw of a removal (kind) of the
// stopped session opens, as one line of single-spaced words.
func reviewOf(t *testing.T, kind, raw string) string {
	t.Helper()
	m := fixture()
	m.width, m.height = 120, 35
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: kind}
	m.page = "progress"
	updated, _ := m.Update(reply(t, m.epoch, "session."+kind, raw))
	m = updated.(Model)
	if m.page != "review" {
		t.Fatalf("the plan did not open its review: %s", m.notice)
	}
	return strings.Join(strings.Fields(strings.Join(m.reviewLines(), " ")), " ")
}

func TestRemovalReviewStatesRuntimeLossByCategory(t *testing.T) {
	for _, kind := range []string{"discard", "delete"} {
		review := reviewOf(t, kind, removalPlan(kind, stoppedID, `{"project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"1111111111111111111111111111111111111111","runtime":{"condition":"present","incus_project":"p-a","instance_name":"p-stopped","original_status":"Stopped"},"branch_loss":{"assigned_ref":"refs/heads/main","commits_losing_p_reachability":[],"p_refs":[]}}`))
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
		if strings.Contains(review, "stops the running container") || strings.Contains(review, "Already gone") {
			t.Errorf("%s review of a stopped container says P stops it or something is gone:\n%s", kind, review)
		}
	}
}

func TestRemovalReviewSaysPStopsARunningContainer(t *testing.T) {
	review := reviewOf(t, "discard", removalPlan("discard", stoppedID, `{"project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"1111111111111111111111111111111111111111","runtime":{"condition":"present","instance_name":"p-stopped","original_status":"Running"}}`))
	if !strings.Contains(review, "stops the running container") {
		t.Fatalf("review of a running container does not say P stops it:\n%s", review)
	}
}

// A missing runtime's contents are already gone: the review says so as
// information, and states no loss of the container.
func TestRemovalReviewOfAMissingRuntimeStatesWhatIsGone(t *testing.T) {
	raw := `{"plan":{"action":"session.discard","target":"session/` + stoppedID + `","allowed":true,"refusals":[],"loss":[],` +
		`"gone":[{"kind":"runtime_data","target":"session/` + stoppedID + `","certain":true}],` +
		`"details":{"project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"1111111111111111111111111111111111111111","runtime":{"condition":"missing","incus_project":"p-a","instance_name":"p-stopped"}}},"change_tag":"` + reviewTag + `"}`
	review := reviewOf(t, "discard", raw)
	if !strings.Contains(review, "already missing") || !strings.Contains(review, "Already gone") || strings.Contains(review, "This will lose work") {
		t.Fatalf("review of a missing runtime:\n%s", review)
	}
}

func TestRemovalReviewStatesWhatHappensToTheBranch(t *testing.T) {
	const tip = "1111111111111111111111111111111111111111"
	const lost = "2222222222222222222222222222222222222222"
	commits := `,"loss":[{"kind":"runtime_data","target":"session/` + stoppedID + `","certain":true},{"kind":"p_commits","target":"project/a:branch/main","count":1,"sample":["` + lost + `"],"certain":true}]`
	withCommits := func(raw string) string {
		return strings.Replace(raw, `,"loss":[{"kind":"runtime_data","target":"session/`+stoppedID+`","certain":true}]`, commits, 1)
	}
	for _, item := range []struct{ name, kind, raw, want, absent string }{
		{"discard keeps the branch", "discard", removalPlan("discard", stoppedID, `{"assigned_ref":"refs/heads/main","assigned_tip":"`+tip+`","runtime":{"condition":"present"}}`), "Discard keeps this branch in P", "keeps no branch"},
		{"delete lists commits leaving P", "delete", withCommits(removalPlan("delete", stoppedID, `{"branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"`+tip+`","runtime":{"condition":"present"},"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"`+tip+`","commits_losing_p_reachability":["`+lost+`"],"p_refs":[]}}`)), lost, "removes nothing"},
		{"delete says P does not check the origin", "delete", withCommits(removalPlan("delete", stoppedID, `{"branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"`+tip+`","runtime":{"condition":"present"},"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"`+tip+`","commits_losing_p_reachability":["`+lost+`"],"p_refs":[]}}`)), "does not check whether the origin", "keeps this branch"},
		{"missing branch: discard keeps none", "discard", removalPlan("discard", stoppedID, `{"assigned_ref":"refs/heads/main","assigned_ref_missing":true,"runtime":{"condition":"present"}}`), "keeps no branch", "keeps this branch"},
		{"missing branch: delete removes nothing", "delete", removalPlan("delete", stoppedID, `{"assigned_ref":"refs/heads/main","assigned_ref_missing":true,"runtime":{"condition":"present"},"branch_loss":{"assigned_ref":"refs/heads/main","commits_losing_p_reachability":[],"p_refs":[]}}`), "removes nothing from P's repository", "removes this branch"},
	} {
		t.Run(item.name, func(t *testing.T) {
			review := reviewOf(t, item.kind, item.raw)
			if !strings.Contains(review, item.want) || strings.Contains(review, item.absent) {
				t.Fatalf("review should contain %q and not %q:\n%s", item.want, item.absent, review)
			}
		})
	}
}

// The commits that leave P are counted in words that agree with the count.
func TestRemovalReviewCountsTheCommitsThatLeaveP(t *testing.T) {
	const one = "1111111111111111111111111111111111111111"
	const two = "2222222222222222222222222222222222222222"
	for _, item := range []struct {
		commits []string
		want    string
	}{
		{[]string{one}, "1 commit on branch main leaves P's repository"},
		{[]string{one, two}, "2 commits on branch main leave P's repository"},
	} {
		sample, _ := json.Marshal(item.commits)
		raw := `{"plan":{"action":"session.delete","target":"session/` + stoppedID + `","allowed":true,"refusals":[],` +
			`"loss":[{"kind":"p_commits","target":"project/a:branch/main","count":` + fmt.Sprint(len(item.commits)) + `,"sample":` + string(sample) + `,"certain":true}],"gone":[],` +
			`"details":{"project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"` + one + `","runtime":{"condition":"present"},` +
			`"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"` + one + `","commits_losing_p_reachability":` + string(sample) + `,"p_refs":[]}}},"change_tag":"` + reviewTag + `"}`
		if review := reviewOf(t, "delete", raw); !strings.Contains(review, item.want) {
			t.Errorf("review of %d commits leaving P should say %q:\n%s", len(item.commits), item.want, review)
		}
	}
}

// A loss item that wraps continues under its own text, and the commits it
// lists sit under it, so where one item ends stays visible at any width.
func TestRemovalReviewKeepsEachLossItemTogetherWhenItWraps(t *testing.T) {
	const tip = "1111111111111111111111111111111111111111"
	const lost = "2222222222222222222222222222222222222222"
	m := fixture()
	m.width, m.height = 48, 16
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: "delete"}
	m.page = "progress"
	raw := `{"plan":{"action":"session.delete","target":"session/` + stoppedID + `","allowed":true,"refusals":[],` +
		`"loss":[{"kind":"runtime_data","target":"session/` + stoppedID + `","certain":true},{"kind":"p_commits","target":"project/a:branch/main","count":1,"sample":["` + lost + `"],"certain":true}],"gone":[],` +
		`"details":{"project":"a","branch":"main","assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","runtime":{"condition":"present","incus_project":"p-a","instance_name":"p-stopped","original_status":"Stopped"},` +
		`"branch_loss":{"assigned_ref":"refs/heads/main","assigned_tip":"` + tip + `","commits_losing_p_reachability":["` + lost + `"],"p_refs":[]}}},"change_tag":"` + reviewTag + `"}`
	updated, _ := m.Update(reply(t, m.epoch, "session.delete", raw))
	m = updated.(Model)
	lines := m.reviewLines()
	first, last := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "- The container") {
			first = i
		}
		if first >= 0 && last < 0 && line == "" {
			last = i
		}
	}
	if first < 0 || last < first+3 {
		t.Fatalf("review does not list its two loss items before the branch:\n%s", strings.Join(lines, "\n"))
	}
	items := 0
	for _, line := range lines[first:last] {
		switch {
		case strings.HasPrefix(line, "- "):
			items++
		case !strings.HasPrefix(line, "  "):
			t.Errorf("line %q of a loss item is not under the item's text:\n%s", line, strings.Join(lines, "\n"))
		}
	}
	if items != 2 || !strings.Contains(strings.Join(lines[first:last], "\n"), "  "+lost) {
		t.Errorf("review shows %d loss items, want 2 with the lost commit under its item:\n%s", items, strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 44 {
			t.Errorf("line %q is wider than the 48-column page", line)
		}
	}
}

// A plan the daemon does not allow, one without a tag, and one of another
// removal than the page asked for never become authorizable; a refusal is
// explained in its own sentence.
func TestRemovalReviewRefusesAPlanItCannotRun(t *testing.T) {
	for _, item := range []struct{ name, raw, notice string }{
		{"not allowed", `{"plan":{"action":"session.discard","target":"session/` + stoppedID + `","allowed":false,"refusals":[{"code":"attachment_active"}],"loss":[],"gone":[],"details":{"runtime":{"condition":"present"}}}}`, "Detach every terminal"},
		{"without a tag", `{"plan":{"action":"session.discard","target":"session/` + stoppedID + `","allowed":true,"refusals":[],"loss":[],"gone":[],"details":{"runtime":{"condition":"present"}}}}`, "No valid removal plan"},
		{"of another session", removalPlan("discard", runningID, `{"runtime":{"condition":"present"}}`), "No valid removal plan"},
		{"of another action", strings.Replace(removalPlan("discard", stoppedID, `{"runtime":{"condition":"present"}}`), "session.discard", "session.delete", 1), "No valid removal plan"},
	} {
		t.Run(item.name, func(t *testing.T) {
			m := fixture()
			m.contextSession = m.data.sessions[0]
			m.removal = &removalIntent{UUID: stoppedID, Kind: "discard"}
			m.page = "progress"
			updated, _ := m.Update(reply(t, m.epoch, "session.discard", item.raw))
			m = updated.(Model)
			if m.page == "review" || m.confirm.method != "" || !strings.Contains(m.notice, item.notice) {
				t.Fatalf("a plan %s: page %s notice %q", item.name, m.page, m.notice)
			}
		})
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

func TestFailedRemovalPlanKeepsItsErrorWithoutPollingTheSession(t *testing.T) {
	m := fixture()
	m.data.sessions = append(m.data.sessions, api.SessionView{UUID: targetID, Project: "b", Branch: "work", Condition: "starting"})
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		if method != "session.discard" {
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

// TestChangedOrExpiredRemovalRunShowsTheCurrentPlan: a run that answers that
// the reviewed plan changed or its tag expired removed nothing; the page reads
// the plan again and shows it, so y never sends the stale tag again.
func TestChangedOrExpiredRemovalRunShowsTheCurrentPlan(t *testing.T) {
	for _, reason := range []string{api.ReasonChanged, api.ReasonExpired} {
		t.Run(reason, func(t *testing.T) {
			var sent []api.SessionDeleteRequest
			m := fixture()
			m.client = &fakeClient{fn: func(method string, p any) (any, error) {
				sent = append(sent, p.(api.SessionDeleteRequest))
				return nil, errors.New("recorded")
			}}
			m.contextSession = m.data.sessions[0]
			m.removal = &removalIntent{UUID: stoppedID, Kind: "delete"}
			m.navigate("progress")
			updated, _ := m.Update(reply(t, m.epoch, "session.delete", removalPlan("delete", stoppedID, `{"project":"a","branch":"main","runtime":{"condition":"present"}}`)))
			m = updated.(Model)
			m, run := press(m, "y")
			run()
			updated, cmd := m.Update(actionDone{epoch: m.epoch, method: api.SessionDelete.Name, err: api.NewError(api.KindPreconditionFailed, reason)})
			m = updated.(Model)
			if m.page != "progress" || m.pendingMethod != api.SessionDelete.Name || cmd == nil {
				t.Fatalf("a %s run did not read the plan again: page %s pending %q", reason, m.page, m.pendingMethod)
			}
			cmd()
			if len(sent) != 2 || sent[1].IfMatch != "" || sent[1].Key != "" || sent[1].UUID != stoppedID {
				t.Fatalf("requests %+v", sent)
			}
			// The new plan arrives while Help is open; Back shows it with the reason.
			newTag := "20261102T120400Z-" + strings.Repeat("f", 64)
			plan := strings.Replace(removalPlan("delete", stoppedID, `{"project":"a","branch":"main-v2","runtime":{"condition":"present"}}`), reviewTag, newTag, 1)
			epoch := m.epoch
			m, _ = press(m, "?")
			updated, _ = m.Update(reply(t, epoch, "session.delete", plan))
			m = updated.(Model)
			m, _ = press(m, "q")
			if m.page != "review" || !strings.Contains(m.review, "main-v2") || !strings.Contains(m.notice, "Nothing was removed") {
				t.Fatalf("page %s notice %q review:\n%s", m.page, m.notice, m.review)
			}
			m, run = press(m, "y")
			run()
			if last := sent[len(sent)-1]; last.IfMatch != newTag || last.UUID != stoppedID || last.Key == "" {
				t.Fatalf("y after the new review sent %+v", last)
			}
		})
	}
	// Another error leaves the review as it was.
	m := fixture()
	m.navigate("review")
	m.confirm = callOf(api.SessionDiscard, api.SessionDiscardRequest{UUID: stoppedID})
	updated, cmd := m.Update(actionDone{epoch: m.epoch, method: api.SessionDiscard.Name, err: api.NewError(api.KindFailed, api.ReasonRuntime)})
	m = updated.(Model)
	if m.page != "review" || cmd != nil {
		t.Fatalf("a failed run left the review: page %s", m.page)
	}
}
