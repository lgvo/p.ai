package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// testReason is the reason a test records for an operation it moves to
// status: blocked and failed operations need one.
func testReason(status string) string {
	if status == "blocked" || status == "failed" {
		return "internal"
	}
	return ""
}

// callMethod calls h with req as its params and decodes the result strictly
// as m's result type, as a client would.
func callMethod[Req api.Request, Res any](t *testing.T, ctx context.Context, h Handler, m api.Method[Req, Res], req Req) (Res, *api.Error) {
	t.Helper()
	params, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := h(ctx, m.Name, params)
	var res Res
	if rpcErr != nil {
		return res, rpcErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("%s result does not encode: %v", m.Name, err)
	}
	if err = strictjson.Decode(encoded, &res); err != nil {
		t.Fatalf("%s result %s does not decode as its result type: %v", m.Name, encoded, err)
	}
	return res, nil
}

// insertOperation records r through the journal, as a request would.
func insertOperation(ctx context.Context, s *Store, r operationRecord) error {
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	if err = j.insert(ctx, r); err != nil {
		return err
	}
	return j.commit(ctx)
}

// testHash is a change hash for store tests.
var testHash = strings.Repeat("c", 64)

// testEpoch is when the package's tests began.
var testEpoch = time.Now()

// testTag is a change tag of testHash. A negative d expired d ago; any other
// d names a tag that stays valid through the tests, the same tag for the same
// d, so a test can repeat a request exactly.
func testTag(d time.Duration) string {
	if d < 0 {
		return changeTag(time.Now().Add(d), testHash)
	}
	return changeTag(testEpoch.Add(time.Hour+d), testHash)
}

// noRecheck is a store Begin's verify that finds the plan unchanged.
func noRecheck(context.Context) error { return nil }
