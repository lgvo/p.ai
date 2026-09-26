package runtimeincus

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestRepairImageObservationSeparatesMissingFromUnavailable(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	present, err := b.ImagePresent(context.Background(), testImage)
	if err != nil || !present {
		t.Fatalf("exact image not observed: %t %v", present, err)
	}
	missing := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	present, err = b.ImagePresent(context.Background(), missing)
	if err != nil || present {
		t.Fatalf("missing image not distinguished: %t %v", present, err)
	}
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if slices.Contains(argv, "image") {
			return nil, errors.New("daemon unreachable")
		}
		return f.run(ctx, binary, argv, env)
	}
	if present, err = b.ImagePresent(context.Background(), testImage); err == nil || present {
		t.Fatalf("unreachable image treated as missing: %t %v", present, err)
	}
}

func TestRepairNativeInitGateAndExactPresentReplay(t *testing.T) {
	f := &fakeIncus{failCreateAfterEffect: true}
	b := fakeBackend(f)
	called := 0
	got, err := b.CreateWithGate(context.Background(), testSession(), func() error { called++; return nil })
	if err != nil || !got.Exists || called != 1 || len(f.mutations) != 1 || f.mutations[0] != "init" {
		t.Fatalf("first exact init: %+v %v marker=%d mutations=%v", got, err, called, f.mutations)
	}
	got, err = b.CreateWithGate(context.Background(), testSession(), func() error { return errors.New("second init forbidden") })
	if err != nil || !got.Exists || len(f.mutations) != 1 {
		t.Fatalf("present exact replay issued second init: %+v %v mutations=%v", got, err, f.mutations)
	}
	f = &fakeIncus{}
	b = fakeBackend(f)
	if _, err = b.CreateWithGate(context.Background(), testSession(), func() error { return errors.New("durable marker refused") }); err == nil || len(f.mutations) != 0 {
		t.Fatalf("pre-init marker refusal still mutated: %v %v", err, f.mutations)
	}
}
