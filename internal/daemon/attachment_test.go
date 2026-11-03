package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

func TestAttachmentPendingClaimExpiryAndStopExclusion(t *testing.T) {
	ctx := context.Background()
	token := strings.Repeat("a", 64)
	pending := &attachment{session: "session", expires: time.Now().Add(time.Minute), ended: make(chan struct{})}
	l := &lifecycle{attachments: map[string]*attachment{token: pending}}
	// Stop must reject before touching native/runtime authority; it reads
	// only the journal, which holds nothing for the session.
	stopping := &lifecycle{store: newJournalFixture(t).store, attachments: map[string]*attachment{token: {session: sessionUUID(1), expires: time.Now().Add(time.Minute)}}}
	if _, err := stopping.StopSession(ctx, sessionUUID(1)); !errors.Is(err, control.ErrConflict) {
		t.Fatal("pending attachment did not block stop", err)
	}
	pending.claimed = true
	pending.expires = time.Now().Add(-time.Second)
	if !l.hasAttachment("session") || !l.hasLiveAttachment("session") {
		t.Fatal("a claimed attachment lost its exclusion when its token's deadline passed")
	}
	l.attachments[token] = &attachment{session: "session", expires: time.Now().Add(-time.Second)}
	if l.hasAttachment("session") {
		t.Fatal("unclaimed expired token blocked stop")
	}
	_, err := l.ClaimAttachment(ctx, token, runtimedriver.Size{Width: 80, Height: 24})
	requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
}

func TestAttachmentUnknownTokenCannotBeClaimed(t *testing.T) {
	l := &lifecycle{}
	_, err := l.ClaimAttachment(context.Background(), strings.Repeat("a", 64), runtimedriver.Size{Width: 80, Height: 24})
	requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
}

