package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

func TestAttachmentScreenSwitchesAndFragmentedOutput(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"tmux exit", ansi.ResetModeAltScreenSaveCursor + "[exited]", "[exited]"},
		{"tmux entry", ansi.SetModeAltScreenSaveCursor + "session", ansi.EraseEntireScreen + ansi.CursorHomePosition + "session"},
		{"legacy screen", "\x1b[?47l\x1b[?1047l", ""},
		{"combined modes", "\x1b[?1049;25l\x1b[?25;1049h", "\x1b[?25l\x1b[?25h" + ansi.EraseEntireScreen + ansi.CursorHomePosition},
		{"eight bit CSI", "\x9b?1049l", ""},
		{"other controls", "λ界\x1b[31mtext\x1b[0m\x1b[?2004h\x1b[?6n\x1b[H", "λ界\x1b[31mtext\x1b[0m\x1b[?2004h\x1b[?6n\x1b[H"},
		{"title", "\x1b]0;session title\x07", "\x1b]0;session title\x07"},
		{"opaque strings", "\x1bPtmux;\x1b\x1b[?1049l\x1b\\", "\x1bPtmux;\x1b\x1b[?1049l\x1b\\"},
		{"tmux color passthrough", "\x1bPtmux;\x1b\x1b[31m\x1b\\", "\x1bPtmux;\x1b\x1b[31m\x1b\\"},
		{"CSI after DCS", "\x1bPdata\x1b\\\x1b[?1049l", "\x1bPdata\x1b\\"},
		{"OSC ends at escape", "\x1b]0;\x1b[?1049l\x07", "\x1b]0;\x1b\x18\x07"},
		{"invalid parameters", "\x1b[?1049:1l", "\x1b[?1049:1l"},
		{"restarted CSI", "\x1b[31\x1b[?1049l", "\x1b[31\x18"},
		{"restarted escape", "\x1b\x1b[?1049l", "\x1b\x18"},
		{"embedded C0", "\x1b[?1049\x07\nl", "\x07\n"},
		{"canceled CSI", "\x1b[?1049\x18text", "\x1b[?1049\x18text"},
		{"ignored DEL", "\x1b[?1049\x7fl", ""},
		{"default parameter", "\x1b[?;1049l", "\x1b[?l"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.input); split++ {
				var output bytes.Buffer
				screen := newAttachmentScreen(&output)
				screen.begin()
				for _, chunk := range []string{test.input[:split], test.input[split:]} {
					if n, err := screen.Write([]byte(chunk)); err != nil || n != len(chunk) {
						t.Fatalf("split %d: write = %d, %v", split, n, err)
					}
				}
				if got := output.String(); got != test.want {
					t.Fatalf("split %d: got %q, want %q", split, got, test.want)
				}
			}
		})
	}
}

func TestAttachmentScreenRestoresOnlyAfterBrowserExit(t *testing.T) {
	var output bytes.Buffer
	screen := newAttachmentScreen(&output)
	write := func(text string) {
		t.Helper()
		if _, err := io.WriteString(screen, text); err != nil {
			t.Fatal(err)
		}
	}
	write(ansi.SetModeAltScreenSaveCursor + "P")
	for range 2 {
		screen.begin()
		write(ansi.ResetModeAltScreenSaveCursor)
		write(ansi.SetModeAltScreenSaveCursor + "tmux")
		write(ansi.ResetModeAltScreenSaveCursor + "[exited]")
		write(ansi.SetModeAltScreenSaveCursor + "P")
	}
	if strings.Contains(output.String(), ansi.ResetModeAltScreenSaveCursor) {
		t.Fatal("handoff revealed the saved console")
	}
	write(ansi.ResetModeAltScreenSaveCursor)
	screen.restore()
	if strings.Count(output.String(), ansi.SetModeAltScreenSaveCursor) != 1 ||
		strings.Count(output.String(), ansi.ResetModeAltScreenSaveCursor) != 1 {
		t.Fatalf("console was not saved/restored exactly once: %q", output.String())
	}
	output.Reset()
	screen.begin()
	screen.restore()
	screen.restore()
	if output.String() != ansi.ResetModeAltScreenSaveCursor {
		t.Fatalf("interrupted handoff did not restore the console once: %q", output.String())
	}
}

type brokenScreenOutput struct{ err error }

func (w brokenScreenOutput) Write([]byte) (int, error) { return 0, w.err }

func TestAttachmentScreenOutputFailure(t *testing.T) {
	for _, err := range []error{nil, errors.New("terminal disconnected")} {
		screen := newAttachmentScreen(brokenScreenOutput{err})
		screen.begin()
		_, got := screen.Write([]byte("session"))
		if err == nil {
			err = io.ErrShortWrite
		}
		if !errors.Is(got, err) {
			t.Fatalf("write error = %v, want %v", got, err)
		}
	}
}

