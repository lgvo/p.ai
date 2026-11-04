package control

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// checkIntent fails unless op, as accepted and as the journal reads it back,
// records want as what its caller named.
func checkIntent(t *testing.T, s *Store, op api.Operation, want api.OperationIntent) {
	t.Helper()
	if op.Intent != want {
		t.Fatalf("%s accepted with intent %+v, want %+v", op.Kind, op.Intent, want)
	}
	read, err := s.GetOperation(context.Background(), op.ID)
	if err != nil || read.Intent != want {
		t.Fatalf("%s reads back intent %+v %v, want %+v", op.Kind, read.Intent, err, want)
	}
}
