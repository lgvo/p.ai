package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionKeyOperationsRefuseAnUnsafeKeyDirectory(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	registered := func(context.Context, string) (string, bool, error) { return "", false, nil }
	for _, test := range []struct {
		name string
		run  func(stateDir string) error
		want string
	}{
		{"load or create", func(stateDir string) error {
			_, _, err := sessionKeyAt(context.Background(), uuid, stateDir, registered)
			return err
		}, "session key directory is unsafe"},
		{"remove", func(stateDir string) error { return removeSessionKeyAt(stateDir, uuid) }, "session key directory identity changed"},
		{"inspect registered", func(stateDir string) error {
			_, err := inspectRegisteredSessionKey(stateDir, uuid)
			return err
		}, "session key directory unavailable"},
		{"principal repair", func(stateDir string) error {
			_, err := principalKeyDir(stateDir)
			return err
		}, "session key directory is unsafe"},
	} {
		for _, unsafe := range []struct {
			name    string
			prepare func(t *testing.T, dir string)
		}{
			{"group readable", func(t *testing.T, dir string) {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0750); err != nil {
					t.Fatal(err)
				}
			}},
			{"symlink to a private directory", func(t *testing.T, dir string) {
				target := dir + "-target"
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			}},
		} {
			t.Run(test.name+" "+unsafe.name, func(t *testing.T) {
				stateDir := t.TempDir()
				dir := filepath.Join(stateDir, "session_keys")
				unsafe.prepare(t, dir)
				err := test.run(stateDir)
				if err == nil || err.Error() != test.want {
					t.Fatalf("answer %v, want %q", err, test.want)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("key directory changed: %v", entries)
				}
			})
		}
	}
}
