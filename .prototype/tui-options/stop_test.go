package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func confirmAndFinishStop(t *testing.T, m app) app {
	t.Helper()
	if m.screen != screenStopConfirm {
		t.Fatal("missing stop confirmation")
	}
	m = pressRune(t, m, 'y')
	return finishFakeStop(m, m.stopViewID)
}

func finishFakeStop(m app, id string) app {
	for n := 0; n <= len(stopLines); n++ {
		p, ok := m.stops[id]
		if !ok {
			break
		}
		next, _ := m.Update(stopTick{id, p.Generation, p.Step})
		m = next.(app)
	}
	return m
}

func TestStopConfirmationAndBackgroundShutdown(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		m := newApp()
		m.browser = true
		id := m.sessions[0].ID
		m = pressRune(t, m, 's')
		if m.screen != screenStopConfirm || m.sessions[0].Lifecycle != "ready" || !strings.Contains(m.View(), m.sessions[0].Branch) {
			t.Fatal("stop must first identify and confirm session")
		}
		m = pressKey(t, m, key)
		if m.screen != screenOverview || m.message != "" || m.sessions[0].Lifecycle != "ready" {
			t.Fatal("cancel changed session")
		}
		m = pressRune(t, m, 's')
		m = pressRune(t, m, 'y')
		if m.sessions[0].Lifecycle != "stopping" || !strings.Contains(m.View(), "STOPPING CONTAINER") {
			t.Fatal("missing shutdown transition")
		}
		p := m.stops[id]
		next, _ := m.Update(stopTick{id, p.Generation, p.Step})
		m = next.(app)
		if m.sessions[0].Agents[0].State != "stopped" || m.sessions[0].Services[0].State != "running" {
			t.Fatal("shutdown phases did not progress")
		}
		m = pressKey(t, m, key)
		m.selectSession(m.sessions[1].ID)
		selected := m.sessions[1].ID
		m = finishFakeStop(m, id)
		idx, _ := m.selectedSessionIndex()
		if m.screen != screenOverview || m.sessions[0].Lifecycle != "stopped" || m.sessions[idx].ID != selected {
			t.Fatal("background stop disrupted picker")
		}
	}
}

func TestStopRechecksAttachments(t *testing.T) {
	m := newApp()
	m.browser = true
	m = pressRune(t, m, 's')
	m.sessions[0].AttachedCount = 1
	m = pressRune(t, m, 'y')
	if m.sessions[0].Lifecycle != "ready" || len(m.stops) != 0 {
		t.Fatal("confirmation bypassed attachment check")
	}
}
