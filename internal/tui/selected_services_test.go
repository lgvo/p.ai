package tui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

type observedServicesClient struct {
	calls    int
	contexts []context.Context
	result   control.ServiceResult
	err      error
}

func (c *observedServicesClient) Call(ctx context.Context, method string, p any) (json.RawMessage, error) {
	c.calls++
	c.contexts = append(c.contexts, ctx)
	if method != "session.services" {
		panic(method)
	}
	if c.err != nil {
		return nil, c.err
	}
	result := c.result
	result.V = 1
	result.UUID = p.(params)["uuid"].(string)
	return json.Marshal(result)
}
func serviceModel() Model {
	m := fixture()
	m.data.capabilities = map[string]bool{"session.services": true}
	return m
}

func TestSelectedServicesCoalescesCancelsAndFencesLateReplies(t *testing.T) {
	m := serviceModel()
	c := &observedServicesClient{}
	m.client = c
	m.syncSelectedServices(nil)
	first := m.selectedServices.generation
	m.move("j")
	m.syncSelectedServices(nil)
	if m.readSelectedServices(selectedServiceDue{first}) != nil {
		t.Fatal("obsolete debounce issued a request")
	}
	chosen := m.selectedServices.generation
	cmd := m.readSelectedServices(selectedServiceDue{chosen})
	if cmd == nil {
		t.Fatal("selected running session not read")
	}
	// No second request can launch before even a cancellation-ignoring old
	// transport finishes, so rapid selection cannot accumulate RPCs.
	oldID := m.selectedServices.uuid
	m.move("k")
	m.syncSelectedServices(nil)
	current := m.selectedServices.generation
	if m.readSelectedServices(selectedServiceDue{current}) != nil {
		t.Fatal("overlapping reads allowed")
	}
	oldReply := cmd().(selectedServiceDone)
	if c.contexts[0].Err() == nil {
		t.Fatal("selection did not cancel the old context")
	}
	m.acceptSelectedServices(oldReply)
	if m.selectedServices.uuid == oldID || m.selectedServices.state != "loading" {
		t.Fatal("late reply corrupted selected summary")
	}
	cmd = m.readSelectedServices(selectedServiceDue{current})
	if cmd == nil {
		t.Fatal("coalesced selected request not allowed after old read settled")
	}
	cancel := m.selectedServices.cancel
	m.acceptSelectedServices(oldReply) // duplicate old reply while new read runs
	if !m.selectedServices.inFlight || m.selectedServices.requestGeneration != current || m.selectedServices.cancel == nil {
		t.Fatal("stale reply cleared current request")
	}
	cancel()
	reply := cmd().(selectedServiceDone)
	m.acceptSelectedServices(reply)
	if m.selectedServices.state != "observed" || c.calls != 2 {
		t.Fatal("selected request count/observation wrong")
	}
}

func TestSelectedServicesLoadingErrorsStoppedAndRefresh(t *testing.T) {
	m := serviceModel()
	c := &observedServicesClient{err: errors.New("offline")}
	m.client = c
	m.syncSelectedServices(nil)
	if m.working || m.selectedServices.state != "loading" {
		t.Fatal("background read blocked browser or omitted loading")
	}
	cmd := m.readSelectedServices(selectedServiceDue{m.selectedServices.generation})
	m.acceptSelectedServices(cmd().(selectedServiceDone))
	if m.selectedServices.state != "unavailable" || m.selectedServices.diagnostic != "offline" {
		t.Fatal("read failure hidden")
	}
	m.syncSelectedServices(loaded{})
	if m.selectedServices.state != "unavailable" {
		t.Fatal("immediate refresh retried without coalescing")
	}
	m.selectedServices.at = time.Now().Add(-4 * time.Second)
	m.syncSelectedServices(loaded{})
	if m.selectedServices.state != "loading" {
		t.Fatal("periodic refresh never retries")
	}
	m.move("end")
	m.syncSelectedServices(nil)
	if m.selectedServices.state != "not running" || m.readSelectedServices(selectedServiceDue{m.selectedServices.generation}) != nil {
		t.Fatal("stopped session read/invented state")
	}
}

func TestServicePageObservationUpdatesBrowserAndMalformedReplyIsRejected(t *testing.T) {
	m := serviceModel()
	m.contextSession, _ = m.selectedSession()
	m.navigate("services")
	m.observeSelectedServices(control.ServiceResult{V: 1, UUID: m.contextSession.UUID, Services: []control.ProjectService{{Unit: "p-project-api.service", ActiveState: "inactive", SubState: "dead"}}})
	m.back()
	m.syncSelectedServices(nil)
	if m.selectedServices.state != "observed" || m.selectedServices.units[0].ActiveState != "inactive" {
		t.Fatal("Back discarded successful page observation")
	}
	// Reject a transport returning another session's body.
	bad := &fakeClient{fn: func(string, params) (any, error) { return control.ServiceResult{V: 1, UUID: "wrong"}, nil }}
	m.client = bad
	m.selectedServices.state = "loading"
	cmd := m.readSelectedServices(selectedServiceDue{m.selectedServices.generation})
	m.acceptSelectedServices(cmd().(selectedServiceDone))
	if m.selectedServices.state != "unavailable" {
		t.Fatal("mismatched UUID accepted")
	}
}

func TestStaleInventoryCancelsReadWithoutPhantomLoadingAndRecovers(t *testing.T) {
	m := serviceModel()
	m.client = &observedServicesClient{}
	m.syncSelectedServices(nil)
	cmd := m.readSelectedServices(selectedServiceDue{m.selectedServices.generation})
	pending := m.selectedServices.generation
	m.stale = true
	if m.syncSelectedServices(loaded{err: errors.New("offline")}) != nil {
		t.Fatal("stale inventory scheduled a read")
	}
	if m.selectedServices.state != "stale" || m.serviceSummary() != "unavailable · stale inventory" {
		t.Fatal("stale read displayed phantom loading")
	}
	m.acceptSelectedServices(cmd().(selectedServiceDone))
	if m.selectedServices.generation == pending || m.selectedServices.state != "stale" {
		t.Fatal("late cancelled read changed unavailable summary")
	}
	m.stale = false
	if m.syncSelectedServices(loaded{}) == nil || m.selectedServices.state != "loading" {
		t.Fatal("fresh inventory failed to restore selected read")
	}
}
