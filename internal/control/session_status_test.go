package control

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func statusFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none","filesystem_mounts":[],"command":["/bin/sh"]}`)); err != nil {
		t.Fatal(err)
	}
	_, session, err := s.ReserveSession(ctx, ReserveSessionRequest{Key: "status-create", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET registry_state='established' WHERE uuid=?`, session.UUID); err != nil {
		t.Fatal(err)
	}
	return s, session.UUID
}

func TestVersionTwoStatusMigrationIsRestartable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(migration1); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(migration2); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO metadata(key,value) VALUES('instance_id','v2-fixture')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := testScopedOpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 16 {
			t.Fatalf("migration version=%d: %v", version, err)
		}
		var sequence int
		if err = s.db.QueryRow(`SELECT next_value FROM status_sequence WHERE id=1`).Scan(&sequence); err != nil || sequence != 1 {
			t.Fatalf("status sequence=%d: %v", sequence, err)
		}
		if _, err = s.db.Exec(`SELECT * FROM session_unattended LIMIT 1`); err != nil {
			t.Fatal(err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func reportLine(condition string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"codex/main","condition":%q,"reason":"permission requested","adapter":"codex","adapter_version":"1"}}`, condition))
}

func TestStatusReportPrintableBounds(t *testing.T) {
	base := StatusReport{V: 1, Condition: "idle", Adapter: "codex", AdapterVersion: "1"}
	if !base.Valid() {
		t.Fatal("optional source rejected")
	}
	for _, mutate := range []func(*StatusReport){
		func(r *StatusReport) { r.Source = strings.Repeat("s", 129) },
		func(r *StatusReport) { r.Reason = strings.Repeat("r", 257) },
		func(r *StatusReport) { r.Reason = "\x1b[31m" },
		func(r *StatusReport) { r.Reason = "\u202e" },
		func(r *StatusReport) { r.Source = string([]byte{0xff}) },
	} {
		v := base
		mutate(&v)
		if v.Valid() {
			t.Fatalf("invalid report accepted: %+v", v)
		}
	}
}

func TestSessionRPCBoundIdentityAndStrictReports(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	query := func(method, params string) map[string]any {
		t.Helper()
		line := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params))
		reply, _ := SessionReply(ctx, s, id, line)
		var v map[string]any
		if err := json.Unmarshal(reply, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := query("session.identity", `{"v":1}`)["result"].(map[string]any); v["uuid"] != id || v["branch"] != "work" {
		t.Fatal(v)
	}
	if v := query("session.capabilities", `{"v":1}`)["result"].(map[string]any); v["effective_capabilities"].(map[string]any)["network"] != "none" {
		t.Fatal(v)
	}
	for _, method := range []string{"session.list", "session.start", "system.inspect", "project.branches"} {
		if query(method, `{"v":1}`)["error"] == nil {
			t.Fatalf("host method %s exposed", method)
		}
	}
	if query("session.identity", `{"v":1,"uuid":"other"}`)["error"] == nil {
		t.Fatal("cross-session UUID accepted")
	}
	if reply, change := SessionReply(ctx, s, id, reportLine("attention")); len(reply) != 0 || change == nil || change.Condition != "attention" {
		t.Fatalf("notification: %s %+v", reply, change)
	}
	beforeCount, before, err := s.SessionStatus(ctx, id)
	if err != nil || beforeCount != 0 || before.Condition != "attention" {
		t.Fatalf("first status: %+v %v", before, err)
	}
	bad := [][]byte{
		reportLine("completed"),
		[]byte(`{"jsonrpc":"2.0","method":"status.report","params":{"v":2,"source":"x","condition":"idle","adapter":"a","adapter_version":"1"}}`),
		[]byte(`{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"x","condition":"idle","adapter":"a","adapter_version":"1","uuid":"other"}}`),
		[]byte(`{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"x","condition":"idle","adapter":"a","adapter_version":"1","adapter":"b"}}`),
		[]byte(`{"jsonrpc":"2.0","id":9,"method":"status.report","params":{"v":1,"source":"x","condition":"idle","adapter":"a","adapter_version":"1"}}`),
	}
	for _, line := range bad {
		_, change := SessionReply(ctx, s, id, line)
		if change != nil {
			t.Fatalf("invalid report changed state: %s", line)
		}
	}
	_, after, err := s.SessionStatus(ctx, id)
	if err != nil || after.ReceiveSequence != before.ReceiveSequence {
		t.Fatalf("malformed report overwrote status: %+v %v", after, err)
	}
}

