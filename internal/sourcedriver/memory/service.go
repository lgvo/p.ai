package memory

// There is no Git wire protocol in memory, so the in-memory Git service
// speaks a line protocol of its own, which keeps the rules of P's Git
// service. Every line ends in a newline; fields are separated by one space;
// an absent object ID is "-".
//
//   - The service speaks first: "head <id> refs/heads/<name>" for every head,
//     in byte order of name, then "end".
//   - Upload: the client sends "want <id>" lines, then "done". The service
//     answers "commit <id> [<parent>...]" for every commit the wants reach,
//     then "end", or "error <reason>" when a want is not an advertised tip.
//   - Receive: the client sends "update <old> <new> <ref>" lines, then its
//     objects as "commit <id> [<parent>...]" and "tree <id>" lines, then
//     "done". The service answers "ok" or "error <reason>".

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// gitService is a Git service a driver runs.
type gitService struct {
	driver      *Driver
	service     sourcedriver.Service
	incarnation string // of the repository it serves
	// mu is held while the service approves and applies its update, so
	// that once ended is set nothing it started can change a head.
	mu    sync.Mutex
	ended bool
}

// SetOtherRef points ref, a ref outside refs/heads such as a tag or a
// private ref of P's, at id in the project's repository.
func (w *World) SetOtherRef(project, ref string, id sourcedriver.OID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	if r.otherRefs == nil {
		r.otherRefs = map[string]sourcedriver.OID{}
	}
	r.otherRefs[ref] = id
}

// ServeGit serves one command of P's Git service over the line protocol
// above. It returns when the service ended, ran past its deadline, its
// context ended or the driver died; from then on nothing it started can
// change a head.
func (d *Driver) ServeGit(ctx context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
	kind, project, ok := parseCommand(command)
	if !ok {
		return fmt.Errorf("the SSH command is not a Git service's: %w", sourcedriver.ErrInvalid)
	}
	if err := d.alive(); err != nil {
		return err
	}
	service, approve, err := open(kind, project)
	if err != nil {
		return err
	}
	if err := checkService(service, kind, project); err != nil {
		return err
	}
	s, err := d.startService(service)
	if err != nil {
		return err
	}
	defer d.w.endService(s)
	ctx, cancel := context.WithTimeout(ctx, service.Deadline)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.serve(stream, approve) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("the service ran past its deadline: %w", sourcedriver.ErrLimit)
		}
		return ctx.Err()
	case <-d.dead:
		return d.alive()
	}
}

// parseCommand reads the SSH command as today's Git service does: the
// service's word, then the project in single quotes, a leading slash
// allowed.
func parseCommand(raw string) (sourcedriver.ServiceKind, string, bool) {
	var kind sourcedriver.ServiceKind
	if rest, ok := strings.CutPrefix(raw, "git-upload-pack '"); ok {
		kind, raw = sourcedriver.ServiceUpload, rest
	} else if rest, ok := strings.CutPrefix(raw, "git-receive-pack '"); ok {
		kind, raw = sourcedriver.ServiceReceive, rest
	} else {
		return "", "", false
	}
	project, ok := strings.CutSuffix(raw, "'")
	project = strings.TrimPrefix(project, "/")
	if !ok || strings.ContainsAny(project, "'\\\n\r\t") || !commandProject(project) {
		return "", "", false
	}
	return kind, project, true
}

// commandProject accepts P's project names as today's Git service does.
func commandProject(path string) bool {
	if path == "" || len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// checkService refuses a service open authorized for another command, or
// without the ceilings and head the service needs.
func checkService(s sourcedriver.Service, kind sourcedriver.ServiceKind, project string) error {
	switch {
	case s.Kind != kind || s.Project != project:
		return fmt.Errorf("core opened a %s service of %q for a %s command of %q", s.Kind, s.Project, kind, project)
	case s.ID == "" || s.MaxInput <= 0 || s.Deadline <= 0:
		return fmt.Errorf("the service lacks an ID or a ceiling: %w", sourcedriver.ErrInvalid)
	case kind == sourcedriver.ServiceReceive && !gitnames.Branch(s.Branch):
		return fmt.Errorf("the receive service's branch: %w", sourcedriver.ErrInvalid)
	}
	return nil
}

// startService records a service of a live driver on an existing
// repository.
func (d *Driver) startService(service sourcedriver.Service) (*gitService, error) {
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := d.alive(); err != nil {
		return nil, err
	}
	r := w.repos[service.Project]
	if r == nil {
		return nil, sourcedriver.ErrNotFound
	}
	s := &gitService{driver: d, service: service, incarnation: r.incarnation}
	if w.services == nil {
		w.services = map[*gitService]struct{}{}
	}
	w.services[s] = struct{}{}
	return s, nil
}

// endService ends s: it waits for an approval or update in progress, so
// nothing s started can change a head afterwards.
func (w *World) endService(s *gitService) {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.services, s)
}

