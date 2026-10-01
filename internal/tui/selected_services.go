package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/control"
)

// One selected-session read at a time. Selection churn cancels and coalesces
// requests; generation and UUID fencing also covers transports replying late.
type selectedServiceObservation struct {
	uuid, condition, state, diagnostic string
	units                              []control.ProjectService
	generation, requestGeneration      int
	inFlight                           bool
	cancel                             context.CancelFunc
	at                                 time.Time
}
type selectedServiceDue struct{ generation int }
type selectedServiceDone struct {
	generation int
	uuid       string
	result     control.ServiceResult
	err        error
}

func (m *Model) syncSelectedServices(msg tea.Msg) tea.Cmd {
	o := &m.selectedServices
	s, ok := m.selectedSession()
	visible := m.page == "sessions" || m.page == "details"
	if !visible || !ok {
		if o.cancel != nil {
			o.cancel()
			o.cancel = nil
		}
		if o.state == "loading" {
			o.generation++
			o.state = ""
		}
		return nil
	}
	if m.stale {
		if o.cancel != nil {
			o.cancel()
			o.cancel = nil
		}
		if o.state != "stale" {
			o.generation++
		}
		o.uuid, o.condition, o.state, o.units = s.UUID, s.Condition, "stale", nil
		o.diagnostic = "Session inventory is stale; service observations are unavailable."
		return nil
	}
	changed := o.uuid != s.UUID || o.condition != s.Condition || o.state == "" || o.state == "stale"
	_, refresh := msg.(loaded)
	if changed || refresh && !o.inFlight && time.Since(o.at) >= 3*time.Second {
		if o.cancel != nil {
			o.cancel()
			o.cancel = nil
		}
		o.generation++
		o.uuid, o.condition, o.units, o.diagnostic = s.UUID, s.Condition, nil, ""
		o.at = time.Now()
		switch {
		case s.Condition != "ready":
			o.state = "not running"
		case !m.data.capabilities["session.services"]:
			o.state = "unavailable"
		default:
			o.state = "loading"
		}
		if o.state == "loading" {
			return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return selectedServiceDue{o.generation} })
		}
	}
	if _, completed := msg.(selectedServiceDone); completed && o.state == "loading" && !o.inFlight {
		generation := o.generation
		return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return selectedServiceDue{generation} })
	}
	return nil
}

func (m *Model) readSelectedServices(v selectedServiceDue) tea.Cmd {
	o := &m.selectedServices
	if v.generation != o.generation || o.state != "loading" || o.inFlight || m.page != "sessions" && m.page != "details" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	o.cancel, o.inFlight, o.requestGeneration = cancel, true, o.generation
	id, generation, client := o.uuid, o.generation, m.client
	return func() tea.Msg {
		defer cancel()
		r, err := request[control.ServiceResult](ctx, client, "session.services", params{"v": 1, "uuid": id})
		if err == nil && (r.V != 1 || r.UUID != id) {
			err = fmt.Errorf("invalid service observation")
		}
		return selectedServiceDone{generation, id, r, err}
	}
}

func (m *Model) acceptSelectedServices(v selectedServiceDone) {
	o := &m.selectedServices
	if !o.inFlight || v.generation != o.requestGeneration {
		return
	}
	o.inFlight, o.cancel = false, nil
	if v.generation != o.generation || v.uuid != o.uuid || m.page != "sessions" && m.page != "details" {
		return
	}
	o.at = time.Now()
	if v.err != nil {
		o.state, o.diagnostic, o.units = "unavailable", v.err.Error(), nil
		return
	}
	o.state, o.units = "observed", v.result.Services
}

func (m *Model) observeSelectedServices(r control.ServiceResult) {
	o := &m.selectedServices
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	o.generation++
	o.uuid, o.condition, o.state, o.diagnostic = r.UUID, m.contextSession.Condition, "observed", ""
	o.units, o.at = r.Services, time.Now()
}