func TestStatusReducerAttachmentAndReceiveOrder(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	report := func(condition string) (UnattendedCondition, bool, error) {
		return s.RecordStatus(ctx, id, StatusReport{V: 1, Source: "codex/main", Condition: condition, Adapter: "codex", AdapterVersion: "1"})
	}
	first, changed, err := report("attention")
	if err != nil || !changed {
		t.Fatal(err)
	}
	second, changed, err := report("running")
	if err != nil || !changed || second.ReceiveSequence <= first.ReceiveSequence {
		t.Fatalf("receive order: %+v %+v %v", first, second, err)
	}
	if count, ok, cleared, err := s.ConfirmAttachment(ctx, id); count != 1 || !ok || !cleared || err != nil {
		t.Fatalf("first attach: %d %t %t %v", count, ok, cleared, err)
	}
	if count, value, err := s.SessionStatus(ctx, id); count != 1 || value != nil || err != nil {
		t.Fatalf("clear: %d %+v %v", count, value, err)
	}
	if _, changed, err := report("failed"); err != nil || changed {
		t.Fatalf("attached report retained: %t %v", changed, err)
	}
	if count, ok, cleared, err := s.ConfirmAttachment(ctx, id); count != 2 || !ok || cleared || err != nil {
		t.Fatal(count, ok, cleared, err)
	}
	if count, ok := s.ReleaseAttachment(id); count != 1 || !ok {
		t.Fatal(count, ok)
	}
	if count, ok := s.ReleaseAttachment(id); count != 0 || !ok {
		t.Fatal(count, ok)
	}
	if _, value, err := s.SessionStatus(ctx, id); value != nil || err != nil {
		t.Fatalf("detach restored stale value: %+v %v", value, err)
	}
	third, changed, err := report("idle")
	if err != nil || !changed || third.ReceiveSequence <= second.ReceiveSequence {
		t.Fatalf("new unattended value: %+v %v", third, err)
	}
}

func TestCommittedStatusAndAttachmentCallbacksKeepReducerOrder(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	report := StatusReport{V: 1, Condition: "attention", Adapter: "codex", AdapterVersion: "1"}
	entered := make(chan struct{})
	release := make(chan struct{})
	ordered := make(chan string, 5)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, changed, err := s.RecordStatusWithCommit(ctx, id, report, func(UnattendedCondition) {
			ordered <- "report"
			close(entered)
			<-release
		})
		if err != nil || !changed {
			t.Errorf("report: changed=%t err=%v", changed, err)
		}
	}()
	<-entered // The report has committed, but its event has not finished enqueueing.
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, confirmed, cleared, err := s.ConfirmAttachmentWithCommit(ctx, id, func(count int, cleared bool) {
			if count != 1 || !cleared {
				t.Errorf("confirm callback: count=%d cleared=%t", count, cleared)
			}
			ordered <- "attach-and-clear"
		})
		if err != nil || !confirmed || !cleared {
			t.Errorf("confirm: confirmed=%t cleared=%t err=%v", confirmed, cleared, err)
		}
	}()
	select {
	case event := <-ordered:
		if event != "report" {
			t.Fatalf("first event = %s", event)
		}
	case <-time.After(time.Second):
		t.Fatal("report event absent")
	}
	select {
	case event := <-ordered:
		t.Fatalf("attachment overtook report: %s", event)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	workers.Wait()
	if event := <-ordered; event != "attach-and-clear" {
		t.Fatalf("second event = %s", event)
	}
	releaseEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		if count, ok := s.ReleaseAttachmentWithCommit(id, func(count int) {
			ordered <- "release"
			close(releaseEntered)
			<-releaseCallback
		}); !ok || count != 0 {
			t.Errorf("release: count=%d ok=%t", count, ok)
		}
	}()
	<-releaseEntered
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, changed, err := s.RecordStatusWithCommit(ctx, id, report, func(UnattendedCondition) { ordered <- "post-release-report" })
		if err != nil || !changed {
			t.Errorf("post-release report: changed=%t err=%v", changed, err)
		}
	}()
	if event := <-ordered; event != "release" {
		t.Fatalf("third event = %s", event)
	}
	select {
	case event := <-ordered:
		t.Fatalf("report overtook release: %s", event)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCallback)
	workers.Wait()
	if event := <-ordered; event != "post-release-report" {
		t.Fatalf("fourth event = %s", event)
	}
}

