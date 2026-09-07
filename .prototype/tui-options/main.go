package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	variantFlag := flag.String("variant", "table", "initial UX variant")
	dataset := flag.String("dataset", "standard", "fixture: standard, dense, empty, single, or long")
	snapshot := flag.Bool("snapshot", false, "render one deterministic frame and exit")
	scenario := flag.String("scenario", "overview", "snapshot scenario: overview, attached-switch, create-failed, replacement-create, branches, policy, delete-preview, delete-progress, delete-complete, or help")
	width := flag.Int("width", 120, "snapshot width")
	height := flag.Int("height", 35, "snapshot height")
	flag.Parse()

	m, err := newAppForDataset(*dataset)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if v, ok := parseVariant(*variantFlag); ok {
		m.variant = v
	} else {
		fmt.Fprintf(os.Stderr, "unknown variant %q\n", *variantFlag)
		os.Exit(2)
	}

	if *snapshot {
		m.width = *width
		m.height = *height
		if err := m.applyScenario(*scenario); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Print(m.View())
		return
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "prototype failed: %v\n", err)
		os.Exit(1)
	}
}
