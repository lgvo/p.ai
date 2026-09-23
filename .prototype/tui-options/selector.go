package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Domain selection remains in app; this adapter gives every selector the same
// Bubbles filtering, paging and rendering without storing a second selection.
type selectorItem struct {
	index        int
	text, search string
}

func (i selectorItem) FilterValue() string { return i.search }

type selectorDelegate struct{}

func (selectorDelegate) Height() int                         { return 1 }
func (selectorDelegate) Spacing() int                        { return 0 }
func (selectorDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (selectorDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	row := item.(selectorItem).text
	if index == m.Index() {
		row = selectedStyle.Render("› " + strings.TrimPrefix(row, "  "))
	}
	fmt.Fprint(w, truncate(row, m.Width()))
}

type selector struct {
	model           list.Model
	count, capacity int
}

func newSelector(items []selectorItem, query string, selected, width, capacity int) selector {
	capacity = max(1, capacity)
	data := make([]list.Item, len(items))
	for i, item := range items {
		data[i] = item
	}
	model := list.New(data, selectorDelegate{}, max(1, width), capacity)
	model.SetShowTitle(false)
	model.SetShowFilter(false)
	model.SetShowStatusBar(false)
	model.SetShowPagination(false)
	model.SetShowHelp(false)
	model.DisableQuitKeybindings()
	model.InfiniteScrolling = true
	model.Styles.NoItems = lipgloss.NewStyle()
	model.KeyMap.PrevPage = key.NewBinding(key.WithKeys("pgup"))
	model.KeyMap.NextPage = key.NewBinding(key.WithKeys("pgdown"))
	model.KeyMap.GoToStart = key.NewBinding(key.WithKeys("home"))
	model.KeyMap.GoToEnd = key.NewBinding(key.WithKeys("end"))
	if query != "" {
		model.SetFilterText(query)
	}
	model.Select(max(0, min(selected, len(model.VisibleItems())-1)))
	return selector{model: model, count: len(items), capacity: capacity}
}
func stringItems(values []string) []selectorItem {
	items := make([]selectorItem, len(values))
	for i, value := range values {
		items[i] = selectorItem{index: i, text: "  " + value, search: value}
	}
	return items
}
func (s selector) indices() []int {
	result := make([]int, 0, len(s.model.VisibleItems()))
	for _, item := range s.model.VisibleItems() {
		result = append(result, item.(selectorItem).index)
	}
	return result
}
func (s selector) rows(empty string, position bool) string {
	lines := strings.Split(s.model.View(), "\n")
	if len(lines) > s.capacity {
		lines = lines[:s.capacity]
	}
	rows := padContentRows(strings.Join(lines, "\n"), s.capacity)
	if len(s.model.VisibleItems()) == 0 {
		rows = padContentRows(empty, s.capacity)
	}
	if position {
		start, end := s.model.Paginator.GetSliceBounds(len(s.model.VisibleItems()))
		rows = mutedStyle.Render(listPosition(start, end, len(s.model.VisibleItems()), s.model.Index())) + "\n" + rows
	}
	return rows
}
func selectorNavigation(keyName string, selected, length, capacity int) (int, bool) {
	switch keyName {
	case "ctrl+u":
		return max(0, selected-max(1, capacity/2)), true
	case "ctrl+d":
		return max(0, min(length-1, selected+max(1, capacity/2))), true
	}
	var msg tea.KeyMsg
	switch keyName {
	case "up", "k":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down", "j":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		msg = tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		msg = tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		msg = tea.KeyMsg{Type: tea.KeyEnd}
	default:
		return selected, false
	}
	items := make([]selectorItem, length)
	s := newSelector(items, "", selected, 1, capacity)
	s.model, _ = s.model.Update(msg)
	return max(0, s.model.Index()), true
}
