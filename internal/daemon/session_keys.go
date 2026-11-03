package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

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

// errSessionKeyMismatch classifies a session key file that is missing or
// differs from the Git principal registered for the session.
var errSessionKeyMismatch = errors.New("session key does not match its Git principal")

// sessionKeyMismatch is an errSessionKeyMismatch with its own text.
type sessionKeyMismatch string

func (e sessionKeyMismatch) Error() string      { return string(e) }
func (sessionKeyMismatch) Is(target error) bool { return target == errSessionKeyMismatch }

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
			return nil, nil, sessionKeyMismatch("registered session key missing; discard the session and create a new one")
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
		return nil, nil, sessionKeyMismatch("registered session key changed; discard the session and create a new one")
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
