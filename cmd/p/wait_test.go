package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

const opID = "9b0e0000-0000-4000-8000-000000000001"

// A wait that answers running is repeated until the operation leaves
// running; the status it ends with decides the exit status.
func TestOperationWaitFollowsTheOperationToItsEnd(t *testing.T) {
	for _, test := range []struct {
		status string
		code   int
	}{
		{"completed", 0},
		{"blocked", 4},
		{"failed", 1},
		{"superseded", 1},
	} {
		t.Run(test.status, func(t *testing.T) {
			d := newFakeDaemon(t)
			inspectRunning(d)
			waits := 0
			handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
				waits++
				op := operation(r.ID, "session.create", "running")
				op.Phase = []string{"image-build", "image-build", "runtime-create"}[min(waits-1, 2)]
				if waits == 3 {
					op.Status = test.status
				}
				return api.OperationResult{Operation: op}, nil
			})
			run := runCLI(context.Background(), d, "operation", "wait", opID)
			if run.code != test.code || waits != 3 {
				t.Fatalf("exit %d after %d waits, want %d after 3: %s", run.code, waits, test.code, run.stderr)
			}
			for _, params := range d.params(api.OperationWait.Name) {
				var wait api.OperationWaitRequest
				if json.Unmarshal([]byte(params), &wait) != nil || wait.ID != opID || wait.Timeout != 20 {
					t.Errorf("wait asked %s, want 20 seconds for %s", params, opID)
				}
			}
		})
	}
}

// A wait asks for 20 seconds, and the call around it, from the connection
// to the reply, stays inside 25 seconds, under the daemon's 30-second
// request deadline.
func TestAWaitFitsItsCallBudget(t *testing.T) {
	budget := callBudget(api.OperationWait.Name)
	if budget <= waitSeconds*time.Second || budget > 25*time.Second {
		t.Fatalf("a %d-second wait has a %s budget", waitSeconds, budget)
	}
}

// Each phase a running operation reports is said once, on standard error.
func TestWaitingSaysEachNewPhase(t *testing.T) {
	d := newFakeDaemon(t)
	inspectRunning(d)
	waits := 0
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		waits++
		op := operation(r.ID, "session.create", "running")
		op.Phase = []string{"image-build", "image-build", "runtime-create", "completed"}[waits-1]
		if waits == 4 {
			op.Status = "completed"
		}
		return api.OperationResult{Operation: op}, nil
	})
	run := runCLI(context.Background(), d, "operation", "wait", opID)
	if run.code != 0 {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	contains(t, run.stderr, "image-build")
	contains(t, run.stderr, "runtime-create")
	if n := countLines(run.stderr, "image-build"); n != 1 {
		t.Errorf("phase image-build said %d times:\n%s", n, run.stderr)
	}
}

// Ctrl-C while waiting stops only the CLI: it exits 4, names the operation
// and how to wait for it again, and sends nothing more.
func TestInterruptWhileWaitingLeavesTheOperationRunning(t *testing.T) {
	d := newFakeDaemon(t)
	inspectRunning(d)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		interrupt()
		time.Sleep(50 * time.Millisecond)
		return api.OperationResult{Operation: operation(r.ID, "session.create", "running")}, nil
	})
	run := runCLI(ctx, d, "operation", "wait", opID)
	if run.code != 4 {
		t.Fatalf("exit %d, want 4: %s", run.code, run.stderr)
	}
	contains(t, run.stderr, "p operation wait "+opID)
	if got := d.methods(); !reflect.DeepEqual(got, []string{"operation.inspect", "operation.wait"}) {
		t.Errorf("calls after the interrupt: %v", got)
	}
}
