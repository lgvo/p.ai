package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// ServiceControls act as a session's Git client, which reaches P's
// repositories only through ServeGit, and set refs no client may see.
type ServiceControls interface {
	// Client returns a new session's clone of the project's repository,
	// holding no objects or refs yet.
	Client(project string) Client
	// SetOtherRef points ref, a ref outside refs/heads such as a tag or a
	// private ref of P's, at id in the project's repository.
	SetOtherRef(project, ref string, id sourcedriver.OID)
}

// Serve connects a client to one ServeGit call. The client calls it once,
// with the SSH exec command it sends and its channel as the stream; Serve
// returns what ServeGit returned, and the client then closes the channel,
// as core's SSH server does.
type Serve func(command string, stream sourcedriver.Stream) error

// Client is a session's Git clone: objects and refs of its own, which reach
// P's repository only through a service. Each operation sends one SSH exec
// command, a Git command addressing the project as ssh://git@p/<project>
// does, through serve, and returns once the client is done and serve has
// returned.
type Client interface {
	// Commit writes a new commit with parents, which the client holds, into
	// the client and returns its ID. Each call writes a distinct commit.
	Commit(parents ...sourcedriver.OID) sourcedriver.OID
	// Tree writes a tree into the client and returns its ID.
	Tree() sourcedriver.OID
	// Has reports whether the client holds the object.
	Has(id sourcedriver.OID) bool
	// Advertised returns the refs an upload service advertises, with their
	// IDs, and fetches nothing.
	Advertised(serve Serve) (map[string]sourcedriver.OID, error)
	// Fetch fetches the head refs/heads/<name> with its history and returns
	// its tip.
	Fetch(serve Serve, name string) (sourcedriver.OID, error)
	// WantID asks an upload service for id by its ID alone, as a client
	// that ignores the advertisement could, and fails unless the service
	// sends it. The client keeps nothing of what it is sent.
	WantID(serve Serve, id sourcedriver.OID) error
	// Push sends updates in one push and fails unless the service accepts
	// it. The client checks nothing itself, as a forced push: each update
	// moves Ref from the value the service advertised to New, or deletes it
	// when New is empty.
	Push(serve Serve, updates ...Update) error
}

// Update is one ref update of a push.
type Update struct {
	Ref string
	New sourcedriver.OID
}

var (
	errOpenRefused     = errors.New("core refused the service")
	errApprovalRefused = errors.New("core refused the update")
	errNotServed       = errors.New("the case served nothing")
)

// session plays core for the services of one session on a project: its open
// authorizes every service, for the session's head and with the case's
// ceilings, and records it; its Approve records every approval and answers
// as the case says.
type session struct {
	t        *testing.T
	f        Fixture
	project  string
	branch   string // the one head a receive service may change
	maxInput int64
	deadline time.Duration

	mu        sync.Mutex
	refusal   error // Approve's answer
	during    func(approval)
	hold      <-chan struct{}
	services  []sourcedriver.Service
	approvals []approval
}

// approval is one call of Approve.
type approval struct {
	service  string
	branch   string
	old, new sourcedriver.OID
}

func newSession(t *testing.T, f Fixture, project string) *session {
	return &session{t: t, f: f, project: project, branch: "work", maxInput: 1 << 30, deadline: time.Minute}
}

// refuseApprovals makes Approve answer err from now on; nil approves.
func (s *session) refuseApprovals(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusal = err
}

// duringApproval runs check inside every later Approve, before it answers.
func (s *session) duringApproval(check func(approval)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.during = check
}

// holdApproved makes the output of every later service that Approve
// approves wait until release is closed, as a client that stopped reading
// would: its service cannot end, so core cannot hear that it did.
func (s *session) holdApproved(release <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = release
}

// serve runs ServeGit on the fixture's current driver with the test's
// context.
func (s *session) serve() Serve { return s.serveWith(s.t.Context()) }

func (s *session) serveWith(ctx context.Context) Serve {
	return func(command string, stream sourcedriver.Stream) error {
		w := watch(stream)
		return s.f.Driver().ServeGit(ctx, command, w.stream(), s.open(w))
	}
}

