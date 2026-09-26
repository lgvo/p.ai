package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type stagedPublisher struct {
	claim  runtimeincus.BuilderImageClaim
	before bool
	after  bool
	calls  int
}

func (p *stagedPublisher) PublishPrivateImageWithGate(_ context.Context, gate func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	p.calls++
	if p.before {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("hook failed before Incus publication")
	}
	if err := gate(p.claim); err != nil {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, err
	}
	if p.after {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("Incus publication outcome unknown")
	}
	return nixenv.Handle{}, runtimeincus.BuilderImageInfo{Fingerprint: strings.Repeat("a", 64), Properties: p.claim.Properties}, nil
}

func TestDurablePublishGateDistinguishesPreparationAndUnknownOutcome(t *testing.T) {
	claim := runtimeincus.BuilderImageClaim{Key: strings.Repeat("b", 64), Properties: map[string]string{"p.environment_key": strings.Repeat("b", 64)}}
	marked, attempts := 0, 0
	validate := func(got runtimeincus.BuilderImageClaim) error {
		if got.Key != claim.Key {
			return errors.New("claim changed")
		}
		return nil
	}
	record := func(runtimeincus.BuilderImageClaim) error { marked++; return nil }
	onAttempt := func() { attempts++ }
	preparation := &stagedPublisher{claim: claim, before: true}
	if _, _, err := runDurableImagePublish(context.Background(), preparation, validate, record, onAttempt); err == nil || marked != 0 || attempts != 0 {
		t.Fatalf("pre-publication failure recorded an attempt: marked=%d attempts=%d err=%v", marked, attempts, err)
	}
	preparation.before = false
	if _, _, err := runDurableImagePublish(context.Background(), preparation, validate, record, onAttempt); err != nil || marked != 1 || attempts != 1 || preparation.calls != 2 {
		t.Fatalf("exact retry after preparation failure: marked=%d attempts=%d calls=%d err=%v", marked, attempts, preparation.calls, err)
	}
	unknown := &stagedPublisher{claim: claim, after: true}
	if _, _, err := runDurableImagePublish(context.Background(), unknown, validate, record, onAttempt); err == nil || marked != 2 || attempts != 2 {
		t.Fatalf("unknown publication outcome lacked durable attempt: marked=%d attempts=%d err=%v", marked, attempts, err)
	}
	bad := &stagedPublisher{claim: runtimeincus.BuilderImageClaim{Key: "wrong"}}
	if _, _, err := runDurableImagePublish(context.Background(), bad, validate, record, onAttempt); err == nil || marked != 2 || attempts != 2 {
		t.Fatalf("changed native claim reached publication: marked=%d attempts=%d err=%v", marked, attempts, err)
	}
}
