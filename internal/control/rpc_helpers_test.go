package control

import (
	"context"
	"encoding/json"
	"testing"

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
