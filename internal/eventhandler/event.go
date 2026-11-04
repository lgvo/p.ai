package eventhandler

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// EventSchema is the schema of the events P sends.
const EventSchema = "p.event/v1"

// MaxEventBytes bounds one event, encoded as JSON.
const MaxEventBytes = 4096

// Event is a reduced core event. Callers cannot pass arbitrary payloads or
// credentials through a handler.
type Event struct {
	Schema     string            `json:"schema"`
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	OccurredAt string            `json:"occurred_at"`
	Instance   string            `json:"instance"`
	Project    string            `json:"project,omitempty"`
	Session    string            `json:"session,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
}

var atomRE = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,256}$`)

var eventField = map[string]string{
	"session.condition_changed":  "condition",
	"session.attachment_changed": "count",
	"session.unattended_changed": "unattended_condition",
	"session.policy_changed":     "policy_condition",
	"operation.progress":         "phase",
}

var allowedValues = map[string]map[string]bool{
	"condition":            {"creating": true, "starting": true, "ready": true, "stopping": true, "stopped": true, "missing": true, "unreachable": true, "discarding": true, "deleting": true},
	"unattended_condition": {"none": true, "running": true, "attention": true, "idle": true, "failed": true, "unknown": true},
	"policy_condition":     {"current": true, "outdated": true, "invalid": true},
	"phase":                {"accepted": true, "running": true, "blocked": true, "completed": true, "failed": true, "cancelled": true},
}

func (e Event) Validate() error {
	if e.Schema != EventSchema {
		return errors.New("unsupported event schema")
	}
	field, known := eventField[e.Kind]
	if !atomRE.MatchString(e.ID) || !atomRE.MatchString(e.Instance) || !known {
		return errors.New("invalid event identity or kind")
	}
	if len(e.OccurredAt) > 64 {
		return errors.New("invalid event time")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return errors.New("invalid event time")
	}
	for _, value := range []string{e.Project, e.Session, e.Branch} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid event context")
		}
	}
	if len(e.Fields) != 1 {
		return errors.New("event requires exactly one reduced field")
	}
	value, ok := e.Fields[field]
	if !ok {
		return fmt.Errorf("event %s requires %s", e.Kind, field)
	}
	if field == "count" {
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || n > 1000000 || strconv.FormatUint(n, 10) != value {
			return errors.New("invalid attachment count")
		}
	} else if !allowedValues[field][value] {
		return fmt.Errorf("invalid %s value", field)
	}
	return nil
}

// ParseEvent reads one event strictly and validates it.
func ParseEvent(data []byte) (Event, error) {
	var event Event
	if len(data) > MaxEventBytes {
		return event, errors.New("event exceeds 4 KiB")
	}
	if err := strictjson.Decode(bytes.TrimSpace(data), &event); err != nil {
		return event, err
	}
	return event, event.Validate()
}
