package runtimekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigCommandIsStructured(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/echo","","$(touch /tmp/never)","semi;colon"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Command) != 4 || cfg.Command[1] != "" || cfg.Command[2] != "$(touch /tmp/never)" {
		t.Fatalf("arguments changed: %#v", cfg.Command)
	}
}

func TestParseConfigPinsOptionalAgentAssetWithoutChangingBaseVersions(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, activation := range []struct{ kind, material string }{
		{"base", ""}, {"devshell", `,"material_sha256":"` + strings.Repeat("b", 64) + `"`},
	} {
		data := `{"schema":"p.runtime-session/v3","activation":"` + activation.kind + `"` + activation.material + `,"agent_sha256":"` + sha + `","command":["/bin/sh"]}`
		cfg, err := ParseConfig([]byte(data))
		if err != nil || cfg.AgentSHA256 != sha {
			t.Fatalf("selected adapter config rejected: %v", err)
		}
	}
	for _, data := range []string{
		`{"schema":"p.runtime-session/v1","activation":"base","agent_sha256":"` + sha + `","command":["/bin/sh"]}`,
		`{"schema":"p.runtime-session/v2","activation":"devshell","material_sha256":"` + sha + `","agent_sha256":"` + sha + `","command":["/bin/sh"]}`,
		`{"schema":"p.runtime-session/v3","activation":"base","command":["/bin/sh"]}`,
	} {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Fatalf("accepted unbound adapter config: %s", data)
		}
	}
}

func TestRegularOwnedRejectsSymlinkAndWritableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("fixed"), 0600); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	if err := regularOwned(path, uid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := regularOwned(link, uid); err == nil {
		t.Fatal("accepted symlink")
	}
	hardlink := filepath.Join(dir, "hardlink")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if err := regularOwned(path, uid); err == nil {
		t.Fatal("accepted hard-linked file")
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if err := regularOwned(path, uid); err == nil {
		t.Fatal("accepted writable file")
	}
	if err := regularOwned(path, uid+1); err == nil {
		t.Fatal("accepted wrong owner")
	}
}

func TestParseConfigRejectsUntrustedShape(t *testing.T) {
	base := `{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/sh"]}`
	cases := map[string]string{
		"duplicate key":       `{"schema":"p.runtime-session/v1","schema":"p.runtime-session/v1","activation":"base","command":["/bin/sh"]}`,
		"unknown key":         `{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/sh"],"hook":"/workspace/hook"}`,
		"future activation":   `{"schema":"p.runtime-session/v1","activation":"devshell","command":["/bin/sh"]}`,
		"relative executable": `{"schema":"p.runtime-session/v1","activation":"base","command":["sh"]}`,
		"unclean executable":  `{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/../bin/sh"]}`,
		"empty executable":    `{"schema":"p.runtime-session/v1","activation":"base","command":[""]}`,
		"NUL argument":        `{"schema":"p.runtime-session/v1","activation":"base","command":["/bin/sh","\u0000"]}`,
		"trailing data":       base + ` {}`,
		"oversized":           strings.Repeat(" ", 64<<10) + base,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(input)); err == nil {
				t.Fatal("accepted invalid session config")
			}
		})
	}
}
