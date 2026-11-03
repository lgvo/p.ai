package runtimeincus

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// createdRuntime creates the test session's runtime in w and returns its
// spec and incarnation.
func createdRuntime(t *testing.T, w *incusWorld) (runtimedriver.Session, runtimedriver.Incarnation) {
	t.Helper()
	s := testSessionSpec(t)
	o, err := w.driver().Create(context.Background(), s, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return s, o.Incarnation
}

func TestDeleteRefusesBeforeItsGate(t *testing.T) {
	for _, c := range []struct {
		name     string
		change   func(w *incusWorld, at *runtimedriver.Incarnation)
		mismatch bool
	}{
		{"without the runtime's incarnation", func(_ *incusWorld, at *runtimedriver.Incarnation) { *at = "" }, true},
		{"a runtime another P instance labeled at the session's name", func(w *incusWorld, _ *runtimedriver.Incarnation) {
			w.instance(testRuntime).config["user.p.instance_uuid"] = otherInstance
		}, true},
		{"a runtime renamed away from the session's name", func(w *incusWorld, _ *runtimedriver.Incarnation) {
			w.instance(testRuntime).name = "renamed-by-hand"
		}, true},
		{"a runtime neither running nor stopped", func(w *incusWorld, _ *runtimedriver.Incarnation) {
			w.instance(testRuntime).status = "Frozen"
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newIncusWorld(t)
			s, at := createdRuntime(t, w)
			w.mu.Lock()
			c.change(w, &at)
			w.mu.Unlock()
			before := w.mutations()
			err := w.driver().Delete(context.Background(), s.Ref, at, func() error { t.Error("gate called"); return nil })
			if err == nil || errors.Is(err, runtimedriver.ErrMismatch) != c.mismatch || !slices.Equal(w.mutations(), before) {
				t.Fatalf("Delete = %v (mismatch %t), effects %v", err, errors.Is(err, runtimedriver.ErrMismatch), w.mutations()[len(before):])
			}
		})
	}
}

func TestDeleteForceStopsARunningRuntimeAndDeletesItOnce(t *testing.T) {
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	if _, err := w.driver().Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	before := len(w.mutations())
	if err := w.driver().Delete(context.Background(), s.Ref, at, func() error { return nil }); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if effects := w.mutations()[before:]; !slices.Equal(effects, []string{"stop", "delete"}) || w.count("stop", testRuntime, "--force") != 1 {
		t.Fatalf("effects %v, forced stops %d; want one forced stop, then one delete", effects, w.count("stop", testRuntime, "--force"))
	}
	if w.instance(testRuntime) != nil {
		t.Fatal("the runtime remains")
	}
}

func TestDeleteWhoseReplyIsLostIsUnknownEvenWithTheRuntimeGone(t *testing.T) {
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	w.after = func(args []string) error {
		if args[0] == "delete" {
			w.mu.Lock()
			w.operations = append(w.operations, worldOperation{StatusCode: 103, Resources: map[string][]string{"instances": {"/1.0/instances/" + testRuntime + "?project=user-1000"}}})
			w.mu.Unlock()
			return errors.New("Incus connection lost")
		}
		return nil
	}
	err := w.driver().Delete(context.Background(), s.Ref, at, func() error { return nil })
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || w.instance(testRuntime) != nil {
		t.Fatalf("Delete = %v with the runtime gone and an Incus operation still on it; want an unknown outcome", err)
	}
}

func TestDeleteOfARuntimeThatDidNotStopIsUnknownAndSendsNoDelete(t *testing.T) {
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	if _, err := w.driver().Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w.before = func(args []string) error {
		if args[0] == "stop" {
			return errors.New("Incus connection lost")
		}
		return nil
	}
	err := w.driver().Delete(context.Background(), s.Ref, at, func() error { return nil })
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || w.count("delete") != 0 {
		t.Fatalf("Delete = %v after %d deletes; want an unknown outcome and none", err, w.count("delete"))
	}
}

// execOnTestRuntime is an Incus operation running on the test runtime, as
// `incus operation list` shows an exec.
var execOnTestRuntime = worldOperation{StatusCode: 103, Resources: map[string][]string{"instances": {"/1.0/instances/" + testRuntime + "?project=user-1000"}}}

// endOperationsAfter clears w's Incus operations after d, running change on
// the test runtime first when it is set.
func endOperationsAfter(w *incusWorld, d time.Duration, change func(*worldInstance)) {
	go func() {
		time.Sleep(d)
		w.mu.Lock()
		defer w.mu.Unlock()
		if change != nil {
			change(w.instance(testRuntime))
		}
		w.operations = nil
	}()
}

// An exec whose incus client was killed before it connected stays running
// in Incus for up to 10 s, whatever the runtime's state; a delete waits it
// out instead of refusing.
func TestDeleteWaitsForAnAbandonedExecToEnd(t *testing.T) {
	for _, running := range []bool{false, true} {
		w := newIncusWorld(t)
		s, at := createdRuntime(t, w)
		if running {
			if _, err := w.driver().Start(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}
		w.mu.Lock()
		w.operations = append(w.operations, execOnTestRuntime)
		w.mu.Unlock()
		endOperationsAfter(w, 2*time.Second, nil)
		var effectsDuringExec []string
		w.before = func(args []string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			if (args[0] == "stop" || args[0] == "delete") && len(w.operations) != 0 {
				effectsDuringExec = append(effectsDuringExec, args[0])
			}
			return nil
		}
		if err := w.driver().Delete(context.Background(), s.Ref, at, func() error { return nil }); err != nil {
			t.Fatalf("running %t: Delete refused an exec that ended within Incus's 10 s: %v", running, err)
		}
		if len(effectsDuringExec) != 0 || w.instance(testRuntime) != nil {
			t.Fatalf("running %t: %v sent while the exec ran, runtime left %t", running, effectsDuringExec, w.instance(testRuntime) != nil)
		}
	}
}

// The operation a delete waited for can have changed the runtime, so the
// delete acts only on the incarnation it finds after the wait.
func TestDeleteRefusesAnIncarnationReplacedWhileItWaited(t *testing.T) {
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	w.mu.Lock()
	w.operations = append(w.operations, execOnTestRuntime)
	w.mu.Unlock()
	endOperationsAfter(w, time.Second, func(in *worldInstance) { in.config["volatile.uuid.generation"] = w.newUUID() })
	before := w.mutations()
	err := w.driver().Delete(context.Background(), s.Ref, at, func() error { t.Error("gate called"); return nil })
	if !errors.Is(err, runtimedriver.ErrMismatch) || !slices.Equal(w.mutations(), before) {
		t.Fatalf("Delete = %v, effects %v; want a mismatch and none", err, w.mutations()[len(before):])
	}
}

func TestDeleteRefusesAnIncusOperationThatOutlastsItsWait(t *testing.T) {
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	w.mu.Lock()
	w.operations = append(w.operations, execOnTestRuntime)
	w.mu.Unlock()
	before := w.mutations()
	began := time.Now()
	err := w.driver().Delete(context.Background(), s.Ref, at, func() error { t.Error("gate called"); return nil })
	if err == nil || errors.Is(err, runtimedriver.ErrMismatch) || !slices.Equal(w.mutations(), before) {
		t.Fatalf("Delete = %v, effects %v; want a refusal and none", err, w.mutations()[len(before):])
	}
	if elapsed := time.Since(began); elapsed <= 10*time.Second || elapsed > 20*time.Second {
		t.Fatalf("refused after %s, not after a wait longer than Incus's 10 s", elapsed)
	}
}
