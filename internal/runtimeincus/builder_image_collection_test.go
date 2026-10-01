package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestOwnedImageCollectionWithoutBaseAndExactDelete(t *testing.T) {
	r, result := imageResultFixture(t)
	instance := "11111111-1111-4111-8111-111111111111"
	claim := BuilderImageClaim{
		Fingerprint: strings.Repeat("f", 64), ProjectPath: r.ProjectPath, Key: result.Selection.KeyDigest,
		BaseFingerprint: r.BaseImageFingerprint, System: result.Selection.System,
		MaterialDigest: result.MaterialDigest, CaptureStorePath: result.CaptureStorePath,
		BuilderRequest: r.RequestUUID, Properties: builderImageProperties(r, result, instance, "user-1000"),
	}
	image := builderImageJSON{Fingerprint: claim.Fingerprint, Type: "container", Architecture: "x86_64", Project: "user-1000", Size: 4096, Properties: maps.Clone(claim.Properties)}
	images := []builderImageJSON{image} // pinned base was externally removed
	deletes := 0
	fake := &fakeIncus{}
	b := &Backend{config: Config{Project: "user-1000", UserSocket: "/var/lib/incus/unix.socket.user", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"}, PInstanceID: instance}}
	b.validate = func(Config) error { return nil }
	b.run = func(_ context.Context, _ string, argv, env []string) ([]byte, error) {
		if len(argv) < 5 || !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || !slices.Contains(env, "INCUS_SOCKET=/var/lib/incus/unix.socket.user") {
			return nil, errors.New("unconfined collection command")
		}
		switch {
		case slices.Equal(argv[3:], []string{"image", "list", "--format", "json"}):
			return json.Marshal(images)
		case slices.Equal(argv[3:], []string{"image", "delete", claim.Fingerprint}):
			deletes++
			images = nil
			return nil, errors.New("client disconnected after daemon deleted image")
		default:
			return fake.run(context.Background(), "", argv, env)
		}
	}
	info, found, err := b.ObserveOwnedBuilderImage(context.Background(), claim)
	if err != nil || !found || info.Size != 4096 {
		t.Fatalf("base-independent exact observation: %+v %v %v", info, found, err)
	}
	if err := b.DeleteOwnedBuilderImage(context.Background(), claim); err != nil || deletes != 1 {
		t.Fatalf("unknown client reply failed to reconcile exact absence: deletes=%d err=%v", deletes, err)
	}
	if err := b.DeleteOwnedBuilderImage(context.Background(), claim); err != nil || deletes != 1 {
		t.Fatalf("ensure-absent retry sent another deletion: deletes=%d err=%v", deletes, err)
	}
	images = []builderImageJSON{image}
	images[0].Properties = maps.Clone(claim.Properties)
	images[0].Properties["os"] = "tampered"
	if err := b.DeleteOwnedBuilderImage(context.Background(), claim); err == nil || deletes != 1 {
		t.Fatalf("tampered target was deleted: deletes=%d err=%v", deletes, err)
	}
	images[0].Properties = maps.Clone(claim.Properties)
	images[0].Public = true
	if err := b.DeleteOwnedBuilderImage(context.Background(), claim); err == nil || deletes != 1 {
		t.Fatalf("public target was deleted: deletes=%d err=%v", deletes, err)
	}
	images[0].Public = false
	claim.Properties["p.instance"] = "22222222-2222-4222-8222-222222222222"
	if err := b.DeleteOwnedBuilderImage(context.Background(), claim); err == nil || deletes != 1 {
		t.Fatalf("foreign P instance target was deleted: deletes=%d err=%v", deletes, err)
	}
}