func TestStatusReducerRateAndDurability(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	report := StatusReport{V: 1, Source: "codex/main", Condition: "idle", Adapter: "codex", AdapterVersion: "1"}
	for i := 0; i < 20; i++ {
		if _, _, err := s.RecordStatus(ctx, id, report); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.RecordStatus(ctx, id, report); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit: %v", err)
	}
	_, before, err := s.SessionStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Another store instance cannot open while the writer lock is held; reopen
	// the same state after closing to prove the latest reduction is durable.
	dir := s.StateDir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, after, err := reopened.SessionStatus(ctx, id)
	if err != nil || after == nil || after.ReceiveSequence != before.ReceiveSequence || after.Condition != "idle" {
		t.Fatalf("reopened value: %+v %v", after, err)
	}
}

func TestStatusConcurrentReportsHaveUniqueSequence(t *testing.T) {
	s, id := statusFixture(t)
	var wg sync.WaitGroup
	seq := make(chan int64, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, changed, err := s.RecordStatus(context.Background(), id, StatusReport{V: 1, Source: "codex/main", Condition: "running", Adapter: "codex", AdapterVersion: "1"})
			if err == nil && changed {
				seq <- v.ReceiveSequence
			}
		}()
	}
	wg.Wait()
	close(seq)
	seen := map[int64]bool{}
	for n := range seq {
		if seen[n] {
			t.Fatalf("duplicate sequence %d", n)
		}
		seen[n] = true
	}
	if len(seen) != 12 {
		t.Fatalf("only %d reports retained", len(seen))
	}
}

func TestStatusReportRacingFirstAttachmentCannotLeaveStaleValue(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 10; i++ {
			_, _, _ = s.RecordStatus(ctx, id, StatusReport{V: 1, Source: "codex/main", Condition: "attention", Adapter: "codex", AdapterVersion: "1"})
		}
	}()
	go func() { defer wg.Done(); <-start; _, _, _, _ = s.ConfirmAttachment(ctx, id) }()
	close(start)
	wg.Wait()
	count, value, err := s.SessionStatus(ctx, id)
	if err != nil || count != 1 || value != nil {
		t.Fatalf("report raced attach: count=%d value=%+v err=%v", count, value, err)
	}
}

type statusLife struct{ store *Store }

func (l statusLife) CreateProject(context.Context, BlankProjectRequest) (Operation, error) {
	return Operation{}, ErrInvalid
}
func (l statusLife) CreateSession(context.Context, ReserveSessionRequest) (Operation, error) {
	return Operation{}, ErrInvalid
}
func (l statusLife) Retry(context.Context, string) (Operation, error) { return Operation{}, ErrInvalid }
func (l statusLife) StartSession(ctx context.Context, id string) (SessionView, error) {
	return l.InspectSession(ctx, id)
}
func (l statusLife) StopSession(ctx context.Context, id string) (SessionView, error) {
	return l.InspectSession(ctx, id)
}
func (l statusLife) InspectSession(ctx context.Context, id string) (SessionView, error) {
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return SessionView{}, err
	}
	v := NewSessionView(s)
	v.Condition = "stopped"
	v.PolicyCondition = "current"
	return v, nil
}

