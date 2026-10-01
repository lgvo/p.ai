package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

func TestCollectionPreflightRejectsChangedFactsBeforeDurableIntent(t *testing.T) {
	entry := control.EnvironmentImage{Project: "team/a", Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		CreatedAt: "2026-09-24T00:00:00Z", LastUsedAt: "2026-09-24T01:00:00Z", LogicalSize: 1024,
		Properties: map[string]string{"os": "NixOS"}}
	claim := control.EnvironmentCollectionClaim{Image: entry, ImagePresent: true, ObservedSize: 1024, RelatedDigest: strings.Repeat("c", 64)}
	state := collectionPreviewState{Claim: claim, ExpiresAt: time.Now().Add(time.Minute)}
	beginCalls := 0
	begin := func(context.Context, string, control.EnvironmentCollectionClaim) (control.Operation, error) {
		beginCalls++
		return control.Operation{ID: "accepted"}, nil
	}
	for _, tc := range []struct {
		name     string
		load     control.EnvironmentImage
		found    bool
		observed control.EnvironmentCollectionClaim
	}{
		{"last accepted use advanced", func() control.EnvironmentImage { e := entry; e.LastUsedAt = "2026-09-24T02:00:00Z"; return e }(), true, claim},
		{"cache row absent", entry, false, claim},
		{"image disappeared", entry, true, func() control.EnvironmentCollectionClaim { c := claim; c.ImagePresent = false; return c }()},
		{"image size changed", entry, true, func() control.EnvironmentCollectionClaim { c := claim; c.ObservedSize++; return c }()},
		{"related sessions changed", entry, true, func() control.EnvironmentCollectionClaim {
			c := claim
			c.RelatedDigest = strings.Repeat("d", 64)
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := confirmCollectionPreflight(context.Background(), "collect", state,
				func(context.Context, string, string) (control.EnvironmentImage, bool, error) {
					return tc.load, tc.found, nil
				},
				func(context.Context, control.EnvironmentImage) (control.EnvironmentCollectionClaim, int, error) {
					return tc.observed, 0, nil
				}, begin)
			if !errors.Is(err, control.ErrConflict) || beginCalls != 0 {
				t.Fatalf("stale preview committed: calls=%d err=%v", beginCalls, err)
			}
		})
	}
	op, err := confirmCollectionPreflight(context.Background(), "collect", state,
		func(context.Context, string, string) (control.EnvironmentImage, bool, error) { return entry, true, nil },
		func(context.Context, control.EnvironmentImage) (control.EnvironmentCollectionClaim, int, error) {
			return claim, 0, nil
		}, begin)
	if err != nil || op.ID != "accepted" || beginCalls != 1 {
		t.Fatalf("unchanged reviewed claim refused: calls=%d op=%+v err=%v", beginCalls, op, err)
	}
}

func TestSameEnvironmentKeySerializesAcceptanceWithoutCrossKeyBlocking(t *testing.T) {
	l := &lifecycle{}
	first, err := l.lockEnvironmentKey(context.Background(), "team/a", "key-one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := l.lockEnvironmentKey(context.Background(), "team/a", "key-two")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := l.lockEnvironmentKey(ctx, "team/a", "key-one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-key acceptance overlapped: %v", err)
	}
	first()
	second, err := l.lockEnvironmentKey(context.Background(), "team/a", "key-one")
	if err != nil {
		t.Fatal(err)
	}
	second()
}
