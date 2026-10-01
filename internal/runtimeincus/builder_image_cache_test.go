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

func TestBuilderImageCacheClaimFreshVerification(t *testing.T) {
	r, result := imageResultFixture(t)
	instance := "11111111-1111-4111-8111-111111111111"
	labels := builderImageProperties(r, result, instance, "user-1000")
	base := builderImageJSON{Fingerprint: r.BaseImageFingerprint, Type: "container", Properties: map[string]string{"os": "NixOS"}}
	properties, err := expectedBuilderImageProperties([]builderImageJSON{base}, r.BaseImageFingerprint, labels)
	if err != nil {
		t.Fatal(err)
	}
	claim := BuilderImageClaim{Fingerprint: strings.Repeat("f", 64), ProjectPath: r.ProjectPath,
		Key: result.Selection.KeyDigest, BaseFingerprint: r.BaseImageFingerprint, System: result.Selection.System,
		MaterialDigest: result.MaterialDigest, CaptureStorePath: result.CaptureStorePath,
		BuilderRequest: r.RequestUUID, Properties: properties}
	image := builderImageJSON{Fingerprint: claim.Fingerprint, Type: "container", Architecture: "x86_64", Project: "user-1000", Size: 1024, Properties: maps.Clone(properties)}
	images := []builderImageJSON{base, image}
	pending := false
	fake := &fakeIncus{}
	b := &Backend{config: Config{Project: "user-1000", UserSocket: "/var/lib/incus/unix.socket.user", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"}, PInstanceID: instance}}
	b.validate = func(Config) error { return nil }
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) < 4 || !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || !slices.Contains(env, "INCUS_SOCKET=/var/lib/incus/unix.socket.user") {
			return nil, errors.New("unconfined image inventory")
		}
		if argv[3] == "image" {
			return json.Marshal(images)
		}
		if argv[3] == "operation" {
			if !slices.Equal(argv[3:], []string{"operation", "list", "--format", "json"}) {
				return nil, errors.New("unexpected operation query")
			}
			if pending {
				return []byte(`[{"description":"Downloading image","status":"Running"}]`), nil
			}
			return []byte(`[]`), nil
		}
		return fake.run(ctx, binary, argv, env)
	}
	if _, found, err := b.VerifyBuilderImage(context.Background(), claim); err != nil || !found {
		t.Fatalf("verified cache hit: found=%v err=%v", found, err)
	}
	reconcile := claim
	reconcile.Fingerprint = ""
	if got, err := b.ReconcileBuilderPublication(context.Background(), reconcile); err != nil || got.Fingerprint != claim.Fingerprint {
		t.Fatalf("exact prior image reconciliation: %+v %v", got, err)
	}
	images = []builderImageJSON{base}
	if _, found, err := b.VerifyBuilderImage(context.Background(), claim); err != nil || found {
		t.Fatalf("externally removed image should miss: found=%v err=%v", found, err)
	}
	pending = true
	if _, err := b.ReconcileBuilderPublication(context.Background(), reconcile); err == nil || !strings.Contains(err.Error(), "1 active image operations") {
		t.Fatalf("uncertain publication retried past active Incus operation: %v", err)
	}
	pending = false
	if _, err := b.ReconcileBuilderPublication(context.Background(), reconcile); err == nil {
		t.Fatal("blindly retried an image absent after uncertain publish")
	}
	images = []builderImageJSON{base, image}
	claim.ProjectPath = "other/project"
	if _, _, err := b.VerifyBuilderImage(context.Background(), claim); err == nil {
		t.Fatal("reused image across P projects")
	}
	claim.ProjectPath = r.ProjectPath
	images[1].Properties = maps.Clone(properties)
	images[1].Properties["os"] = "forged"
	if _, _, err := b.VerifyBuilderImage(context.Background(), claim); err == nil {
		t.Fatal("accepted changed inherited metadata")
	}
}
