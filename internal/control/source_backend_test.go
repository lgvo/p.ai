package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAnotherSourceBackendIsRefusedWhileAProjectExists(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.PinSourceBackend(ctx, "git"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PinSourceBackend(ctx, "forge"); !errors.Is(err, ErrConflict) {
		t.Fatalf("another backend over a project answered %v", err)
	}
	if err := store.PinSourceBackend(ctx, "git"); err != nil {
		t.Fatalf("the pinned backend was refused after another was: %v", err)
	}
}

func TestTheSameSourceBackendStartsAgainOverItsProjects(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.PinSourceBackend(ctx, "git"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PinSourceBackend(ctx, "git"); err != nil {
		t.Fatalf("the same backend was refused over its projects: %v", err)
	}
}

func TestAnotherSourceBackendReplacesThePinWhileNoProjectExists(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.PinSourceBackend(ctx, "forge"); err != nil {
		t.Fatal(err)
	}
	if err := store.PinSourceBackend(ctx, "git"); err != nil {
		t.Fatalf("another backend was refused with no project: %v", err)
	}
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PinSourceBackend(ctx, "forge"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the earlier backend was still pinned: %v", err)
	}
}
