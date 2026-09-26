package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestRecoverBlockedSourceReadyDoesNotManufactureEndpoint(t *testing.T) {
	prefix, e := os.MkdirTemp("/tmp", "p-recover-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(prefix)
	manager, e := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Close()
	session := control.Session{UUID: "550e8400-e29b-41d4-a716-446655440000", Project: "app", Registry: "creating"}
	op := control.Operation{Kind: "session.create", SessionUUID: session.UUID, Project: session.Project, Status: "blocked", Phase: "source-ready"}
	if e = recoverSessionEndpoint(context.Background(), session, func(context.Context, string) (control.Operation, error) { return op, nil }, manager.Ensure); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(filepath.Join(prefix, session.UUID)); !os.IsNotExist(e) {
		t.Fatalf("restart manufactured an endpoint for a no-effect blocked creation: %v", e)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.opened[session.UUID] != nil {
		t.Fatal("restart opened a no-effect blocked creation endpoint")
	}
}

func TestEndpointRecoveryRequiresRecognizedDurableCreationProgress(t *testing.T) {
	ctx := context.Background()
	session := control.Session{UUID: "550e8400-e29b-41d4-a716-446655440000", Project: "app", Registry: "creating", PolicySHA256: strings.Repeat("d", 64)}
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	evidence, _ := json.Marshal(control.CreationEvidence{ImageFingerprint: strings.Repeat("e", 64), PolicySHA256: session.PolicySHA256, Selection: selection})
	base := control.Operation{Kind: "session.create", Project: session.Project, SessionUUID: session.UUID, Status: "blocked", Phase: "principals-ready", Committed: true, Evidence: evidence}
	for _, tc := range []struct {
		name, registry, phase string
		mutate                func(*control.Operation)
		loadErr               error
		want                  bool
	}{
		{name: "blocked-source", phase: "source-ready"},
		{name: "assigned", phase: "branch-assigned"},
		{name: "builder-publishing", phase: "environment-publishing"},
		{name: "builder-ready", phase: "environment-ready"},
		{name: "principals", phase: "principals-ready", want: true},
		{name: "runtime", phase: "runtime-created", want: true},
		{name: "assembly", phase: "assembly-ready", want: true},
		{name: "workspace", phase: "workspace-ready", want: true},
		{name: "established-checkpoint", phase: "established", want: true},
		{name: "future-phase", phase: "future-phase"},
		{name: "established-session", registry: "established", loadErr: errors.New("must not load"), want: true},
		{name: "removing", registry: "removing", loadErr: errors.New("must not load")},
		{name: "missing-creation", phase: "principals-ready", loadErr: control.ErrNotFound},
		{name: "wrong-kind", phase: "principals-ready", mutate: func(op *control.Operation) { op.Kind = "session.repair" }},
		{name: "wrong-session", phase: "principals-ready", mutate: func(op *control.Operation) { op.SessionUUID = "foreign" }},
		{name: "wrong-project", phase: "principals-ready", mutate: func(op *control.Operation) { op.Project = "foreign" }},
		{name: "uncommitted", phase: "principals-ready", mutate: func(op *control.Operation) { op.Committed = false }},
		{name: "terminal", phase: "principals-ready", mutate: func(op *control.Operation) { op.Status = "superseded" }},
		{name: "missing-evidence", phase: "principals-ready", mutate: func(op *control.Operation) { op.Evidence = nil }},
		{name: "foreign-policy", phase: "principals-ready", mutate: func(op *control.Operation) {
			ev := control.CreationEvidence{ImageFingerprint: strings.Repeat("e", 64), PolicySHA256: strings.Repeat("f", 64), Selection: selection}
			op.Evidence, _ = json.Marshal(ev)
		}},
		{name: "blank-bootstrap", phase: "principals-ready", mutate: func(op *control.Operation) { op.Kind = "project.create" }, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := base
			op.Phase = tc.phase
			if tc.mutate != nil {
				tc.mutate(&op)
			}
			s := session
			if tc.registry != "" {
				s.Registry = tc.registry
			}
			calls := 0
			e := recoverSessionEndpoint(ctx, s, func(context.Context, string) (control.Operation, error) { return op, tc.loadErr }, func(context.Context, string) (string, error) { calls++; return "", nil })
			if e != nil || (calls == 1) != tc.want {
				t.Fatalf("ensure calls=%d want=%v error=%v", calls, tc.want, e)
			}
		})
	}
}

func TestEndpointRecoveryPreservesEarlyArtifactsAndReopensLateSockets(t *testing.T) {
	prefix, e := os.MkdirTemp("/tmp", "p-recover-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(prefix)
	ctx := context.Background()
	early := control.Session{UUID: "550e8400-e29b-41d4-a716-446655440000", Project: "app", Registry: "creating"}
	unexpected := filepath.Join(prefix, early.UUID)
	if e = os.Mkdir(unexpected, 0755); e != nil {
		t.Fatal(e)
	}
	sentinel := filepath.Join(unexpected, "unexpected-private")
	if e = os.WriteFile(sentinel, []byte("preserved"), 0600); e != nil {
		t.Fatal(e)
	}
	manager, e := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if e != nil {
		t.Fatal(e)
	}
	established := control.Session{UUID: "11111111-1111-4111-8111-111111111111", Project: "app", Registry: "established"}
	if _, e = manager.Ensure(ctx, established.UUID); e != nil {
		t.Fatal(e)
	}

	late := control.Session{UUID: "22222222-2222-4222-8222-222222222222", Project: "app", Registry: "creating", PolicySHA256: strings.Repeat("d", 64)}
	if _, e = manager.Ensure(ctx, late.UUID); e != nil {
		t.Fatal(e)
	}
	manager.Close()
	restarted, e := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	if e = recoverSessionEndpoint(ctx, early, func(context.Context, string) (control.Operation, error) {
		return control.Operation{Kind: "session.create", Project: early.Project, SessionUUID: early.UUID, Status: "blocked", Phase: "branch-assigned", Committed: true}, nil
	}, restarted.Ensure); e != nil {
		t.Fatal(e)
	}
	if got, e := os.ReadFile(sentinel); e != nil || string(got) != "preserved" {
		t.Fatalf("early artifact changed: %q %v", got, e)
	}
	if restarted.opened[early.UUID] != nil {
		t.Fatal("unexpected early endpoint adopted")
	}
	if e = recoverSessionEndpoint(ctx, established, func(context.Context, string) (control.Operation, error) {
		t.Fatal("established registry requires no creation lookup")
		return control.Operation{}, nil
	}, restarted.Ensure); e != nil {
		t.Fatal(e)
	}
	if restarted.opened[established.UUID] == nil {
		t.Fatal("established endpoint not reopened")
	}

	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	evidence, _ := json.Marshal(control.CreationEvidence{ImageFingerprint: strings.Repeat("e", 64), PolicySHA256: late.PolicySHA256, Selection: selection})
	if e = recoverSessionEndpoint(ctx, late, func(context.Context, string) (control.Operation, error) {
		return control.Operation{Kind: "session.create", Project: late.Project, SessionUUID: late.UUID, Status: "blocked", Phase: "runtime-created", Committed: true, Evidence: evidence}, nil
	}, restarted.Ensure); e != nil {
		t.Fatal(e)
	}
	if restarted.opened[late.UUID] == nil {
		t.Fatal("late creating runtime endpoint not reopened")
	}
	for _, id := range []string{established.UUID, late.UUID} {
		for _, name := range []string{"git.sock", "session.sock"} {
			info, e := os.Lstat(filepath.Join(prefix, id, name))
			if e != nil || info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("recovered socket %s/%s: %v", id, name, e)
			}
		}
	}
}
