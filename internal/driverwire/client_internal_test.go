package driverwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// The test protocol's kinds: a contract's sentinels, and no typed errors.
var (
	errTestUnavailable    = errors.New("the test backend cannot be reached")
	errTestUnknownOutcome = errors.New("the test effect may or may not have happened")
	testSentinels         = Sentinels{{Name: "unavailable", Err: errTestUnavailable}, {Name: "unknown_outcome", Err: errTestUnknownOutcome}}
)

type testData struct {
	Kinds     []string `json:"kinds"`
	Supported []string `json:"supported,omitempty"`
}

type testKinds struct{}

func (testKinds) Data(err error) any { return &testData{Kinds: testSentinels.Names(err)} }

func (testKinds) Decode(raw json.RawMessage) (ErrorData, error) {
	var d testData
	if err := strictjson.Decode(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (d *testData) Errors(message string) ([]error, error) {
	var kinds []error
	for _, name := range d.Kinds {
		kind := testSentinels.Kind(name)
		if kind == nil {
			return nil, fmt.Errorf("unknown kind %q: %s", name, message)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

var testProtocol = &Protocol{Name: "test driver", Version: "p.test-driver/v1", Effects: map[string]bool{"apply": true},
	Unavailable: errTestUnavailable, UnknownOutcome: errTestUnknownOutcome, Kinds: testKinds{}}

// A read whose context ended returns its cancellation even when the
// driver's answer, an error, came before the call stopped waiting for it,
// an order no test outside the package can force: which came first decides
// nothing. The answer keeps its kinds.
func TestACancelledReadKeepsItsCancellationWhenTheDriversErrorCameFirst(t *testing.T) {
	local, remote, err := SocketPair("test connection")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	cl := &Client{c: newConn(local, testProtocol, false)}
	defer cl.Close()
	o := &outgoing{done: make(chan struct{})}
	id, err := cl.c.register(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cl.c.deliver(&message{JSONRPC: "2.0", ID: &id, Error: &wireError{Code: codeDriver, Message: "inventory: the backend went away",
		decoded: &testData{Kinds: []string{"unavailable"}}}})
	err = cl.await(ctx, id, "inventory", o, nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errTestUnavailable) {
		t.Fatalf("a cancelled Inventory whose error answer came first = %v, want the cancellation and the answer's kind", err)
	}
}
