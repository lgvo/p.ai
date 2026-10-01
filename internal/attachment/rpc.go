package attachment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

type leaseRPC struct {
	conn      net.Conn
	reader    *bufio.Reader
	mu        sync.Mutex
	sequence  int
	lost      chan struct{}
	once      sync.Once
	responses chan []byte
}

func dialLease(ctx context.Context, socket string) (*leaseRPC, error) {
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	r := &leaseRPC{conn: c, reader: bufio.NewReaderSize(c, control.MaxFrameBytes+1), lost: make(chan struct{}), responses: make(chan []byte, 1)}
	go func() {
		for {
			raw, e := r.reader.ReadSlice('\n')
			if e != nil || len(raw) > control.MaxFrameBytes {
				r.fail()
				return
			}
			copyRaw := append([]byte(nil), raw...)
			select {
			case r.responses <- copyRaw:
			case <-r.lost:
				return
			}
		}
	}()
	return r, nil
}
func (r *leaseRPC) fail() { r.once.Do(func() { close(r.lost); r.conn.Close() }) }
func (r *leaseRPC) call(ctx context.Context, method string, params, dest any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	timeout := 10 * time.Second
	if method == "attachment.confirm" {
		timeout = 35 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	r.conn.SetWriteDeadline(deadline)
	r.sequence++
	if err := json.NewEncoder(r.conn).Encode(map[string]any{"jsonrpc": "2.0", "id": r.sequence, "method": method, "params": params}); err != nil {
		r.fail()
		return err
	}
	var raw []byte
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case raw = <-r.responses:
	case <-ctx.Done():
		r.fail()
		return ctx.Err()
	case <-timer.C:
		r.fail()
		return errors.New("attachment RPC timeout")
	case <-r.lost:
		return errors.New("attachment RPC connection lost")
	}
	var reply struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      int               `json:"id"`
		Result  json.RawMessage   `json:"result"`
		Error   *control.RPCError `json:"error"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != r.sequence {
		r.fail()
		return errors.New("invalid attachment RPC response")
	}
	if reply.Error != nil {
		return errors.New(reply.Error.Message)
	}
	if dest != nil {
		return json.Unmarshal(reply.Result, dest)
	}
	return nil
}
func (r *leaseRPC) monitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.lost:
			return
		case <-ticker.C:
			if r.call(ctx, "attachment.ping", map[string]int{"v": 1}, nil) != nil {
				r.fail()
				return
			}
		}
	}
}
