package attachment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/api"
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

// dialLease connects to the control socket and agrees the protocol. The
// connection then becomes the attachment's lease: losing it ends the
// attachment.
func dialLease(ctx context.Context, socket string) (*leaseRPC, error) {
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	r := &leaseRPC{conn: c, reader: bufio.NewReaderSize(c, api.MaxFrameBytes+1), lost: make(chan struct{}), responses: make(chan []byte, 1)}
	go func() {
		for {
			raw, e := r.reader.ReadSlice('\n')
			if e != nil || len(raw) > api.MaxFrameBytes {
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
	if _, err := api.Do(ctx, r, api.SystemHello, api.HelloRequest{Protocol: api.Protocol}); err != nil {
		r.fail()
		return nil, err
	}
	return r, nil
}
func (r *leaseRPC) fail() { r.once.Do(func() { close(r.lost); r.conn.Close() }) }

// Call implements api.Caller: one request at a time on the lease connection.
func (r *leaseRPC) Call(ctx context.Context, method string, params, result any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	timeout := 10 * time.Second
	if method == api.AttachmentConfirm.Name {
		timeout = 35 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	r.conn.SetWriteDeadline(deadline)
	r.sequence++
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", r.sequence, method, params}
	if err := json.NewEncoder(r.conn).Encode(request); err != nil {
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
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *api.RPCError   `json:"error"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != r.sequence {
		r.fail()
		return errors.New("invalid attachment RPC response")
	}
	if reply.Error != nil {
		return errors.New(reply.Error.Message)
	}
	return json.Unmarshal(reply.Result, result)
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
			if _, err := api.Do(ctx, r, api.AttachmentPing, api.AttachmentPingRequest{}); err != nil {
				r.fail()
				return
			}
		}
	}
}
