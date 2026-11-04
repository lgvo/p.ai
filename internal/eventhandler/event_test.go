package eventhandler

import (
	"strings"
	"testing"
)

func ready() Event {
	return Event{Schema: EventSchema, ID: "e-1", Kind: "session.condition_changed", OccurredAt: "2026-09-23T00:00:00Z", Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
}

func TestEventValidation(t *testing.T) {
	if err := ready().Validate(); err != nil {
		t.Fatalf("a reduced event: %v", err)
	}
	for _, c := range []struct {
		name   string
		change func(*Event)
	}{
		{"another schema", func(e *Event) { e.Schema = "p.event/v2" }},
		{"an unknown kind", func(e *Event) { e.Kind = "session.secret" }},
		{"the wrong field", func(e *Event) { e.Fields = map[string]string{"phase": "running"} }},
		{"an unknown condition", func(e *Event) { e.Fields = map[string]string{"condition": "secret-value"} }},
		{"two fields", func(e *Event) { e.Fields["phase"] = "running" }},
		{"a negative attachment count", func(e *Event) {
			e.Kind, e.Fields = "session.attachment_changed", map[string]string{"count": "-1"}
		}},
		{"a time that is no time", func(e *Event) { e.OccurredAt = "now" }},
		{"a context with a newline", func(e *Event) { e.Branch = "main\nsecret" }},
	} {
		e := ready()
		c.change(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("an event with %s was accepted", c.name)
		}
	}
}

func TestParseEventRefusesAnUnreducedPayload(t *testing.T) {
	if _, err := ParseEvent([]byte(`{"schema":"p.event/v1","id":"e-2","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-one","fields":{"condition":"ready"},"secret":"x"}`)); err == nil || !strings.Contains(err.Error(), "unknown or inexact JSON field") {
		t.Fatalf("an unrecognized payload: %v", err)
	}
	if _, err := ParseEvent([]byte(strings.Repeat(" ", MaxEventBytes+1))); err == nil {
		t.Fatal("an event over 4 KiB was accepted")
	}
}
