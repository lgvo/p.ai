package eventfile

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const line = `{"schema":"p.event/v1","id":"e-1","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-one","fields":{"condition":"ready"}}` + "\n"

func privateLog(t *testing.T) (string, Config) {
	t.Helper()
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	return private, Config{Path: filepath.Join(private, "events.ndjson"), MaxBytes: 1024}
}

func TestAppendWritesAPrivateFile(t *testing.T) {
	_, config := privateLog(t)
	if err := Append(config, []byte(line)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config.Path)
	if err != nil || string(data) != line {
		t.Fatalf("the log holds %q (%v), want the line", data, err)
	}
	if info, err := os.Stat(config.Path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe file mode: %v, %v", info, err)
	}
}

func TestAppendRefusesAnUnsafeLog(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T, private string, config Config) Config
		want    string
	}{
		{"a symbolic link", func(t *testing.T, private string, config Config) Config {
			if err := os.Symlink(filepath.Join(t.TempDir(), "target"), config.Path); err != nil {
				t.Fatal(err)
			}
			return config
		}, "too many levels of symbolic links"},
		{"a FIFO", func(t *testing.T, private string, config Config) Config {
			if err := syscall.Mkfifo(config.Path, 0600); err != nil {
				t.Fatal(err)
			}
			return config
		}, "no such device or address"},
		{"a second link", func(t *testing.T, private string, config Config) Config {
			target := filepath.Join(private, "target")
			if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, config.Path); err != nil {
				t.Fatal(err)
			}
			return config
		}, "unlinked elsewhere"},
		{"a directory others may read", func(t *testing.T, private string, config Config) Config {
			if err := os.Chmod(private, 0755); err != nil {
				t.Fatal(err)
			}
			return config
		}, "log directory must be private"},
		{"a symbolic link among its directories", func(t *testing.T, private string, config Config) Config {
			linked := filepath.Join(t.TempDir(), "linked")
			if err := os.Symlink(private, linked); err != nil {
				t.Fatal(err)
			}
			config.Path = filepath.Join(linked, "events.ndjson")
			return config
		}, "not a directory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			private, config := privateLog(t)
			config = c.arrange(t, private, config)
			start := time.Now()
			err := Append(config, []byte(line))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Append = %v, want %q", err, c.want)
			}
			if time.Since(start) > time.Second {
				t.Fatal("Append blocked")
			}
		})
	}
}

func TestAppendKeepsOnePriorSegment(t *testing.T) {
	_, config := privateLog(t)
	for i := 0; i < 20; i++ {
		if err := Append(config, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{config.Path, config.Path + ".1"} {
		if info, err := os.Stat(p); err != nil || info.Size() > 1024 {
			t.Fatalf("rotation failed for %s: %v %v", p, info, err)
		}
	}
	if _, err := os.Stat(config.Path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("a second prior segment: %v", err)
	}
}

func TestParseConfig(t *testing.T) {
	for _, c := range []struct {
		config, want string
	}{
		{`{"path":"/private/events.ndjson","max_bytes":4096}`, ""},
		{`{"path":"events.ndjson","max_bytes":4096}`, "absolute and clean"},
		{`{"path":"/private/../events.ndjson","max_bytes":4096}`, "absolute and clean"},
		{`{"path":"/private/events.ndjson","max_bytes":1023}`, "between 1024 and 104857600"},
		{`{"path":"/private/events.ndjson","max_bytes":104857601}`, "between 1024 and 104857600"},
		{`{"path":"/private/events.ndjson","max_bytes":4096,"mode":"0644"}`, "unknown or inexact JSON field"},
		{`{"path":"/a","path":"/b","max_bytes":4096}`, "duplicate"},
	} {
		_, err := ParseConfig([]byte(c.config))
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("ParseConfig(%s) = %v, want %q", c.config, err, c.want)
		}
	}
}
