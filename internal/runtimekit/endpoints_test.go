package runtimekit

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestEndpointPreparationBindsOnlyValidatedStaging(t *testing.T) {
	const staging = "36 25 0:32 /session /opt/p/endpoints ro,nosuid,nodev master:3 - ext4 /dev/root rw\n"
	const final = "37 26 0:32 /session /run/p ro,nosuid,nodev - ext4 /dev/root rw\n"
	for name, test := range map[string]struct {
		staging        string
		final          string
		socketError    bool
		targetError    bool
		identityError  bool
		mountErrorAt   int
		stillInherited bool
		writableAfter  bool
		wantCalls      int
		wantError      bool
	}{
		"new bind":                   {staging: staging, wantCalls: 3},
		"repeat exact bind":          {staging: staging, final: final, wantCalls: 1},
		"missing staging":            {wantError: true},
		"writable staging":           {staging: strings.Replace(staging, "ro,nosuid", "rw,nosuid", 1), wantError: true},
		"stacked staging":            {staging: staging + staging, wantError: true},
		"nested staging":             {staging: staging + "38 36 0:32 /other /opt/p/endpoints/nested ro - ext4 /dev/root rw\n", wantError: true},
		"bad staging sockets":        {staging: staging, socketError: true, wantError: true},
		"unsafe target directory":    {staging: staging, targetError: true, wantError: true},
		"unrelated target mount":     {staging: staging, final: final, identityError: true, wantError: true},
		"writable existing target":   {staging: staging, final: strings.Replace(final, "ro,nosuid", "rw,nosuid", 1), wantError: true},
		"shared existing target":     {staging: staging, final: strings.Replace(final, " - ", " shared:4 - ", 1), wantError: true},
		"stacked existing target":    {staging: staging, final: final + final, wantError: true},
		"nested existing target":     {staging: staging, final: "38 36 0:32 /other /run/p/nested ro - ext4 /dev/root rw\n", wantError: true},
		"staging propagation denied": {staging: staging, mountErrorAt: 1, wantCalls: 1, wantError: true},
		"staging still inherited":    {staging: staging, stillInherited: true, wantCalls: 1, wantError: true},
		"bind denied":                {staging: staging, mountErrorAt: 2, wantCalls: 2, wantError: true},
		"final propagation denied":   {staging: staging, mountErrorAt: 3, wantCalls: 3, wantError: true},
		"writable new bind rejected": {staging: staging, writableAfter: true, wantCalls: 3, wantError: true},
		"new bind identity mismatch": {staging: staging, identityError: true, wantCalls: 3, wantError: true},
	} {
		t.Run(name, func(t *testing.T) {
			current, calls := test.staging+test.final, 0
			err := prepareEndpointBind(
				func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(current)), nil },
				func(path string) error {
					if test.socketError {
						return errors.New("bad sockets")
					}
					return nil
				},
				func() error {
					if test.targetError {
						return errors.New("unsafe target")
					}
					return nil
				},
				func() error {
					if test.identityError {
						return errors.New("identity mismatch")
					}
					return nil
				},
				func(source, target string, flags uintptr) error {
					calls++
					wantSource, wantTarget, wantFlags := "", endpointStagingPath, uintptr(syscall.MS_PRIVATE|syscall.MS_REC)
					if calls == 2 {
						wantSource, wantTarget, wantFlags = endpointStagingPath, endpointRuntimePath, syscall.MS_BIND
					}
					if calls == 3 {
						wantTarget = endpointRuntimePath
					}
					if source != wantSource || target != wantTarget || flags != wantFlags {
						t.Fatalf("unsafe/out-of-order mount call %d: %q %q %x", calls, source, target, flags)
					}
					if calls == test.mountErrorAt {
						return syscall.EPERM
					}
					if calls == 1 && !test.stillInherited {
						current = strings.ReplaceAll(current, " master:3", "")
					}
					if calls == 2 {
						added := final
						if test.writableAfter {
							added = strings.Replace(added, "ro,nosuid", "rw,nosuid", 1)
						}
						current += added
					}
					return nil
				},
			)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, want error %v", err, test.wantError)
			}
			if calls != test.wantCalls {
				t.Fatalf("mount calls = %d, want %d", calls, test.wantCalls)
			}
			if test.mountErrorAt != 0 && !errors.Is(err, syscall.EPERM) {
				t.Fatalf("lost mount errno: %v", err)
			}
		})
	}
}

