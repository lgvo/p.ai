package incus

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var testTerminalSize = runtimedriver.Size{Width: 80, Height: 24}

// attachedWorld is a running session with a terminal open on it, and the
// attach program's side of it.
func attachedWorld(t *testing.T) (*incusWorld, runtimedriver.Target, runtimedriver.Terminal, *worldGuest) {
	t.Helper()
	w := newIncusWorld(t)
	w.withSessionHostLinks()
	target := runningSession(t, w, nil)
	term, err := w.driver().OpenTerminal(context.Background(), target, testTerminalSize)
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { term.Close() })
	return w, target, term, w.guest(term.ID())
}

func endedWithin(t *testing.T, term runtimedriver.Terminal) runtimedriver.Completion {
	t.Helper()
	select {
	case c := <-term.Ended():
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal did not end")
	}
	return runtimedriver.Completion{}
}

func execCount(w *incusWorld) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.execs)
}

func TestOpenTerminalRefusesAnUnreadyHostAndChangedHostAssets(t *testing.T) {
	setFile := func(path string, change func(*guestFile)) func(*incusWorld, *runtimedriver.Session) {
		return func(w *incusWorld, _ *runtimedriver.Session) {
			w.change(testRuntime, func(in *worldInstance) {
				f := in.files[path]
				change(&f)
				in.files[path] = f
			})
		}
	}
	for _, c := range []struct {
		name     string
		change   func(*incusWorld, *runtimedriver.Session)
		notReady bool
	}{
		{"nothing changed", func(*incusWorld, *runtimedriver.Session) {}, false},
		{"the session host is not ready", func(w *incusWorld, _ *runtimedriver.Session) {
			w.change(testRuntime, func(in *worldInstance) { in.host = "activating/start-post/success" })
		}, true},
		{"the attach program's bytes changed", setFile("/etc/p/assets/p-attach", func(f *guestFile) { f.data = []byte("other\n") }), false},
		{"the host unit's mode changed", setFile("/etc/p/assets/p-interactive.service", func(f *guestFile) { f.mode = 0600 }), false},
		{"the attach program belongs to the session user", setFile("/etc/p/assets/p-attach", func(f *guestFile) { f.uid = 1000 }), false},
		{"the attach link leads to another asset", setFile(attachProgram, func(f *guestFile) { f.data = []byte("/etc/p/assets/p-git-ssh") }), false},
		{"the session user can write the asset directory", setFile("/etc/p/assets", func(f *guestFile) { f.mode = 0777 }), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newIncusWorld(t)
			w.withSessionHostLinks()
			target := runningSession(t, w, nil)
			c.change(w, &target.Session)
			term, err := w.driver().OpenTerminal(context.Background(), target, testTerminalSize)
			if c.name == "nothing changed" {
				if err != nil {
					t.Fatalf("OpenTerminal: %v", err)
				}
				term.Close()
				return
			}
			if err == nil {
				term.Close()
				t.Fatal("OpenTerminal succeeded")
			}
			if errors.Is(err, runtimedriver.ErrNotReady) != c.notReady || errors.Is(err, runtimedriver.ErrUnknownOutcome) {
				t.Fatalf("OpenTerminal: %v, want not ready %t and a definite refusal", err, c.notReady)
			}
			if n := execCount(w); n != 0 {
				t.Fatalf("a refused terminal created %d execs", n)
			}
		})
	}
}

func TestOpenTerminalRefusesASizeOutsideAPTYsBounds(t *testing.T) {
	w := newIncusWorld(t)
	w.withSessionHostLinks()
	target := runningSession(t, w, nil)
	for _, size := range []runtimedriver.Size{{Width: 0, Height: 24}, {Width: 80, Height: 0}, {Width: 65536, Height: 24}, {Width: 80, Height: 65536}} {
		if term, err := w.driver().OpenTerminal(context.Background(), target, size); err == nil {
			term.Close()
			t.Fatalf("OpenTerminal at %+v succeeded", size)
		}
	}
	if n := execCount(w); n != 0 {
		t.Fatalf("refused terminals created %d execs", n)
	}
}

func TestResizeRefusesASizeOutsideAPTYsBounds(t *testing.T) {
	_, _, term, guest := attachedWorld(t)
	for _, size := range []runtimedriver.Size{{Width: 0, Height: 24}, {Width: 80, Height: -1}, {Width: 65536, Height: 24}, {Width: 80, Height: 65536}} {
		if err := term.Resize(size); err == nil {
			t.Fatalf("Resize to %+v succeeded", size)
		}
	}
	if got := guest.Size(); got != testTerminalSize {
		t.Fatalf("after refused resizes the PTY is %+v, want %+v", got, testTerminalSize)
	}
}

