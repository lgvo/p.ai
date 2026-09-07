package main

import (
	"fmt"
	"sort"
)

type datasetDefinition struct {
	mode        experienceMode
	name        string
	description string
	build       func() []session
}

func datasetCatalog() []datasetDefinition {
	return []datasetDefinition{
		{modeExplore, "standard", "nine sessions covering the presentation contract", fixtureSessions},
		{modeExplore, "dense", "forty representative sessions across eight projects", denseSessions},
		{modeExplore, "empty", "no projects or sessions", func() []session { return nil }},
		{modeExplore, "single", "one unattended session", func() []session { return []session{fixtureSessions()[0]} }},
		{modeExplore, "long", "representative sessions with deliberately long names", longNameSessions},
		{modeStress, "baseline", "stress harness control using the curated standard fixture", fixtureSessions},
		{modeStress, "massive", "one thousand sessions across twenty-five projects", massiveSessions},
		{modeStress, "many-projects", "one hundred eighty projects with one session each", manyProjectSessions},
		{modeStress, "skewed", "three hundred sessions dominated by one urgent condition", skewedSessions},
	}
}

func datasetsForMode(mode experienceMode) []datasetDefinition {
	var result []datasetDefinition
	for _, definition := range datasetCatalog() {
		if definition.mode == mode {
			result = append(result, definition)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}

func parseExperienceMode(value string) (experienceMode, bool) {
	switch experienceMode(value) {
	case modeExplore:
		return modeExplore, true
	case modeStress:
		return modeStress, true
	default:
		return "", false
	}
}

func defaultDatasetForMode(mode experienceMode) string {
	if mode == modeStress {
		return "baseline"
	}
	return "standard"
}

func (m *app) applyDataset(name string) error {
	return m.applyDatasetForMode(modeExplore, name)
}

func (m *app) applyDatasetForMode(mode experienceMode, name string) error {
	var definition *datasetDefinition
	for _, candidate := range datasetCatalog() {
		if candidate.mode == mode && candidate.name == name {
			copy := candidate
			definition = &copy
			break
		}
	}
	if definition == nil {
		return fmt.Errorf("unknown %s dataset %q", mode, name)
	}
	sessions := definition.build()
	m.mode = mode
	m.dataset = name
	m.datasetNote = definition.description
	m.sessions = sessions
	m.selected, m.project = 0, 0
	m.clientAttach = ""
	for _, s := range sessions {
		if s.AttachedCount > 0 {
			m.clientAttach = s.ID
			break
		}
	}
	m.compareAnchor = m.clientAttach
	if m.compareAnchor == "" && len(sessions) > 0 {
		m.compareAnchor = sessions[0].ID
	}
	for projectIndex, project := range m.projects() {
		for _, s := range sessions {
			if s.ID == "s-auth" && s.Project == project {
				m.project = projectIndex
				break
			}
		}
	}
	for nodeIndex, node := range m.outlineNodes() {
		if !node.isProject && sessions[node.sessionIndex].ID == "s-auth" {
			m.outlineCursor = nodeIndex
			break
		}
	}
	return nil
}

func denseSessions() []session {
	base := fixtureSessions()
	projects := []string{"forge", "orbit", "p.ai", "labs", "demo", "relay", "quartz", "harbor"}
	result := make([]session, 0, 40)
	for i := 0; i < 40; i++ {
		s := base[i%len(base)]
		s.ID = fmt.Sprintf("s-dense-%02d", i+1)
		s.Project = projects[i%len(projects)]
		s.Branch = fmt.Sprintf("%s/%02d-%s", branchPrefix(s.Lifecycle), i+1, s.Branch)
		s.AttachedCount = 0
		if i == 11 {
			s.AttachedCount = 1
			s.Agent, s.AgentReason = "", ""
		}
		result = append(result, s)
	}
	return result
}

func longNameSessions() []session {
	result := fixtureSessions()
	for i := range result {
		result[i].Project = fmt.Sprintf("project-with-a-deliberately-long-owner-and-name-%02d", i+1)
		result[i].Branch = "feature/a-deeply-nested-workstream-name-that-must-truncate-without-hiding-status/" + result[i].Branch
	}
	return result
}

func massiveSessions() []session {
	return scaledSessions(1000, 25, "massive")
}

func manyProjectSessions() []session {
	return scaledSessions(180, 180, "many")
}

func scaledSessions(count, projectCount int, prefix string) []session {
	base := fixtureSessions()
	result := make([]session, 0, count)
	for i := 0; i < count; i++ {
		s := base[i%len(base)]
		s.ID = fmt.Sprintf("s-%s-%04d", prefix, i+1)
		s.Project = fmt.Sprintf("project-%03d", i%projectCount+1)
		s.Branch = fmt.Sprintf("%s/%04d-%s", branchPrefix(s.Lifecycle), i+1, s.Branch)
		s.AttachedCount = 0
		if i == count/2 {
			s.AttachedCount = 1
			s.Agent, s.AgentReason = "", ""
		}
		result = append(result, s)
	}
	return result
}

func skewedSessions() []session {
	result := scaledSessions(300, 12, "skewed")
	for i := range result {
		result[i].Lifecycle = "ready"
		result[i].Agent = "attention"
		result[i].AgentReason = "permission required in a deliberately skewed queue"
		result[i].Policy = "current"
		result[i].Operation = ""
		result[i].AttachedCount = 0
	}
	result[len(result)/2].AttachedCount = 1
	result[len(result)/2].Agent, result[len(result)/2].AgentReason = "", ""
	return result
}

func branchPrefix(lifecycle string) string {
	switch lifecycle {
	case "creating", "starting":
		return "boot"
	case "missing", "unreachable":
		return "recover"
	case "discarding", "deleting":
		return "retire"
	default:
		return "work"
	}
}
