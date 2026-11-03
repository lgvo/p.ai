package conformance

import (
	"io"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var terminalSize = runtimedriver.Size{Width: 80, Height: 24}

var terminalCases = []testCase{
	{"a terminal carries input to the session host and its output back, at the size asked", func(t *testing.T, f Fixture) {
		a := attached(t, f)
		if got := a.guest.Size(); got != terminalSize {
			t.Fatalf("the guest sees size %+v, want %+v", got, terminalSize)
		}
		if _, err := a.term.Write([]byte("input\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got := readFull(t, a.guest, len("input\n")); got != "input\n" {
			t.Fatalf("the guest read %q, want %q", got, "input\n")
		}
		if _, err := a.guest.Write([]byte("output")); err != nil {
			t.Fatalf("the guest's Write: %v", err)
		}
		if got := readFull(t, a.term, len("output")); got != "output" {
			t.Fatalf("the client read %q, want %q", got, "output")
		}
		resized := runtimedriver.Size{Width: 132, Height: 43}
		if err := a.term.Resize(resized); err != nil {
			t.Fatalf("Resize: %v", err)
		}
		if got := a.guest.Size(); got != resized {
			t.Fatalf("after Resize the guest sees %+v, want %+v", got, resized)
		}
	}},
	{"a terminal refuses a stopped runtime and another incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		if term, err := f.Driver().OpenTerminal(t.Context(), target(s, at), terminalSize); err == nil {
			term.Close()
			t.Fatal("OpenTerminal on a stopped runtime succeeded")
		}
		if _, err := f.Driver().Start(t.Context(), s); err != nil {
			t.Fatalf("Start: %v", err)
		}
		other := created(t, f, f.Session(SessionOptions{})).Incarnation
		if term, err := f.Driver().OpenTerminal(t.Context(), target(s, other), terminalSize); err == nil {
			term.Close()
			t.Fatal("OpenTerminal at another incarnation succeeded")
		}
	}},
	{"the end of the guest process is proven, once, and settles the terminal", func(t *testing.T, f Fixture) {
		a := attached(t, f)
		a.guest.EndProcess()
		wantEnded(t, a.term, true)
		drained := async(func() error {
			_, err := io.ReadAll(a.term)
			return err
		})
		if err := within(t, drained, "the ended terminal's output to end"); err != nil {
			t.Fatalf("reading an ended terminal to its end: %v", err)
		}
		wantSettled(t, f, a.effect(), runtimedriver.Applied)
	}},
	{"a dropped channel ends the terminal unproven; it settles once the guest process ends", func(t *testing.T, f Fixture) {
		a := attached(t, f)
		a.guest.DropChannel()
		wantEnded(t, a.term, false)
		if got := settle(t, f, a.effect()); got != runtimedriver.Unknown {
			t.Fatalf("Settle(terminal) while its guest process runs = %v, want unknown", got)
		}
		a.guest.EndProcess()
		wantSettled(t, f, a.effect(), runtimedriver.Applied)
	}},
	{"a driver restart ends terminals unproven; they settle once the session stops", func(t *testing.T, f Fixture) {
		a := attached(t, f)
		f.Restart()
		wantEnded(t, a.term, false)
		if got := settle(t, f, a.effect()); got != runtimedriver.Unknown {
			t.Fatalf("Settle(terminal) while its session runs = %v, want unknown", got)
		}
		if _, err := f.Driver().Stop(t.Context(), a.session); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		wantSettled(t, f, a.effect(), runtimedriver.Applied)
	}},
	{"close ends the attachment, delivering its end once", func(t *testing.T, f Fixture) {
		a := attached(t, f)
		if err := a.term.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		within(t, a.term.Ended(), "the closed terminal to end")
		wantNoSecondEnd(t, a.term)
		if _, err := a.term.Write([]byte("late")); err == nil {
			t.Fatal("Write to a closed terminal succeeded")
		}
	}},
}

// attachment is a terminal open on a running session, with its guest side.
type attachment struct {
	session runtimedriver.Session
	at      runtimedriver.Incarnation
	term    runtimedriver.Terminal
	guest   GuestTerminal
}

func (a attachment) effect() runtimedriver.Effect {
	return runtimedriver.Effect{Kind: runtimedriver.EffectTerminal, Target: target(a.session, a.at), Terminal: a.term.ID()}
}

// attached starts a new session and opens a terminal on it at its
// incarnation.
func attached(t *testing.T, f Fixture) attachment {
	t.Helper()
	s := f.Session(SessionOptions{})
	at := running(t, f, s).Incarnation
	term, err := f.Driver().OpenTerminal(t.Context(), target(s, at), terminalSize)
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { term.Close() })
	return attachment{session: s, at: at, term: term, guest: f.Terminals().Guest(term.ID())}
}

// wantEnded checks that the terminal ends with the proof wanted, and only
// once.
func wantEnded(t *testing.T, term runtimedriver.Terminal, proven bool) {
	t.Helper()
	if c := within(t, term.Ended(), "the terminal to end"); c.Proven != proven {
		t.Fatalf("the terminal ended with %+v, want proven %t", c, proven)
	}
	wantNoSecondEnd(t, term)
}

func wantNoSecondEnd(t *testing.T, term runtimedriver.Terminal) {
	t.Helper()
	select {
	case c, ok := <-term.Ended():
		if ok {
			t.Fatalf("Ended delivered a second completion %+v", c)
		}
	default:
	}
}

// readFull reads n bytes from r, failing the case after waitLimit.
func readFull(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	read := async(func() error {
		_, err := io.ReadFull(r, buf)
		return err
	})
	if err := within(t, read, "a read of the terminal"); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf)
}
