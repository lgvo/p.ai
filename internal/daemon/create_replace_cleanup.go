package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"golang.org/x/crypto/ssh"
)

func replacementLocalIdentity(path string) (control.ReplacementLocalIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return control.ReplacementLocalIdentity{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return control.ReplacementLocalIdentity{}, control.ErrConflict
	}
	return control.ReplacementLocalIdentity{Device: uint64(st.Dev), Inode: st.Ino, UID: st.Uid, Mode: st.Mode, Links: uint64(st.Nlink)}, nil
}

func matchReplacementLocal(path string, approved control.ReplacementLocalIdentity, allowAbsent bool) error {
	got, err := replacementLocalIdentity(path)
	if allowAbsent && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// A sibling directory may change the parent's link count. Device, inode,
	// owner and mode still bind the exact parent; regular keys require one link.
	if approved.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		got.Links = approved.Links
	}
	if err != nil || got != approved {
		return control.ErrConflict
	}
	return nil
}

// readReplacementKey never creates/rotates a missing key and never follows it.
func readReplacementKey(path string, identity control.ReplacementLocalIdentity) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() > 4096 || info.Size() < 1 ||
		uint64(st.Dev) != identity.Device || st.Ino != identity.Inode || st.Uid != identity.UID || st.Mode != 0100600 || st.Nlink != 1 {
		return "", control.ErrConflict
	}
	data := make([]byte, info.Size())
	if _, err = f.ReadAt(data, 0); err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", control.ErrConflict
	}
	return gitservice.Fingerprint(signer.PublicKey()), nil
}

func reviewReplacementLocal(stateDir string, m *endpointManager, uuid, op, image, principal string) (*control.CreateReplacementCleanup, error) {
	c := &control.CreateReplacementCleanup{OldUUID: uuid, OldOperationID: op, OldImageFingerprint: image, KeyFingerprint: principal}
	var err error
	c.KeyDirectory, err = replacementLocalIdentity(filepath.Join(stateDir, "session_keys"))
	if err != nil {
		return nil, err
	}
	c.Key, err = replacementLocalIdentity(filepath.Join(stateDir, "session_keys", uuid))
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c.EndpointPrefix, err = replacementLocalIdentity(m.prefix)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.prefix, uuid)
	c.EndpointDirectory, err = replacementLocalIdentity(dir)
	if err != nil {
		return nil, err
	}
	c.GitSocket, err = replacementLocalIdentity(filepath.Join(dir, "git.sock"))
	if err != nil {
		return nil, err
	}
	c.SessionSocket, err = replacementLocalIdentity(filepath.Join(dir, "session.sock"))
	if err != nil {
		return nil, err
	}
	if !c.Valid() {
		return nil, control.ErrConflict
	}
	if err = checkReplacementEndpointsLocked(m, c, false); err != nil {
		return nil, err
	}
	fp, err := readReplacementKey(filepath.Join(stateDir, "session_keys", uuid), c.Key)
	if err != nil || fp != principal {
		return nil, control.ErrConflict
	}
	return c, nil
}

func checkReplacementEndpointsLocked(m *endpointManager, c *control.CreateReplacementCleanup, allowAbsent bool) error {
	if err := matchReplacementLocal(m.prefix, c.EndpointPrefix, false); err != nil {
		return err
	}
	dir := filepath.Join(m.prefix, c.OldUUID)
	if err := matchReplacementLocal(dir, c.EndpointDirectory, allowAbsent); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if allowAbsent && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "git.sock" && e.Name() != "session.sock" {
			return control.ErrConflict
		}
	}
	for name, i := range map[string]control.ReplacementLocalIdentity{"git.sock": c.GitSocket, "session.sock": c.SessionSocket} {
		if err := matchReplacementLocal(filepath.Join(dir, name), i, allowAbsent); err != nil {
			return err
		}
	}
	return nil
}

