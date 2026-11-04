package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// exportRef names the exported commit inside a bundle. It is the bundle's
// only ref; P's repository never holds it.
const exportRef = "refs/p/source"

func (d *Driver) InspectCommit(ctx context.Context, project string, id sourcedriver.OID) (sourcedriver.Commit, error) {
	if err := validCommit(id); err != nil {
		return sourcedriver.Commit{}, err
	}
	repo, err := d.open(ctx, project)
	if err != nil {
		return sourcedriver.Commit{}, err
	}
	types, err := d.objectTypes(ctx, repo, []sourcedriver.OID{id})
	if err != nil {
		return sourcedriver.Commit{}, err
	}
	if types[id] != "commit" {
		return sourcedriver.Commit{}, nil
	}
	reachable, err := d.reachableFromHead(ctx, repo, id)
	if err != nil {
		return sourcedriver.Commit{}, err
	}
	return sourcedriver.Commit{Present: true, Reachable: reachable}, nil
}

// reachableFromHead reports whether some head reaches the commit id. A head
// Git names proves it. Git names none also when it skipped a head it could
// not read, sometimes without saying so, as for a head at an object the
// repository does not hold: before answering that none does, it reads every
// head as Branches does, which fails on such a head.
func (d *Driver) reachableFromHead(ctx context.Context, repo string, id sourcedriver.OID) (bool, error) {
	found, err := d.git(ctx, repo, len("refs/heads/")+4096+1, "for-each-ref", "--count=1", "--contains="+string(id), "--format=%(refname)", "refs/heads/")
	if err != nil {
		return false, err
	}
	if found.Exit != 0 {
		return false, errors.New("Git could not tell which heads reach the commit")
	}
	if strings.HasPrefix(found.Stdout, "refs/heads/") {
		return true, nil
	}
	if found.Incomplete {
		return false, errors.New("Git could not tell which heads reach the commit")
	}
	if _, err := d.readHeads(ctx, repo); err != nil {
		return false, err
	}
	return false, nil
}

func (d *Driver) LosingCommits(ctx context.Context, project string, removed []string) (sourcedriver.Loss, error) {
	for _, name := range removed {
		if err := validBranch(name); err != nil {
			return sourcedriver.Loss{}, err
		}
	}
	repo, err := d.open(ctx, project)
	if err != nil {
		return sourcedriver.Loss{}, err
	}
	var commits []sourcedriver.OID
	heads, err := d.observe(ctx, repo, func(heads []sourcedriver.Head) error {
		var err error
		commits, err = d.losing(ctx, repo, heads, removed)
		return err
	})
	if err != nil {
		return sourcedriver.Loss{}, err
	}
	return sourcedriver.Loss{Heads: heads, Commits: commits}, nil
}

