package main

import "fmt"

const maxStressStep = 4

func (m *app) applyStressStep(step int) error {
	if step < 0 || step > maxStressStep {
		return fmt.Errorf("stress step %d is outside 0..%d", step, maxStressStep)
	}
	if m.mode != modeStress || m.dataset != "churn" {
		if step != 0 {
			return fmt.Errorf("--stress-step requires --mode stress --dataset churn")
		}
		return nil
	}
	for current := 1; current <= step; current++ {
		m.replaceObservedSessions(churnSessionsAtStep(current), current)
	}
	m.stressStep = step
	return nil
}

func churnSessionsAtStep(step int) []session {
	base := churnSessions()
	switch step {
	case 0:
		return base
	case 1:
		reverseSessions(base)
		return base
	case 2:
		reverseSessions(base)
		for i := range base {
			if base[i].ID == "s-churn-focus" {
				base[i].Lifecycle = "stopped"
				base[i].Agent = "failed"
				base[i].AgentReason = "tests changed state during refresh"
				base[i].Operation = "observation revision 2 replaced revision 1"
			}
		}
		inserted := base[0]
		inserted.ID = "s-churn-inserted"
		inserted.Project = "project-000"
		inserted.Branch = "agent/arrived-during-refresh"
		inserted.AttachedCount = 0
		return append([]session{inserted}, base...)
	case 3:
		previous := churnSessionsAtStep(2)
		result := make([]session, 0, len(previous)-1)
		for _, s := range previous {
			if s.ID != "s-churn-focus" {
				result = append(result, s)
			}
		}
		return result
	case 4:
		return nil
	default:
		return base
	}
}

func reverseSessions(sessions []session) {
	for left, right := 0, len(sessions)-1; left < right; left, right = left+1, right-1 {
		sessions[left], sessions[right] = sessions[right], sessions[left]
	}
}

func (m *app) replaceObservedSessions(next []session, step int) {
	selectedIndex, selected := m.selectedSessionIndex()
	selectedID := ""
	if selected {
		selectedID = m.sessions[selectedIndex].ID
	}
	oldSessions := append([]session(nil), m.sessions...)
	m.sessions = sanitizeSessions(next)

	if m.clientAttach != "" && !m.hasSessionID(m.clientAttach) {
		m.clientAttach = ""
	}
	if m.compareAnchor != "" && !m.hasSessionID(m.compareAnchor) {
		m.compareAnchor = ""
	}

	if selectedID != "" && m.selectStableSession(selectedID) {
		m.message = fmt.Sprintf("Churn step %d: selection preserved by UUID %s.", step, selectedID)
		return
	}

	fallbackID := nearestRemainingID(oldSessions, selectedIndex, m.sessions)
	if fallbackID != "" {
		m.selectStableSession(fallbackID)
		m.message = fmt.Sprintf("Churn step %d: %s disappeared; moved to neighbor %s.", step, selectedID, fallbackID)
		return
	}
	m.selected, m.project, m.outlineCursor = 0, 0, 0
	m.message = fmt.Sprintf("Churn step %d: %s disappeared; no sessions remain.", step, selectedID)
}

func (m app) hasSessionID(id string) bool {
	for _, s := range m.sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

func (m *app) selectStableSession(id string) bool {
	for _, s := range m.sessions {
		if s.ID != id {
			continue
		}
		for projectIndex, project := range m.projects() {
			if project == s.Project {
				m.project = projectIndex
				break
			}
		}
		if !m.selectSession(id) {
			return false
		}
		if m.compareAnchor == "" {
			m.compareAnchor = id
		}
		for nodeIndex, node := range m.outlineNodes() {
			if !node.isProject && m.sessions[node.sessionIndex].ID == id {
				m.outlineCursor = nodeIndex
				break
			}
		}
		return true
	}
	return false
}

func nearestRemainingID(previous []session, selectedIndex int, next []session) string {
	present := make(map[string]bool, len(next))
	for _, s := range next {
		present[s.ID] = true
	}
	for distance := 1; distance <= len(previous); distance++ {
		for _, candidate := range []int{selectedIndex + distance, selectedIndex - distance} {
			if candidate >= 0 && candidate < len(previous) && present[previous[candidate].ID] {
				return previous[candidate].ID
			}
		}
	}
	if len(next) > 0 {
		return next[0].ID
	}
	return ""
}
