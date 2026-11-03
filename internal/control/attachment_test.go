package control

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConnectionLeaseCleanupOnceAndClosedOwnerRefused(t *testing.T) {
	c := &Connection{}
	var calls atomic.Int32
	if err := c.Own(func() { calls.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := c.Own(func() { t.Error("second owner ran") }); err == nil {
		t.Fatal("connection allowed two leases")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.close() }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cleanup not once", calls.Load())
	}
	if err := c.Own(func() {}); err == nil {
		t.Fatal("closed connection acquired lease")
	}
}
func TestOrdinaryHostProcessCannotClaimOrConfirmPresence(t *testing.T) {
	c := &Connection{peerPID: os.Getpid(), peerUID: uint32(os.Geteuid())}
	if c.trustedHelper() {
		t.Fatal("test/API process accepted as fixed helper")
	}
	ctx := context.WithValue(context.Background(), connectionKey{}, c)
	// A nil implementation is intentional: denial must happen before lifecycle.
	table := NewMethodTable()
	addAttachmentMethods(table, nil)
	h := table.Handler()
	for method, params := range map[string]string{"attachment.claim": `{"token":"` + strings.Repeat("a", 64) + `"}`, "attachment.confirm": `{"operation":"/1.0/operations/` + strings.Repeat("a", 36) + `"}`} {
		if _, err := h(ctx, method, json.RawMessage(params)); err == nil {
			t.Fatal("presence assertion accepted", method)
		}
	}
}
