// Package attachment implements the trusted, temporary host attachment helper.
package attachment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lgvo/p.ai/internal/control"
)

type nativeOperation struct {
	StatusCode int `json:"status_code"`
	Metadata   struct {
		FDs    map[string]string `json:"fds"`
		Return *int              `json:"return"`
	} `json:"metadata"`
}
type nativeResponse struct {
	Type      string          `json:"type"`
	Operation string          `json:"operation"`
	Error     string          `json:"error"`
	Metadata  nativeOperation `json:"metadata"`
}
type nativeChannel struct {
	client                     *http.Client
	transport                  *http.Transport
	socket, project, operation string
	control, data              *websocket.Conn
	controlDone                chan struct{}
	closeOnce                  sync.Once
	controlWrite               sync.Mutex
	operationEnded             atomic.Bool
	cancelOperation            context.CancelFunc
	observerDone               chan struct{}
}

var operationPath = regexp.MustCompile(`^/1\.0/operations/[0-9a-f-]{36}$`)

func newNative(launch control.AttachmentLaunch) (*nativeChannel, error) {
	if launch.V != 1 || launch.Socket == "" || launch.Spec.Project == "" || !regexp.MustCompile(`^p-[0-9a-f-]{36}$`).MatchString(launch.Spec.Instance) || len(launch.Spec.Argv) != 1 || launch.Spec.Argv[0] != "/usr/libexec/p/attach" {
		return nil, errors.New("invalid fixed attachment launch")
	}
	n := &nativeChannel{socket: launch.Socket, project: launch.Spec.Project, controlDone: make(chan struct{}), observerDone: make(chan struct{})}
	n.transport = &http.Transport{DialContext: n.dial, ResponseHeaderTimeout: 5 * time.Second}
	n.client = &http.Client{Transport: n.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Incus redirect refused") }}
	return n, nil
}
func (n *nativeChannel) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", n.socket)
}
func (n *nativeChannel) request(ctx context.Context, method, path string, body any) (nativeResponse, error) {
	var result nativeResponse
	var reader io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return result, e
		}
		reader = bytes.NewReader(raw)
	}
	relative, err := url.ParseRequestURI(path)
	if err != nil {
		return result, err
	}
	u := url.URL{Scheme: "http", Host: "incus", Path: relative.Path, RawQuery: relative.RawQuery}
	q := u.Query()
	q.Set("project", n.project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return result, err
	}
	if len(raw) > 65536 || json.Unmarshal(raw, &result) != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Type == "error" {
		return result, errors.New("Incus attachment request failed")
	}
	return result, nil
}
func (n *nativeChannel) websocket(ctx context.Context, secret string) (*websocket.Conn, error) {
	if secret == "" || len(secret) > 256 {
		return nil, errors.New("missing Incus websocket secret")
	}
	u := url.URL{Scheme: "ws", Host: "incus", Path: n.operation + "/websocket"}
	q := u.Query()
	q.Set("project", n.project)
	q.Set("secret", secret)
	u.RawQuery = q.Encode()
	d := websocket.Dialer{NetDialContext: n.dial, HandshakeTimeout: 5 * time.Second}
	c, resp, err := d.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return c, err
}
func (n *nativeChannel) establish(ctx context.Context, launch control.AttachmentLaunch, width, height int) error {
	request := struct {
		Command     []string          `json:"command"`
		Wait        bool              `json:"wait-for-websocket"`
		Interactive bool              `json:"interactive"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Environment map[string]string `json:"environment"`
	}{launch.Spec.Argv, true, true, width, height, map[string]string{"TERM": "xterm-256color"}}
	result, err := n.request(ctx, "POST", "/1.0/instances/"+launch.Spec.Instance+"/exec", request)
	if err != nil {
		return err
	}
	if result.Type != "async" || !operationPath.MatchString(result.Operation) {
		return errors.New("invalid Incus exec operation")
	}
	n.operation = result.Operation
	// Capture Incus's own completion witness before allowing execution. Its
	// flushed wait response pins the operation even after registry expiry.
	if err = n.observeOperation(ctx); err != nil {
		return err
	}
	n.control, err = n.websocket(ctx, result.Metadata.Metadata.FDs["control"])
	if err != nil {
		return err
	}
	n.data, err = n.websocket(ctx, result.Metadata.Metadata.FDs["0"])
	if err != nil {
		return err
	}
	n.control.SetReadLimit(65536)
	n.data.SetReadLimit(1 << 20)
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	proof := hex.EncodeToString(nonce[:])
	pong := make(chan struct{}, 1)
	n.control.SetPongHandler(func(value string) error {
		if value == proof {
			select {
			case pong <- struct{}{}:
			default:
			}
		}
		return nil
	})
	go func() {
		defer close(n.controlDone)
		for {
			if _, _, e := n.control.ReadMessage(); e != nil {
				return
			}
		}
	}()
	// Incus 7.4 cmd/incusd/instance_exec.go starts the control NextReader loop
	// only after instance.Exec succeeds. Its RFC6455 ping handler cannot send
	// this nonce's pong until that loop runs. The upgrade alone is NOT proof.
	if err = n.control.WriteControl(websocket.PingMessage, []byte(proof), time.Now().Add(5*time.Second)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-n.controlDone:
		return errors.New("attachment exited before confirmation")
	case <-n.observerDone:
		return errors.New("attachment completion observer ended before confirmation")
	case <-pong:
	}
	done, err := n.ended(ctx)
	if err != nil {
		return err
	}
	if done {
		return errors.New("attachment exited before confirmation")
	}
	select {
	case <-n.observerDone:
		return errors.New("native completion observer ended before confirmation")
	default:
	}
	return nil
}
func (n *nativeChannel) ended(ctx context.Context) (bool, error) {
	if n.operation == "" || n.operationEnded.Load() {
		return true, nil
	}
	r, err := n.request(ctx, "GET", n.operation, nil)
	if err != nil {
		if n.operationEnded.Load() {
			return true, nil
		}
		return false, err
	}
	done := r.Metadata.StatusCode >= 200
	if done {
		n.operationEnded.Store(true)
	}
	return done, nil
}
func (n *nativeChannel) closeChannel() {
	n.closeOnce.Do(func() {
		if n.control != nil {
			n.control.Close()
		}
		if n.data != nil {
			n.data.Close()
		}
	})
}
func (n *nativeChannel) resize(width, height int) error {
	if width < 1 || height < 1 || width > 65535 || height > 65535 {
		return errors.New("invalid terminal size")
	}
	n.controlWrite.Lock()
	defer n.controlWrite.Unlock()
	n.control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return n.control.WriteJSON(map[string]any{"command": "window-resize", "args": map[string]string{"width": strconv.Itoa(width), "height": strconv.Itoa(height)}})
}
func (n *nativeChannel) teardown(ctx context.Context) error {
	n.closeChannel()
	defer func() {
		if n.cancelOperation != nil {
			n.cancelOperation()
		}
		n.transport.CloseIdleConnections()
	}()
	for {
		done, err := n.ended(ctx)
		if err == nil && done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("attachment completion unavailable: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Incus 7.4 operationWaitGet captures the operation pointer and flushes 200
// headers before waiting. Receiving those headers is required before sockets
// are connected; Body remains open without a client timeout for the session.
func (n *nativeChannel) observeOperation(opening context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	n.cancelOperation = cancel
	stopOpening := context.AfterFunc(opening, cancel)
	defer stopOpening()
	u := url.URL{Scheme: "http", Host: "incus", Path: n.operation + "/wait"}
	q := u.Query()
	q.Set("project", n.project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: n.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Incus redirect refused") }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || opening.Err() != nil {
		response.Body.Close()
		return errors.New("native completion observer unavailable")
	}
	go func() {
		defer close(n.observerDone)
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || len(raw) > 65536 {
			return
		}
		var result nativeResponse
		if json.Unmarshal(raw, &result) == nil && result.Type == "sync" && result.Metadata.StatusCode >= 200 {
			n.operationEnded.Store(true)
		}
	}()
	return nil
}
