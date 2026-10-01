package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the CLI transport rather than inspecting a timeout constant: a
// multi-session confirmation can legitimately outlast the ordinary API budget.
func TestProjectDeleteConfirmationWaitsForFreshInspection(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			served <- err
			return
		}
		if request.Method != "project.delete.confirm" || request.JSONRPC != "2.0" {
			served <- &net.OpError{Op: "unexpected confirmation request", Net: "unix"}
			return
		}
		time.Sleep(11 * time.Second)
		served <- json.NewEncoder(conn).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"result": map[string]any{"id": "fixture-confirmation", "status": "running"},
		})
	}()
	if err := run([]string{"api", socket, "project.delete.confirm", `{"v":1,"key":"fixture","project":"fixture","confirmation_token":"fixture"}`}); err != nil {
		t.Fatalf("confirmation did not wait for fresh inspection: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