// settleService answers Unknown while a live driver runs a service of that
// ID, and Applied otherwise: a dead driver's services can change nothing.
func (d *Driver) settleService(e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	if e.Service == "" {
		return sourcedriver.Unknown, fmt.Errorf("a service effect without its ID: %w", sourcedriver.ErrInvalid)
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	for s := range d.w.services {
		if s.service.ID == e.Service && s.driver.alive() == nil {
			return sourcedriver.Unknown, nil
		}
	}
	return sourcedriver.Applied, nil
}

// serve speaks the protocol with the client.
func (s *gitService) serve(stream sourcedriver.Stream, approve sourcedriver.Approve) error {
	in := bufio.NewReader(&ceilingReader{source: stream.In, left: s.service.MaxInput})
	heads := s.advertise()
	var advertisement strings.Builder
	for _, h := range heads {
		fmt.Fprintf(&advertisement, "head %s refs/heads/%s\n", h.Tip, h.Name)
	}
	advertisement.WriteString("end\n")
	if _, err := io.WriteString(stream.Out, advertisement.String()); err != nil {
		return err
	}
	if s.service.Kind == sourcedriver.ServiceUpload {
		return s.upload(in, stream.Out, heads)
	}
	return s.receive(in, stream.Out, approve)
}

// advertise lists the heads, and only the heads, in byte order of name.
func (s *gitService) advertise() []sourcedriver.Head {
	w := s.driver.w
	w.mu.Lock()
	defer w.mu.Unlock()
	var heads []sourcedriver.Head
	if r := w.repos[s.service.Project]; r != nil {
		for name, tip := range r.heads {
			heads = append(heads, sourcedriver.Head{Name: name, Tip: tip})
		}
	}
	slices.SortFunc(heads, func(a, b sourcedriver.Head) int { return strings.Compare(a.Name, b.Name) })
	return heads
}

// upload sends the commits the client's wants reach, refusing a want that is
// not an advertised tip, as Git does with P's upload settings.
func (s *gitService) upload(in *bufio.Reader, out io.Writer, heads []sourcedriver.Head) error {
	var wants []sourcedriver.OID
	for {
		line, err := readLine(in)
		if err != nil {
			return err
		}
		if line == "done" {
			break
		}
		id, ok := strings.CutPrefix(line, "want ")
		if !ok || !gitnames.OID(id) {
			return fmt.Errorf("an upload request line is malformed: %w", sourcedriver.ErrInvalid)
		}
		if !slices.ContainsFunc(heads, func(h sourcedriver.Head) bool { return h.Tip == sourcedriver.OID(id) }) {
			_, _ = io.WriteString(out, "error not our ref\n")
			return fmt.Errorf("a want of an object no head names: %w", sourcedriver.ErrConflict)
		}
		wants = append(wants, sourcedriver.OID(id))
	}
	var reply strings.Builder
	for _, line := range s.commitLines(wants) {
		reply.WriteString(line)
	}
	reply.WriteString("end\n")
	_, err := io.WriteString(out, reply.String())
	return err
}

// commitLines are the repository's commits the ids reach, as upload sends
// them.
func (s *gitService) commitLines(ids []sourcedriver.OID) []string {
	w := s.driver.w
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repos[s.service.Project]
	if r == nil {
		return nil
	}
	seen := map[sourcedriver.OID]bool{}
	var lines []string
	for stack := slices.Clone(ids); len(stack) > 0; {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		o := r.objects[id]
		if seen[id] || o == nil || o.typ != commitObject {
			continue
		}
		seen[id] = true
		line := "commit " + string(id)
		for _, p := range o.parents {
			line += " " + string(p)
		}
		lines = append(lines, line+"\n")
		stack = append(stack, o.parents...)
	}
	return lines
}

// update is one ref update a push asks for; an empty ID is absent.
type update struct {
	old, new sourcedriver.OID
	ref      string
}

// receive applies a push of exactly one update of the service's head, to a
// commit, a fast-forward or a creation, after Approve. The pushed objects
// stay apart until then.
func (s *gitService) receive(in *bufio.Reader, out io.Writer, approve sourcedriver.Approve) error {
	updates, pushed, err := readPush(in)
	if err != nil {
		return err
	}
	answer := "ok"
	if reason := s.applyPush(updates, pushed, approve); reason != "" {
		answer = "error " + reason
	}
	_, err = io.WriteString(out, answer+"\n")
	return err
}

// applyPush checks the push, has core approve it, and moves the head by
// compare-and-swap from the old value. It answers why it refused, or "".
func (s *gitService) applyPush(updates []update, pushed map[sourcedriver.OID]*object, approve sourcedriver.Approve) string {
	if len(updates) != 1 {
		return "a push updates exactly one head"
	}
	u := updates[0]
	if reason := s.check(u, pushed); reason != "" {
		return reason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.driver.alive() != nil {
		return "the service ended"
	}
	if err := approve(s.service.Branch, u.old, u.new); err != nil {
		return "core refused the update"
	}
	return s.apply(u, pushed)
}

// check refuses an update of another head, a deletion, a new value that is
// not a commit, a history that is incomplete, and a non-fast-forward.
func (s *gitService) check(u update, pushed map[sourcedriver.OID]*object) string {
	switch {
	case u.ref != "refs/heads/"+s.service.Branch:
		return "only refs/heads/" + s.service.Branch + " may change"
	case u.new == "":
		return "deletion prohibited"
	}
	w := s.driver.w
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repos[s.service.Project]
	if r == nil {
		return "the repository is gone"
	}
	lookup := func(id sourcedriver.OID) *object {
		if o := pushed[id]; o != nil {
			return o
		}
		return r.objects[id]
	}
	for _, o := range pushed {
		for _, p := range o.parents {
			if lookup(p) == nil {
				return "the pushed history is incomplete"
			}
		}
	}
	if o := lookup(u.new); o == nil || o.typ != commitObject {
		return "the new value is not a commit"
	}
	if u.old != "" && !reaches(lookup, u.new, u.old) {
		return "not a fast-forward"
	}
	return ""
}

// apply moves the head from u.old to u.new, bringing the pushed objects in,
// only while the driver lives and the repository and head are as the
// service saw them.
func (s *gitService) apply(u update, pushed map[sourcedriver.OID]*object) string {
	w := s.driver.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.driver.alive() != nil {
		return "the service ended"
	}
	if err := w.checkStorage(); err != nil {
		return "the storage is unsafe"
	}
	r := w.repos[s.service.Project]
	if r == nil || r.incarnation != s.incarnation {
		return "the repository was made again"
	}
	name := s.service.Branch
	if _, symbolic := r.symbolic[name]; symbolic || r.heads[name] != u.old {
		return "the head moved"
	}
	for id, o := range pushed {
		if r.objects[id] == nil {
			r.objects[id] = o
		}
	}
	r.heads[name] = u.new
	return ""
}

// reaches reports whether from reaches id through parents, from included.
func reaches(lookup func(sourcedriver.OID) *object, from, id sourcedriver.OID) bool {
	seen := map[sourcedriver.OID]bool{}
	for stack := []sourcedriver.OID{from}; len(stack) > 0; {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if next == id {
			return true
		}
		if seen[next] {
			continue
		}
		seen[next] = true
		if o := lookup(next); o != nil && o.typ == commitObject {
			stack = append(stack, o.parents...)
		}
	}
	return false
}

// readPush reads a push request up to its "done".
func readPush(in *bufio.Reader) ([]update, map[sourcedriver.OID]*object, error) {
	var updates []update
	pushed := map[sourcedriver.OID]*object{}
	malformed := fmt.Errorf("a push request line is malformed: %w", sourcedriver.ErrInvalid)
	for {
		line, err := readLine(in)
		if err != nil {
			return nil, nil, err
		}
		fields := strings.Split(line, " ")
		switch {
		case line == "done":
			return updates, pushed, nil
		case fields[0] == "update" && len(fields) == 4:
			old, okOld := pushedID(fields[1])
			new, okNew := pushedID(fields[2])
			if !okOld || !okNew {
				return nil, nil, malformed
			}
			updates = append(updates, update{old: old, new: new, ref: fields[3]})
		case fields[0] == "commit" && len(fields) >= 2 || fields[0] == "tree" && len(fields) == 2:
			o := &object{typ: objectType(fields[0])}
			for _, field := range fields[1:] {
				if !gitnames.OID(field) {
					return nil, nil, malformed
				}
			}
			for _, p := range fields[2:] {
				o.parents = append(o.parents, sourcedriver.OID(p))
			}
			pushed[sourcedriver.OID(fields[1])] = o
		default:
			return nil, nil, malformed
		}
	}
}

// pushedID reads an object ID of an update, "-" being absent.
func pushedID(field string) (sourcedriver.OID, bool) {
	if field == "-" {
		return "", true
	}
	return sourcedriver.OID(field), gitnames.OID(field)
}

// readLine reads one line without its newline.
func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// ceilingReader passes at most left bytes of the client's input; a byte
// more fails the service.
type ceilingReader struct {
	source io.Reader
	left   int64
}

func (r *ceilingReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		var one [1]byte
		if n, err := r.source.Read(one[:]); n == 0 {
			return 0, err
		}
		return 0, fmt.Errorf("the client's input passed the service's ceiling: %w", sourcedriver.ErrLimit)
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.source.Read(p)
	r.left -= int64(n)
	return n, err
}