// open authorizes one service: it checks that it is called once, for the
// session's project, before the service used its stream.
func (s *session) open(w *watchedStream) sourcedriver.Open {
	var calls atomic.Int32
	return func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
		if calls.Add(1) > 1 {
			s.t.Errorf("open was called again for one %s service", kind)
		}
		if w.used() {
			s.t.Errorf("open(%s, %q) was called after the service used its stream", kind, project)
		}
		if project != s.project {
			s.t.Errorf("open(%s, %q) for a client of %q", kind, project, s.project)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		service := sourcedriver.Service{
			ID: fmt.Sprintf("service-%d", len(s.services)+1), Project: project, Kind: kind,
			Branch: s.branch, MaxInput: s.maxInput, Deadline: s.deadline,
		}
		s.services = append(s.services, service)
		return service, s.approve(service, w), nil
	}
}

// approve records the service's approvals and answers as the case says.
func (s *session) approve(service sourcedriver.Service, w *watchedStream) sourcedriver.Approve {
	var calls atomic.Int32
	return func(branch string, old, new sourcedriver.OID) error {
		if calls.Add(1) > 1 {
			s.t.Errorf("Approve was called again for service %s", service.ID)
		}
		if service.Kind != sourcedriver.ServiceReceive {
			s.t.Errorf("Approve was called for an %s service", service.Kind)
		}
		a := approval{service: service.ID, branch: branch, old: old, new: new}
		s.mu.Lock()
		s.approvals = append(s.approvals, a)
		refusal, during, hold := s.refusal, s.during, s.hold
		s.mu.Unlock()
		if during != nil {
			during(a)
		}
		if refusal == nil && hold != nil {
			w.holdOutput(hold)
		}
		return refusal
	}
}

// receiveServices are the receive services opened so far.
func (s *session) receiveServices() []sourcedriver.Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	var receive []sourcedriver.Service
	for _, service := range s.services {
		if service.Kind == sourcedriver.ServiceReceive {
			receive = append(receive, service)
		}
	}
	return receive
}

func (s *session) lastService() sourcedriver.Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.services) == 0 {
		s.t.Fatal("no service was opened")
	}
	return s.services[len(s.services)-1]
}

// wantApprovals checks every approval so far, in order, by branch and IDs.
func (s *session) wantApprovals(want ...approval) {
	s.t.Helper()
	s.mu.Lock()
	got := slices.Clone(s.approvals)
	s.mu.Unlock()
	same := len(got) == len(want)
	for i := 0; same && i < len(got); i++ {
		same = got[i].branch == want[i].branch && got[i].old == want[i].old && got[i].new == want[i].new
	}
	if !same {
		s.t.Fatalf("approvals %+v, want %+v", got, want)
	}
}

// head reads refs/heads/<name> through the driver: its tip, or empty when
// absent.
func (s *session) head(name string) sourcedriver.OID {
	s.t.Helper()
	tip, ok, err := s.f.Driver().InspectBranch(s.t.Context(), s.project, name)
	if err != nil {
		s.t.Fatalf("InspectBranch(%q): %v", name, err)
	}
	if !ok {
		return ""
	}
	return tip
}

func (s *session) wantHead(name string, tip sourcedriver.OID) {
	s.t.Helper()
	if got := s.head(name); got != tip {
		s.t.Fatalf("refs/heads/%s is at %q, want %q", name, got, tip)
	}
}

// awaitHead waits until refs/heads/<name> is at tip.
func (s *session) awaitHead(name string, tip sourcedriver.OID) {
	s.t.Helper()
	for limit := time.Now().Add(waitLimit); s.head(name) != tip; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(limit) {
			s.t.Fatalf("refs/heads/%s did not reach %s within %v", name, tip, waitLimit)
		}
	}
}

