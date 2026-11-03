package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// incarnationFile holds a repository's incarnation: a random token the
// driver writes before the repository appears at its address, so a
// repository made again carries another one. Git ignores the file.
const incarnationFile = "p-incarnation"

// Check makes the storage and its repositories directory when absent,
// proves both private to P's account, and proves that git runs.
func (d *Driver) Check(ctx context.Context) error {
	if err := d.prepareStorage(); err != nil {
		return err
	}
	if err := d.endEarlierServices(); err != nil {
		return err
	}
	result, err := d.run(ctx, d.storage, nil, 4096, "--version")
	if err != nil {
		return err
	}
	if result.Exit != 0 || !strings.HasPrefix(result.Stdout, "git version ") {
		return errors.New("the git executable does not run")
	}
	return nil
}

// repositories is the directory that holds every repository.
func (d *Driver) repositories() string { return filepath.Join(d.storage, "repositories") }

// prepareStorage is the storage half of Check, which every mutation makes
// first: it makes the storage and its repositories directory when absent
// (their parent must exist) and proves both private to P's account.
func (d *Driver) prepareStorage() error {
	for _, dir := range []string{d.storage, d.repositories()} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := privateDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

// storagePresent proves the storage private for a read: false when the
// storage or its repositories directory is absent, so no repository exists.
func (d *Driver) storagePresent() (bool, error) {
	for _, dir := range []string{d.storage, d.repositories()} {
		if err := privateDirectory(dir); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

// InspectRepository reports what is at the project's address: nothing, or
// a complete, safe bare repository. Anything else there is an error.
func (d *Driver) InspectRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	repo, err := d.repository(project)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	if present, err := d.storagePresent(); err != nil || !present {
		return sourcedriver.Repository{Project: project}, err
	}
	return d.inspectAt(ctx, project, repo)
}

// inspectAt reads the repository at repo, the project's address.
func (d *Driver) inspectAt(ctx context.Context, project, repo string) (sourcedriver.Repository, error) {
	if _, err := os.Lstat(repo); errors.Is(err, os.ErrNotExist) {
		return sourcedriver.Repository{Project: project}, nil
	} else if err != nil {
		return sourcedriver.Repository{}, err
	}
	if _, err := d.checkRepository(ctx, repo); err != nil {
		return sourcedriver.Repository{}, err
	}
	incarnation, err := readIncarnation(repo)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	return sourcedriver.Repository{Project: project, Exists: true, Incarnation: incarnation}, nil
}

// checkRepository proves repo a safe bare repository whose HEAD names a P
// branch, and returns that branch.
func (d *Driver) checkRepository(ctx context.Context, repo string) (string, error) {
	if err := verifyBareDir(repo); err != nil {
		return "", err
	}
	bare, err := d.git(ctx, repo, 64, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", err
	}
	if bare.Exit != 0 || strings.TrimSpace(bare.Stdout) != "true" {
		return "", errors.New("the repository is not a bare Git repository")
	}
	head, err := d.git(ctx, repo, 4096, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch, ok := strings.CutPrefix(strings.TrimSuffix(head.Stdout, "\n"), "refs/heads/")
	if head.Exit != 0 || !ok || !gitnames.Branch(branch) {
		return "", errors.New("the repository's HEAD is invalid")
	}
	return branch, nil
}

// git runs git on the bare repository repo, named explicitly so that Git
// never discovers another repository above it.
func (d *Driver) git(ctx context.Context, repo string, max int, args ...string) (gitResult, error) {
	return d.run(ctx, repo, nil, max, append([]string{"--git-dir=" + repo}, args...)...)
}

// verifyBareDir proves repo and its control paths owned by P's account and
// writable by no one else, its control files regular and unlinked
// elsewhere.
func verifyBareDir(repo string) error {
	if err := privateDirectory(repo); err != nil {
		return err
	}
	for _, name := range []string{"HEAD", "config", "objects", "refs", incarnationFile} {
		info, err := os.Lstat(filepath.Join(repo, name))
		if err != nil {
			return fmt.Errorf("the repository is incomplete: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
			return errors.New("the repository has an unsafe control path")
		}
		switch name {
		case "objects", "refs":
			if !info.IsDir() {
				return errors.New("a repository control directory is unsafe")
			}
		default:
			if !info.Mode().IsRegular() || stat.Nlink != 1 {
				return errors.New("a repository control file is unsafe")
			}
		}
	}
	return nil
}

// readIncarnation reads the token verifyBareDir proved safe.
func readIncarnation(repo string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repo, incarnationFile))
	if err != nil {
		return "", err
	}
	token := string(data)
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		return "", errors.New("the repository's incarnation is malformed")
	}
	return token, nil
}

// CreateRepository converges: it keeps an existing repository whose HEAD
// names main, as today's git.project.ensure does, and otherwise makes the
// repository in a temporary directory and renames it into place, so the
// address holds a complete repository or nothing. It holds the
// repositories lock throughout, so no deletion interleaves.
func (d *Driver) CreateRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	repo, err := d.repository(project)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	if err := d.prepareStorage(); err != nil {
		return sourcedriver.Repository{}, err
	}
	unlock, err := d.lockRepositories(ctx)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	defer unlock()
	if _, err := os.Lstat(repo); errors.Is(err, os.ErrNotExist) {
		if err := d.initRepository(ctx, repo); err != nil {
			return sourcedriver.Repository{}, err
		}
	} else if err != nil {
		return sourcedriver.Repository{}, err
	}
	return d.keepExisting(ctx, project, repo)
}