func TestHostInspectAndListProjectFourFacts(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	_, changed, err := s.RecordStatus(ctx, id, StatusReport{V: 1, Source: "codex/main", Condition: "attention", Adapter: "codex", AdapterVersion: "1"})
	if err != nil || !changed {
		t.Fatal(err)
	}
	h := StateHandlerWithLifecycle(s, nil, nil, statusLife{s})
	for _, call := range []struct{ method, params string }{
		{"session.inspect", fmt.Sprintf(`{"v":1,"uuid":%q}`, id)},
		{"session.list", `{"v":1,"limit":8}`},
	} {
		result, rpcErr := h(ctx, call.method, json.RawMessage(call.params))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var projection struct {
			Session  SessionView   `json:"session"`
			Sessions []SessionView `json:"sessions"`
		}
		if err := json.Unmarshal(encoded, &projection); err != nil {
			t.Fatal(err)
		}
		v := projection.Session
		if call.method == "session.list" {
			if len(projection.Sessions) != 1 {
				t.Fatal(projection)
			}
			v = projection.Sessions[0]
		}
		if v.Condition != "stopped" || v.PolicyCondition != "current" || v.AttachedCount != 0 || v.LatestUnattendedCondition == nil || v.LatestUnattendedCondition.Condition != "attention" {
			t.Fatalf("%s: %+v", call.method, v)
		}
	}
}

func TestSessionStreamPostCommitFailureAndFraming(t *testing.T) {
	s, id := statusFixture(t)
	server, client := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	called := make(chan struct{}, 1)
	go ServeSessionConn(context.Background(), server, s, id, func(context.Context, UnattendedCondition) error {
		called <- struct{}{}
		return errors.New("injected handler failure")
	})
	if _, err := client.Write(append(reportLine("attention"), '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"session.identity","params":{"v":1}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(line, &envelope); err != nil || envelope["result"] == nil {
		t.Fatalf("query after report: %s %v", line, err)
	}
	select {
	case <-called:
	default:
		t.Fatal("post-commit callback not called")
	}
	_, latest, err := s.SessionStatus(context.Background(), id)
	if err != nil || latest == nil || latest.Condition != "attention" {
		t.Fatalf("handler failure rolled back status: %+v %v", latest, err)
	}
	malformed, change := SessionReply(context.Background(), s, id, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})
	if change != nil || !strings.Contains(string(malformed), "parse_error") {
		t.Fatalf("invalid UTF-8 accepted: %s", malformed)
	}
}

func TestSessionStreamAttemptBudgetAndOversizeFrame(t *testing.T) {
	s, id := statusFixture(t)
	server, client := net.Pipe()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	go ServeSessionConn(context.Background(), server, s, id, nil)
	reader := bufio.NewReader(client)
	for i := 0; i < 100; i++ {
		if _, err := client.Write([]byte("{\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Write(append(reportLine("idle"), '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadBytes('\n'); err == nil {
		t.Fatal("over-budget notification received a reply")
	}
	client.Close()
	_, latest, err := s.SessionStatus(context.Background(), id)
	if err != nil || latest != nil {
		t.Fatalf("over-budget line changed state: %+v %v", latest, err)
	}
	server, client = net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	go ServeSessionConn(context.Background(), server, s, id, nil)
	go func() { _, _ = client.Write([]byte(strings.Repeat("x", MaxFrameBytes+1) + "\n")) }()
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), "parse_error") {
		t.Fatalf("oversize frame: %s %v", line, err)
	}
}