func (s *session) wantPresent(id sourcedriver.OID, present bool) {
	s.t.Helper()
	commit, err := s.f.Driver().InspectCommit(s.t.Context(), s.project, id)
	if err != nil {
		s.t.Fatalf("InspectCommit(%s): %v", id, err)
	}
	if commit.Present != present {
		s.t.Fatalf("commit %s present in P's repository: %t, want %t", id, commit.Present, present)
	}
}

func (s *session) settle(service string) sourcedriver.Settlement {
	s.t.Helper()
	got, err := s.f.Driver().Settle(s.t.Context(), sourcedriver.Effect{Kind: sourcedriver.EffectService, Project: s.project, Service: service})
	if err != nil {
		s.t.Fatalf("Settle(service %s): %v", service, err)
	}
	return got
}

// stalledService is a service whose client never answers: its output goes
// nowhere, and no input ever comes.
type stalledService struct {
	started <-chan struct{} // closed at the service's first output
	result  <-chan error
}

// stall starts a service of kind on the session's project with a client
// that never answers.
func (s *session) stall(ctx context.Context, kind sourcedriver.ServiceKind) stalledService {
	in, never := io.Pipe()
	s.t.Cleanup(func() { never.Close() })
	out := &firstOutput{started: make(chan struct{})}
	command := gitCommand(kind, s.project)
	result := inBackground(func() error {
		return s.serveWith(ctx)(command, sourcedriver.Stream{In: in, Out: out, Err: io.Discard})
	})
	return stalledService{started: out.started, result: result}
}

// gitCommand is the SSH exec command Git's client sends for a service of
// kind on the project at ssh://git@p/<project>.
func gitCommand(kind sourcedriver.ServiceKind, project string) string {
	word := map[sourcedriver.ServiceKind]string{sourcedriver.ServiceUpload: "git-upload-pack", sourcedriver.ServiceReceive: "git-receive-pack"}[kind]
	return word + " '/" + project + "'"
}

// watchedStream is a service's stream as the case sees it: whether the
// service used it yet, and output that, once held, waits for a release.
type watchedStream struct {
	inner  sourcedriver.Stream
	reads  atomic.Int32
	writes atomic.Int32

	mu   sync.Mutex
	hold <-chan struct{}
}

func watch(stream sourcedriver.Stream) *watchedStream { return &watchedStream{inner: stream} }

func (w *watchedStream) stream() sourcedriver.Stream {
	return sourcedriver.Stream{In: watchedInput{w}, Out: watchedOutput{w, w.inner.Out}, Err: watchedOutput{w, w.inner.Err}}
}

func (w *watchedStream) used() bool { return w.reads.Load() > 0 || w.writes.Load() > 0 }

func (w *watchedStream) holdOutput(release <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hold = release
}

type watchedInput struct{ w *watchedStream }

func (in watchedInput) Read(p []byte) (int, error) {
	in.w.reads.Add(1)
	return in.w.inner.In.Read(p)
}

type watchedOutput struct {
	w     *watchedStream
	inner io.Writer
}

func (out watchedOutput) Write(p []byte) (int, error) {
	out.w.writes.Add(1)
	out.w.mu.Lock()
	hold := out.w.hold
	out.w.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if out.inner == nil {
		return len(p), nil
	}
	return out.inner.Write(p)
}

// firstOutput discards a service's output and closes started at its first
// byte.
type firstOutput struct {
	once    sync.Once
	started chan struct{}
}

func (o *firstOutput) Write(p []byte) (int, error) {
	o.once.Do(func() { close(o.started) })
	return len(p), nil
}

// unusedStream is a stream that records whether a service used it.
func unusedStream() *watchedStream {
	return watch(sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
}

// inBackground runs call and delivers its result.
func inBackground(call func() error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- call() }()
	return result
}

// awaitResult waits for a call in the background, failing the case after
// waitLimit.
func awaitResult(t *testing.T, result <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(waitLimit):
		t.Fatalf("%s did not end within %v", what, waitLimit)
		return nil
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(waitLimit):
		t.Fatalf("%s did not happen within %v", what, waitLimit)
	}
}

// pushed checks that a push succeeded.
func pushed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
}

