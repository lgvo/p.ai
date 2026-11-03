package incus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// attachProgram is the session host's fixed attach program: the image's
// link to the p-attach asset, which attaches its terminal to the host.
const attachProgram = "/usr/libexec/p/attach"

// terminalOpenWait bounds the opening of a terminal's channel.
const terminalOpenWait = 15 * time.Second

// terminalEndWait bounds how long an ending terminal lets its consumer read
// the rest of the output before it hangs up, and how long it then waits for
// Incus to prove that its exec ended before it ends unproven.
var terminalEndWait = 5 * time.Second

// An Incus operation's path, with its ID, and a terminal's ID.
var (
	operationPath     = regexp.MustCompile(`^/1\.0/operations/([0-9a-f-]{36})$`)
	terminalIDPattern = regexp.MustCompile(`^[0-9a-f-]{36}$`)
)

// OpenTerminal attaches a PTY of size to the session host of the running
// runtime at t.At: an interactive Incus exec of the fixed attach program.
// It first checks that the host is ready and that the image's links to the
// host's unit and attach program resolve to the session's assets of those
// roles, unchanged, and checks the runtime again after those reads. A
// refusal's text is a fixed code: native errors and guest content stay out
// of it. An open that fails once Incus admitted the exec, without proof
// that the exec ended, returns a *runtimedriver.UnendedTerminal.
func (d *Driver) OpenTerminal(ctx context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	if !validTerminalSize(size) {
		return nil, errors.New("invalid terminal size")
	}
	b := d.backend
	s, err := b.NativeSession(t.Session)
	if err != nil {
		return nil, err
	}
	if err := b.attachable(ctx, s, t.At, "initial-"); err != nil {
		return nil, err
	}
	files := attachmentFileAPI{&unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}}
	if err := verifyAttachAssets(ctx, files, t.Session.Assets); err != nil {
		return nil, err
	}
	// Inspect again after the asset reads; it checks current Incus ownership.
	if err := b.attachable(ctx, s, t.At, "final-"); err != nil {
		return nil, err
	}
	opening, cancel := context.WithTimeout(ctx, terminalOpenWait)
	defer cancel()
	return b.openTerminal(opening, b.name(s), size)
}

func validTerminalSize(size runtimedriver.Size) bool {
	return size.Width >= 1 && size.Height >= 1 && size.Width <= 65535 && size.Height <= 65535
}

// attachable refuses a runtime that cannot be read, is another incarnation
// than at, or whose session host is not running and ready. Its codes start
// with stage.
func (b *Backend) attachable(ctx context.Context, s Session, at runtimedriver.Incarnation, stage string) error {
	h, err := b.ObserveHost(ctx, s)
	switch {
	case err != nil:
		return attachmentFailure(stage+"host-observation", err)
	case h.Exists && at != "" && incarnationOf(h.Observation) != at:
		return attachmentFailure(stage+"incarnation", runtimedriver.ErrNotReady)
	case !h.Exists || h.Status != "Running" || !h.Ready:
		return attachmentFailure(stage+"host-not-ready", runtimedriver.ErrNotReady)
	}
	return nil
}

// terminal is an attachment: an interactive Incus exec of the attach
// program with its data and control websockets. Incus's record of the exec
// is its completion witness: a wait on it, armed before the exec starts,
// and, once that wait is lost, reads of it.
type terminal struct {
	project, socket string
	transport       *http.Transport
	client          *http.Client
	// operation is the exec's operation path, and id its ID.
	operation, id string

	data, control *websocket.Conn
	dataWrite     sync.Mutex
	controlWrite  sync.Mutex
	controlDone   chan struct{} // closes once the control websocket closed

	// reading guards the output; outputDone closes once it ended.
	reading    sync.Mutex
	reader     io.Reader
	readErr    error
	outputDone chan struct{}

	// witnessDone closes once the wait on the exec returned; it is nil
	// until the wait is armed. execEnded is set once Incus reported the
	// exec finished.
	witnessDone chan struct{}
	stopWitness context.CancelFunc
	execEnded   atomic.Bool

	closing               chan struct{}
	closeOnce, hangUpOnce sync.Once
	ended                 chan runtimedriver.Completion
	endWait               time.Duration // terminalEndWait when it opened
}

