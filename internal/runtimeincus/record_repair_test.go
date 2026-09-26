package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestRecordRepairRequiresCompleteProjectAbsence(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	s := testSession()
	if err := b.ConfirmSessionRuntimeAbsent(context.Background(), s); err != nil {
		t.Fatalf("exact project absence: %v", err)
	}
	f.instance, f.status = true, "Stopped"
	if err := b.ConfirmSessionRuntimeAbsent(context.Background(), s); err == nil {
		t.Fatal("expected runtime was treated as absent")
	}
	f.instance = false
	base := f.run
	for _, peer := range []instanceJSON{
		{Name: "p-renamed", Type: "container", Status: "Stopped", Config: map[string]string{"user.p.session_uuid": s.SessionUUID}, ExpandedConfig: map[string]string{}},
		{Name: "p-foreign", Type: "container", Status: "Running", Config: map[string]string{}, ExpandedConfig: map[string]string{"user.p.session_uuid": s.SessionUUID}},
		{Name: "p-foreign", Type: "container", Status: "Unknown", Config: map[string]string{}, ExpandedConfig: map[string]string{}},
		{Name: "p-foreign", Type: "container", Status: "Stopped", Config: nil, ExpandedConfig: map[string]string{}},
	} {
		peer := peer
		b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
			if slices.Contains(argv, "list") && !slices.Contains(argv, "^p-"+s.SessionUUID+"$") {
				return json.Marshal([]instanceJSON{peer})
			}
			return base(ctx, binary, argv, env)
		}
		if err := b.ConfirmSessionRuntimeAbsent(context.Background(), s); err == nil {
			t.Fatalf("competing or opaque project instance accepted: %+v", peer)
		}
	}
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if slices.Contains(argv, "list") {
			return nil, errors.New("unreachable")
		}
		return base(ctx, binary, argv, env)
	}
	if err := b.ConfirmSessionRuntimeAbsent(context.Background(), s); err == nil {
		t.Fatal("unreachable native inventory was treated as absent")
	}
}