func TestAttachmentReleasePanicLeavesLifecycleLockFree(t *testing.T) {
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	selection := control.CreationSelection{HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	op, session, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "attached", Project: "app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("e", 40), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.ConfirmAttachment(ctx, session.UUID); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	l := &lifecycle{ctx: ctx, store: store,
		attachments:  map[string]*attachment{token: {session: session.UUID, claimed: true, confirmed: true, ended: make(chan struct{})}},
		onAttachment: func(context.Context, string, int, bool) { panic("attachment event bug") }}
	// The watcher recovers a panic in the release it runs when a terminal ends.
	panicked := func() (value any) {
		defer func() { value = recover() }()
		l.releaseAttachment(token)
		return nil
	}()
	if panicked == nil {
		t.Fatal("release did not reach the panicking attachment callback")
	}
	answered := make(chan bool, 1)
	go func() { answered <- l.hasAttachment(session.UUID) }()
	select {
	case attached := <-answered:
		if attached {
			t.Fatal("the attachment outlived its release")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the lifecycle lock stayed held after a panic in attachment release")
	}
}

func TestAttachmentReservationLimitsAndExpiredCapacity(t *testing.T) {
	l := &lifecycle{}
	for i := 0; i < maxSessionAttachments; i++ {
		if _, err := l.reserveAttachment("one"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.reserveAttachment("one"); !errors.Is(err, control.ErrConflict) {
		t.Fatal("per-session bound missing")
	}
	for _, a := range l.attachments {
		a.expires = time.Now().Add(-time.Second)
	}
	if _, err := l.reserveAttachment("one"); err != nil {
		t.Fatal("expiry did not free capacity", err)
	}
	for i := len(l.attachments); i < maxAttachments; i++ {
		if _, err := l.reserveAttachment(string(rune(i + 100))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.reserveAttachment("extra"); !errors.Is(err, control.ErrConflict) {
		t.Fatal("global bound missing")
	}
}

// supervisedDriver is the in-memory driver behind a supervisor that restarts
// it: after Kill the driver that died refuses every call, as its process
// would, until Restart puts a new driver over the same World in its place.
type supervisedDriver struct {
	mu      sync.Mutex
	world   *memory.World
	current *memory.Driver
	// openErr, when set, is OpenTerminal's error.
	openErr error
	// beforeOpen, when set, runs as OpenTerminal begins.
	beforeOpen func()
	// openUnended makes OpenTerminal fail once the attach began, with the
	// terminal's channel closed and its guest process still running.
	openUnended bool
	// unendedID is the terminal the last unended open named.
	unendedID string
	// hold, when set, holds back each terminal's end until it closes.
	hold chan struct{}
}

var _ runtimedriver.Driver = (*supervisedDriver)(nil)

func newSupervisedDriver(w *memory.World) *supervisedDriver {
	return &supervisedDriver{world: w, current: memory.NewDriver(w)}
}

func (d *supervisedDriver) now() *memory.Driver {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current
}

func (d *supervisedDriver) Kill() { d.now().Kill() }

func (d *supervisedDriver) Restart() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = memory.NewDriver(d.world)
}

func (d *supervisedDriver) Describe() runtimedriver.Capabilities { return d.now().Describe() }
func (d *supervisedDriver) BuildImage(ctx context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	return d.now().BuildImage(ctx, b, g)
}
func (d *supervisedDriver) Images(ctx context.Context) ([]runtimedriver.Image, error) {
	return d.now().Images(ctx)
}
func (d *supervisedDriver) InspectImage(ctx context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	return d.now().InspectImage(ctx, ref)
}
func (d *supervisedDriver) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	return d.now().DeleteImage(ctx, img, g)
}
func (d *supervisedDriver) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	return d.now().Settle(ctx, e)
}
func (d *supervisedDriver) Delete(ctx context.Context, r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	return d.now().Delete(ctx, r, at, g)
}
func (d *supervisedDriver) Check(ctx context.Context) error { return d.now().Check(ctx) }
func (d *supervisedDriver) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	return d.now().Inventory(ctx)
}
func (d *supervisedDriver) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return d.now().Inspect(ctx, s)
}
func (d *supervisedDriver) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	return d.now().Create(ctx, s, at, g)
}
func (d *supervisedDriver) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return d.now().Start(ctx, s)
}
func (d *supervisedDriver) Stop(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return d.now().Stop(ctx, s)
}
func (d *supervisedDriver) HostLog(ctx context.Context, t runtimedriver.Target) (string, error) {
	return d.now().HostLog(ctx, t)
}
func (d *supervisedDriver) ReadWorkspace(ctx context.Context, t runtimedriver.Target, path string, limit int64) (runtimedriver.File, error) {
	return d.now().ReadWorkspace(ctx, t, path, limit)
}
func (d *supervisedDriver) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	return d.now().WriteWorkspace(ctx, t, path, f, g)
}
func (d *supervisedDriver) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, path string, g runtimedriver.Gate) error {
	return d.now().RemoveWorkspace(ctx, t, path, g)
}
func (d *supervisedDriver) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	return d.now().Services(ctx, t)
}
func (d *supervisedDriver) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	return d.now().ServiceAction(ctx, t, name, a)
}
func (d *supervisedDriver) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	return d.now().ServiceLogs(ctx, t, name)
}

func (d *supervisedDriver) OpenTerminal(ctx context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	if d.beforeOpen != nil {
		d.beforeOpen()
	}
	term, err := d.now().OpenTerminal(ctx, t, size)
	if err == nil && d.openUnended {
		d.world.Terminal(term.ID()).DropChannel()
		d.mu.Lock()
		d.unendedID = term.ID()
		d.mu.Unlock()
		// The driver gave up on its own deadline while the claim's
		// context is still live.
		return nil, &runtimedriver.UnendedTerminal{ID: term.ID(), Err: fmt.Errorf("opening the terminal: %w", context.DeadlineExceeded)}
	}
	if err != nil || d.hold == nil {
		return term, err
	}
	held := heldTerminal{Terminal: term, ended: make(chan runtimedriver.Completion, 1)}
	go func() {
		c := <-term.Ended()
		<-d.hold
		held.ended <- c
		close(held.ended)
	}()
	return held, nil
}

