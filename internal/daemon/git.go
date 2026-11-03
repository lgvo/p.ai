package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/lgvo/p.ai/internal/api"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sshserver"
	"golang.org/x/crypto/ssh"
)

type gitCapability struct {
	// driver is the source driver: P's repositories, heads, commits and
	// the commit export, the Git service and the origin.
	driver sourcedriver.Driver
	// endServices ends the Git services the driver runs and refuses later
	// ones.
	endServices func() error
	// server is P's SSH server, with Git its one service.
	server   *sshserver.Server
	listener net.Listener
	info     *api.GitInfo
}

// newGitCapability starts the source driver, pins its backend, and makes P's
// SSH server on the configured loopback address with the driver's Git
// service.
func newGitCapability(ctx context.Context, cfg control.HostConfig, store *control.Store) (_ *gitCapability, err error) {
	selected := cfg.Git
	if selected == nil {
		return nil, errors.New("Git configuration is absent")
	}
	driver, endServices, err := newSourceDriver(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			endServices()
		}
	}()
	if err := store.PinSourceBackend(ctx, driver.Describe().Backend); err != nil {
		return nil, fmt.Errorf("source driver: %w", err)
	}
	serverKey, serverPub, err := loadPinnedServerKey(ctx, store, filepath.Join(cfg.StateDir, "git_server_key"))
	if err != nil {
		return nil, fmt.Errorf("Git server key: %w", err)
	}
	_, clientPub, err := loadOrCreateKey(filepath.Join(cfg.StateDir, "git_client_key"))
	if err != nil {
		return nil, fmt.Errorf("Git client key: %w", err)
	}
	if err := store.EnsureHostGitPrincipal(ctx, sshserver.Fingerprint(clientPub)); err != nil {
		return nil, fmt.Errorf("Git host principal: %w", err)
	}
	listener, err := net.Listen("tcp", selected.Listen)
	if err != nil {
		return nil, err
	}
	server, err := sshserver.Prepare(listener, serverKey, store, sshserver.Git(ctx, store, driver))
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("Git SSH setup: %w", err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		return nil, err
	}
	endpoint := net.JoinHostPort(host, port)
	info := &api.GitInfo{
		Endpoint:        endpoint,
		URLTemplate:     "ssh://git@" + endpoint + "/{project}",
		HostPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverPub))),
		KnownHosts:      "[" + host + "]:" + port + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverPub))),
		ClientPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(clientPub))),
		ClientKeyPath:   filepath.Join(cfg.StateDir, "git_client_key"),
		DriverBackend:   driver.Describe().Backend,
		DriverVersion:   driver.Describe().Version,
	}
	return &gitCapability{driver: driver, endServices: endServices, server: server, listener: listener, info: info}, nil
}

// close stops P's SSH endpoint and ends the Git services the driver runs.
func (g *gitCapability) close() {
	g.listener.Close()
	g.endServices()
}

// inspectBranch reads head branch of project exactly: its tip, or "" and
// false when nothing holds the name. A symbolic ref holding the name is also
// control.ErrConflict, which callers compare; a failed read answers failed,
// reason git.
func (l *lifecycle) inspectBranch(ctx context.Context, project, branch string) (string, bool, error) {
	tip, exists, err := l.git.driver.InspectBranch(ctx, project, branch)
	switch {
	case errors.Is(err, sourcedriver.ErrConflict):
		return "", false, errors.Join(err, control.ErrConflict)
	case err != nil:
		return "", false, control.Failed(api.ReasonGit, err)
	}
	return string(tip), exists, nil
}

// createBranch creates head branch of project at commit at, only if nothing
// holds the name, calling gate right before the effect.
func (l *lifecycle) createBranch(ctx context.Context, project, branch, at string, gate sourcedriver.Gate) error {
	return l.headEffect(ctx, creation(project, branch, at), gate, func(g sourcedriver.Gate) error {
		return l.git.driver.CreateBranch(ctx, project, branch, sourcedriver.OID(at), g)
	})
}

// deleteBranch deletes head branch of project only while it names tip,
// calling gate right before the effect.
func (l *lifecycle) deleteBranch(ctx context.Context, project, branch, tip string, gate sourcedriver.Gate) error {
	return l.headEffect(ctx, deletion(project, branch, tip), gate, func(g sourcedriver.Gate) error {
		return l.git.driver.DeleteBranch(ctx, project, branch, sourcedriver.OID(tip), g)
	})
}

