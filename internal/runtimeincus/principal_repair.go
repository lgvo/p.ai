package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/gitservice"
	"golang.org/x/crypto/ssh"
)

const sessionIdentityPath = "/etc/p/git/identity"

type SessionIdentityObservation struct {
	Exists      bool
	Fingerprint string
	SHA256      string
}

type principalRepairFiles interface {
	lstat(context.Context, string) (guestFile, error)
	lstatOptional(context.Context, string) (guestFile, bool, error)
	getBounded(context.Context, string, int64) (guestFile, bool, error)
	put(context.Context, string, guestFile) error
}

func checkedSessionIdentityPath(ctx context.Context, api principalRepairFiles) error {
	for _, path := range []string{"/", "/etc", "/etc/p", "/etc/p/git"} {
		f, err := api.lstat(ctx, path)
		if err != nil || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode != 0755 {
			return errors.Join(err, fmt.Errorf("session key parent unavailable at %s", path))
		}
	}
	f, exists, err := api.lstatOptional(ctx, sessionIdentityPath)
	if err != nil {
		return err
	}
	if exists && (f.typ != "file" || f.uid != 1000 || f.gid != 1000 || f.mode != 0400 || len(f.data) > 4096) {
		return errors.New("session key target is not a private regular file")
	}
	return nil
}

func verifiedSessionIdentity(ctx context.Context, api principalRepairFiles, want []byte) error {
	if err := checkedSessionIdentityPath(ctx, api); err != nil {
		return err
	}
	f, exists, err := api.getBounded(ctx, sessionIdentityPath, 4096)
	if err != nil || !exists || f.typ != "file" || f.uid != 1000 || f.gid != 1000 || f.mode != 0400 || !bytes.Equal(f.data, want) {
		return errors.Join(err, errors.New("session key installation is not exact"))
	}
	return nil
}

func (b *Backend) stoppedSessionIdentityAPI(ctx context.Context, s Session, incusUUID, generation string) (*unixFileAPI, error) {
	if s.WorkspaceOwner != "" || !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) {
		return nil, errors.New("session key repair identity unavailable")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return nil, err
	}
	observed, err := b.Inspect(ctx, s)
	if err != nil || !stoppedSessionIdentityMatches(observed, b.name(s), s.ImageFingerprint, incusUUID, generation) {
		return nil, errors.Join(err, errors.New("session key repair requires exact stopped runtime"))
	}
	return &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}, nil
}

func stoppedSessionIdentityMatches(observed Observation, name, image, incusUUID, generation string) bool {
	return observed.Exists && observed.Name == name && observed.Status == "Stopped" &&
		observed.IncusUUID == incusUUID && observed.Generation == generation &&
		observed.Fingerprint == image && observed.EndpointMounted
}

// CheckStoppedSessionIdentity is read-only; it proves only that the fixed
// identity target can be replaced in the already stopped, exact runtime.
func (b *Backend) CheckStoppedSessionIdentity(ctx context.Context, s Session, incusUUID, generation string) error {
	_, err := b.InspectStoppedSessionIdentity(ctx, s, incusUUID, generation)
	return err
}

// InspectStoppedSessionIdentity reads only the fixed private key in a stopped
// runtime. A malformed but safely located key remains replaceable and is
// reported without treating its bytes as an authority claim.
func (b *Backend) InspectStoppedSessionIdentity(ctx context.Context, s Session, incusUUID, generation string) (SessionIdentityObservation, error) {
	api, err := b.stoppedSessionIdentityAPI(ctx, s, incusUUID, generation)
	if err != nil {
		return SessionIdentityObservation{}, err
	}
	if err := checkedSessionIdentityPath(ctx, api); err != nil {
		return SessionIdentityObservation{}, err
	}
	f, exists, err := api.getBounded(ctx, sessionIdentityPath, 4096)
	if err != nil || exists && (f.typ != "file" || f.uid != 1000 || f.gid != 1000 || f.mode != 0400) {
		return SessionIdentityObservation{}, errors.Join(err, errors.New("session key changed during read"))
	}
	if _, err = b.stoppedSessionIdentityAPI(ctx, s, incusUUID, generation); err != nil {
		return SessionIdentityObservation{}, err
	}
	if !exists {
		return SessionIdentityObservation{}, nil
	}
	hash := sha256.Sum256(f.data)
	result := SessionIdentityObservation{Exists: true, SHA256: hex.EncodeToString(hash[:])}
	signer, err := ssh.ParsePrivateKey(f.data)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return result, nil
	}
	result.Fingerprint = gitservice.Fingerprint(signer.PublicKey())
	return result, nil
}

func (b *Backend) VerifyStoppedSessionIdentity(ctx context.Context, s Session, incusUUID, generation string, key []byte) error {
	api, err := b.stoppedSessionIdentityAPI(ctx, s, incusUUID, generation)
	if err != nil {
		return err
	}
	if err := verifiedSessionIdentity(ctx, api, key); err != nil {
		return err
	}
	_, err = b.stoppedSessionIdentityAPI(ctx, s, incusUUID, generation)
	return err
}

// InstallStoppedSessionIdentity records a durable attempt immediately before
// its sole Incus file POST. An uncertain POST must be reconciled by exact
// positive readback and must not be repeated against a name.
func (b *Backend) InstallStoppedSessionIdentity(ctx context.Context, s Session, incusUUID, generation string, key []byte, beforePost func() error) error {
	if beforePost == nil || len(key) == 0 || len(key) > 4096 {
		return errors.New("invalid session key repair request")
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return errors.New("session key repair requires Ed25519 identity")
	}
	api, err := b.stoppedSessionIdentityAPI(ctx, s, incusUUID, generation)
	if err != nil {
		return err
	}
	if err := installStoppedSessionIdentityFiles(ctx, api, key, beforePost); err != nil {
		return err
	}
	return b.VerifyStoppedSessionIdentity(ctx, s, incusUUID, generation, key)
}

func installStoppedSessionIdentityFiles(ctx context.Context, api principalRepairFiles, key []byte, beforePost func() error) error {
	if err := checkedSessionIdentityPath(ctx, api); err != nil {
		return err
	}
	if err := beforePost(); err != nil {
		return err
	}
	if err := api.put(ctx, sessionIdentityPath, guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0400, data: key}); err != nil {
		return err
	}
	return verifiedSessionIdentity(ctx, api, key)
}
