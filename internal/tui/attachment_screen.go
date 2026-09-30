package tui

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// attachmentScreen keeps the browser's alternate screen during ExecProcess's
// terminal release, the attached client's lifetime, and subsequent redraws.
// Input modes still belong to Bubble Tea and the attachment client. Only screen
// switches are held; the original console is restored when the browser exits.
type attachmentScreen struct {
	output  io.Writer
	mu      sync.Mutex
	active  bool
	parser  *ansi.Parser
	pending []byte
	dcs     bool
	dcsEsc  bool
}

func newAttachmentScreen(output io.Writer) *attachmentScreen {
	return &attachmentScreen{output: output, parser: ansi.NewParser()}
}

// Preserve the terminal descriptor so Bubble Tea can observe window sizes.
type attachmentScreenTTY struct {
	*os.File
	screen *attachmentScreen
}

func (s attachmentScreenTTY) Write(p []byte) (int, error) { return s.screen.Write(p) }

func (s *attachmentScreen) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = true
	n, err := io.WriteString(s.output, ansi.SetModeAltScreenSaveCursor)
	if err == nil && n != len(ansi.SetModeAltScreenSaveCursor) {
		err = io.ErrShortWrite
	}
	return err
}

func (s *attachmentScreen) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return
	}
	s.active = true
	s.parser.Reset()
	s.pending = nil
}

// Keep ownership until Run returns: Bubble Tea queues its restart screen switch
// and flushes it asynchronously, after the ExecProcess callback has returned.
func (s *attachmentScreen) restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		s.active = false
		s.pending = nil
		if s.dcs || s.parser.State() != parser.GroundState {
			_, _ = s.output.Write([]byte{ansi.CAN})
		}
		_, _ = io.WriteString(s.output, ansi.ResetModeAltScreenSaveCursor)
	}
}

func (s *attachmentScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return s.output.Write(p)
	}
	output := make([]byte, 0, len(p))
	for _, b := range p {
		if s.dcs {
			// DCS data is opaque, including tmux's doubled-ESC passthrough.
			// Never rewrite an inner CSI as though it belonged to this stream.
			output = append(output, b)
			if b == ansi.ST || b == ansi.CAN || b == ansi.SUB || s.dcsEsc && b == '\\' {
				s.dcs, s.dcsEsc = false, false
				s.parser.Reset()
			} else if s.dcsEsc {
				s.dcsEsc = false
			} else {
				s.dcsEsc = b == ansi.ESC
			}
			continue
		}
		if len(s.pending) > 0 && (b == ansi.ESC || b == ansi.CSI) {
			// A new escape cancels the unfinished one. Preserve that cancellation
			// even if the new screen-switch sequence is subsequently held.
			output = append(output, s.pending...)
			output = append(output, ansi.CAN)
			s.pending = nil
			s.parser.Reset()
		}
		if len(s.pending) > 0 && b < 0x20 {
			// C0 controls execute inside CSI without belonging to its parameters.
			// CAN and SUB also cancel the pending escape entirely.
			s.parser.Advance(b)
			if b == ansi.CAN || b == ansi.SUB {
				output = append(output, s.pending...)
				s.pending = nil
			}
			output = append(output, b)
			continue
		}
		if len(s.pending) > 0 && b == 0x7f {
			// DEL is ignored inside escape sequences, not a parameter byte.
			s.parser.Advance(b)
			continue
		}
		state := s.parser.State()
		if len(s.pending) == 0 && state == parser.EscapeState && b == '[' {
			// ESC can terminate an OSC/DCS string and begin a new CSI. That ESC
			// already reached the terminal: cancel it and collect the new CSI.
			output = append(output, ansi.CAN)
			s.pending = append(s.pending, ansi.ESC)
		}
		if len(s.pending) > 0 || state == parser.GroundState && (b == ansi.ESC || b == ansi.CSI) {
			s.pending = append(s.pending, b)
			s.parser.Advance(b)
			switch s.parser.State() {
			case parser.EscapeState, parser.EscapeIntermediateState,
				parser.CsiEntryState, parser.CsiParamState, parser.CsiIntermediateState:
				// Bound buffering even for an unfinished or malformed CSI.
				if len(s.pending) < 128 {
					continue
				}
			}
			output = append(output, retainAlternateScreen(s.pending)...)
			s.pending = nil
		} else {
			output = append(output, b)
			s.parser.Advance(b)
		}
		if s.parser.State() == parser.DcsEntryState {
			s.dcs = true
		}
	}
	if len(output) > 0 {
		n, err := s.output.Write(output)
		if err != nil {
			return 0, err
		}
		if n != len(output) {
			return 0, io.ErrShortWrite
		}
	}
	return len(p), nil
}

func retainAlternateScreen(sequence []byte) []byte {
	text := string(sequence)
	prefix := "\x1b[?"
	if strings.HasPrefix(text, "\x9b?") {
		prefix = "\x9b?"
	}
	if !strings.HasPrefix(text, prefix) || len(text) <= len(prefix) {
		return sequence
	}
	final := text[len(text)-1]
	if final != 'h' && final != 'l' {
		return sequence
	}
	var retained []string
	changed, clear := false, false
	for _, parameter := range strings.Split(text[len(prefix):len(text)-1], ";") {
		if parameter == "" {
			retained = append(retained, parameter)
			continue
		}
		mode, err := strconv.Atoi(parameter)
		if err != nil {
			return sequence
		}
		switch mode {
		case 47, 1047, 1049:
			changed = true
			clear = clear || final == 'h' && mode != 47
		default:
			retained = append(retained, parameter)
		}
	}
	if !changed {
		return sequence
	}
	var result string
	if len(retained) > 0 {
		result = prefix + strings.Join(retained, ";") + string(final)
	}
	if clear {
		// Match alternate-screen entry without replacing the saved console.
		result += ansi.EraseEntireScreen + ansi.CursorHomePosition
	}
	return []byte(result)
}

func attachmentProcess(cmd *exec.Cmd, screen *attachmentScreen) tea.Cmd {
	if screen == nil {
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return attached{err} })
	}
	cmd.Stdout, cmd.Stderr = screen, screen
	execute := tea.ExecProcess(cmd, func(err error) tea.Msg { return attached{err} })
	return func() tea.Msg {
		// ExecProcess releases the terminal before starting cmd. Begin holding
		// screen switches before that release, and keep them held until exit.
		screen.begin()
		return execute()
	}
}
