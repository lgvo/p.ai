package main

import "fmt"

func (m *app) applyDataset(name string) error {
	var sessions []session
	switch name {
	case "standard":
		sessions = fixtureSessions()
	case "dense":
		sessions = denseSessions()
	case "empty":
		sessions = nil
	case "single":
		sessions = []session{fixtureSessions()[0]}
	case "long":
		sessions = longNameSessions()
	default:
		return fmt.Errorf("unknown dataset %q", name)
	}
	m.dataset = name
	m.sessions = sessions
	m.selected, m.project = 0, 0
	m.clientAttach = ""
	for _, s := range sessions {
		if s.AttachedCount > 0 {
			m.clientAttach = s.ID
			break
		}
	}
	for projectIndex, project := range m.projects() {
		for _, s := range sessions {
			if s.ID == "s-auth" && s.Project == project {
				m.project = projectIndex
				break
			}
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
