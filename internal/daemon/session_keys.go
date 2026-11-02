package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"golang.org/x/crypto/ssh"
)

func sessionKeysDir(stateDir string) string { return filepath.Join(stateDir, "session_keys") }

func sessionKeyPath(stateDir, uuid string) string {
	return filepath.Join(sessionKeysDir(stateDir), uuid)
}

func ownedPrivateDirectory(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode().Perm() == 0700 && owner.Uid == uint32(os.Geteuid())
}

func (l *lifecycle) sessionKey(ctx context.Context, id string) ([]byte, ssh.PublicKey, error) {
	return sessionKeyAt(ctx, id, l.store.StateDir(), l.store.SessionGitPrincipal)
}

func sessionKeyAt(ctx context.Context, id, stateDir string, principal func(context.Context, string) (string, bool, error)) ([]byte, ssh.PublicKey, error) {
	path := sessionKeysDir(stateDir)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !ownedPrivateDirectory(info) {
		return nil, nil, errors.New("session key directory is unsafe")
	}
	file := filepath.Join(path, id)
	registered, exists, err := principal(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if exists {
		if _, err = os.Lstat(file); errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.New("registered session key missing; repair required")
		}
		if err != nil {
			return nil, nil, err
		}
	}
	key, pub, err := loadOrCreateKey(file)
	if err != nil {
		return nil, nil, err
	}
	if exists && registered != gitservice.Fingerprint(pub) {
		return nil, nil, errors.New("registered session key changed; repair required")
	}
	return key, pub, nil
}

func removeSessionKeyAt(stateDir, uuid string) error {
	if len(uuid) != 36 {
		return errors.New("invalid session key identity")
	}
	dir := sessionKeysDir(stateDir)
	parent, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ownedPrivateDirectory(parent) {
		return errors.New("session key directory identity changed")
	}
	file := filepath.Join(dir, uuid)
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return errors.New("session key file identity changed")
	}
	if err = os.Remove(file); err != nil {
		return err
	}
	opened, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := opened.Sync()
	closeErr := opened.Close()
	return errors.Join(syncErr, closeErr)
}

// A preview must never create or rotate the P-owned session credential.
func inspectRegisteredSessionKey(stateDir, uuid string) (string, error) {
	if len(uuid) != 36 {
		return "", control.ErrInvalid
	}
	dir := sessionKeysDir(stateDir)
	parent, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !ownedPrivateDirectory(parent) {
		return "", errors.New("session key directory unavailable")
	}
	name := filepath.Join(dir, uuid)
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 4096 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("session key file unavailable")
	}
	data := make([]byte, info.Size())
	if _, err = io.ReadFull(f, data); err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("session key identity unavailable")
	}
	return gitservice.Fingerprint(signer.PublicKey()), nil
}

func principalKeyDir(stateDir string) (string, error) {
	dir := sessionKeysDir(stateDir)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !ownedPrivateDirectory(info) {
		return "", errors.New("session key directory is unsafe")
	}
	return dir, nil
}

func principalKeyStatus(stateDir, uuid, old string) (string, error) {
	if _, err := principalKeyDir(stateDir); err != nil {
		return "unsafe", err
	}
	fingerprint, err := inspectRegisteredSessionKey(stateDir, uuid)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "unsafe", err
	}
	if old == "" || fingerprint != old {
		return "mismatch", nil
	}
	return "matching", nil
}

func principalRepairStage(stateDir, opID string) string {
	return sessionKeyPath(stateDir, ".repair-"+opID)
}

func principalRepairKey(path, expected string, create bool) ([]byte, error) {
	if !create {
		if _, err := os.Lstat(path); err != nil {
			return nil, err
		}
	}
	key, pub, err := loadOrCreateKey(path)
	if err != nil || expected != "" && gitservice.Fingerprint(pub) != expected {
		return nil, errors.Join(err, errors.New("principal repair staged key identity changed"))
	}
	return key, nil
}
