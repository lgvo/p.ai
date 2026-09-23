package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type actionEligibility struct {
	name      string
	available bool
	reason    string
}

func (m app) actionSheetView(width, _ int) string {
	indices := m.visibleIndices()
	attached := 0
	for _, idx := range indices {
		attached += m.sessions[idx].AttachedCount
	}
	list := titleStyle.Render("Choose an object") + fmt.Sprintf("  ·  attached %d", attached) + "\n" + m.actionObjectRows(4)
	sheet := m.selectedActionSheet()
	if width >= 110 {
		leftWidth := width * 42 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, list), " ", renderPanel(width-leftWidth-1, sheet))
	}
	return renderPanel(width, titleStyle.Render("Action eligibility sheet")+"\n"+
		mutedStyle.Render(truncate("Available and unavailable actions stay visible together; reasons come from explicit fixture facts.", width-8))+"\n\n"+
		fmt.Sprintf("object %d/%d · attached %d · j/k changes object", minInt(m.selected+1, len(indices)), len(indices), attached)+"\n\n"+
		m.selectedActionSheetCompact())
}

func (m app) selectedActionSheetCompact() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No selected object, so no action is implied.")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	available, unavailable := []string{}, []string{}
	for _, action := range eligibilityFor(s, m.clientAttach) {
		if action.available {
			available = append(available, action.name)
		} else {
			unavailable = append(unavailable, action.name+" ("+action.reason+")")
		}
	}
	rows := []string{
		titleStyle.Render("Eligibility · "+s.ID) + "  " + truncate(s.Project+" / "+s.Branch, 36),
		strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · "),
		titleStyle.Render("AVAILABLE NOW") + "  " + strings.Join(available, " · "),
		titleStyle.Render("UNAVAILABLE · EXPLAINED"),
		"  " + strings.Join(unavailable, " · "),
	}
	if s.Operation != "" {
		rows = append(rows, "progress  "+s.Operation)
	}
	return strings.Join(rows, "\n")
}

func (m app) actionObjectRows(limit int) string {
	indices := m.visibleIndices()
	if len(indices) == 0 {
		return mutedStyle.Render("No objects can be selected.")
	}
	rows := make([]string, 0, limit+1)
	start := m.selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > len(indices) {
		start = maxInt(0, len(indices)-limit)
	}
	end := start + limit
	if end > len(indices) {
		end = len(indices)
	}
	for position := start; position < end; position++ {
		s := m.sessions[indices[position]]
		presence, agent := sessionSignals(s)
		line := fmt.Sprintf("  %s %s · %s · %s · %s", padRight(s.Project+"/"+s.Branch, 25), s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, m.selectableLine(position, line, s))
	}
	if end < len(indices) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d more objects", len(indices)-end)))
	}
	return strings.Join(rows, "\n")
}

func (m app) selectedActionSheet() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return titleStyle.Render("Eligibility") + "\n" + mutedStyle.Render("No selected object, so no action is implied.")
	}
	s := m.sessions[idx]
	rows := []string{
		titleStyle.Render("Eligibility · " + s.ID),
		truncate(s.Project+" / "+s.Branch, 52),
		"",
		titleStyle.Render("AVAILABLE NOW"),
	}
	for _, action := range eligibilityFor(s, m.clientAttach) {
		if action.available {
			rows = append(rows, "  ✓ "+action.name+" — "+action.reason)
		}
	}
	rows = append(rows, "", titleStyle.Render("UNAVAILABLE · EXPLAINED"))
	for _, action := range eligibilityFor(s, m.clientAttach) {
		if !action.available {
			rows = append(rows, mutedStyle.Render("  × "+action.name+" — "+action.reason))
		}
	}
	if s.Operation != "" {
		rows = append(rows, "", "progress  "+s.Operation)
	}
	return strings.Join(rows, "\n")
}

func eligibilityFor(s session, clientAttach string) []actionEligibility {
	if warning := observationWarning(s); warning != "" {
		reason := strings.ToLower(warning) + " observations cannot authorize mutation"
		return []actionEligibility{
			{"inspect", true, "read-only facts remain available"},
			{"refresh " + warning + " facts", true, "obtain a current complete observation"},
			{"attach / switch", false, reason},
			{"detach this client", false, reason},
			{"recovery plan", false, reason},
			{"policy diff", false, reason},
			{"recreate", false, reason},
		}
	}
	attachable := s.Lifecycle == "ready" || (s.Lifecycle == "stopped" && s.Policy != "invalid")
	detachable := s.ID == clientAttach
	recoverable := s.Lifecycle == "missing" || s.Lifecycle == "unreachable" || s.Lifecycle == "stopped"
	recreatable := s.Policy == "outdated"
	return []actionEligibility{
		{"inspect", true, "read-only facts are always available"},
		{"attach / switch", attachable, ternaryReason(attachable, "host accepts a client", "lifecycle is "+s.Lifecycle)},
		{"detach this client", detachable, ternaryReason(detachable, "this client owns the attachment", "this client is attached elsewhere")},
		{"recovery plan", recoverable, ternaryReason(recoverable, "runtime needs bounded reconciliation", "runtime is already observed")},
		{"policy diff", s.Policy != "current", ternaryReason(s.Policy != "current", "snapshot differs from current policy", "snapshot is current")},
		{"recreate", recreatable, ternaryReason(recreatable, "new identity can adopt current policy", "no eligible policy drift")},
	}
}

func ternaryReason(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