// heldTerminal is a terminal whose witness reports its end late.
type heldTerminal struct {
	runtimedriver.Terminal
	ended chan runtimedriver.Completion
}

func (t heldTerminal) Ended() <-chan runtimedriver.Completion { return t.ended }

// attachFixture is a ready session on the supervised in-memory driver, whose
// presence transitions the lifecycle reports to events.
type attachFixture struct {
	*driverFixture
	driver *supervisedDriver
	mu     sync.Mutex
	events []int // each presence count reported
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()
	f := &attachFixture{driverFixture: startFixture(t)}
	f.createRuntime()
	f.establish()
	f.driver = newSupervisedDriver(f.world)
	f.l.driver = f.driver
	f.l.terminalSettleInterval = 5 * time.Millisecond
	f.l.onAttachment = func(_ context.Context, _ string, count int, _ bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, count)
	}
	started, err := f.l.StartSession(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if v := f.awaitChange(started); v.Condition != "ready" {
		t.Fatalf("the session is not ready: %+v", v)
	}
	return f
}

// pending is a pending attachment of the session, as session.attach gives it.
func (f *attachFixture) pending() string {
	f.t.Helper()
	p, err := f.l.AttachSession(f.ctx, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	return p.Token
}

// attach claims a pending attachment, as the helper does.
func (f *attachFixture) attach() (control.Terminal, *memory.GuestTerminal) {
	f.t.Helper()
	term, err := f.l.ClaimAttachment(f.ctx, f.pending(), runtimedriver.Size{Width: 80, Height: 24})
	if err != nil {
		f.t.Fatal(err)
	}
	return term, f.world.Terminal(term.(interface{ ID() string }).ID())
}

// presence is the session's attached count.
func (f *attachFixture) presence() int {
	f.t.Helper()
	count, _, err := f.l.store.SessionStatus(f.ctx, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	return count
}

func (f *attachFixture) reported() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.events...)
}

// awaitReleased waits until the session has no attachment left.
func (f *attachFixture) awaitReleased() {
	f.t.Helper()
	for deadline := time.Now().Add(2 * time.Second); f.l.hasAttachment(f.session.UUID); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			f.t.Fatal("the attachment's lease was kept")
		}
	}
}

// heldFor checks that the attachment's lease and presence hold for a while.
func (f *attachFixture) heldFor(d time.Duration) {
	f.t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if !f.l.hasAttachment(f.session.UUID) || f.presence() != 1 {
			f.t.Fatal("the attachment's lease ended before its terminal's end was proven")
		}
	}
}

func readFull(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, n)
		k, _ := io.ReadFull(r, buf)
		got <- string(buf[:k])
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading the terminal")
	}
	return ""
}