func TestEndpointMountRejectsWritableOrAmbiguousPlacement(t *testing.T) {
	const valid = "36 25 0:32 /session /run/p ro,nosuid,nodev - ext4 /dev/root rw\n"
	cases := map[string]string{
		"missing":                  "25 1 0:32 / / ro - ext4 /dev/root ro\n",
		"writable bind":            strings.Replace(valid, "ro,nosuid", "rw,nosuid", 1),
		"readonly superblock only": "36 25 0:32 /session /run/p rw - ext4 /dev/root ro\n",
		"stacked bind":             valid + valid,
		"nested bind":              valid + "37 36 0:32 /other /run/p/git.sock rw - ext4 /dev/root rw\n",
		"shared propagation":       strings.Replace(valid, " - ", " shared:3 - ", 1),
		"slave propagation":        strings.Replace(valid, " - ", " master:3 - ", 1),
		"propagation source":       strings.Replace(valid, " - ", " propagate_from:3 - ", 1),
		"unbindable propagation":   strings.Replace(valid, " - ", " unbindable - ", 1),
		"missing separator":        strings.Replace(valid, " - ", " ", 1),
		"missing filesystem":       "36 25 0:32 /session /run/p ro -\n",
		"missing source":           "36 25 0:32 /session /run/p ro - ext4\n",
		"missing super options":    "36 25 0:32 /session /run/p ro - ext4 /dev/root\n",
	}
	if err := readonlyEndpointMount(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for name, mounts := range cases {
		t.Run(name, func(t *testing.T) {
			if err := readonlyEndpointMount(strings.NewReader(mounts)); err == nil {
				t.Fatal("accepted unsafe endpoint placement")
			}
		})
	}
}

func TestEndpointDirectoryContainsOnlyUsableSockets(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session.sock", "git.sock"} {
		path := filepath.Join(dir, name)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		if err := os.Chmod(path, 0666); err != nil {
			t.Fatal(err)
		}
	}
	if err := endpointSockets(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte("secret"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := endpointSockets(dir); err == nil {
		t.Fatal("accepted credential on host mount")
	}
	if err := os.Remove(filepath.Join(dir, "identity")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "git.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := endpointSockets(dir); err == nil {
		t.Fatal("accepted socket inaccessible to unmapped guest uid")
	}
}

func TestEndpointTargetRejectsSymlinksAndOtherOwners(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := endpointTarget(link); err == nil {
		t.Fatal("accepted symlink endpoint target")
	}
	if os.Geteuid() != 0 {
		if err := endpointTarget(dir); err == nil {
			t.Fatal("accepted endpoint target owned by a non-root user")
		}
	} else {
		if err := endpointTarget(dir); err != nil {
			t.Fatalf("rejected empty root-owned target: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "existing"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := endpointTarget(dir); err == nil {
			t.Fatal("accepted nonempty target")
		}
	}
}

func TestEndpointIdentityRejectsDifferentDirectoryOrSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := sameEndpointDirectory(dir, dir); err != nil {
		t.Fatal(err)
	}
	if err := sameEndpointDirectory(dir, t.TempDir()); err == nil {
		t.Fatal("accepted a different directory")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := sameEndpointDirectory(dir, link); err == nil {
		t.Fatal("accepted a symlink to staging")
	}
}

func TestEndpointDiagnosticsDistinguishAccessTypeAndMode(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := endpointSockets(missing); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect endpoint directory") {
		t.Fatalf("missing directory lost its syscall error: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0755); err != nil {
		t.Fatal(err)
	}
	if err := endpointSockets(file); err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("regular file reported as a permission problem: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	err := endpointSockets(dir)
	if err == nil || !strings.Contains(err.Error(), "requires mode 0755") || !strings.Contains(err.Error(), "mode=0700") || !strings.Contains(err.Error(), "uid=") {
		t.Fatalf("mode rejection lacks observed metadata: %v", err)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0700)
		if err := endpointSockets(filepath.Join(dir, "denied")); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("inaccessible ancestor lost its syscall error: %v", err)
		}
	}
}

func TestEndpointFailurePreservesErrno(t *testing.T) {
	err := endpointFailure(&os.PathError{Op: "lstat", Path: "/run/p", Err: syscall.EACCES})
	if !errors.Is(err, syscall.EACCES) || !strings.Contains(err.Error(), "errno=13") || !strings.Contains(err.Error(), "euid=") {
		t.Fatalf("failure lost process identity or errno: %v", err)
	}
	if endpointFailure(nil) != nil {
		t.Fatal("successful validation acquired an error")
	}
}

func TestEndpointMountDiagnosticIsBoundedAndOmitsSources(t *testing.T) {
	const input = "25 1 0:32 /other-secret /unrelated rw - ext4 secret-device secret-option\n" +
		"36 25 0:32 /host-secret /run/p ro,nosuid,nodev master:3 - ext4 secret-device secret-option\n"
	summary := endpointMountSummary(strings.NewReader(input))
	for _, want := range []string{"target=/run/p", "flags=ro,nosuid,nodev", "propagation=master:3", "filesystem=ext4"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q lacks %q", summary, want)
		}
	}
	if strings.Contains(summary, "secret") || strings.Contains(summary, "unrelated") {
		t.Fatalf("summary disclosed unrelated or source data: %q", summary)
	}
	staging := "38 25 0:32 /staging-secret /opt/p/endpoints ro - ext4 secret-device secret-option\n"
	if summary := endpointMountSummary(strings.NewReader(input + staging)); !strings.Contains(summary, "target=/opt/p/endpoints") || strings.Contains(summary, "secret") {
		t.Fatalf("staging diagnostic missing or disclosed source: %q", summary)
	}
	long := strings.Replace(input, "master:3", "master:"+strings.Repeat("3", 2048), 1)
	if summary := endpointMountSummary(strings.NewReader(long)); len(summary) > 512 {
		t.Fatalf("unbounded summary: %d bytes", len(summary))
	}
	for input, want := range map[string]string{
		"":                                       "missing",
		"36 25 0:32 /session /run/p ro master:3": "invalid",
	} {
		if got := endpointMountSummary(strings.NewReader(input)); got != want {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
}

func TestPrivateKeyRestrictsAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte("test key"), 0400); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	if err := privateKey(path, uid); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0444, 0000} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := privateKey(path, uid); err == nil {
			t.Fatalf("accepted private key mode %04o", mode)
		}
	}
}
