package control

import (
	"context"
	"strings"
	"testing"
)

// TestSessionRPCErrorsTakeTheErrorShape checks the private session socket's
// error answers: the kind as message, a reason, no text from a cause.
func TestSessionRPCErrorsTakeTheErrorShape(t *testing.T) {
	s, id := statusFixture(t)
	ctx := context.Background()
	for _, test := range []struct{ name, line, want string }{
		{"unknown param", `{"jsonrpc":"2.0","id":1,"method":"session.identity","params":{"v":1,"uuid":"x"}}`,
			`"error":{"code":-32602,"message":"invalid","data":{"reason":"param","field":"uuid"}}`},
		{"wrong version", `{"jsonrpc":"2.0","id":2,"method":"session.identity","params":{"v":2}}`,
			`"error":{"code":-32602,"message":"invalid","data":{"reason":"param","field":"v"}}`},
		{"report with an id", `{"jsonrpc":"2.0","id":3,"method":"status.report","params":{"v":1,"condition":"idle","adapter":"a","adapter_version":"1"}}`,
			`"error":{"code":-32600,"message":"invalid_request","data":{"reason":"envelope"}}`},
		{"unknown method", `{"jsonrpc":"2.0","id":4,"method":"session.inspect","params":{"v":1}}`,
			`"error":{"code":-32601,"message":"method_not_found"}`},
		{"not JSON", `{"jsonrpc":"2.0","id":5,`, `"error":{"code":-32700,"message":"parse_error"}`},
	} {
		reply, _ := SessionReply(ctx, s, id, []byte(test.line))
		if !strings.Contains(string(reply), test.want) {
			t.Errorf("%s answered %s, want %s", test.name, reply, test.want)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=?`, id); err != nil {
		t.Fatal(err)
	}
	reply, _ := SessionReply(ctx, s, id, []byte(`{"jsonrpc":"2.0","id":6,"method":"session.identity","params":{"v":1}}`))
	want := `"error":{"code":-32015,"message":"not_found","data":{"reason":"session","target":"session/` + id + `"}}`
	if !strings.Contains(string(reply), want) {
		t.Errorf("a session that is not established answered %s, want %s", reply, want)
	}
}

// TestSessionRPCReadFailureIsNotAMissingSession checks that a failed read of
// the session answers by its cause instead of saying the session is gone.
func TestSessionRPCReadFailureIsNotAMissingSession(t *testing.T) {
	s, id := statusFixture(t)
	if _, err := s.db.Exec(`DROP TABLE session_unattended`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE sessions RENAME TO sessions_gone`); err != nil {
		t.Fatal(err)
	}
	reply, _ := SessionReply(context.Background(), s, id, []byte(`{"jsonrpc":"2.0","id":1,"method":"session.identity","params":{"v":1}}`))
	if want := `"error":{"code":-32603,"message":"internal","data":{"reason":"unexpected"}}`; !strings.Contains(string(reply), want) {
		t.Fatalf("a failed read answered %s, want %s", reply, want)
	}
}
