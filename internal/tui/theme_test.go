package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/control"
)

func TestBackgroundDetectionAndExplicitThemeModes(t *testing.T) {
	for _, mode := range []ThemeMode{ThemeAuto, ThemeLight, ThemeDark} {
		m := NewWithTheme(nil, "", PrototypeTheme(), mode)
		if m.darkBackground != (mode != ThemeLight) {
			t.Fatalf("%s initial background/fallback incorrect", mode)
		}
		m.page, m.query = "projects", "retained search"
		for _, background := range []color.Color{color.White, color.Black, color.White} {
			updated, cmd := m.Update(tea.BackgroundColorMsg{Color: background})
			m = updated.(Model)
			wantDark := mode == ThemeDark || (mode == ThemeAuto && background == color.Black)
			if m.darkBackground != wantDark || cmd != nil || m.page != "projects" || m.query != "retained search" {
				t.Fatalf("%s background reply changed mode/navigation: %+v", mode, m)
			}
			want := PrototypeTheme().Light.Accent
			if wantDark {
				want = PrototypeTheme().Dark.Accent
			}
			if m.styles.heading.GetForeground() != PrototypeTheme().styles(wantDark).heading.GetForeground() {
				t.Fatalf("%s failed to resolve accent %s", mode, want)
			}
		}
	}
}

func TestThemeRenderingRolesAndGeometry(t *testing.T) {
	var plain string
	for _, mode := range []ThemeMode{ThemeLight, ThemeDark} {
		m := fixture()
		m.themeMode, m.darkBackground = mode, mode == ThemeDark
		m.styles = m.theme.styles(m.darkBackground)
		m.width, m.height = 120, 35
		m.data.sessions = append(m.data.sessions, control.SessionView{UUID: "failed", Project: "a", Branch: "failure", Condition: "unreachable"})
		m.clamp()
		frame := m.View().Content
		if mode == ThemeLight {
			plain = ansi.Strip(frame)
		} else if ansi.Strip(frame) != plain {
			t.Fatal("changing theme changed frame text or geometry")
		}
		// Each role must reach rendered output, including semantic foregrounds
		// inside the selected row and the distinct selection background.
		want := []string{"38;2;21;32;43", "38;2;82;96;109", "38;2;91;33;182", "38;2;180;35;24", "38;2;154;103;0", "38;2;19;115;51", "38;2;203;213;225", "48;2;237;233;254"}
		if mode == ThemeDark {
			want = []string{"38;2;230;237;243", "38;2;139;148;158", "38;2;196;167;255", "38;2;255;123;114", "38;2;227;179;65", "38;2;86;211;100", "38;2;48;54;61", "48;2;49;46;129"}
		}
		for _, color := range want {
			if !strings.Contains(frame, color) {
				t.Errorf("%s rendering missing role color %s", mode, color)
			}
		}
	}
}

func TestInjectedThemesRemainIndependent(t *testing.T) {
	custom := PrototypeTheme()
	custom.Light.Accent = "#123456"
	custom.Dark.Accent = "#ABCDEF"
	a := NewWithTheme(nil, "", custom, ThemeAuto)
	b := New(nil, "")
	original := b.View().Content
	after, _ := a.Update(tea.BackgroundColorMsg{Color: color.White})
	a = after.(Model)
	if !strings.Contains(a.View().Content, "38;2;18;52;86") {
		t.Fatal("injected light palette did not reach the renderer")
	}
	if b.View().Content != original || !strings.Contains(original, "38;2;196;167;255") {
		t.Fatal("one model's palette update contaminated another browser")
	}
	// Mutating the caller's palette after injection also cannot alter a model.
	custom.Light.Accent = "#000000"
	if !strings.Contains(a.View().Content, "38;2;18;52;86") {
		t.Fatal("theme was shared through the constructor")
	}
}
