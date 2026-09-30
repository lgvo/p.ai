package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/control"
)

func Run(args []string) error {
	explicit := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		explicit, args = args[0], args[1:]
		if explicit == "" {
			return errors.New("control socket must be an absolute path")
		}
	}
	socket, err := control.ClientSocket(explicit)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	snapshot := flags.Bool("snapshot", false, "render one live frame without attaching or mutating")
	page := flags.String("page", "sessions", "snapshot page")
	id := flags.String("uuid", "", "snapshot session UUID")
	width := flags.Int("width", 120, "snapshot columns")
	height := flags.Int("height", 35, "snapshot rows")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected TUI arguments")
	}
	if *width < 1 || *width > 500 || *height < 1 || *height > 200 {
		return errors.New("snapshot dimensions out of bounds")
	}
	c := SocketClient{socket}
	if *snapshot {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		frame, err := Snapshot(ctx, c, socket, *page, *id, *width, *height)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, frame)
		return err
	}
	screen := newAttachmentScreen(os.Stdout)
	defer screen.restore()
	if err := screen.start(); err != nil {
		return err
	}
	model := New(c, socket)
	model.attachmentScreen = screen
	_, err = tea.NewProgram(model, tea.WithOutput(attachmentScreenTTY{os.Stdout, screen})).Run()
	return err
}
