package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	modeFlag := flag.String("mode", "explore", "experience: explore or stress")
	barFields := flag.String("session-bar", "project,branch,status", "terminal bar fields, in order: project, branch, status, id (comma separated)")
	gallery := flag.Bool("gallery", false, "enable the layout comparison gallery")
	variantFlag := flag.String("variant", "topology", "initial UX variant")
	dataset := flag.String("dataset", "", "fixture name for the selected mode")
	listDatasets := flag.Bool("list-datasets", false, "list fixture names for the selected mode and exit")
	stressStep := flag.Int("stress-step", 0, "churn transition step (0 through 4; stress/churn only)")
	snapshot := flag.Bool("snapshot", false, "render one deterministic frame and exit")
	scenario := flag.String("scenario", "overview", "snapshot scenario: overview, attached-switch, create-failed, replacement-create, branches, policy, delete-preview, delete-progress, delete-complete, or help")
	width := flag.Int("width", 120, "snapshot width")
	height := flag.Int("height", 35, "snapshot height")
	flag.Parse()

	mode, ok := parseExperienceMode(*modeFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown mode %q; expected explore or stress\n", *modeFlag)
		os.Exit(2)
	}
	if *listDatasets {
		for _, definition := range datasetsForMode(mode) {
			fmt.Printf("%-16s %s\n", definition.name, definition.description)
		}
		return
	}
	if *dataset == "" {
		*dataset = defaultDatasetForMode(mode)
		if !*gallery && mode == modeExplore {
			*dataset = "portfolio"
		}
	}
	m, err := newAppForModeDataset(mode, *dataset)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, field := range strings.Split(*barFields, ",") {
		field = strings.TrimSpace(field)
		switch field {
		case "project", "branch", "status", "id":
			m.sessionBarFields = append(m.sessionBarFields, field)
		case "":
		default:
			fmt.Fprintf(os.Stderr, "unknown session-bar field %q\n", field)
			os.Exit(2)
		}
	}
	if m.sessionBarFields == nil {
		m.sessionBarFields = []string{}
	}
	m.browser = !*gallery
	if m.browser {
		m.clientAttach = ""
	}
	if v, ok := parseVariant(*variantFlag); ok {
		m.variant = v
	} else {
		fmt.Fprintf(os.Stderr, "unknown variant %q\n", *variantFlag)
		os.Exit(2)
	}
	if err := m.applyStressStep(*stressStep); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