// headEffect sends head effect e through the source driver with gate. A
// failure of the gate is returned as it is. An effect whose outcome is
// unknown is settled, never sent again; any other failure of the driver
// answers failed, reason git.
func (l *lifecycle) headEffect(ctx context.Context, e sourcedriver.Effect, gate sourcedriver.Gate, send func(sourcedriver.Gate) error) error {
	var gateErr error
	err := send(func() error {
		gateErr = gate()
		return gateErr
	})
	switch {
	case err == nil:
		return nil
	case gateErr != nil:
		return gateErr
	case errors.Is(err, sourcedriver.ErrUnknownOutcome):
		return l.settleHead(ctx, e, err)
	}
	return control.Failed(api.ReasonGit, err)
}

// creation is the creation of head branch of project at commit at, as Settle
// takes it.
func creation(project, branch, at string) sourcedriver.Effect {
	return sourcedriver.Effect{Kind: sourcedriver.EffectCreateBranch, Project: project, Branch: branch, New: sourcedriver.OID(at)}
}

// deletion is the deletion of head branch of project at tip, as Settle takes
// it.
func deletion(project, branch, tip string) sourcedriver.Effect {
	return sourcedriver.Effect{Kind: sourcedriver.EffectDeleteBranch, Project: project, Branch: branch, Old: sourcedriver.OID(tip)}
}

// errHeadNotApplied: the source driver settled a head effect as never
// applied.
var errHeadNotApplied = errors.New("the head effect did not happen")

// settleHead settles head effect e, whose outcome is unknown because of
// cause. It succeeds only when the source driver settles e applied. An effect
// that did not happen fails with reason git; any other answer leaves the
// outcome unknown. Core keeps its guard and never sends e again.
func (l *lifecycle) settleHead(ctx context.Context, e sourcedriver.Effect, cause error) error {
	settled, err := l.git.driver.Settle(ctx, e)
	switch {
	case err == nil && settled == sourcedriver.Applied:
		return nil
	case err == nil && settled == sourcedriver.NotApplied:
		return control.Failed(api.ReasonGit, errors.Join(cause, errHeadNotApplied))
	}
	return control.OutcomeUnknown(errors.Join(cause, err))
}

type serverIdentityStore interface {
	GitServerIdentity(context.Context) (string, bool, error)
	EnsureGitServerIdentity(context.Context, string) error
}

func loadPinnedServerKey(ctx context.Context, store serverIdentityStore, path string) ([]byte, ssh.PublicKey, error) {
	_, pinned, err := store.GitServerIdentity(ctx)
	if err != nil {
		return nil, nil, err
	}
	if pinned {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.New("pinned Git server key is missing")
		} else if err != nil {
			return nil, nil, err
		}
	}
	key, public, err := loadOrCreateKey(path)
	if err != nil {
		return nil, nil, err
	}
	if err := store.EnsureGitServerIdentity(ctx, sshserver.Fingerprint(public)); err != nil {
		return nil, nil, fmt.Errorf("Git server identity: %w", err)
	}
	return key, public, nil
}

func privateTrustedFile(path string) error {
	if err := control.CheckTrustedAncestors(path); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		return errors.New("file must be a private, singly linked regular file owned by the daemon user")
	}
	return nil
}

func loadOrCreateKey(path string) ([]byte, ssh.PublicKey, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			return nil, nil, err
		}
		data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if err != nil {
			return nil, nil, err
		}
		f := os.NewFile(uintptr(fd), path)
		if _, err := f.Write(data); err != nil {
			f.Close()
			return nil, nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, nil, err
		}
		if err := f.Close(); err != nil {
			return nil, nil, err
		}
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return nil, nil, err
		}
		if err := dir.Sync(); err != nil {
			dir.Close()
			return nil, nil, err
		}
		if err := dir.Close(); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return nil, nil, errors.New("key must be a private, singly linked 0600 regular file")
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, nil, errors.New("key is not a valid Ed25519 private key")
	}
	return data, signer.PublicKey(), nil
}
