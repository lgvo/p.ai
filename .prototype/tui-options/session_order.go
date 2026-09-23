package main

import (
	"sort"
	"time"
)

// Priority is global. Project grouping only applies within each priority band.
func sessionPriority(s session) int {
	if sessionFlag(s) == "waiting" {
		return 0
	}
	if s.Lifecycle == "ready" {
		return 1
	}
	return 2
}

func (m app) orderSessionIndices(indices []int) []int {
	if !m.browser {
		return indices
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := m.sessions[indices[i]], m.sessions[indices[j]]
		if sessionPriority(a) != sessionPriority(b) {
			return sessionPriority(a) < sessionPriority(b)
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.LastInteraction != b.LastInteraction {
			return a.LastInteraction > b.LastInteraction
		}
		if a.Branch != b.Branch {
			return a.Branch < b.Branch
		}
		return a.ID < b.ID
	})
	return indices
}
func (m *app) recordInteraction(id string) {
	for i := range m.sessions {
		if m.sessions[i].ID == id {
			m.sessions[i].LastInteraction = time.Now().Unix()
			m.selectSession(id)
			return
		}
	}
}