func checkReplacementKey(stateDir string, c *control.CreateReplacementCleanup, allowAbsent bool) error {
	dir := filepath.Join(stateDir, "session_keys")
	if err := matchReplacementLocal(dir, c.KeyDirectory, false); err != nil {
		return err
	}
	path := filepath.Join(dir, c.OldUUID)
	if err := matchReplacementLocal(path, c.Key, allowAbsent); err != nil {
		return err
	}
	fp, err := readReplacementKey(path, c.Key)
	if allowAbsent && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || fp != c.KeyFingerprint {
		return control.ErrConflict
	}
	return nil
}

// cleanupReplacementLocal repeats the native proof at each recovery boundary.
// Missing approved entries can only be accepted after durable confirmation.
// Native objects and Git refs are never changed here.
func cleanupReplacementLocal(ctx context.Context, stateDir string, m *endpointManager, c *control.CreateReplacementCleanup, prove func(context.Context) error) error {
	if c == nil || !c.Valid() || c.Completed {
		return control.ErrConflict
	}
	if err := prove(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	err := checkReplacementEndpointsLocked(m, c, true)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err = checkReplacementKey(stateDir, c, true); err != nil {
		return err
	}
	if err = removeSessionKeyAt(stateDir, c.OldUUID); err != nil {
		return err
	}
	if err = prove(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = checkReplacementEndpointsLocked(m, c, true); err != nil {
		return err
	}
	if pair := m.opened[c.OldUUID]; pair != nil {
		// Closing must not unlink a substituted path; exact manual deletion follows.
		for _, listener := range []net.Listener{pair.git, pair.session} {
			if unix, ok := listener.(*net.UnixListener); ok {
				unix.SetUnlinkOnClose(false)
			}
			if err = listener.Close(); err != nil {
				return err
			}
		}
		delete(m.opened, c.OldUUID)
	}
	dir := filepath.Join(m.prefix, c.OldUUID)
	for _, name := range []string{"git.sock", "session.sock"} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent, err := os.Open(m.prefix)
	if err != nil {
		return err
	}
	err = errors.Join(parent.Sync(), parent.Close())
	if err != nil {
		return err
	}
	return prove(ctx)
}

func (l *lifecycle) completeReplacementCleanup(ctx context.Context, op *control.Operation) error {
	ev, err := control.Evidence(*op)
	if err != nil {
		return err
	}
	c := ev.ReplacementCleanup
	if c == nil || !c.Valid() || c.Completed || c.OldUUID != ev.SupersedesUUID || c.OldOperationID != ev.SupersedesOperationID || c.OldUUID == op.SessionUUID || op.Committed {
		return control.ErrConflict
	}
	slot := l.sessionLockSlot(c.OldUUID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-slot:
	}
	defer func() { slot <- struct{}{} }()
	old, err := l.store.GetOperation(ctx, c.OldOperationID)
	if err != nil || old.Status != "superseded" || old.Kind != "session.create" || old.SessionUUID != c.OldUUID || old.Project != op.Project {
		return control.ErrConflict
	}
	oldEv, err := control.Evidence(old)
	if err != nil || oldEv.ImageFingerprint != c.OldImageFingerprint {
		return control.ErrConflict
	}
	if _, found, err := l.store.SessionGitPrincipal(ctx, c.OldUUID); err != nil || found {
		return control.ErrConflict
	}
	prove := func(call context.Context) error {
		return l.runtime.ConfirmFailedCreateEffectsAbsent(call, runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: c.OldUUID, ProjectPath: op.Project, ContractVersion: "1", ImageFingerprint: c.OldImageFingerprint}, c.OldOperationID)
	}
	if err = cleanupReplacementLocal(ctx, l.store.StateDir(), l.endpoints, c, prove); err != nil {
		return err
	}
	c.Completed = true
	priorEvidence := op.Evidence
	op.Evidence, err = json.Marshal(ev)
	if err != nil {
		return err
	}
	if err = l.advance(op, "source-ready", false); err != nil {
		op.Evidence = priorEvidence
		return err
	}
	return nil
}
