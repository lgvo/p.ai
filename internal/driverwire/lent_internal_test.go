package driverwire

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// A call that lends what its protocol does not name, or passes another
// number of descriptors than its method takes, is a fault of the caller:
// it is refused before anything is sent.
func TestACallLendingWhatItsProtocolDoesNotNameIsRefusedBeforeItIsSent(t *testing.T) {
	p := *testProtocol
	p.Callbacks = map[string]bool{"open": true, "approve": true}
	p.Descriptors = map[string][]string{"apply": {"stream", "error stream"}}
	local, remote, err := SocketPair("test connection")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	cl := &Client{c: newConn(local, &p, false)}
	go cl.c.readLoop(cl)
	defer cl.Close()
	run := func(json.RawMessage) (any, error) { return nil, nil }
	for name, c := range map[string]struct {
		o    CallOptions
		want string
	}{
		"a callback the protocol does not have": {CallOptions{Callbacks: map[string]Callback{"delete": {Run: run}}, FDs: []*os.File{remote, remote}}, "no callback"},
		"two gates":                             {CallOptions{Gate: func() error { return nil }, Callbacks: map[string]Callback{"open": {Run: run, Gate: true}}, FDs: []*os.File{remote, remote}}, "one gate"},
		"another number of descriptors":         {CallOptions{FDs: []*os.File{remote}}, "descriptors"},
	} {
		if err := cl.CallWith(context.Background(), "apply", struct{}{}, c.o, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("a call with %s = %v, want a refusal naming %q", name, err, c.want)
		}
	}
	peer, err := net.FileConn(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if n, _ := peer.Read(make([]byte, 1)); n != 0 {
		t.Fatal("a refused call was sent")
	}
}
