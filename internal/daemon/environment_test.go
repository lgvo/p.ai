package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type stagedPublisher struct {
	claim  runtimeincus.BuilderImageClaim
	before bool
	after  bool
	calls  int
}

func (p *stagedPublisher) PublishBuilderImageWithGate(_ context.Context, _ runtimeincus.Builder, _ runtimeincus.BuilderNixResult, gate func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
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
	fingerprint := strings.Repeat("a", 64)
	return nixenv.Handle{TargetKind: "incus-system-image", Locator: fingerprint}, runtimeincus.BuilderImageInfo{Fingerprint: fingerprint, Properties: p.claim.Properties}, nil
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
	if _, _, err := runDurableImagePublish(context.Background(), preparation, runtimeincus.Builder{}, runtimeincus.BuilderNixResult{}, validate, record, onAttempt); err == nil || marked != 0 || attempts != 0 {
		t.Fatalf("pre-publication failure recorded an attempt: marked=%d attempts=%d err=%v", marked, attempts, err)
	}
	preparation.before = false
	if _, _, err := runDurableImagePublish(context.Background(), preparation, runtimeincus.Builder{}, runtimeincus.BuilderNixResult{}, validate, record, onAttempt); err != nil || marked != 1 || attempts != 1 || preparation.calls != 2 {
		t.Fatalf("exact retry after preparation failure: marked=%d attempts=%d calls=%d err=%v", marked, attempts, preparation.calls, err)
	}
	unknown := &stagedPublisher{claim: claim, after: true}
	if _, _, err := runDurableImagePublish(context.Background(), unknown, runtimeincus.Builder{}, runtimeincus.BuilderNixResult{}, validate, record, onAttempt); err == nil || marked != 2 || attempts != 2 {
		t.Fatalf("unknown publication outcome lacked durable attempt: marked=%d attempts=%d err=%v", marked, attempts, err)
	}
	bad := &stagedPublisher{claim: runtimeincus.BuilderImageClaim{Key: "wrong"}}
	if _, _, err := runDurableImagePublish(context.Background(), bad, runtimeincus.Builder{}, runtimeincus.BuilderNixResult{}, validate, record, onAttempt); err == nil || marked != 2 || attempts != 2 {
		t.Fatalf("changed native claim reached publication: marked=%d attempts=%d err=%v", marked, attempts, err)
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

func TestNixStagesRunTwoAtATimeWithADeadline(t *testing.T) {
	release := make(chan struct{})
	started := make(chan time.Time, 3)
	done := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := runNixStage(context.Background(), func(ctx context.Context) (struct{}, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					return struct{}{}, errors.New("stage has no deadline")
				}
				started <- deadline
				<-release
				return struct{}{}, nil
			})
			done <- err
		}()
	}
	for range 2 {
		select {
		case deadline := <-started:
			if time.Until(deadline) > nixStageDeadline {
				t.Fatalf("stage deadline %s is later than %s", deadline, nixStageDeadline)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("two stages did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("a third stage ran while two held the slots")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
