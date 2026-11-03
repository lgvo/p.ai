package memory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// exportRef names the exported commit in an export, as the bundled driver
// names it in its bundle.
const exportRef = "refs/p/source"

// exportHeader starts the in-memory driver's export file.
const exportHeader = "p-memory-export 1"

func (d *Driver) InspectCommit(_ context.Context, project string, id sourcedriver.OID) (sourcedriver.Commit, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Commit{}, err
	}
	if err := validCommit(id); err != nil {
		return sourcedriver.Commit{}, err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	r, err := d.w.open(project)
	if err != nil {
		return sourcedriver.Commit{}, err
	}
	if o := r.objects[id]; o == nil || o.typ != commitObject {
		return sourcedriver.Commit{}, nil
	}
	return sourcedriver.Commit{Present: true, Reachable: r.reachableFromHead(id)}, nil
}

// reachableFromHead reports whether some head reaches id. The caller holds
// w.mu.
func (r *repo) reachableFromHead(id sourcedriver.OID) bool {
	for _, tip := range r.heads {
		if r.reachable(tip, id) {
			return true
		}
	}
	return false
}

// history is every commit the tips reach, the tips included. The caller
// holds w.mu.
func (r *repo) history(tips ...sourcedriver.OID) map[sourcedriver.OID]bool {
	seen := map[sourcedriver.OID]bool{}
	stack := slices.Clone(tips)
	for len(stack) > 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		o := r.objects[next]
		if seen[next] || o == nil || o.typ != commitObject {
			continue
		}
		seen[next] = true
		stack = append(stack, o.parents...)
	}
	return seen
}

func (d *Driver) LosingCommits(_ context.Context, project string, removed []string) (sourcedriver.Loss, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Loss{}, err
	}
	for _, name := range removed {
		if err := validBranch(name); err != nil {
			return sourcedriver.Loss{}, err
		}
	}
	var commits []sourcedriver.OID
	heads, err := d.w.observe(project, func(r *repo, heads []sourcedriver.Head) error {
		var err error
		commits, err = r.losing(heads, removed)
		return err
	})
	if err != nil {
		return sourcedriver.Loss{}, err
	}
	return sourcedriver.Loss{Heads: heads, Commits: commits}, nil
}

// losing is the commits the removed heads reach and no other head does,
// in byte order; nil removed removes every head. The caller holds w.mu.
func (r *repo) losing(heads []sourcedriver.Head, removed []string) ([]sourcedriver.OID, error) {
	var gone, kept []sourcedriver.OID
	for _, h := range heads {
		if removed == nil || slices.Contains(removed, h.Name) {
			gone = append(gone, h.Tip)
		} else {
			kept = append(kept, h.Tip)
		}
	}
	remaining := r.history(kept...)
	var lost []sourcedriver.OID
	for id := range r.history(gone...) {
		if !remaining[id] {
			lost = append(lost, id)
		}
	}
	if len(lost) > sourcedriver.MaxLosingCommits {
		return nil, fmt.Errorf("%d losing commits: %w", len(lost), sourcedriver.ErrLimit)
	}
	slices.Sort(lost)
	return lost, nil
}

// Export writes a file into dir that names the commit and its history, in a
// form ExportedHistory reads back: no Git bundle, since the World holds no
// trees. It stages the file under a temporary name and links it into place,
// so the export appears complete or not at all and never replaces a file.
func (d *Driver) Export(ctx context.Context, project string, id sourcedriver.OID, dir string) (sourcedriver.Export, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Export{}, err
	}
	if err := validCommit(id); err != nil {
		return sourcedriver.Export{}, err
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return sourcedriver.Export{}, fmt.Errorf("export directory %q: %w", dir, sourcedriver.ErrInvalid)
	}
	if err := privateStaging(dir); err != nil {
		return sourcedriver.Export{}, err
	}
	content, cut, err := d.w.exportContent(project, id)
	if err != nil {
		return sourcedriver.Export{}, err
	}
	path := filepath.Join(dir, "source.bundle")
	if err := stageAndLink(ctx, dir, path, content, cut); err != nil {
		return sourcedriver.Export{}, err
	}
	return sourcedriver.Export{Bundle: path, Ref: exportRef, Commit: id}, nil
}