// keepExisting returns the repository at repo, refusing one whose HEAD does
// not name main.
func (d *Driver) keepExisting(ctx context.Context, project, repo string) (sourcedriver.Repository, error) {
	head, err := d.checkRepository(ctx, repo)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	if head != "main" {
		return sourcedriver.Repository{}, fmt.Errorf("the existing repository's HEAD names %q, not main: %w", head, sourcedriver.ErrConflict)
	}
	incarnation, err := readIncarnation(repo)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	return sourcedriver.Repository{Project: project, Exists: true, Incarnation: incarnation}, nil
}

// initRepository makes a bare repository whose HEAD names main, with a new
// incarnation, in a temporary directory beside repo and renames it to repo.
// A repository another call put at repo first wins; this one is dropped.
func (d *Driver) initRepository(ctx context.Context, repo string) error {
	temp, err := os.MkdirTemp(filepath.Dir(repo), ".p-git-init-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	initialized, err := d.run(ctx, filepath.Dir(repo), nil, 4096, "init", "--quiet", "--bare", "--initial-branch=main", temp)
	if err != nil {
		return err
	}
	if initialized.Exit != 0 {
		return errors.New("git init of the bare repository failed")
	}
	if err := writeIncarnation(temp); err != nil {
		return err
	}
	head, err := d.checkRepository(ctx, temp)
	if err != nil {
		return err
	}
	if head != "main" {
		return errors.New("the new repository's HEAD does not name main")
	}
	if err := os.Rename(temp, repo); err != nil {
		if _, statErr := os.Lstat(repo); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// writeIncarnation writes a new random incarnation into repo.
func writeIncarnation(repo string) error {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(repo, incarnationFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(hex.EncodeToString(token[:]))
	return errors.Join(err, file.Sync(), file.Close())
}

// DeleteRepository removes the repository at r's incarnation. It holds the
// repositories lock while it checks the incarnation and again while it
// moves the repository away, so that no creation or deletion interleaves;
// it releases the lock across the gate, since core's gate may call the
// driver. It checks the incarnation again after the gate and moves nothing
// else: a repository made again while the gate ran, or anything else that
// took the address, stays there. The move empties the address at once, so a
// repeated call finds nothing; the removal follows outside the lock.
func (d *Driver) DeleteRepository(ctx context.Context, r sourcedriver.Repository, g sourcedriver.Gate) error {
	repo, err := d.repository(r.Project)
	if err != nil {
		return err
	}
	if err := d.prepareStorage(); err != nil {
		return err
	}
	if present, err := d.atIncarnation(ctx, r, repo); !present || err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	trash, err := d.moveAway(ctx, r, repo)
	if trash == "" || err != nil {
		return err
	}
	return os.RemoveAll(trash)
}

// atIncarnation reports, under the repositories lock, whether r's
// repository is at its address; another incarnation is ErrConflict.
func (d *Driver) atIncarnation(ctx context.Context, r sourcedriver.Repository, repo string) (bool, error) {
	unlock, err := d.lockRepositories(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	return d.isIncarnation(ctx, r, repo)
}

// isIncarnation reports whether r's repository is at its address; another
// incarnation is ErrConflict, and anything else there an error. The caller
// holds the repositories lock.
func (d *Driver) isIncarnation(ctx context.Context, r sourcedriver.Repository, repo string) (bool, error) {
	current, err := d.inspectAt(ctx, r.Project, repo)
	switch {
	case err != nil:
		return false, err
	case !current.Exists:
		return false, nil
	case current.Incarnation != r.Incarnation:
		return false, fmt.Errorf("another incarnation of the repository: %w", sourcedriver.ErrConflict)
	}
	return true, nil
}

// moveAway checks r's incarnation again and moves it into a new directory
// inside the storage, which it returns; nothing when the repository is
// already gone. It holds the repositories lock throughout.
func (d *Driver) moveAway(ctx context.Context, r sourcedriver.Repository, repo string) (string, error) {
	unlock, err := d.lockRepositories(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if present, err := d.isIncarnation(ctx, r, repo); !present || err != nil {
		return "", err
	}
	trash, err := os.MkdirTemp(d.repositories(), ".p-git-delete-")
	if err != nil {
		return "", err
	}
	if err := os.Rename(repo, filepath.Join(trash, "repository")); err != nil {
		return "", errors.Join(err, os.Remove(trash))
	}
	if moved := d.seams.takeRepositoryMoved(); moved != nil {
		if err := moved(); err != nil {
			return "", err
		}
	}
	return trash, nil
}

// repositoriesLock is the lock file that serializes the creation and
// deletion of repositories across drivers and processes over the storage.
const repositoriesLock = ".p-lock"

// lockRepositories takes the repositories lock, waiting until ctx ends, and
// returns its release.
func (d *Driver) lockRepositories(ctx context.Context) (func(), error) {
	file, err := os.OpenFile(filepath.Join(d.repositories(), repositoriesLock), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return nil, errors.Join(err, file.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), file.Close())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
