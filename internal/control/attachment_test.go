package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
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
		_, err := h(ctx, method, json.RawMessage(params))
		if want := api.NewError(api.KindInvalidRequest, api.ReasonConnection); !reflect.DeepEqual(err, want) {
			t.Fatalf("%s from an ordinary process answered %v, want %v", method, err, want)
		}
	}
}

func TestAttachmentConfirmationRefusals(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	missing := "11111111-2222-3333-4444-555555555555"
	_, _, _, err := s.ConfirmAttachment(ctx, missing)
	if answer := answerError(ctx, err); answer.Kind() != api.KindNotFound || answer.Reason() != api.ReasonSession || answer.Target() != api.TargetSession(missing) {
		t.Fatalf("confirmation for a missing session answered %v", answer)
	}
	if err := (&Connection{dedicated: true}).Own(func() {}); answerError(ctx, err).Kind() != api.KindInvalidRequest || !errors.Is(err, ErrConflict) {
		t.Fatalf("a second lease on a connection answered %v", err)
	}
}