// operationResponse is Incus's response about an operation, and
// execOperation an exec's operation.
type operationResponse struct {
	Type      string        `json:"type"`
	Operation string        `json:"operation"`
	Metadata  execOperation `json:"metadata"`
}

type execOperation struct {
	StatusCode int `json:"status_code"`
	Metadata   struct {
		FDs map[string]string `json:"fds"`
	} `json:"metadata"`
}

func refuseIncusRedirect(*http.Request, []*http.Request) error {
	return errors.New("Incus redirect refused")
}

// openTerminal opens the exec of the attach program on instance. When it
// fails, it hangs up what it opened and gives Incus terminalEndWait to end
// the exec. An exec Incus admitted whose end it did not prove may still
// run: the error is then a *runtimedriver.UnendedTerminal naming it, for
// core to settle.
func (b *Backend) openTerminal(ctx context.Context, instance string, size runtimedriver.Size) (*terminal, error) {
	t := &terminal{project: b.config.Project, socket: b.config.UserSocket, controlDone: make(chan struct{}), outputDone: make(chan struct{}),
		closing: make(chan struct{}), ended: make(chan runtimedriver.Completion, 1), endWait: terminalEndWait}
	t.transport = &http.Transport{DialContext: t.dial, ResponseHeaderTimeout: 5 * time.Second}
	t.client = &http.Client{Transport: t.transport, Timeout: 10 * time.Second, CheckRedirect: refuseIncusRedirect}
	if err := t.establish(ctx, instance, size); err != nil {
		t.hangUp()
		ended := t.awaitEnd()
		t.release()
		if !ended {
			return nil, &runtimedriver.UnendedTerminal{ID: t.id, Err: err}
		}
		return nil, err
	}
	go t.watch()
	return t, nil
}

func (t *terminal) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", t.socket)
}