var serviceCases = []testCase{
	{"a session's client sends commands whose first words Describe names, and each word named is served", func(t *testing.T, f Fixture) {
		words := f.Driver().Describe().Commands
		if len(words) == 0 {
			t.Fatal("Describe names no command words")
		}
		for _, word := range words {
			if word == "" || strings.ContainsAny(word, " \t\r\n'") {
				t.Errorf("Describe names %q, which is not one word", word)
			}
		}
		repository(t, f, "app")
		client := f.Service().Client("app")
		var mu sync.Mutex
		var sent []string
		record := func(command string, _ sourcedriver.Stream) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, command)
			return errNotServed
		}
		if _, err := client.Advertised(record); err == nil {
			t.Error("a fetch nothing served succeeded")
		}
		if err := client.Push(record, Update{Ref: "refs/heads/work", New: client.Commit()}); err == nil {
			t.Error("a push nothing served succeeded")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(sent) != 2 {
			t.Fatalf("the client sent %q, want one fetch and one push", sent)
		}
		for _, command := range sent {
			if word, _, _ := strings.Cut(command, " "); !slices.Contains(words, word) {
				t.Errorf("the client sent %q, whose first word Describe does not name", command)
			}
		}
		for _, word := range words {
			calls := 0
			err := f.Driver().ServeGit(t.Context(), word+" 'app'", unusedStream().stream(), func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
				calls++
				return sourcedriver.Service{}, nil, errOpenRefused
			})
			if !errors.Is(err, errOpenRefused) || calls != 1 {
				t.Errorf("ServeGit(%q) = %v after %d calls of open; want open's refusal after one", word+" 'app'", err, calls)
			}
		}
	}},
	{"a command that does not parse is refused as invalid before open", func(t *testing.T, f Fixture) {
		for _, command := range []string{
			"", "id", "git-upload-archive 'app'", "git-upload-pack /app", "git-upload-pack 'app'; id'",
			"git-upload-pack '//app'", "git-upload-pack 'app/../other'", "git-upload-pack ''", "git-upload-pack 'app",
			"git-receive-pack 'app' extra", "git-receive-pack 'a\npp'", "git-receive-pack 'a\\pp'", " git-upload-pack 'app'",
		} {
			w := unusedStream()
			err := f.Driver().ServeGit(t.Context(), command, w.stream(), func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
				t.Errorf("ServeGit(%q) opened a %s service of %q", command, kind, project)
				return sourcedriver.Service{}, nil, errOpenRefused
			})
			if !errors.Is(err, sourcedriver.ErrInvalid) {
				t.Errorf("ServeGit(%q) = %v, want ErrInvalid", command, err)
			}
			if w.used() {
				t.Errorf("ServeGit(%q) used the stream", command)
			}
		}
	}},
	{"a command opens its service once, for the project it names, before any Git runs; open's refusal ends the call", func(t *testing.T, f Fixture) {
		for _, c := range []struct {
			command string
			kind    sourcedriver.ServiceKind
			project string
		}{
			{"git-upload-pack '/app'", sourcedriver.ServiceUpload, "app"},
			{"git-receive-pack '/app'", sourcedriver.ServiceReceive, "app"},
			{"git-upload-pack 'team/app'", sourcedriver.ServiceUpload, "team/app"},
			{"git-receive-pack 'app'", sourcedriver.ServiceReceive, "app"},
			{"git-upload-pack 'refs/p'", sourcedriver.ServiceUpload, "refs/p"},
		} {
			w := unusedStream()
			calls := 0
			err := f.Driver().ServeGit(t.Context(), c.command, w.stream(), func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
				calls++
				if kind != c.kind || project != c.project {
					t.Errorf("ServeGit(%q) opened a %s service of %q, want %s of %q", c.command, kind, project, c.kind, c.project)
				}
				if w.used() {
					t.Errorf("ServeGit(%q) used the stream before open", c.command)
				}
				return sourcedriver.Service{}, nil, errOpenRefused
			})
			if !errors.Is(err, errOpenRefused) || calls != 1 {
				t.Errorf("ServeGit(%q) = %v after %d calls of open; want open's refusal after one", c.command, err, calls)
			}
			if w.used() {
				t.Errorf("ServeGit(%q) used the stream after open refused", c.command)
			}
		}
	}},
	{"a service of a project with no repository ends as not found, serving nothing", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		for _, kind := range []sourcedriver.ServiceKind{sourcedriver.ServiceUpload, sourcedriver.ServiceReceive} {
			s := newSession(t, f, "other")
			w := unusedStream()
			if err := f.Driver().ServeGit(t.Context(), gitCommand(kind, "other"), w.stream(), s.open(w)); !errors.Is(err, sourcedriver.ErrNotFound) {
				t.Errorf("a %s service of a project with no repository = %v, want ErrNotFound", kind, err)
			}
			if w.used() {
				t.Errorf("a %s service of a project with no repository used the stream", kind)
			}
			if got := s.settle(s.lastService().ID); got != sourcedriver.Applied {
				t.Errorf("Settle(a %s service that found no repository) = %v, want applied", kind, got)
			}
		}
	}},
	{"upload advertises only heads, at their tips", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		n := f.Natives()
		a := n.Commit("app")
		b := n.Commit("app", a)
		hidden := n.Commit("app")
		n.SetHead("app", "main", a)
		n.SetHead("app", "feature/x", b)
		f.Service().SetOtherRef("app", "refs/tags/v1", a)
		f.Service().SetOtherRef("app", "refs/p/secret", hidden)
		s := newSession(t, f, "app")
		got, err := f.Service().Client("app").Advertised(s.serve())
		if err != nil {
			t.Fatalf("Advertised: %v", err)
		}
		want := map[string]sourcedriver.OID{"refs/heads/main": a, "refs/heads/feature/x": b}
		for ref, id := range got {
			// Git's clients follow HEAD to the default head.
			if ref == "HEAD" && slices.Contains([]sourcedriver.OID{a, b}, id) {
				continue
			}
			if want[ref] != id {
				t.Errorf("upload advertised %s at %s", ref, id)
			}
		}
		for ref, id := range want {
			if got[ref] != id {
				t.Errorf("upload advertised %s at %q, want %s", ref, got[ref], id)
			}
		}
	}},
	{"upload sends a head with its history and refuses a want by ID of anything but an advertised tip", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		n := f.Natives()
		a := n.Commit("app")
		b := n.Commit("app", a)
		hidden := n.Commit("app")
		n.SetHead("app", "main", b)
		f.Service().SetOtherRef("app", "refs/p/secret", hidden)
		s := newSession(t, f, "app")
		client := f.Service().Client("app")
		if tip, err := client.Fetch(s.serve(), "main"); err != nil || tip != b {
			t.Fatalf("Fetch(main) = %q, %v; want %s", tip, err, b)
		}
		if !client.Has(b) || !client.Has(a) {
			t.Fatal("the fetch did not bring the head's history")
		}
		other := f.Service().Client("app")
		for _, id := range []sourcedriver.OID{a, hidden} {
			if err := other.WantID(s.serve(), id); err == nil {
				t.Errorf("a want of %s by its ID was served", id)
			}
			if other.Has(id) {
				t.Errorf("the client got %s, which it wanted by its ID", id)
			}
		}
		if err := other.WantID(s.serve(), b); err != nil {
			t.Errorf("a want of the advertised tip %s by its ID was refused: %v", b, err)
		}
	}},
	{"receive approves one fast-forward of the allowed head once, before the update and with the pushed objects kept apart, and moves the head", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		a := f.Natives().Commit("app")
		f.Natives().SetHead("app", "work", a)
		s := newSession(t, f, "app")
		client := f.Service().Client("app")
		if tip, err := client.Fetch(s.serve(), "work"); err != nil || tip != a {
			t.Fatalf("Fetch(work) = %q, %v; want %s", tip, err, a)
		}
		b := client.Commit(a)
		s.duringApproval(func(approval) {
			if tip, ok, err := f.Driver().InspectBranch(t.Context(), "app", "work"); err != nil || !ok || tip != a {
				t.Errorf("during Approve refs/heads/work is %q, %t, %v; want the old tip %s", tip, ok, err, a)
			}
			if commit, err := f.Driver().InspectCommit(t.Context(), "app", b); err != nil || commit.Present {
				t.Errorf("during Approve the pushed commit is %+v, %v; want it apart from P's repository", commit, err)
			}
		})
		pushed(t, client.Push(s.serve(), Update{Ref: "refs/heads/work", New: b}))
		s.wantApprovals(approval{branch: "work", old: a, new: b})
		s.wantHead("work", b)
		s.wantPresent(b, true)
	}},
	{"receive refuses a second update, another head, a deletion, a non-commit and a non-fast-forward, without approval", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		a := f.Natives().Commit("app")
		b := f.Natives().Commit("app", a)
		f.Natives().SetHead("app", "work", b)
		s := newSession(t, f, "app")
		client := f.Service().Client("app")
		if tip, err := client.Fetch(s.serve(), "work"); err != nil || tip != b {
			t.Fatalf("Fetch(work) = %q, %v; want %s", tip, err, b)
		}
		next, divergent, tree := client.Commit(b), client.Commit(a), client.Tree()
		creation := newSession(t, f, "app")
		creation.branch = "fresh"
		for _, push := range []struct {
			what    string
			s       *session
			updates []Update
		}{
			{"a second update", s, []Update{{"refs/heads/work", next}, {"refs/heads/other", next}}},
			{"an update of another head", s, []Update{{"refs/heads/other", next}}},
			{"a deletion", s, []Update{{"refs/heads/work", ""}}},
			{"a non-commit", s, []Update{{"refs/heads/work", tree}}},
			{"a non-commit creating the head", creation, []Update{{"refs/heads/fresh", tree}}},
			{"a non-fast-forward", s, []Update{{"refs/heads/work", divergent}}},
			{"a rewind", s, []Update{{"refs/heads/work", a}}},
		} {
			if err := client.Push(push.s.serve(), push.updates...); err == nil {
				t.Errorf("receive accepted %s", push.what)
			}
		}
		s.wantApprovals()
		creation.wantApprovals()
		s.wantHead("work", b)
		s.wantHead("other", "")
		s.wantHead("fresh", "")
	}},
	{"a refusal by Approve leaves the head and keeps the pushed objects out", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		a := f.Natives().Commit("app")
		f.Natives().SetHead("app", "work", a)
		s := newSession(t, f, "app")
		client := f.Service().Client("app")
		if _, err := client.Fetch(s.serve(), "work"); err != nil {
			t.Fatalf("Fetch(work): %v", err)
		}
		b := client.Commit(a)
		s.refuseApprovals(errApprovalRefused)
		if err := client.Push(s.serve(), Update{Ref: "refs/heads/work", New: b}); err == nil {
			t.Fatal("a push Approve refused succeeded")
		}
		s.wantApprovals(approval{branch: "work", old: a, new: b})
		s.wantHead("work", a)
		s.wantPresent(b, false)
	}},
	{"a first push to an empty repository needs approval, which sees an empty old value", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		s := newSession(t, f, "app")
		s.branch = "main"
		client := f.Service().Client("app")
		root := client.Commit()
		s.refuseApprovals(errApprovalRefused)
		if err := client.Push(s.serve(), Update{Ref: "refs/heads/main", New: root}); err == nil {
			t.Fatal("a first push Approve refused succeeded")
		}
		s.wantHead("main", "")
		s.refuseApprovals(nil)
		pushed(t, client.Push(s.serve(), Update{Ref: "refs/heads/main", New: root}))
		s.wantApprovals(approval{branch: "main", old: "", new: root}, approval{branch: "main", old: "", new: root})
		s.wantHead("main", root)
	}},
	{"the input ceiling holds for upload and receive", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		a := f.Natives().Commit("app")
		f.Natives().SetHead("app", "work", a)
		tight := newSession(t, f, "app")
		tight.maxInput = 16
		client := f.Service().Client("app")
		if _, err := client.Fetch(tight.serve(), "work"); err == nil {
			t.Error("a fetch past the input ceiling succeeded")
		}
		if client.Has(a) {
			t.Error("a fetch past the input ceiling brought the head")
		}
		if _, err := client.Fetch(newSession(t, f, "app").serve(), "work"); err != nil {
			t.Fatalf("Fetch(work): %v", err)
		}
		b := client.Commit(a)
		if err := client.Push(tight.serve(), Update{Ref: "refs/heads/work", New: b}); err == nil {
			t.Error("a push past the input ceiling succeeded")
		}
		tight.wantApprovals()
		tight.wantHead("work", a)
	}},
	{"the time ceiling ends a stalled service of either kind, which then settles ended", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		for _, kind := range []sourcedriver.ServiceKind{sourcedriver.ServiceUpload, sourcedriver.ServiceReceive} {
			s := newSession(t, f, "app")
			s.deadline = 200 * time.Millisecond
			stalled := s.stall(t.Context(), kind)
			if err := awaitResult(t, stalled.result, "a stalled "+string(kind)+" service"); err == nil {
				t.Errorf("a %s service cut off by its deadline returned no error", kind)
			}
			if got := s.settle(s.lastService().ID); got != sourcedriver.Applied {
				t.Errorf("Settle(%s service past its deadline) = %v, want applied", kind, got)
			}
		}
	}},
	{"a running service settles unknown, and applied once its call returns, as a service never run does", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		s := newSession(t, f, "app")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stalled := s.stall(ctx, sourcedriver.ServiceReceive)
		awaitSignal(t, stalled.started, "the service's first output")
		id := s.lastService().ID
		if got := s.settle(id); got != sourcedriver.Unknown {
			t.Errorf("Settle(a running service) = %v, want unknown", got)
		}
		cancel()
		if err := awaitResult(t, stalled.result, "a cancelled service"); err == nil {
			t.Error("a cancelled service returned no error")
		}
		if got := s.settle(id); got != sourcedriver.Applied {
			t.Errorf("Settle(a service whose call returned) = %v, want applied", got)
		}
		if got := s.settle("never-run"); got != sourcedriver.Applied {
			t.Errorf("Settle(a service never run) = %v, want applied", got)
		}
	}},
	{"two approved pushes of one session lost with the driver settle ended after its restart, and neither is sent again", func(t *testing.T, f Fixture) {
		repository(t, f, "app")
		a := f.Natives().Commit("app")
		f.Natives().SetHead("app", "work", a)
		s := newSession(t, f, "app")
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		client := f.Service().Client("app")
		if _, err := client.Fetch(s.serve(), "work"); err != nil {
			t.Fatalf("Fetch(work): %v", err)
		}
		// Each push's service is held once approved, so the driver dies
		// before core hears that either ended.
		s.holdApproved(release)
		b := client.Commit(a)
		first := inBackground(func() error { return client.Push(s.serve(), Update{Ref: "refs/heads/work", New: b}) })
		s.awaitHead("work", b)
		c := client.Commit(b)
		second := inBackground(func() error { return client.Push(s.serve(), Update{Ref: "refs/heads/work", New: c}) })
		s.awaitHead("work", c)
		select {
		case <-second:
			t.Fatal("the second push ended before the driver died; the case needs its call lost")
		default:
		}
		f.Restart()
		receive := s.receiveServices()
		if len(receive) != 2 {
			t.Fatalf("receive services %+v, want two", receive)
		}
		for _, service := range receive {
			if got := s.settle(service.ID); got != sourcedriver.Applied {
				t.Errorf("Settle(%s) after the restart = %v, want applied", service.ID, got)
			}
		}
		// The clients see their pushes end, whatever the outcome: the calls
		// were lost.
		awaitResult(t, first, "the first push")
		awaitResult(t, second, "the second push")
		s.wantApprovals(approval{branch: "work", old: a, new: b}, approval{branch: "work", old: b, new: c})
		s.wantHead("work", c)
	}},
}