// A claim opens the session's terminal at the client's size and counts the
// attachment in the session's presence, clearing its unattended report. The
// token is used once; Stop is refused while the terminal is attached. When
// the relay closes the terminal, its proven end releases the lease and the
// presence.
func TestAClaimOpensTheTerminalAndCountsItsPresenceUntilItsEndIsProven(t *testing.T) {
	f := newAttachFixture(t)
	if _, _, err := f.l.store.RecordStatus(f.ctx, f.session.UUID, api.StatusReport{V: api.SessionRPCVersion, Condition: "attention", Adapter: "codex", AdapterVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	token := f.pending()
	term, err := f.l.ClaimAttachment(f.ctx, token, runtimedriver.Size{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	guest := f.world.Terminal(term.(interface{ ID() string }).ID())
	if count, latest, _ := f.l.store.SessionStatus(f.ctx, f.session.UUID); count != 1 || latest != nil {
		t.Fatalf("presence %d, unattended report %+v", count, latest)
	}
	if guest.Size() != (runtimedriver.Size{Width: 80, Height: 24}) {
		t.Fatalf("the terminal opened at %+v", guest.Size())
	}
	if _, err = term.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := readFull(t, guest, 3); got != "ls\r" {
		t.Fatalf("the session host read %q", got)
	}
	if _, err = guest.Write([]byte("files")); err != nil {
		t.Fatal(err)
	}
	if got := readFull(t, term, 5); got != "files" {
		t.Fatalf("the client read %q", got)
	}
	if err = term.Resize(runtimedriver.Size{Width: 132, Height: 43}); err != nil || guest.Size() != (runtimedriver.Size{Width: 132, Height: 43}) {
		t.Fatalf("resize: %+v %v", guest.Size(), err)
	}
	_, err = f.l.ClaimAttachment(f.ctx, token, runtimedriver.Size{Width: 80, Height: 24})
	requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
	_, err = f.l.StopSession(f.ctx, f.session.UUID)
	requireRefusal(t, err, api.KindConflict, api.ReasonAttachmentActive, api.TargetSession(f.session.UUID))
	if err = term.Close(); err != nil {
		t.Fatal(err)
	}
	f.awaitReleased()
	if f.presence() != 0 {
		t.Fatalf("presence %d after the terminal ended", f.presence())
	}
	if got := f.reported(); len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("presence transitions %v, want [1 0]", got)
	}
	stopped, err := f.l.StopSession(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatalf("Stop after the attachment ended: %v", err)
	}
	if v := f.awaitChange(stopped); v.Condition != "stopped" {
		t.Fatalf("after Stop: %+v", v)
	}
}

// Closing the terminal ends the attachment, but its lease and presence hold
// until the driver's witness proves the attached process ended.
func TestTheLeaseIsKeptUntilTheTerminalsEndIsProven(t *testing.T) {
	f := newAttachFixture(t)
	f.driver.hold = make(chan struct{})
	term, _ := f.attach()
	term.Close()
	f.heldFor(100 * time.Millisecond)
	_, err := f.l.StopSession(f.ctx, f.session.UUID)
	requireRefusal(t, err, api.KindConflict, api.ReasonAttachmentActive, api.TargetSession(f.session.UUID))
	close(f.driver.hold)
	f.awaitReleased()
	if f.presence() != 0 {
		t.Fatalf("presence %d after the proven end", f.presence())
	}
}

// A claim whose terminal cannot be opened never counts presence, and frees
// the session at once. A host that is not ready refuses the claim as not
// running; a terminal the driver could not open failed in the runtime.
func TestAFailedOpenNeverCountsPresence(t *testing.T) {
	for name, test := range map[string]struct {
		fail   func(*attachFixture)
		kind   api.ErrorKind
		reason string
	}{
		"the driver finds the host not ready": {func(f *attachFixture) { f.driver.openErr = runtimedriver.ErrNotReady }, api.KindConflict, api.ReasonSessionNotRunning},
		"the runtime stopped after session.attach": {func(f *attachFixture) {
			if _, err := memory.NewDriver(f.world).Stop(f.ctx, f.spec()); err != nil {
				f.t.Fatal(err)
			}
		}, api.KindConflict, api.ReasonSessionNotRunning},
		"the driver cannot open the terminal": {func(f *attachFixture) { f.driver.openErr = runtimedriver.ErrUnavailable }, api.KindFailed, api.ReasonRuntime},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAttachFixture(t)
			token := f.pending()
			test.fail(f)
			_, err := f.l.ClaimAttachment(f.ctx, token, runtimedriver.Size{Width: 80, Height: 24})
			target := api.TargetSession(f.session.UUID)
			if test.kind == api.KindFailed {
				target = ""
			}
			requireRefusal(t, err, test.kind, test.reason, target)
			if f.presence() != 0 || len(f.reported()) != 0 || f.l.hasAttachment(f.session.UUID) {
				t.Fatalf("a failed open left presence %d, transitions %v, or a lease", f.presence(), f.reported())
			}
			_, err = f.l.ClaimAttachment(f.ctx, token, runtimedriver.Size{Width: 80, Height: 24})
			requireRefusal(t, err, api.KindPreconditionFailed, api.ReasonExpired, "")
		})
	}
}

// When the driver dies its terminals end unproven: the client is told, input
// stops and is not replayed, and the lease and presence stay, so the
// session's removals stay refused. The lease ends once the restarted driver
// settles the terminal as ended.
func TestADriverFailureEndsTheTerminalUnprovenAndKeepsTheLeaseUntilSettled(t *testing.T) {
	f := newAttachFixture(t)
	term, guest := f.attach()
	f.driver.Kill()
	if _, err := io.ReadAll(term); !errors.Is(err, errUnprovenEnd) {
		t.Fatalf("the terminal's output ended with %v, want the unproven end", err)
	}
	if _, err := term.Write([]byte("late\r")); err == nil {
		t.Fatal("input reached a terminal whose channel closed")
	}
	f.heldFor(50 * time.Millisecond)
	if _, err := f.l.StopSession(f.ctx, f.session.UUID); err == nil {
		t.Fatal("Stop succeeded through a dead driver")
	}
	f.driver.Restart()
	f.heldFor(50 * time.Millisecond)
	guest.EndProcess()
	f.awaitReleased()
	if f.presence() != 0 {
		t.Fatalf("presence %d after the terminal settled", f.presence())
	}
	if got := readFull(t, guest, 5); got != "" {
		t.Fatalf("the session host read %q after the driver died", got)
	}
}

// Stop is allowed while the session's only attachment ended unproven, and
// ends that attachment's lease: no process of it outlives the runtime.
func TestStoppingTheSessionEndsAnUnprovenLease(t *testing.T) {
	f := newAttachFixture(t)
	f.l.terminalSettleInterval = time.Hour
	term, _ := f.attach()
	f.driver.Kill()
	f.driver.Restart()
	io.ReadAll(term)
	if !f.l.hasAttachment(f.session.UUID) || f.l.hasLiveAttachment(f.session.UUID) {
		t.Fatal("the unproven attachment is not kept as a lease alone")
	}
	stopped, err := f.l.StopSession(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatalf("Stop with an unproven lease: %v", err)
	}
	if v := f.awaitChange(stopped); v.Condition != "stopped" {
		t.Fatalf("after Stop with an unproven lease: %+v", v)
	}
	if f.l.hasAttachment(f.session.UUID) || f.presence() != 0 {
		t.Fatal("the unproven lease outlived the stopped runtime")
	}
}

// A session's discard and delete are refused while a claimed terminal is
// attached, and a discard is accepted once the terminal's end is proven.
func TestRemovalsWaitForTheAttachedTerminalsProvenEnd(t *testing.T) {
	f := newSessionFixture(t)
	dry, err := f.l.DiscardSession(f.ctx, f.session.UUID, api.ChangeRequest{})
	if err != nil || dry.ChangeTag == "" {
		t.Fatalf("discard plan: %+v %v", dry.Plan, err)
	}
	pending, err := f.l.reserveAttachment(f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	term, err := f.l.ClaimAttachment(f.ctx, pending.Token, runtimedriver.Size{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	discard := api.ChangeRequest{Key: "discard", IfMatch: dry.ChangeTag}
	if _, err = f.l.DiscardSession(f.ctx, f.session.UUID, discard); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("discard of an attached session: %v", err)
	}
	if plan, err := f.removal("delete")(f.ctx, f.session.UUID, api.ChangeRequest{}); err != nil || plan.Plan == nil || plan.Plan.Allowed {
		t.Fatalf("delete plan of an attached session: %+v %v", plan.Plan, err)
	}
	term.Close()
	for deadline := time.Now().Add(2 * time.Second); f.l.hasAttachment(f.session.UUID); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the attachment's lease was kept")
		}
	}
	if _, err = f.l.DiscardSession(f.ctx, f.session.UUID, discard); err != nil {
		t.Fatalf("discard once the terminal ended: %v", err)
	}
}

// unattended is the session's latest unattended report, nil when it has none.
func (f *attachFixture) unattended() *api.UnattendedCondition {
	f.t.Helper()
	_, latest, err := f.l.store.SessionStatus(f.ctx, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	return latest
}

// leasedWithoutPresence checks for a while that the session's attachment
// holds its lease, refusing removals but not Stop, and counts no presence.
func (f *attachFixture) leasedWithoutPresence(d time.Duration) {
	f.t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if !f.l.hasAttachment(f.session.UUID) || f.presence() != 0 || len(f.reported()) != 0 {
			f.t.Fatalf("lease %v, presence %d, transitions %v", f.l.hasAttachment(f.session.UUID), f.presence(), f.reported())
		}
	}
}

// When the attach began but the terminal did not open and its end is
// unproven, the attachment never counts presence, and its lease refuses the
// session's removals until the driver settles the terminal as ended.
func TestAnUnendedFailedOpenKeepsTheLeaseWithoutPresenceUntilSettled(t *testing.T) {
	f := newAttachFixture(t)
	token := f.pending()
	f.driver.openUnended = true
	_, err := f.l.ClaimAttachment(f.ctx, token, runtimedriver.Size{Width: 80, Height: 24})
	// The driver's own deadline is the runtime's failure, not the request's.
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the claim ended with %v, a request deadline", err)
	}
	requireRefusal(t, err, api.KindFailed, api.ReasonRuntime, "")
	f.leasedWithoutPresence(50 * time.Millisecond)
	if f.l.hasLiveAttachment(f.session.UUID) {
		t.Fatal("the unended terminal's lease refuses Stop")
	}
	f.world.Terminal(f.driver.unendedID).EndProcess()
	f.awaitReleased()
	if f.presence() != 0 || len(f.reported()) != 0 {
		t.Fatalf("presence %d, transitions %v after the terminal settled", f.presence(), f.reported())
	}
}