// exportContent is the export of id, which must be a commit a head
// reaches, and whether a control cut this export off.
func (w *World) exportContent(project string, id sourcedriver.OID) (string, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, err := w.open(project)
	if err != nil {
		return "", false, err
	}
	if o := r.objects[id]; o == nil || o.typ != commitObject || !r.reachableFromHead(id) {
		return "", false, fmt.Errorf("%s is not a commit a head reaches: %w", id, sourcedriver.ErrConflict)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s %s\n", exportHeader, exportRef, id)
	commits := make([]sourcedriver.OID, 0)
	for c := range r.history(id) {
		commits = append(commits, c)
	}
	slices.Sort(commits)
	for _, c := range commits {
		fields := append([]string{string(c)}, oidStrings(r.objects[c].parents)...)
		fmt.Fprintln(&b, strings.Join(fields, " "))
	}
	cut := w.cutExport
	w.cutExport = false
	return b.String(), cut, nil
}

func oidStrings(ids []sourcedriver.OID) []string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = string(id)
	}
	return s
}

// stageAndLink writes content to a temporary file in dir and links it to
// path; a cut export, or one whose context ended, removes what it staged.
func stageAndLink(ctx context.Context, dir, path, content string, cut bool) error {
	staged, err := os.CreateTemp(dir, ".p-export-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	_, err = staged.WriteString(content)
	if err = errors.Join(err, staged.Close()); err != nil {
		return err
	}
	if cut {
		return context.DeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Link(staged.Name(), path)
}

// privateStaging accepts only a private directory of P's account, as the
// bundled driver does.
func privateStaging(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
		return errors.New("the export directory is not private")
	}
	return nil
}

// Snapshot is the project's repository written out whole.
func (w *World) Snapshot(project string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	var lines []string
	for name, tip := range r.heads {
		lines = append(lines, "head "+name+" "+string(tip))
	}
	for name, target := range r.symbolic {
		lines = append(lines, "symbolic "+name+" "+target)
	}
	for ref, id := range r.otherRefs {
		lines = append(lines, "ref "+ref+" "+string(id))
	}
	for id, o := range r.objects {
		lines = append(lines, fmt.Sprintf("object %s %s %s %v", id, o.typ, o.target, o.parents))
	}
	slices.Sort(lines)
	return r.incarnation + " " + r.head + "\n" + strings.Join(lines, "\n")
}

// ExportedHistory reads an export as a clone would: the commits its ref
// reaches through the parents the export records, the ref's commit first.
// It returns nil for an export it cannot read whole.
func (w *World) ExportedHistory(e sourcedriver.Export) []sourcedriver.OID {
	file, err := os.Open(e.Bundle)
	if err != nil {
		return nil
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	if !lines.Scan() || lines.Text() != exportHeader || !lines.Scan() || lines.Text() != e.Ref+" "+string(e.Commit) {
		return nil
	}
	parents := map[sourcedriver.OID][]sourcedriver.OID{}
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) == 0 {
			return nil
		}
		ps := make([]sourcedriver.OID, 0, len(fields)-1)
		for _, p := range fields[1:] {
			ps = append(ps, sourcedriver.OID(p))
		}
		parents[sourcedriver.OID(fields[0])] = ps
	}
	history := []sourcedriver.OID{e.Commit}
	seen := map[sourcedriver.OID]bool{e.Commit: true}
	for i := 0; i < len(history); i++ {
		ps, ok := parents[history[i]]
		if !ok {
			return nil
		}
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				history = append(history, p)
			}
		}
	}
	return history
}

// CutExport cuts the next Export off once it has staged its file.
func (w *World) CutExport() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cutExport = true
}