// losing is the commits the removed heads reach and no other head does,
// read with one bounded rev-list; nil removed removes every head.
func (d *Driver) losing(ctx context.Context, repo string, heads []sourcedriver.Head, removed []string) ([]sourcedriver.OID, error) {
	var gone, kept []string
	for _, h := range heads {
		if removed == nil || slices.Contains(removed, h.Name) {
			gone = append(gone, string(h.Tip))
		} else {
			kept = append(kept, string(h.Tip))
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	limit := sourcedriver.MaxLosingCommits + 1
	args := append([]string{"rev-list", "--max-count=" + strconv.Itoa(limit)}, gone...)
	args = append(append(args, "--not"), kept...)
	listed, err := d.git(ctx, repo, limit*65, args...)
	if err != nil {
		return nil, err
	}
	all, err := lines(listed.Stdout)
	if listed.Exit != 0 || listed.Incomplete || err != nil {
		return nil, errors.New("Git could not list the commits the heads reach")
	}
	if len(all) > sourcedriver.MaxLosingCommits {
		return nil, fmt.Errorf("more than %d losing commits: %w", sourcedriver.MaxLosingCommits, sourcedriver.ErrLimit)
	}
	commits := make([]sourcedriver.OID, len(all))
	for i, id := range all {
		if !gitnames.OID(id) {
			return nil, errors.New("Git listed a malformed commit")
		}
		commits[i] = sourcedriver.OID(id)
	}
	return commits, nil
}

// Export writes dir/source.bundle. A temporary bare repository inside dir
// borrows P's objects through its alternates and holds the only ref, so
// P's repository is only read; the bundle appears in dir only complete and
// verified, by a link that never replaces a file.
func (d *Driver) Export(ctx context.Context, project string, id sourcedriver.OID, dir string) (sourcedriver.Export, error) {
	if err := validCommit(id); err != nil {
		return sourcedriver.Export{}, err
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return sourcedriver.Export{}, fmt.Errorf("export directory %q: %w", dir, sourcedriver.ErrInvalid)
	}
	if err := privateStaging(dir); err != nil {
		return sourcedriver.Export{}, err
	}
	ctx, cut := context.WithTimeout(ctx, sourcedriver.ExportDeadline)
	defer cut()
	repo, err := d.open(ctx, project)
	if err != nil {
		return sourcedriver.Export{}, err
	}
	if err := d.exportable(ctx, repo, id); err != nil {
		return sourcedriver.Export{}, err
	}
	work, err := os.MkdirTemp(dir, ".p-export-")
	if err != nil {
		return sourcedriver.Export{}, err
	}
	defer os.RemoveAll(work)
	written, err := d.writeBundle(ctx, cut, repo, id, work)
	if err != nil {
		return sourcedriver.Export{}, err
	}
	path := filepath.Join(dir, "source.bundle")
	if err := os.Link(written, path); err != nil {
		return sourcedriver.Export{}, err
	}
	if err := os.RemoveAll(work); err != nil {
		return sourcedriver.Export{}, errors.Join(err, os.Remove(path))
	}
	return sourcedriver.Export{Bundle: path, Ref: exportRef, Commit: id}, nil
}

// exportable refuses what is not a commit a head reaches.
func (d *Driver) exportable(ctx context.Context, repo string, id sourcedriver.OID) error {
	types, err := d.objectTypes(ctx, repo, []sourcedriver.OID{id})
	if err != nil {
		return err
	}
	if types[id] != "commit" {
		return fmt.Errorf("%s is not a commit of the repository: %w", id, sourcedriver.ErrConflict)
	}
	reachable, err := d.reachableFromHead(ctx, repo, id)
	if err != nil {
		return err
	}
	if !reachable {
		return fmt.Errorf("no head reaches %s: %w", id, sourcedriver.ErrConflict)
	}
	return nil
}

// writeBundle writes the bundle of id in work and returns its path, once
// Git verified that it names exactly the commit.
func (d *Driver) writeBundle(ctx context.Context, cut context.CancelFunc, repo string, id sourcedriver.OID, work string) (string, error) {
	export := filepath.Join(work, "repository.git")
	written := filepath.Join(work, "source.bundle")
	step := func(args ...string) (string, error) {
		done, err := d.run(ctx, work, nil, 4096, args...)
		if err != nil {
			return "", err
		}
		if done.Exit != 0 {
			return "", errors.New("the Git bundle export failed")
		}
		return strings.TrimSpace(done.Stdout), nil
	}
	if _, err := step("init", "--quiet", "--bare", "--template=", export); err != nil {
		return "", err
	}
	if staged := d.seams.takeExportStaged(); staged != nil {
		staged(cut)
	}
	if err := os.WriteFile(filepath.Join(export, "objects", "info", "alternates"), []byte(filepath.Join(repo, "objects")+"\n"), 0o600); err != nil {
		return "", err
	}
	if _, err := step("--git-dir="+export, "update-ref", exportRef, string(id)); err != nil {
		return "", err
	}
	if _, err := step("--git-dir="+export, "bundle", "create", "--quiet", written, exportRef); err != nil {
		return "", err
	}
	heads, err := step("--git-dir="+export, "bundle", "list-heads", written)
	if err != nil {
		return "", err
	}
	if heads != string(id)+" "+exportRef {
		return "", errors.New("the exported bundle does not name exactly the commit")
	}
	return written, nil
}

// privateStaging accepts only the private directory core made for this
// export: the bundle is written nowhere another user could read or replace
// it.
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
