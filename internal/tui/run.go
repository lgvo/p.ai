package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
)

func Run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: p tui CONTROL_SOCKET [--snapshot --page sessions|services|agents|policy --uuid UUID --width N --height N]")
	}
	socket := args[0]
	if !filepath.IsAbs(socket) {
		return errors.New("control socket must be an absolute path")
	}
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	snapshot := flags.Bool("snapshot", false, "render one live frame without attaching or mutating")
	page := flags.String("page", "sessions", "snapshot page")
	id := flags.String("uuid", "", "snapshot session UUID")
	width := flags.Int("width", 120, "snapshot columns")
	height := flags.Int("height", 35, "snapshot rows")
	if err := flags.Parse(args[1:]); err != nil {
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
	_, err := tea.NewProgram(New(c, socket)).Run()
	return err
}
