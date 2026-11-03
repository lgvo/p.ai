package api

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type refusingCaller struct{ t *testing.T }

func (c refusingCaller) Call(context.Context, string, any, any) error {
	c.t.Fatal("an invalid request reached the daemon")
	return nil
}

func TestDoValidatesBeforeSending(t *testing.T) {
	_, err := Do(context.Background(), refusingCaller{t}, SessionList, SessionListRequest{Limit: 9})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Kind != "invalid_params" || rpcErr.Code != -32602 {
		t.Fatalf("invalid request answered %v", err)
	}
}

// fakeDaemon answers each request line on one connection with the next reply.
func fakeDaemon(t *testing.T, replies ...string) (string, <-chan []string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	requests := make(chan []string, 1)
	go func() {
		var seen []string
		defer func() { requests <- seen }()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		for _, reply := range replies {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			seen = append(seen, strings.TrimSpace(line))
			if _, err = conn.Write([]byte(reply + "\n")); err != nil {
				return
			}
		}
	}()
	return socket, requests
}

func TestDialAgreesTheProtocolFirst(t *testing.T) {
	socket, requests := fakeDaemon(t,
		`{"jsonrpc":"2.0","id":1,"result":{"protocol":"p.control/v2","build_version":"b","instance_id":"i","capabilities":["git"]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"control_state":"ready"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !conn.Hello().Has(CapabilityGit) || conn.Hello().Has(CapabilityRuntime) {
		t.Fatalf("hello %+v", conn.Hello())
	}
	health, err := Do(ctx, conn, SystemHealth, HealthRequest{})
	if err != nil || health.ControlState != "ready" {
		t.Fatalf("health %+v %v", health, err)
	}
	conn.Close()
	sent := <-requests
	if len(sent) != 2 || sent[0] != `{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"protocol":"p.control/v2"}}` ||
		sent[1] != `{"jsonrpc":"2.0","id":2,"method":"system.health","params":{}}` {
		t.Fatalf("requests %q", sent)
	}
}

func TestDialRefusesADaemonThatDoesNotSpeakTheProtocol(t *testing.T) {
	socket, _ := fakeDaemon(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"kind":"unsupported_version","message":"protocol is not supported","data":{"reason":"protocol","supported":["p.control/v3"]}}}`)
	_, err := Dial(context.Background(), socket)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Kind != "unsupported_version" || rpcErr.Data == nil || rpcErr.Data.Supported[0] != "p.control/v3" {
		t.Fatalf("Dial answered %v", err)
	}
}

func TestCallRefusesAMismatchedOrUntypedReply(t *testing.T) {
	hello := `{"jsonrpc":"2.0","id":1,"result":{"protocol":"p.control/v2","build_version":"","instance_id":"","capabilities":[]}}`
	for _, reply := range []string{
		`{"jsonrpc":"2.0","id":7,"result":{"control_state":"ready"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"control_state":"ready","v":1}}`,
		`{"jsonrpc":"2.0","id":2}`,
	} {
		socket, _ := fakeDaemon(t, hello, reply)
		conn, err := Dial(context.Background(), socket)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Do(context.Background(), conn, SystemHealth, HealthRequest{}); err == nil {
			t.Errorf("reply %s accepted", reply)
		}
		conn.Close()
	}
}
