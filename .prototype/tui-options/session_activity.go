package main

import (
	"strings"
	"time"
)

func (m app) sessionActivityDetails(s session) string {
	presence, _ := sessionSignals(s)
	lines := []string{
		titleStyle.Render("Session details"),
		truncate(s.Project+" / "+s.Branch, 50),
		mutedStyle.Render("UUID " + s.ID),
		"Runtime " + styledFact(m.lifecycleLabel(s.Lifecycle)) + " · Policy " + styledFact(s.Policy),
		"Terminals " + presence,
	}
	if m.width < 110 {
		lines[3] += " · " + lines[4]
		lines = append(lines[:4], lines[5:]...)
	}
	lines = append(lines, processTreeRows("Agents", s.Agents, s, m.width < 110)...)
	lines = append(lines, processRows("Services", projectServices(s), s)...)
	if s.Operation != "" {
		lines = append(lines, "Progress "+truncate(s.Operation, 42))
	}
	actions := strings.Join(availableActions(s), " · ")
	if canStop(s) {
		actions += " · stop"
	}
	actions = strings.ReplaceAll(actions, "attach", "enter")
	if s.LastInteraction != 0 && m.width >= 110 {
		lines = append(lines, "Last interaction "+time.Unix(s.LastInteraction, 0).UTC().Format("Jan 02 15:04 UTC"))
	}
	lines = append(lines, "Actions "+actions)
	if m.width < 110 {
		compact := make([]string, 0, len(lines))
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			if strings.HasPrefix(line, "Actions ") {
				continue
			}
			if (line == "├─ Agents [A]" || line == "└─ Project services [S]") && i+1 < len(lines) {
				i++
				child := strings.TrimLeft(lines[i], " │├└─")
				line += " · " + child
			}
			compact = append(compact, line)
		}
		lines = compact
	}
	return strings.Join(lines, "\n")
}

func processRows(label string, processes []sessionProcess, s session) []string {
	return processTreeRows(label, processes, s, false)
}

func processTreeRows(label string, processes []sessionProcess, s session, compact bool) []string {
	connector, indent := "├─ ", "│  "
	if label == "Services" {
		connector, indent = "└─ ", "   "
	}
	heading := connector + label
	if label == "Agents" {
		heading += " [A]"
	}
	if label == "Services" {
		heading = connector + "Project services [S]"
	}
	// Unknown runtime evidence must not become a claim that processes are alive.
	if observationWarning(s) != "" || s.Lifecycle == "unreachable" || s.Lifecycle == "missing" {
		return []string{heading + " · " + styledFact("unknown")}
	}
	if !s.ProcessesKnown {
		return []string{heading + " · not observed"}
	}
	if label == "Agents" {
		processes = activeAgentProcesses(s, processes)
	}
	if len(processes) == 0 {
		if label == "Agents" {
			return []string{heading + " · none active"}
		}
		return []string{heading + " · none observed"}
	}
	rows := []string{heading}
	for i, p := range processes {
		state := p.State
		if s.Lifecycle == "stopped" {
			state = "stopped"
		} else if s.Lifecycle != "ready" {
			state = "unknown"
		}
		branch := "├─ "
		if i == len(processes)-1 {
			branch = "└─ "
		}
		prefix := indent + branch
		name := p.Name
		if p.Label != "" {
			name += " [" + p.Label + "]"
		}
		row := prefix + name + " · " + styledFact(state)
		if label == "Services" {
			active, sub := unitState(s, p)
			row = prefix + name + " · " + styledFact(active) + " (" + sub + ")"
		}
		if p.Endpoint != "" && state == "running" {
			row += " (" + p.Endpoint + ")"
		}
		rows = append(rows, row)
		if label == "Agents" {
			_, reason := agentReport(s, p)
			rows[len(rows)-1] = prefix + name + " · " + styledFact(agentSummary(s, p))
			if !compact && reason != "" && state == "running" {
				childIndent := "│  "
				if i == len(processes)-1 {
					childIndent = "   "
				}
				rows = append(rows, indent+childIndent+"└─ "+truncate(reason, 38))
			}
		}
	}
	return rows
}