// The control websocket's upgrade alone does not prove the exec started:
// Incus reads control messages, and answers a ping, only once it did.
func TestOpenTerminalReturnsOnlyOnceIncusStartedTheExec(t *testing.T) {
	w := newIncusWorld(t)
	w.withSessionHostLinks()
	target := runningSession(t, w, nil)
	hold := make(chan struct{})
	w.mu.Lock()
	w.holdExec = hold
	w.mu.Unlock()
	type open struct {
		term runtimedriver.Terminal
		err  error
	}
	opened := make(chan open, 1)
	go func() {
		term, err := w.driver().OpenTerminal(context.Background(), target, testTerminalSize)
		opened <- open{term, err}
	}()
	select {
	case o := <-opened:
		t.Fatalf("OpenTerminal returned before Incus started the exec: %v", o.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	o := <-opened
	if o.err != nil {
		t.Fatalf("OpenTerminal: %v", o.err)
	}
	o.term.Close()
}

func TestOpenTerminalRefusesAnExecThatEndsWhileOpening(t *testing.T) {
	w := newIncusWorld(t)
	w.withSessionHostLinks()
	target := runningSession(t, w, nil)
	w.mu.Lock()
	w.execExits = true
	w.mu.Unlock()
	term, err := w.driver().OpenTerminal(context.Background(), target, testTerminalSize)
	if err == nil {
		term.Close()
		t.Fatal("OpenTerminal of an exec that ended succeeded")
	}
	if errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("OpenTerminal of an exec proven ended: %v, want a definite error", err)
	}
}

// An open that fails once Incus admitted the exec, without proof that the
// exec ended, names the terminal, so that core keeps its guard until the
// terminal settles.
func TestAFailedOpenWhoseExecMayRunNamesTheTerminal(t *testing.T) {
	shortSettle(t)
	endWait := terminalEndWait
	terminalEndWait = 200 * time.Millisecond
	t.Cleanup(func() { terminalEndWait = endWait })
	w := newIncusWorld(t)
	w.withSessionHostLinks()
	target := runningSession(t, w, nil)
	// Incus admitted the exec but never starts it, so the open gives up.
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	w.mu.Lock()
	w.holdExec = hold
	w.mu.Unlock()
	opening, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	term, err := w.driver().OpenTerminal(opening, target, testTerminalSize)
	if err == nil {
		term.Close()
		t.Fatal("OpenTerminal of an exec Incus never started succeeded")
	}
	var unended *runtimedriver.UnendedTerminal
	if !errors.As(err, &unended) || !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("OpenTerminal: %v, want an unended terminal", err)
	}
	w.mu.Lock()
	ids := slices.Collect(maps.Keys(w.execs))
	w.mu.Unlock()
	if !slices.Equal(ids, []string{unended.ID}) {
		t.Fatalf("the error names terminal %q, want the exec Incus admitted, of %v", unended.ID, ids)
	}
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectTerminal, Target: target, Terminal: unended.ID}
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Unknown || err != nil {
		t.Fatalf("Settle while the exec runs = %v, %v; want unknown", got, err)
	}
	w.guest(unended.ID).EndProcess()
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Applied || err != nil {
		t.Fatalf("Settle once the exec ended = %v, %v; want applied", got, err)
	}
}

// Incus finishes an exec whose program exited only once the client closed
// its channel: the terminal hangs up as soon as its output was read to the
// end, never waiting out terminalEndWait, and then the end is proven.
func TestATerminalsOutputIsReadToItsEndAndThenItsEndIsProven(t *testing.T) {
	endWait := terminalEndWait
	terminalEndWait = time.Minute
	t.Cleanup(func() { terminalEndWait = endWait })
	_, _, term, guest := attachedWorld(t)
	if _, err := guest.Write([]byte("last words")); err != nil {
		t.Fatal(err)
	}
	guest.EndProcess()
	output, err := io.ReadAll(term)
	if err != nil || string(output) != "last words" {
		t.Fatalf("the output read to its end is %q, %v; want %q", output, err, "last words")
	}
	if c := endedWithin(t, term); !c.Proven {
		t.Fatalf("the terminal ended with %+v, want proven", c)
	}
}

func TestCloseHangsUpAndTheWitnessProvesTheEnd(t *testing.T) {
	_, _, term, guest := attachedWorld(t)
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if c := endedWithin(t, term); !c.Proven {
		t.Fatalf("the closed terminal ended with %+v, want proven", c)
	}
	if status := guest.Status(); status < 200 {
		t.Fatalf("the exec's operation is %d after the proven end, want finished", status)
	}
}

// Once the wait on the exec is lost, the terminal can no longer learn of
// the end as it happens: it hangs up, and reads the operation for proof.
func TestALostWitnessHangsUpAndProvesTheEndFromTheOperation(t *testing.T) {
	_, _, term, guest := attachedWorld(t)
	guest.LoseWitness()
	if c := endedWithin(t, term); !c.Proven {
		t.Fatalf("the terminal ended with %+v, want proven", c)
	}
	if status := guest.Status(); status < 200 {
		t.Fatalf("the exec's operation is %d after the proven end, want finished", status)
	}
}

func TestSettleOfATerminalIsAppliedOnlyOnceIncusNoLongerRunsItsExec(t *testing.T) {
	shortSettle(t)
	w, target, term, guest := attachedWorld(t)
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectTerminal, Target: target, Terminal: term.ID()}
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Unknown || err != nil {
		t.Fatalf("Settle while the exec runs = %v, %v; want unknown", got, err)
	}
	w.mu.Lock()
	w.before = func(args []string) error {
		if args[0] == "operation" {
			return errors.New("Incus did not answer")
		}
		return nil
	}
	w.mu.Unlock()
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Unknown || err == nil {
		t.Fatalf("Settle with unreadable operations = %v, %v; want unknown with the error", got, err)
	}
	w.mu.Lock()
	w.before = nil
	w.mu.Unlock()
	guest.EndProcess()
	if _, err := io.Copy(io.Discard, term); err != nil {
		t.Fatal(err)
	}
	endedWithin(t, term)
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Applied || err != nil {
		t.Fatalf("Settle once the exec finished = %v, %v; want applied", got, err)
	}
	guest.Expire()
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Applied || err != nil {
		t.Fatalf("Settle once Incus forgot the exec = %v, %v; want applied", got, err)
	}
	effect.Terminal = "/1.0/operations/" + term.ID()
	if got, err := w.driver().Settle(context.Background(), effect); got != runtimedriver.Unknown || err == nil {
		t.Fatalf("Settle of a malformed terminal ID = %v, %v; want unknown with an error", got, err)
	}
}