// A claim whose helper hung up while the terminal opened counts no presence
// and keeps the unattended report; the terminal it opened is closed, and its
// lease holds until the terminal's end is proven.
func TestAClaimWhoseHelperHungUpWhileItOpenedCountsNoPresence(t *testing.T) {
	f := newAttachFixture(t)
	if _, _, err := f.l.store.RecordStatus(f.ctx, f.session.UUID, api.StatusReport{V: api.SessionRPCVersion, Condition: "attention", Adapter: "codex", AdapterVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	token := f.pending()
	opening, gate := make(chan struct{}), make(chan struct{})
	f.driver.beforeOpen = func() { close(opening); <-gate }
	f.driver.hold = make(chan struct{})
	ctx, hangUp := context.WithCancel(f.ctx)
	claimed := make(chan error, 1)
	go func() {
		_, err := f.l.ClaimAttachment(ctx, token, runtimedriver.Size{Width: 80, Height: 24})
		claimed <- err
	}()
	<-opening
	hangUp()
	close(gate)
	if err := <-claimed; !errors.Is(err, context.Canceled) {
		t.Fatalf("the claim ended with %v, want its cancellation", err)
	}
	f.leasedWithoutPresence(50 * time.Millisecond)
	if f.unattended() == nil {
		t.Fatal("a claim whose helper hung up cleared the unattended report")
	}
	close(f.driver.hold)
	f.awaitReleased()
	if f.presence() != 0 || len(f.reported()) != 0 || f.unattended() == nil {
		t.Fatalf("presence %d, transitions %v, report %+v after the terminal ended", f.presence(), f.reported(), f.unattended())
	}
}
