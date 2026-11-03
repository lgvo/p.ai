package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lgvo/p.ai/internal/control"
)

// BundleRef names the exported commit inside a bundle. It is the bundle's
// only ref; P's repository never holds it.
const BundleRef = "refs/p/source"

// Bundle is one exported commit: the file at Path holds Commit and every
// commit it reaches, with Ref naming Commit.
type Bundle struct{ Path, Ref, Commit string }

// ExportBundle writes dir/source.bundle for an image build. A temporary bare
// repository inside dir borrows P's objects through its alternates and holds
// the only ref, so P's repository is only read.
func (b *Backend) ExportBundle(ctx context.Context, project, commit, dir string) (Bundle, error) {
	if !validProject(project) || !validOID(commit) || allZero(commit) || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return Bundle{}, control.ErrInvalid
	}
	if err := privateStaging(dir); err != nil {
		return Bundle{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var bundle Bundle
	// The origin lock is also the future P-controlled GC lock: the export reads
	// P's objects in place, so none may be pruned until the bundle is written.
	err := b.WithOrigin(ctx, project, func(_ *OriginScope) error {
		var e error
		bundle, e = b.exportBundle(ctx, project, commit, dir)
		return e
	})
	return bundle, err
}

func (b *Backend) exportBundle(ctx context.Context, project, commit, dir string) (Bundle, error) {
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return Bundle{}, err
	}
	source := &gitBroker{backend: b, project: project}
	if err := source.commitType(ctx, commit); err != nil {
		return Bundle{}, err
	}
	if err := source.reachableFromHead(ctx, commit); err != nil {
		return Bundle{}, err
	}
	work, err := os.MkdirTemp(dir, ".p-export-")
	if err != nil {
		return Bundle{}, err
	}
	defer os.RemoveAll(work)
	export := filepath.Join(work, "repository.git")
	written := filepath.Join(work, "source.bundle")
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, b.gitPath, args...)
		cmd.Env = append(b.gitEnv(), "GIT_NO_LAZY_FETCH=1")
		var out, diagnostic sourceGitOutput
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		if e := cmd.Run(); e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("Git bundle export failed")
		}
		return strings.TrimSpace(string(out.text)), nil
	}
	if _, err := git("init", "--quiet", "--bare", "--template=", export); err != nil {
		return Bundle{}, err
	}
	if err := os.WriteFile(filepath.Join(export, "objects", "info", "alternates"), []byte(filepath.Join(repo, "objects")+"\n"), 0600); err != nil {
		return Bundle{}, err
	}
	if _, err := git("-C", export, "update-ref", BundleRef, commit); err != nil {
		return Bundle{}, err
	}
	if _, err := git("-C", export, "bundle", "create", "--quiet", written, BundleRef); err != nil {
		return Bundle{}, err
	}
	heads, err := git("-C", export, "bundle", "list-heads", written)
	if err != nil {
		return Bundle{}, err
	}
	if heads != commit+" "+BundleRef {
		return Bundle{}, errors.New("exported bundle does not name exactly the commit")
	}
	// A link never replaces an existing file, and the bundle appears only
	// complete and verified.
	path := filepath.Join(dir, "source.bundle")
	if err := os.Link(written, path); err != nil {
		return Bundle{}, err
	}
	if err := os.RemoveAll(work); err != nil {
		return Bundle{}, errors.Join(err, os.Remove(path))
	}
	return Bundle{Path: path, Ref: BundleRef, Commit: commit}, nil
}

// privateStaging accepts only the private directory core made for this build:
// the bundle is written nowhere another user could read or replace it.
func privateStaging(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		return errors.New("bundle staging directory is not private")
	}
	return nil
}
