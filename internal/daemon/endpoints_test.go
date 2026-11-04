package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEndpointRestartAndScopedSessionSocket(t *testing.T) {
	prefix, err := os.MkdirTemp("/tmp", "p-e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	m, err := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "550e8400-e29b-41d4-a716-446655440000"
	dir, err := m.Ensure(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git.sock", "session.sock"} {
		info, e := os.Lstat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0666 || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("%s: %v %v", name, info, e)
		}
	}
	call := func(method string) map[string]any {
		conn, e := net.DialTimeout("unix", filepath.Join(dir, "session.sock"), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		request := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"v":1}}` + "\n"
		if _, e = conn.Write([]byte(request)); e != nil {
			t.Fatal(e)
		}
		line, e := bufio.NewReader(conn).ReadBytes('\n')
		if e != nil {
			t.Fatal(e)
		}
		var reply map[string]any
		if e = json.Unmarshal(line, &reply); e != nil {
			t.Fatal(e)
		}
		return reply
	}
	if _, ok := call("session.health")["error"]; !ok {
		t.Fatal("unbound session socket advertised a health method")
	}
	if _, ok := call("system.inspect")["error"]; !ok {
		t.Fatal("host method exposed on session socket")
	}
	m.Close()
	restarted, err := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err = restarted.Ensure(context.Background(), id); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "session.sock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestEndpointDirectoryModeWithRestrictiveUmask(t *testing.T) {
	if os.Getenv("P_TEST_ENDPOINT_UMASK_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEndpointDirectoryModeWithRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "P_TEST_ENDPOINT_UMASK_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restricted-umask subprocess: %v\n%s", err, output)
		}
		return
	}
	syscall.Umask(077)
	prefix, err := os.MkdirTemp("/tmp", "p-e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	if err := os.Chmod(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := newEndpointManager(prefix, "127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	dir, err := m.Ensure(context.Background(), "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("endpoint directory mode = %#o, want 0755", got)
	}
	for _, name := range []string{"git.sock", "session.sock"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0666 {
			t.Fatalf("%s mode = %v, want socket mode 0666", name, info.Mode())
		}
	}
}

func TestEndpointRejectsExistingUnsafeDirectoryWithoutChangingIt(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	for _, kind := range []string{"wrong-mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			prefix := t.TempDir()
			if err := os.Chmod(prefix, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(prefix, id)
			var target string
			if kind == "wrong-mode" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				target = t.TempDir()
				if err := os.Chmod(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			m, err := newEndpointManager(prefix, "127.0.0.1:1", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if _, err := m.Ensure(context.Background(), id); err == nil || !strings.Contains(err.Error(), "unsafe identity") {
				t.Fatalf("Ensure error = %v, want unsafe identity", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "wrong-mode" && (!info.IsDir() || info.Mode().Perm() != 0700) {
				t.Fatalf("existing directory changed: %v", info.Mode())
			}
			if kind == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("existing symlink changed: %v", info.Mode())
			}
			if target != "" {
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("symlink target mode changed: %v, %v", info, err)
				}
			}
		})
	}
}
