package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

const replacementOldUUID = "550e8400-e29b-41d4-a716-446655440000"
const replacementOtherUUID = "11111111-1111-4111-8111-111111111111"

func replacementLocalFixture(t *testing.T) (string, *endpointManager, *control.CreateReplacementCleanup) {
	t.Helper()
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "session_keys"), 0700); err != nil {
		t.Fatal(err)
	}
	_, pub, err := loadOrCreateKey(filepath.Join(state, "session_keys", replacementOldUUID))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = loadOrCreateKey(filepath.Join(state, "session_keys", replacementOtherUUID))
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.MkdirTemp("/tmp", "p-rc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	m, err := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	for _, uuid := range []string{replacementOldUUID, replacementOtherUUID} {
		if _, err = m.Ensure(context.Background(), uuid); err != nil {
			t.Fatal(err)
		}
	}
	c, err := reviewReplacementLocal(state, m, replacementOldUUID, replacementOtherUUID, strings.Repeat("a", 64), gitservice.Fingerprint(pub))
	if err != nil {
		t.Fatal(err)
	}
	return state, m, c
}

func TestReplacementLocalCleanupCrashCheckpointAndRestart(t *testing.T) {
	state, m, c := replacementLocalFixture(t)
	ctx := context.Background()
	siblingKey, err := os.ReadFile(filepath.Join(state, "session_keys", replacementOtherUUID))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	interrupted := errors.New("fixture crash after approved key deletion")
	err = cleanupReplacementLocal(ctx, state, m, c, func(context.Context) error {
		n++
		if n == 2 {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || n != 2 {
		t.Fatalf("checkpoint not reached: %v proofs=%d", err, n)
	}
	if _, err = os.Lstat(filepath.Join(state, "session_keys", replacementOldUUID)); !os.IsNotExist(err) {
		t.Fatal("approved key not removed")
	}
	if _, err = os.Lstat(filepath.Join(m.prefix, replacementOldUUID)); err != nil {
		t.Fatal("uncompleted cleanup lost endpoint identity")
	}
	// Restart listeners without recreating either approved socket (old session
	// authority is retired). Durable evidence tolerates approved entries absent.
	for _, pair := range m.opened {
		for _, listener := range []net.Listener{pair.git, pair.session} {
			listener.(*net.UnixListener).SetUnlinkOnClose(false)
		}
	}
	m.Close()
	restarted, err := newEndpointManager(m.prefix, "127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	proofs := 0
	prove := func(context.Context) error { proofs++; return nil }
	if err = cleanupReplacementLocal(ctx, state, restarted, c, prove); err != nil {
		t.Fatal(err)
	}
	if proofs != 3 {
		t.Fatalf("native recovery proofs=%d", proofs)
	}
	if err = cleanupReplacementLocal(ctx, state, restarted, c, prove); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(m.prefix, replacementOldUUID)); !os.IsNotExist(err) {
		t.Fatal("old endpoint remains")
	}
	got, err := os.ReadFile(filepath.Join(state, "session_keys", replacementOtherUUID))
	if err != nil || string(got) != string(siblingKey) {
		t.Fatal("sibling key changed")
	}
	if _, err = os.Lstat(filepath.Join(m.prefix, replacementOtherUUID, "git.sock")); err != nil {
		t.Fatal("sibling endpoint changed")
	}
}

func TestReplacementLocalCleanupRefusesChangedReviewedIdentity(t *testing.T) {
	for _, name := range []string{"key-fingerprint", "key-mode", "key-symlink", "key-hardlink", "key-replaced", "parent-replaced", "endpoint-extra", "endpoint-symlink", "socket-replaced", "socket-mode", "native-appeared", "native-after-key"} {
		t.Run(name, func(t *testing.T) {
			state, m, c := replacementLocalFixture(t)
			key := filepath.Join(state, "session_keys", replacementOldUUID)
			endpoint := filepath.Join(m.prefix, replacementOldUUID)
			switch name {
			case "key-fingerprint":
				// Keep inode metadata while replacing bytes with another real P Git key.
				data, err := os.ReadFile(filepath.Join(state, "session_keys", replacementOtherUUID))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(key, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "key-mode":
				os.Chmod(key, 0644)
			case "key-symlink":
				os.Remove(key)
				os.Symlink(filepath.Join(state, "session_keys", replacementOtherUUID), key)
			case "key-hardlink":
				os.Link(key, key+"-alias")
			case "key-replaced":
				os.Rename(key, key+"-old")
				loadOrCreateKey(key)
			case "parent-replaced":
				os.Rename(filepath.Dir(key), filepath.Dir(key)+"-old")
				os.Mkdir(filepath.Dir(key), 0700)
			case "endpoint-extra":
				os.WriteFile(filepath.Join(endpoint, "unexpected"), []byte("preserve"), 0600)
			case "endpoint-symlink":
				os.Rename(endpoint, endpoint+"-old")
				os.Symlink(endpoint+"-old", endpoint)
			case "socket-replaced":
				os.Remove(filepath.Join(endpoint, "git.sock"))
				os.WriteFile(filepath.Join(endpoint, "git.sock"), []byte("preserve"), 0666)
			case "socket-mode":
				os.Chmod(filepath.Join(endpoint, "git.sock"), 0600)
			}
			proofs := 0
			err := cleanupReplacementLocal(context.Background(), state, m, c, func(context.Context) error {
				proofs++
				if name == "native-appeared" || name == "native-after-key" && proofs == 2 {
					return errors.New("native UUID present")
				}
				return nil
			})
			if err == nil {
				t.Fatal("unsafe cleanup admitted")
			}
			if _, err = os.Lstat(endpoint); err != nil {
				t.Fatal("refusal lost reviewed endpoint")
			}
			if name == "socket-replaced" {
				m.Close()
				data, e := os.ReadFile(filepath.Join(endpoint, "git.sock"))
				if e != nil || string(data) != "preserve" {
					t.Fatal("shutdown unlinked refused substituted socket")
				}
			}
			if name != "native-after-key" && name != "parent-replaced" {
				if _, err = os.Lstat(key); err != nil {
					t.Fatal("refusal removed key before identity/native proof")
				}
			}
		})
	}
}

func TestReplacementPreviewLocalReviewRejectsFingerprintAndUnexpectedMaterial(t *testing.T) {
	state, m, c := replacementLocalFixture(t)
	if _, err := reviewReplacementLocal(state, m, c.OldUUID, c.OldOperationID, c.OldImageFingerprint, strings.Repeat("f", 64)); err == nil {
		t.Fatal("wrong principal accepted")
	}
	if err := os.WriteFile(filepath.Join(m.prefix, c.OldUUID, "extra"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewReplacementLocal(state, m, c.OldUUID, c.OldOperationID, c.OldImageFingerprint, c.KeyFingerprint); err == nil {
		t.Fatal("extra endpoint material accepted")
	}
}
