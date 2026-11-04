package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

func TestCreationRefusesByProjectPolicy(t *testing.T) {
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	b := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/bash"}}
	raw, _, err := control.ProjectPolicySnapshot(a)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "team/a", raw); err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{ctx: ctx, store: store, cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{"team/a": a}}}
	if err = l.currentProjectPolicy(ctx, "team/a"); err != nil {
		t.Fatalf("current policy refused: %v", err)
	}
	wantRefusal(t, "unknown project", l.currentProjectPolicy(ctx, "team/missing"), api.KindNotFound, api.ReasonProject, "project/team/missing")
	_, err = l.CreateSession(ctx, control.ReserveSessionRequest{Key: "s", Project: "team/missing", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	wantRefusal(t, "session of an unknown project", err, api.KindNotFound, api.ReasonProject, "project/team/missing")
	_, err = l.CreateProject(ctx, control.BlankProjectRequest{Key: "p", Project: "team/new"})
	wantRefusal(t, "project without a configured policy", err, api.KindNotConfigured, api.ReasonProjectPolicy, "project/team/new")

	l.cfg.ProjectPolicies["team/a"] = b
	wantRefusal(t, "changed policy", l.currentProjectPolicy(ctx, "team/a"), api.KindConflict, api.ReasonPolicyInvalid, "project/team/a")
	delete(l.cfg.ProjectPolicies, "team/a")
	wantRefusal(t, "policy no longer configured", l.currentProjectPolicy(ctx, "team/a"), api.KindNotConfigured, api.ReasonProjectPolicy, "project/team/a")
	_, err = l.CreateSession(ctx, control.ReserveSessionRequest{Key: "s", Project: "team/a", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	wantRefusal(t, "session without a configured policy", err, api.KindNotConfigured, api.ReasonProjectPolicy, "project/team/a")
}
