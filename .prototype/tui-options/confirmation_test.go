package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestConfirmationsDefaultToNoInsidePanels(t *testing.T) {
	for _, screen := range []screen{screenStopConfirm, screenCreate, screenDeletePreview} {
		for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'n'}}, {Type: tea.KeyRunes, Runes: []rune{'N'}}} {
			m := newApp()
			m.browser = true
			m.screen, m.createStep = screen, 2
			m.draft = createDraft{Project: "forge", Branch: "main"}
			m.stopViewID, m.deleteProject = m.sessions[0].ID, "forge"
			view := m.View()
			prompt, bottom := strings.Index(view, "[y/N]"), strings.LastIndex(view, "╰")
			if prompt < 0 || prompt > bottom || strings.Count(view, "[y/N]") != 1 {
				t.Fatalf("confirmation must appear once inside panel: %s", view)
			}
			original := len(m.sessions)
			m = pressKey(t, m, key)
			if m.confirmationActive() || len(m.sessions) != original || m.sessions[0].Lifecycle != "ready" || len(m.stops) != 0 {
				t.Fatalf("No accepted confirmation on %v", screen)
			}
		}
	}
}

func TestUppercaseYesAcceptsStop(t *testing.T) {
	m := newApp()
	m.browser = true
	m = pressRune(t, m, 's')
	m = pressRune(t, m, 'Y')
	if m.screen != screenStopping {
		t.Fatal("Y did not confirm stop")
	}
}
