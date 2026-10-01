package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

type sessionAdmissionFixture struct {
	m        *endpointManager
	ctx      context.Context
	sessions []control.Session
}

func newSessionAdmissionFixture(t *testing.T) *sessionAdmissionFixture {
	t.Helper()
	prefix, err := os.MkdirTemp("/tmp", "p-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	store, err := control.OpenStore(filepath.Join(prefix, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	m, err := newEndpointManager(filepath.Join(prefix, "endpoints"), "127.0.0.1:1", store)
	if err != nil {
		t.Fatal(err)
	}
	f := &sessionAdmissionFixture{m: m, ctx: ctx}
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	for i := 0; i < 5; i++ {
		op, s, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: fmt.Sprintf("session-%d", i), Project: "app", Branch: fmt.Sprintf("work-%d", i), Choice: "existing"}, strings.Repeat("d", 64), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.CompleteCreation(ctx, op.ID); err != nil {
			t.Fatal(err)
		}
		f.sessions = append(f.sessions, s)
	}
	t.Cleanup(func() { cancel(); m.Close(); sessionAdmissionSlots(t, m, 0); store.Close(); os.RemoveAll(prefix) })
	return f
}
func (f *sessionAdmissionFixture) client(t *testing.T, session int) net.Conn {
	t.Helper()
	dir, err := f.m.Ensure(f.ctx, f.sessions[session].UUID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", filepath.Join(dir, "session.sock"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}
func sessionAdmissionSlots(t *testing.T, m *endpointManager, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(m.sessionSlots) != want {
		if time.Now().After(deadline) {
			t.Fatalf("RPC slots=%d, want%d", len(m.sessionSlots), want)
		}
		time.Sleep(time.Millisecond)
	}
}
func sessionAdmissionQuery(t *testing.T, c net.Conn, method string, s control.Session) {
	t.Helper()
	if _, err := fmt.Fprintf(c, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":%q,\"params\":{\"v\":1}}\n", method); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			UUID   string `json:"uuid"`
			Branch string `json:"branch"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err = json.Unmarshal(line, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Error) != 0 || reply.Result.UUID != s.UUID || reply.Result.Branch != s.Branch {
		t.Fatalf("incorrect bound RPC result: %s", line)
	}
}
func TestSessionRPCAdmissionPreservesSiblingAndPersistentQueries(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	var idle []net.Conn
	for i := 0; i < rpcConnectionsPerSession; i++ {
		idle = append(idle, f.client(t, 0))
	}
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession)
	rejected := f.client(t, 0)
	if data, err := io.ReadAll(rejected); err != nil || len(data) != 0 {
		t.Fatalf("excess connection not rejected: %q %v", data, err)
	}
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession)
	sibling := f.client(t, 1)
	sessionAdmissionQuery(t, sibling, "session.identity", f.sessions[1])
	sessionAdmissionQuery(t, sibling, "session.capabilities", f.sessions[1])
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession+1)
	idle[0].Close()
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession)
	replacement := f.client(t, 0)
	sessionAdmissionQuery(t, replacement, "session.identity", f.sessions[0])
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession+1)
	// Initial idle admissions consume no status/frame budget; permitted queries
	// on this same connection remain subject to the existing handler budgets.
	sessionAdmissionQuery(t, replacement, "session.capabilities", f.sessions[0])
	f.m.Close()
	sessionAdmissionSlots(t, f.m, 0)
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	if len(f.m.sessionByUUID) != 0 || len(f.m.conns) != 0 {
		t.Fatalf("shutdown leaked accounting: UUIDs%d conns%d", len(f.m.sessionByUUID), len(f.m.conns))
	}
}
func TestSessionRPCAdmissionSurvivesListenerRecreation(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	var idle []net.Conn
	for i := 0; i < rpcConnectionsPerSession; i++ {
		idle = append(idle, f.client(t, 0))
	}
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession)
	if err := f.m.RemoveSession(f.sessions[0].UUID); err != nil {
		t.Fatal(err)
	}
	rejected := f.client(t, 0)
	if data, err := io.ReadAll(rejected); err != nil || len(data) != 0 {
		t.Fatalf("new listener bypassed UUID cap: %q %v", data, err)
	}
	sessionAdmissionQuery(t, idle[0], "session.identity", f.sessions[0])
	idle[0].Close()
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession-1)
	c := f.client(t, 0)
	sessionAdmissionQuery(t, c, "session.identity", f.sessions[0])
	sessionAdmissionSlots(t, f.m, rpcConnectionsPerSession)
}
func TestSessionRPCAdmissionKeepsGlobalCeiling(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	var idle []net.Conn
	for session := 0; session < 4; session++ {
		for i := 0; i < rpcConnectionsPerSession; i++ {
			idle = append(idle, f.client(t, session))
		}
	}
	sessionAdmissionSlots(t, f.m, 16)
	rejected := f.client(t, 4)
	if data, err := io.ReadAll(rejected); err != nil || len(data) != 0 {
		t.Fatalf("global overflow not closed: %q %v", data, err)
	}
	sessionAdmissionSlots(t, f.m, 16)
	idle[0].Close()
	sessionAdmissionSlots(t, f.m, 15)
	c := f.client(t, 4)
	sessionAdmissionQuery(t, c, "session.identity", f.sessions[4])
	sessionAdmissionSlots(t, f.m, 16)
	f.m.Close()
	sessionAdmissionSlots(t, f.m, 0)
}
