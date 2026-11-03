package daemon

import (
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// wantRefusal fails the test unless err is a refusal of kind with reason and
// target.
func wantRefusal(t *testing.T, what string, err error, kind api.ErrorKind, reason, target string) {
	t.Helper()
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != kind || refusal.Reason != reason || refusal.Target != target {
		t.Fatalf("%s answered %v, want %s %s %s", what, err, kind, reason, target)
	}
}
