package tui

import "charm.land/lipgloss/v2"

// ThemeMode selects a palette. Auto follows the terminal's background replies,
// using the dark palette until the terminal supplies its background color.
type ThemeMode string

const (
	ThemeAuto  ThemeMode = "auto"
	ThemeLight ThemeMode = "light"
	ThemeDark  ThemeMode = "dark"
)

// Palette names rendering roles rather than terminal color indexes. Keep color
// values here so a future theme can replace them without changing the views.
type Palette struct {
	Ink, Muted, Accent, Urgent, Warning, Positive, Edge, Selection string
}

// Theme carries the two palettes together; each browser owns its theme/styles.
type Theme struct{ Light, Dark Palette }

// PrototypeTheme is the palette of the selected session-browser prototype.
func PrototypeTheme() Theme {
	return Theme{
		Light: Palette{
			Ink: "#15202B", Muted: "#52606D", Accent: "#5B21B6",
			Urgent: "#B42318", Warning: "#9A6700", Positive: "#137333",
			Edge: "#CBD5E1", Selection: "#EDE9FE",
		},
		Dark: Palette{
			Ink: "#E6EDF3", Muted: "#8B949E", Accent: "#C4A7FF",
			Urgent: "#FF7B72", Warning: "#E3B341", Positive: "#56D364",
			Edge: "#30363D", Selection: "#312E81",
		},
	}
}

type styles struct {
	text, heading, selected, muted, warning, danger, running, panel lipgloss.Style
}

func (t Theme) styles(dark bool) styles {
	p := t.Light
	if dark {
		p = t.Dark
	}
	return styles{
		text:     lipgloss.NewStyle().Foreground(lipgloss.Color(p.Ink)),
		heading:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Accent)),
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.Ink)).Background(lipgloss.Color(p.Selection)),
		muted:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.Muted)),
		warning:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)),
		danger:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.Urgent)),
		running:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.Positive)),
		panel:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(p.Edge)),
	}
}
