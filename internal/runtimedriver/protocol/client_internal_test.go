package protocol

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// A read whose context ended returns its cancellation even when the
// driver's answer, an error, came before the call stopped waiting for it,
// an order no test outside the package can force: which came first decides
// nothing. The answer keeps its kinds.
func TestACancelledReadKeepsItsCancellationWhenTheDriversErrorCameFirst(t *testing.T) {
	local, remote, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	cl := &Client{c: newConn(local, false)}
	defer cl.Close()
	o := &outgoing{done: make(chan struct{})}
	id, err := cl.c.register(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cl.c.deliver(&message{JSONRPC: "2.0", ID: &id, Error: &wireError{Code: codeDriver, Message: "inventory: the backend went away",
		Data: &errorData{Kinds: []string{"unavailable"}}}})
	err = cl.await(ctx, id, methodInventory, o, nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("a cancelled Inventory whose error answer came first = %v, want the cancellation and the answer's kind", err)
	}
}