func (t *terminal) request(ctx context.Context, method, path string, body any) (operationResponse, error) {
	var result operationResponse
	var reader io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return result, e
		}
		reader = bytes.NewReader(raw)
	}
	u := url.URL{Scheme: "http", Host: "incus", Path: path}
	q := u.Query()
	q.Set("project", t.project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
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

func (t *terminal) websocket(ctx context.Context, secret string) (*websocket.Conn, error) {
	if secret == "" || len(secret) > 256 {
		return nil, errors.New("missing Incus websocket secret")
	}
	u := url.URL{Scheme: "ws", Host: "incus", Path: t.operation + "/websocket"}
	q := u.Query()
	q.Set("project", t.project)
	q.Set("secret", secret)
	u.RawQuery = q.Encode()
	d := websocket.Dialer{NetDialContext: t.dial, HandshakeTimeout: 5 * time.Second}
	c, resp, err := d.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return c, err
}

// establish creates the exec, which Incus starts only once both its
// websockets are connected, arms the witness, connects the websockets and
// proves that the exec started and still runs.
func (t *terminal) establish(ctx context.Context, instance string, size runtimedriver.Size) error {
	request := struct {
		Command     []string          `json:"command"`
		Wait        bool              `json:"wait-for-websocket"`
		Interactive bool              `json:"interactive"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Environment map[string]string `json:"environment"`
	}{[]string{attachProgram}, true, true, size.Width, size.Height, map[string]string{"TERM": "xterm-256color"}}
	result, err := t.request(ctx, http.MethodPost, "/1.0/instances/"+instance+"/exec", request)
	if err != nil {
		return err
	}
	match := operationPath.FindStringSubmatch(result.Operation)
	if result.Type != "async" || match == nil {
		return errors.New("invalid Incus exec operation")
	}
	t.operation, t.id = result.Operation, match[1]
	// Capture Incus's own completion witness before allowing execution. Its
	// flushed wait response pins the operation even after registry expiry.
	if err = t.armWitness(ctx); err != nil {
		return err
	}
	if t.control, err = t.websocket(ctx, result.Metadata.Metadata.FDs["control"]); err != nil {
		return err
	}
	if t.data, err = t.websocket(ctx, result.Metadata.Metadata.FDs["0"]); err != nil {
		return err
	}
	t.control.SetReadLimit(65536)
	t.data.SetReadLimit(1 << 20)
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	proof := hex.EncodeToString(nonce[:])
	pong := make(chan struct{}, 1)
	t.control.SetPongHandler(func(value string) error {
		if value == proof {
			select {
			case pong <- struct{}{}:
			default:
			}
		}
		return nil
	})
	go func() {
		defer close(t.controlDone)
		for {
			if _, _, e := t.control.ReadMessage(); e != nil {
				return
			}
		}
	}()
	// Incus 7.4 cmd/incusd/instance_exec.go starts the control NextReader loop
	// only after instance.Exec succeeds. Its RFC6455 ping handler cannot send
	// this nonce's pong until that loop runs. The upgrade alone is NOT proof.
	if err = t.control.WriteControl(websocket.PingMessage, []byte(proof), time.Now().Add(5*time.Second)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.controlDone:
		return errors.New("attachment exited while opening")
	case <-t.witnessDone:
		return errors.New("attachment completion witness ended while opening")
	case <-pong:
	}
	done, err := t.operationFinished(ctx)
	if err != nil {
		return err
	}
	if done {
		return errors.New("attachment exited while opening")
	}
	select {
	case <-t.witnessDone:
		return errors.New("attachment completion witness ended while opening")
	default:
	}
	return nil
}

// armWitness waits on the exec's operation. Incus 7.4 operationWaitGet
// captures the operation pointer and flushes 200 headers before waiting.
// Receiving those headers is required before the websockets are connected;
// the body stays open, without a client timeout, until the exec ends.
func (t *terminal) armWitness(opening context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	t.stopWitness = cancel
	stopOpening := context.AfterFunc(opening, cancel)
	defer stopOpening()
	u := url.URL{Scheme: "http", Host: "incus", Path: t.operation + "/wait"}
	q := u.Query()
	q.Set("project", t.project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: t.transport, CheckRedirect: refuseIncusRedirect}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || opening.Err() != nil {
		response.Body.Close()
		return errors.New("attachment completion witness unavailable")
	}
	t.witnessDone = make(chan struct{})
	go func() {
		defer close(t.witnessDone)
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || len(raw) > 65536 {
			return
		}
		var result operationResponse
		if json.Unmarshal(raw, &result) == nil && result.Type == "sync" && result.Metadata.StatusCode >= 200 {
			t.execEnded.Store(true)
		}
	}()
	return nil
}

// operationFinished reads the exec's operation and reports whether it
// finished.
func (t *terminal) operationFinished(ctx context.Context) (bool, error) {
	if t.operation == "" || t.execEnded.Load() {
		return true, nil
	}
	r, err := t.request(ctx, http.MethodGet, t.operation, nil)
	if err != nil {
		if t.execEnded.Load() {
			return true, nil
		}
		return false, err
	}
	done := r.Metadata.StatusCode >= 200
	if done {
		t.execEnded.Store(true)
	}
	return done, nil
}

// watch ends the terminal when Close hangs it up, when the witness answers
// or is lost, or when the exec's output ended or Incus closed the control
// channel, as it does when the attach program exits. Incus finishes an
// exec, and so proves its end, only once the client closed its websockets:
// watch hangs up, then waits for the proof and delivers the end. Before it
// hangs up it lets the consumer read the output to its end, at most for
// endWait; a channel whose witness is lost it hangs up at once.
func (t *terminal) watch() {
	lost := false
	select {
	case <-t.closing:
	case <-t.witnessDone:
		lost = !t.execEnded.Load()
	case <-t.controlDone:
	case <-t.outputDone:
	}
	if !lost {
		timer := time.NewTimer(t.endWait)
		select {
		case <-t.outputDone:
		case <-t.closing:
		case <-timer.C:
		}
		timer.Stop()
	}
	t.hangUp()
	proven := t.awaitEnd()
	t.ended <- runtimedriver.Completion{Proven: proven}
	close(t.ended)
	t.release()
}

// awaitEnd waits, at most endWait, for Incus to prove that the exec
// ended: the witness's answer or, once the witness is lost, the operation
// read as finished.
func (t *terminal) awaitEnd() bool {
	ctx, cancel := context.WithTimeout(context.Background(), t.endWait)
	defer cancel()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	witness := t.witnessDone
	for {
		if t.execEnded.Load() {
			return true
		}
		if witness == nil {
			if done, err := t.operationFinished(ctx); err == nil && done {
				return true
			}
		}
		select {
		case <-witness:
			witness = nil
		case <-poll.C:
		case <-ctx.Done():
			return t.execEnded.Load()
		}
	}
}

// hangUp closes both websockets without a close frame, as a lost client
// does: Incus then ends the exec.
func (t *terminal) hangUp() {
	t.hangUpOnce.Do(func() {
		if t.control != nil {
			t.control.Close()
		}
		if t.data != nil {
			t.data.Close()
		}
	})
}

// release hangs up, stops the witness and closes the idle connections to
// Incus.
func (t *terminal) release() {
	t.hangUp()
	if t.stopWitness != nil {
		t.stopWitness()
	}
	t.transport.CloseIdleConnections()
}

func (t *terminal) ID() string { return t.id }

func (t *terminal) Ended() <-chan runtimedriver.Completion { return t.ended }

// Read returns the terminal's output, and io.EOF once Incus ended it or the
// channel closed.
func (t *terminal) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.reading.Lock()
	defer t.reading.Unlock()
	for t.readErr == nil {
		if t.reader == nil {
			kind, r, err := t.data.NextReader()
			switch {
			// Incus sends a text frame as its write barrier/end-of-stream marker.
			case err != nil || kind == websocket.TextMessage:
				t.endOutput(io.EOF)
			case kind != websocket.BinaryMessage:
				t.endOutput(errors.New("invalid Incus terminal frame"))
			default:
				t.reader = r
			}
			continue
		}
		n, err := t.reader.Read(p)
		if err != nil {
			t.reader = nil
			if err != io.EOF {
				t.endOutput(io.EOF)
			}
		}
		if n > 0 {
			return n, nil
		}
	}
	return 0, t.readErr
}

func (t *terminal) endOutput(err error) {
	t.readErr = err
	close(t.outputDone)
}

// Write sends input to the terminal.
func (t *terminal) Write(p []byte) (int, error) {
	t.dataWrite.Lock()
	defer t.dataWrite.Unlock()
	t.data.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := t.data.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize resizes the PTY through the control channel.
func (t *terminal) Resize(size runtimedriver.Size) error {
	if !validTerminalSize(size) {
		return errors.New("invalid terminal size")
	}
	t.controlWrite.Lock()
	defer t.controlWrite.Unlock()
	t.control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return t.control.WriteJSON(map[string]any{"command": "window-resize", "args": map[string]string{"width": strconv.Itoa(size.Width), "height": strconv.Itoa(size.Height)}})
}

// Close hangs up the channel. Incus then ends the exec, and Ended delivers
// the end.
func (t *terminal) Close() error {
	t.closeOnce.Do(func() { close(t.closing) })
	t.hangUp()
	return nil
}

// observeTerminalEnded: Incus no longer runs the exec of the terminal id;
// its operation is finished or gone.
func (b *Backend) observeTerminalEnded(ctx context.Context, id string) (string, bool, error) {
	raw, err := b.command(ctx, "operation", "list", "--format", "json")
	if err != nil {
		return "", false, err
	}
	var operations []struct {
		ID         string `json:"id"`
		StatusCode int    `json:"status_code"`
	}
	if err := decode(raw, &operations); err != nil {
		return "", false, err
	}
	if len(operations) > 256 {
		return "", false, errors.New("Incus operation inventory exceeds bound")
	}
	for _, op := range operations {
		if op.StatusCode < 100 || op.StatusCode > 401 {
			return "", false, errors.New("Incus operation status invalid")
		}
		if op.ID == id && op.StatusCode < 200 {
			return "", false, nil
		}
	}
	return "ended", true, nil
}