func TestAttachmentScreenKeepsTerminalDescriptor(t *testing.T) {
	output := attachmentScreenTTY{os.Stdout, newAttachmentScreen(os.Stdout)}
	var terminal term.File = output
	if terminal.Fd() != os.Stdout.Fd() {
		t.Fatal("screen wrapper hid the output terminal descriptor")
	}
}

// Exercise Bubble Tea's actual release/restore and a separate attached process,
// including failures and repeated attachment. A buffer-only transformation test
// would miss holding the screen too late or ending before Bubble Tea's repaint.
type attachmentScreenModel struct {
	screen  *attachmentScreen
	output  *bytes.Buffer
	cmd     func() *exec.Cmd
	left    int
	err     error
	exposed bool
	size    tea.WindowSizeMsg
}

type attachScreenNext struct{}

func (m *attachmentScreenModel) Init() tea.Cmd {
	// Let the initial P frame reach the terminal before attaching, as it does
	// when a user presses Enter. Stay alive after detach for the async repaint.
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return attachScreenNext{} })
}
func (m *attachmentScreenModel) View() tea.View {
	v := tea.NewView(fmt.Sprintf("P browser · %d attachments left", m.left))
	v.AltScreen = true
	return v
}
func (m *attachmentScreenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.size = size
	}
	if done, ok := msg.(attached); ok {
		m.err = done.err
		m.left--
		return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return attachScreenNext{} })
	}
	if _, ok := msg.(attachScreenNext); ok {
		m.screen.mu.Lock()
		if m.output != nil && strings.Contains(m.output.String(), ansi.ResetModeAltScreenSaveCursor) {
			m.exposed = true
		}
		m.screen.mu.Unlock()
		if m.left > 0 {
			return m, attachmentProcess(m.cmd(), m.screen)
		}
		return m, tea.Quit
	}
	return m, nil
}

func TestAttachmentScreenWithBubbleTeaExec(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		failure bool
	}{{"attached and detached twice", false}, {"failed attachment", true}} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			screen := newAttachmentScreen(&output)
			model := &attachmentScreenModel{screen: screen, output: &output, left: 2, cmd: func() *exec.Cmd {
				if test.failure {
					return exec.Command("/nonexistent/p-attach")
				}
				cmd := exec.Command(executable, "-test.run=^TestAttachmentScreenChild$")
				cmd.Env = append(os.Environ(), "P_TEST_ATTACHMENT_SCREEN_CHILD=1")
				return cmd
			}}
			program := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(screen), tea.WithoutSignalHandler())
			if err := screen.start(); err != nil {
				t.Fatal(err)
			}
			if _, err := program.Run(); err != nil {
				t.Fatal(err)
			}
			screen.restore()
			if model.exposed {
				t.Fatal("attachment exposed the console before returning to P")
			}
			if (model.err != nil) != test.failure {
				t.Fatalf("attachment error = %v, failure expected = %t", model.err, test.failure)
			}
			if strings.Count(output.String(), ansi.SetModeAltScreenSaveCursor) != 1 ||
				strings.Count(output.String(), ansi.ResetModeAltScreenSaveCursor) != 1 {
				t.Fatalf("wrong console switches: %q", output.String())
			}
		})
	}
}

func TestAttachmentScreenWithRealTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 28, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	// Closing the master must interrupt the reader, including on test failure.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, master)
		close(drained)
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	screen := newAttachmentScreen(slave)
	model := &attachmentScreenModel{screen: screen, left: 2, cmd: func() *exec.Cmd {
		cmd := exec.Command(executable, "-test.run=^TestAttachmentScreenChild$")
		cmd.Env = append(os.Environ(), "P_TEST_ATTACHMENT_SCREEN_CHILD=1")
		return cmd
	}}
	program := tea.NewProgram(model, tea.WithInput(slave),
		tea.WithOutput(attachmentScreenTTY{slave, screen}), tea.WithoutSignalHandler(),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	if err := screen.start(); err != nil {
		t.Fatal(err)
	}
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	screen.restore()
	if model.err != nil {
		t.Fatal(model.err)
	}
	if model.size.Width != 100 || model.size.Height != 28 {
		t.Fatalf("browser lost terminal size: %+v", model.size)
	}
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}
	<-drained
	if strings.Count(output.String(), ansi.SetModeAltScreenSaveCursor) != 1 ||
		strings.Count(output.String(), ansi.ResetModeAltScreenSaveCursor) != 1 ||
		strings.Count(output.String(), "session tmux") != 2 {
		t.Fatalf("wrong screen handoff on real terminal: %q", output.String())
	}
}

func TestAttachmentScreenChild(t *testing.T) {
	if os.Getenv("P_TEST_ATTACHMENT_SCREEN_CHILD") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, ansi.SetModeAltScreenSaveCursor+"session tmux")
	_, _ = io.WriteString(os.Stdout, ansi.ResetModeAltScreenSaveCursor+"[exited]")
	os.Exit(0)
}
