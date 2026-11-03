package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// errMoved is a head observation whose two reads differ: a new read may
// succeed.
var errMoved = fmt.Errorf("the heads moved while they were read: %w", sourcedriver.ErrConflict)

// maxHeadLine bounds one line of the heads listing: an object ID, a ref of
// P's longest branch, and a symbolic ref's target, which Git bounds alike.
const maxHeadLine = 64 + 1 + len("refs/heads/") + 200 + 1 + 4096 + 1

// open proves the project's repository a complete, safe bare repository and
// returns its path; a project without one is ErrNotFound.
func (d *Driver) open(ctx context.Context, project string) (string, error) {
	repo, err := d.repository(project)
	if err != nil {
		return "", err
	}
	present, err := d.storagePresent()
	if err != nil {
		return "", err
	}
	if present {
		_, err = os.Lstat(repo)
	}
	if !present || errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("project %q: %w", project, sourcedriver.ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if _, err := d.checkRepository(ctx, repo); err != nil {
		return "", err
	}
	return repo, nil
}

// openToChange is open for a mutation, which proves the storage first.
func (d *Driver) openToChange(ctx context.Context, project string) (string, error) {
	if _, err := d.repository(project); err != nil {
		return "", err
	}
	if err := d.prepareStorage(); err != nil {
		return "", err
	}
	return d.open(ctx, project)
}

// validBranch refuses a branch name CheckNames refuses.
func validBranch(name string) error {
	if !gitnames.Branch(name) {
		return fmt.Errorf("branch %q: %w", name, sourcedriver.ErrInvalid)
	}
	return nil
}

// validCommit refuses a commit ID CheckNames refuses.
func validCommit(id sourcedriver.OID) error {
	if !gitnames.OID(string(id)) {
		return fmt.Errorf("commit %q: %w", id, sourcedriver.ErrInvalid)
	}
	return nil
}

// lines splits Git's newline-terminated output into its lines.
func lines(out string) ([]string, error) {
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\n") {
		return nil, errors.New("Git's output was cut short")
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n"), nil
}

// readHeads reads every head once: in byte order, each a P branch at a
// commit present in the repository. A symbolic ref that resolves is an
// error, since deleting its target would leave it dangling; Git lists no
// dangling one, which holds nothing.
func (d *Driver) readHeads(ctx context.Context, repo string) ([]sourcedriver.Head, error) {
	listed, err := d.git(ctx, repo, (sourcedriver.MaxHeads+1)*maxHeadLine, "for-each-ref",
		"--count="+strconv.Itoa(sourcedriver.MaxHeads+1), "--format=%(objectname) %(refname) %(symref)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	if listed.Exit != 0 || listed.Incomplete {
		return nil, errors.New("Git could not list the heads")
	}
	all, err := lines(listed.Stdout)
	if err != nil {
		return nil, err
	}
	if len(all) > sourcedriver.MaxHeads {
		return nil, fmt.Errorf("more than %d heads: %w", sourcedriver.MaxHeads, sourcedriver.ErrLimit)
	}
	heads := make([]sourcedriver.Head, 0, len(all))
	for _, line := range all {
		h, err := parseHead(line)
		if err != nil {
			return nil, err
		}
		if len(heads) > 0 && h.Name <= heads[len(heads)-1].Name {
			return nil, errors.New("Git listed the heads out of order")
		}
		heads = append(heads, h)
	}
	tips := make([]sourcedriver.OID, len(heads))
	for i, h := range heads {
		tips[i] = h.Tip
	}
	types, err := d.objectTypes(ctx, repo, tips)
	if err != nil {
		return nil, err
	}
	for _, h := range heads {
		if types[h.Tip] != "commit" {
			return nil, fmt.Errorf("the head %q is not at a commit of the repository", h.Name)
		}
	}
	return heads, nil
}

// parseHead reads one line of the heads listing.
func parseHead(line string) (sourcedriver.Head, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 {
		return sourcedriver.Head{}, errors.New("Git listed a malformed head")
	}
	name, ok := strings.CutPrefix(fields[1], "refs/heads/")
	switch {
	case fields[2] != "":
		return sourcedriver.Head{}, fmt.Errorf("the symbolic ref %q holds a head's name", fields[1])
	case !ok || !gitnames.Branch(name):
		return sourcedriver.Head{}, fmt.Errorf("the head %q is not a P branch", fields[1])
	case !gitnames.OID(fields[0]):
		return sourcedriver.Head{}, errors.New("Git listed a malformed head")
	}
	return sourcedriver.Head{Name: name, Tip: sourcedriver.OID(fields[0])}, nil
}

// maxTypeAnswer bounds Git's answer for one object: the longest ID, a
// SHA-256 one, and its longest word, which Git writes for an ID it cannot
// tell from another; "missing" is one shorter.
const maxTypeAnswer = 64 + len(" ambiguous\n")

// objectTypes reads the type of each object, "missing" for an object the
// repository does not hold.
func (d *Driver) objectTypes(ctx context.Context, repo string, ids []sourcedriver.OID) (map[sourcedriver.OID]string, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(unique) == 0 {
		return map[sourcedriver.OID]string{}, nil
	}
	var input strings.Builder
	for _, id := range unique {
		input.WriteString(string(id) + "\n")
	}
	read, err := d.gitInput(ctx, repo, input.String(), len(unique)*maxTypeAnswer, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	answers, err := lines(read.Stdout)
	if read.Exit != 0 || read.Incomplete || err != nil || len(answers) != len(unique) {
		return nil, errors.New("Git could not read the objects' types")
	}
	types := make(map[sourcedriver.OID]string, len(unique))
	for i, answer := range answers {
		id, typ, ok := strings.Cut(answer, " ")
		if !ok || id != string(unique[i]) || typ == "" {
			return nil, errors.New("Git read the objects' types malformed")
		}
		types[unique[i]] = typ
	}
	return types, nil
}

// gitInput is git with input on its standard input.
func (d *Driver) gitInput(ctx context.Context, repo, input string, max int, args ...string) (gitResult, error) {
	return d.runInput(ctx, repo, nil, input, max, append([]string{"--git-dir=" + repo}, args...)...)
}

// observe reads every head of the repository, runs use on them, and reads
// them again: heads that moved between the reads are errMoved.
func (d *Driver) observe(ctx context.Context, repo string, use func([]sourcedriver.Head) error) ([]sourcedriver.Head, error) {
	first, err := d.readHeads(ctx, repo)
	if err != nil {
		return nil, err
	}
	if use != nil {
		if err := use(first); err != nil {
			return nil, err
		}
	}
	if between := d.seams.takeBetweenReads(); between != nil {
		between()
	}
	again, err := d.readHeads(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(first, again) {
		return nil, errMoved
	}
	return first, nil
}

// Branches returns every head as one observation, read twice.
func (d *Driver) Branches(ctx context.Context, project string) ([]sourcedriver.Head, error) {
	repo, err := d.open(ctx, project)
	if err != nil {
		return nil, err
	}
	return d.observe(ctx, repo, nil)
}

// headState is what holds a branch name: a head's tip, a symbolic ref, or
// nothing.
type headState struct {
	tip      sourcedriver.OID
	symbolic bool
}

// readHead reads what holds refs/heads/<name>. It checks for a symbolic ref
// first: show-ref follows one that resolves and reports a dangling one
// missing.
func (d *Driver) readHead(ctx context.Context, repo, name string) (headState, error) {
	ref := "refs/heads/" + name
	symbolic, err := d.git(ctx, repo, 4096, "symbolic-ref", "--quiet", ref)
	if err != nil {
		return headState{}, err
	}
	switch symbolic.Exit {
	case 0:
		return headState{symbolic: true}, nil
	case 1:
	default:
		return headState{}, errors.New("Git could not read the symbolic ref")
	}
	// Git returns 128, not 1, for a missing ref with --hash; its quiet
	// verification has an unambiguous absence status.
	exists, err := d.git(ctx, repo, 0, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return headState{}, err
	}
	switch exists.Exit {
	case 1:
		return headState{}, nil
	case 0:
	default:
		return headState{}, errors.New("Git could not read the head")
	}
	tip, err := d.git(ctx, repo, 128, "show-ref", "--verify", "--hash", ref)
	if err != nil {
		return headState{}, err
	}
	id := strings.TrimSuffix(tip.Stdout, "\n")
	if tip.Exit != 0 || !gitnames.OID(id) {
		return headState{}, errors.New("Git read a malformed head")
	}
	return headState{tip: sourcedriver.OID(id)}, nil
}

func (d *Driver) InspectBranch(ctx context.Context, project, name string) (sourcedriver.OID, bool, error) {
	if err := validBranch(name); err != nil {
		return "", false, err
	}
	repo, err := d.open(ctx, project)
	if err != nil {
		return "", false, err
	}
	s, err := d.readHead(ctx, repo, name)
	if err != nil {
		return "", false, err
	}
	if s.symbolic {
		return "", false, fmt.Errorf("a symbolic ref holds %q: %w", name, sourcedriver.ErrConflict)
	}
	return s.tip, s.tip != "", nil
}

// CreateBranch creates the head by compare-and-swap from the zero ID, with
// --no-deref so that a dangling symbolic ref is refused, not followed to
// create another branch.
func (d *Driver) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	if err := errors.Join(validBranch(name), validCommit(at)); err != nil {
		return err
	}
	repo, err := d.openToChange(ctx, project)
	if err != nil {
		return err
	}
	if err := d.expect(ctx, repo, name, ""); err != nil {
		return err
	}
	types, err := d.objectTypes(ctx, repo, []sourcedriver.OID{at})
	if err != nil {
		return err
	}
	if types[at] != "commit" {
		return fmt.Errorf("%s is not a commit of the repository: %w", at, sourcedriver.ErrConflict)
	}
	if err := g(); err != nil {
		return err
	}
	zero := strings.Repeat("0", len(at))
	return d.landHead(ctx, repo, name, "", "update-ref", "--no-deref", "refs/heads/"+name, string(at), zero)
}

// DeleteBranch deletes the head by compare-and-swap from tip and reads its
// absence back.
func (d *Driver) DeleteBranch(ctx context.Context, project, name string, tip sourcedriver.OID, g sourcedriver.Gate) error {
	if err := errors.Join(validBranch(name), validCommit(tip)); err != nil {
		return err
	}
	repo, err := d.openToChange(ctx, project)
	if err != nil {
		return err
	}
	if err := d.expect(ctx, repo, name, tip); err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if err := d.landHead(ctx, repo, name, tip, "update-ref", "--no-deref", "-d", "refs/heads/"+name, string(tip)); err != nil {
		return err
	}
	s, err := d.readHead(ctx, repo, name)
	if err != nil || s.symbolic || s.tip != "" {
		return errors.Join(err, fmt.Errorf("the deleted head is not absent: %w", sourcedriver.ErrUnknownOutcome))
	}
	return nil
}

// expect refuses unless the name holds exactly tip, a head, or nothing when
// tip is empty.
func (d *Driver) expect(ctx context.Context, repo, name string, tip sourcedriver.OID) error {
	s, err := d.readHead(ctx, repo, name)
	if err != nil {
		return err
	}
	if s.symbolic || s.tip != tip {
		return fmt.Errorf("the head %q is not as expected: %w", name, sourcedriver.ErrConflict)
	}
	return nil
}

// landHead runs the head effect args after the gate. Git's compare-and-swap
// is the authority: a refusal leaves the head as it was, and only a call
// cut off while Git ran leaves the outcome unknown. old is the state the
// name held before the effect.
func (d *Driver) landHead(ctx context.Context, repo, name string, old sourcedriver.OID, args ...string) error {
	landed, err := d.git(ctx, repo, 4096, args...)
	if err != nil {
		return fmt.Errorf("%w: %v", sourcedriver.ErrUnknownOutcome, err)
	}
	if landed.Exit != 0 {
		if s, err := d.readHead(ctx, repo, name); err == nil && (s.symbolic || s.tip != old) {
			return fmt.Errorf("the head %q moved: %w", name, sourcedriver.ErrConflict)
		}
		return errors.New("Git refused the head's update")
	}
	if died := d.seams.takeEffectLanded(); died != nil {
		if err := died(); err != nil {
			return fmt.Errorf("%w: %v", sourcedriver.ErrUnknownOutcome, err)
		}
	}
	return nil
}
