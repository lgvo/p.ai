package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
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
		{modeStress, "unicode", "wide glyphs, combining marks, emoji, and natural right-to-left names", unicodeSessions},
		{modeStress, "hostile-text", "ANSI, bidi controls, embedded control bytes, and oversized diagnostics", hostileTextSessions},
		{modeStress, "ambiguous-identities", "near-identical project, branch, and session labels", ambiguousIdentitySessions},
		{modeStress, "high-attachments", "simultaneous attachment counts from one through four digits", highAttachmentSessions},
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
	sessions := sanitizeSessions(definition.build())
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

func unicodeSessions() []session {
	result := fixtureSessions()
	projects := []string{"工房・東京", "مختبر-القاهرة", "cafe\u0301-equipe", "🚀-launch-pad", "מעבדת-תל-אביב", "naïve-über", "데이터-연구소", "👩🏽‍💻-agents", "Δοκιμή"}
	branches := []string{
		"機能/認証フロー", "ميزة/دليل-الإضافة", "correção/cache-concorrente",
		"🚀/interactive-cli", "תיקון/חוזה-תוסף", "prototype/界面-🧪",
		"정리/보존된-상태", "essai/emoji-👨‍👩‍👧‍👦", "αρχειο/παλιό-πείραμα",
	}
	for i := range result {
		result[i].Project = projects[i]
		result[i].Branch = branches[i]
		if result[i].AgentReason != "" {
			result[i].AgentReason += " · 사용자 확인 필요"
		}
		if result[i].Operation != "" {
			result[i].Operation += " · مرحلة تجريبية"
		}
	}
	return result
}

func hostileTextSessions() []session {
	result := fixtureSessions()
	result[0].Project = "forge\x1b[31m-red"
	result[0].Branch = "agent/oauth\nspoofed-row\tcolumn"
	result[0].AgentReason = "permission\a required\rreplace"
	result[1].Project = "link\x1b]8;;https://invalid.example\x1b\\project"
	result[1].Branch = "docs/\u202Eevil-bidi\u202C-guide"
	result[2].Operation = strings.Repeat("diagnostic-segment-", 400)
	result[3].Branch = "feature/zero\x00byte-and-del\x7f"
	result[4].Project = "isolate-\u2066left\u2069-project"
	return result
}

func ambiguousIdentitySessions() []session {
	result := scaledSessions(24, 4, "ambiguous")
	projects := []string{"platform-api-current", "platform-api-currant", "platform-api-current-2", "platform-api-current-old"}
	for i := range result {
		result[i].Project = projects[i%len(projects)]
		result[i].Branch = fmt.Sprintf("feature/session-synchronization-worker-%03d", i%3+1)
		result[i].ID = fmt.Sprintf("s-ambiguous-id-%06d", i+1)
	}
	result[1].AttachedCount = 1
	result[1].Agent, result[1].AgentReason = "", ""
	return result
}

func highAttachmentSessions() []session {
	result := scaledSessions(20, 5, "presence")
	counts := []int{0, 1, 2, 12, 9999}
	for i := range result {
		result[i].AttachedCount = counts[i%len(counts)]
		if result[i].AttachedCount > 0 {
			result[i].Agent, result[i].AgentReason = "", ""
		}
	}
	return result
}

func sanitizeSessions(input []session) []session {
	result := make([]session, len(input))
	for i, s := range input {
		s.ID = sanitizeField(s.ID, 64)
		s.Project = sanitizeField(s.Project, 128)
		s.Branch = sanitizeField(s.Branch, 256)
		s.AgentReason = sanitizeField(s.AgentReason, 256)
		s.Operation = sanitizeField(s.Operation, 512)
		for j := range s.PolicyDiff {
			s.PolicyDiff[j] = sanitizeField(s.PolicyDiff[j], 512)
		}
		result[i] = s
	}
	return result
}

func sanitizeField(value string, maxRunes int) string {
	var builder strings.Builder
	count := 0
	for _, r := range value {
		if count >= maxRunes {
			builder.WriteRune('…')
			break
		}
		switch r {
		case '\n':
			builder.WriteString("<LF>")
		case '\r':
			builder.WriteString("<CR>")
		case '\t':
			builder.WriteString("<TAB>")
		case '\x1b':
			builder.WriteString("<ESC>")
		default:
			if unicode.IsControl(r) {
				builder.WriteString(fmt.Sprintf("<U+%04X>", r))
			} else if (r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069') {
				builder.WriteString("<BIDI>")
			} else {
				builder.WriteRune(r)
			}
		}
		count++
	}
	return builder.String()
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
