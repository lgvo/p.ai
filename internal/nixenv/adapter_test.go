package nixenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = `{"bashFunctions":{"say_hi":" printf '%s\\n' hi\n"},"variables":{"outputs":{"type":"var","value":"out"},"out":{"type":"exported","value":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo"},"PATH":{"type":"exported","value":"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bin/bin"},"plain":{"type":"var","value":"a ' quote $(touch /tmp/no)\nline"},"arr":{"type":"array","value":["one two","three'four"]},"assoc":{"type":"associative","value":{"hello world":"a'b"}},"shellHook":{"type":"var","value":"say_hi; export HOOK_RAN=yes"},"HOME":{"type":"exported","value":"/build/home"}}}`

func TestParseRenderPinnedFixture(t *testing.T) {
	e, err := Parse(NixVersion, []byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != MaterialSchema || m.NixVersion != NixVersion || m.AdapterVersion != AdapterVersion {
		t.Fatalf("wrong material contract: %+v", m)
	}
	wantCapture := sha256.Sum256([]byte(fixture))
	if m.CaptureSHA256 != hex.EncodeToString(wantCapture[:]) {
		t.Fatal("material did not bind captured JSON")
	}
	for _, part := range []string{
		"out='/workspace/outputs/out'", "export out", "export -n plain",
		"declare -a arr=( 'one two' 'three'\\''four' )",
		"declare -A assoc=( ['hello world']='a'\\''b' )",
		"plain='a '\\'' quote $(touch /tmp/no)",
		"say_hi ()\n{", "builtin eval \"${shellHook:-}\" || return $?",
		"builtin export HOME='/home/p' USER=p LOGNAME=p", "builtin cd '/workspace' || return $?",
	} {
		if !strings.Contains(m.Script, part) {
			t.Errorf("script missing %q", part)
		}
	}
	if strings.Contains(m.Script, "/build/home") {
		t.Fatal("captured HOME entered script")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(m.Script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid generated Bash: %v: %s", err, out)
	}
}

func TestSourceKnownFixture(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "unexpected-command")
	input := strings.Replace(fixture, "/tmp/no", marker, 1)
	input = strings.Replace(input, "say_hi; export HOOK_RAN=yes", "HOME=/tmp/hook; cd /; say_hi; export HOOK_RAN=yes", 1)
	input = strings.Replace(input, `"plain":{"type":"var"`, `"XDG_DATA_DIRS":{"type":"var","value":"/nix/data"},"plain":{"type":"var"`, 1)
	e, err := Parse(NixVersion, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dir, "workspace")
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := render(e, workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "activation.sh")
	if err := os.WriteFile(script, []byte(m.Script), 0600); err != nil {
		t.Fatal(err)
	}
	assertions := `source "$1" || exit 21
[[ $plain == "$2" ]] || exit 22
[[ $(declare -p plain) == 'declare --'* ]] || exit 23
[[ $(declare -p out) == 'declare -x'* ]] || exit 24
[[ $(declare -p XDG_DATA_DIRS) == 'declare --'* ]] || exit 25
[[ $(declare -p PATH) == 'declare -x'* ]] || exit 26
[[ ${arr[0]} == 'one two' && ${arr[1]} == "three'four" ]] || exit 27
[[ ${assoc['hello world']} == "a'b" ]] || exit 28
[[ $HOOK_RAN == yes ]] || exit 29
[[ $HOME == "$3" && $PWD == "$4" && $USER == p && $LOGNAME == p ]] || exit 30
[[ $out == /workspace/outputs/out ]] || exit 31
[[ $PATH == /nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bin/bin:* ]] || exit 32
[[ $XDG_DATA_DIRS == /nix/data:/host/data ]] || exit 33
`
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", assertions, "p-test", script, "a ' quote $(touch "+marker+")\nline", home, workspace)
	cmd.Env = []string{"PATH=/run/current-system/sw/bin:/usr/bin:/bin", "HOME=/tmp/preexisting", "XDG_DATA_DIRS=/host/data", "TMPDIR=" + dir}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sourced fixture failed: %v: %s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("quoted value executed command substitution: %v", err)
	}
	digest, err := m.Digest()
	if err != nil || !hexDigest(digest) {
		t.Fatalf("material digest: %v", err)
	}
}

func TestSourceHookFailure(t *testing.T) {
	input := strings.Replace(fixture, "say_hi; export HOOK_RAN=yes", "false", 1)
	e, err := Parse(NixVersion, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := render(e, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "activation.sh")
	if err := os.WriteFile(script, []byte(m.Script), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `source "$1"; exit $?`, "p-test", script)
	cmd.Env = []string{"PATH=/run/current-system/sw/bin:/usr/bin:/bin", "TMPDIR=" + dir}
	if err := cmd.Run(); err == nil {
		t.Fatal("failed shell hook was accepted")
	}
}

func TestStructuredAttrsAndOutputRewrite(t *testing.T) {
	input := `{"bashFunctions":{},"variables":{"outputs":{"type":"associative","value":{"out":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo"}},"NIX_ATTRS_SH_FILE":{"type":"exported","value":"/build/.attrs.sh"},"NIX_ATTRS_JSON_FILE":{"type":"exported","value":"/build/.attrs.json"},"shellHook":{"type":"var","value":"cat /build/.attrs.json; echo /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo"}},"structuredAttrs":{".attrs.sh":"out=/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo",".attrs.json":"{\"out\":\"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-demo\"}"}}`
	e, err := Parse(NixVersion, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Script, AttrsDir+"/.attrs.sh") || !strings.Contains(m.Script, AttrsDir+"/.attrs.json") || !strings.Contains(m.AttrsSH, "/workspace/outputs/out") || !strings.Contains(m.AttrsJSON, "/workspace/outputs/out") {
		t.Fatalf("structured attrs not rewritten: %+v", m)
	}
	if strings.Contains(m.Script, "/build/.attrs") {
		t.Fatal("builder attr path leaked")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMaterial(data)
	if err != nil || !got.HasStructuredAttrs || got.AttrsJSON != m.AttrsJSON {
		t.Fatalf("material roundtrip: %v", err)
	}
}

func TestRejectUnsupportedAndMalformed(t *testing.T) {
	cases := map[string]string{
		"unknown root":          `{"bashFunctions":{},"variables":{},"new":1}`,
		"duplicate root":        `{"bashFunctions":{},"variables":{},"variables":{}}`,
		"unknown value field":   `{"bashFunctions":{},"variables":{"x":{"type":"var","value":"x","new":1}}}`,
		"unknown type":          `{"bashFunctions":{},"variables":{"x":{"type":"unknown"}}}`,
		"null scalar":           `{"bashFunctions":{},"variables":{"x":{"type":"var","value":null}}}`,
		"null array entry":      `{"bashFunctions":{},"variables":{"x":{"type":"array","value":[null]}}}`,
		"null map entry":        `{"bashFunctions":{},"variables":{"x":{"type":"associative","value":{"a":null}}}}`,
		"bad name":              `{"bashFunctions":{},"variables":{"a;touch":{"type":"var","value":"x"}}}`,
		"missing functions":     `{"variables":{}}`,
		"extra structured attr": `{"bashFunctions":{},"variables":{},"structuredAttrs":{".attrs.sh":"",".attrs.json":"","extra":""}}`,
		"null structured attr":  `{"bashFunctions":{},"variables":{},"structuredAttrs":null}`,
		"NUL":                   `{"bashFunctions":{},"variables":{"x":{"type":"var","value":"\u0000"}}}`,
		"array hook":            `{"bashFunctions":{},"variables":{"shellHook":{"type":"array","value":["echo hi"]}}}`,
		"capitalized root":      `{"Variables":{},"bashFunctions":{}}`,
		"mixed case root":       `{"variables":{},"Variables":{},"bashFunctions":{}}`,
		"capitalized type":      `{"bashFunctions":{},"variables":{"x":{"Type":"var","value":"x"}}}`,
		"mixed case type":       `{"bashFunctions":{},"variables":{"x":{"type":"var","Type":"exported","value":"x"}}}`,
		"capitalized attrs":     `{"bashFunctions":{},"variables":{},"structuredAttrs":{".Attrs.sh":"",".attrs.json":""}}`,
		"null function body":    `{"bashFunctions":{"f":null},"variables":{}}`,
		"null attrs content":    `{"bashFunctions":{},"variables":{},"structuredAttrs":{".attrs.sh":null,".attrs.json":""}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(NixVersion, []byte(input)); err == nil {
				t.Fatal("accepted malformed JSON")
			}
		})
	}
	if _, err := Parse("2.35.0", []byte(fixture)); err == nil {
		t.Fatal("accepted other Nix version")
	}
	if _, err := Parse(NixVersion, bytes.Repeat([]byte("x"), MaxJSON+1)); err == nil {
		t.Fatal("accepted oversize JSON")
	}
	if _, err := Parse(NixVersion, []byte{0xff}); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	if _, err := ParseMaterial([]byte(`{"schema":"future","nix_version":"2.34.8","adapter_version":"p.nix-print-dev-env/1","script":"echo hi","has_structured_attrs":false}`)); err == nil {
		t.Fatal("accepted future material schema")
	}
	for _, bad := range []string{
		`{"Schema":"p.nix-activation/v1","nix_version":"2.34.8","adapter_version":"p.nix-print-dev-env/1","script":"echo hi","has_structured_attrs":false,"capture_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"schema":"p.nix-activation/v1","nix_version":"2.34.8","adapter_version":"p.nix-print-dev-env/1","script":"echo hi","capture_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
	} {
		if _, err := ParseMaterial([]byte(bad)); err == nil {
			t.Fatal("accepted malformed material")
		}
	}
}

func TestMaterialRejectsNullWrongTypesAndMissingAttrs(t *testing.T) {
	const base = `{"schema":"p.nix-activation/v1","nix_version":"2.34.8","adapter_version":"p.nix-print-dev-env/1","script":"true","has_structured_attrs":true,"capture_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attrs_sh":"","attrs_json":""}`
	if _, err := ParseMaterial([]byte(base)); err != nil {
		t.Fatalf("valid structured material rejected: %v", err)
	}
	cases := map[string]string{
		"null boolean":   strings.Replace(base, `"has_structured_attrs":true`, `"has_structured_attrs":null`, 1),
		"string boolean": strings.Replace(base, `"has_structured_attrs":true`, `"has_structured_attrs":"true"`, 1),
		"null schema":    strings.Replace(base, `"schema":"p.nix-activation/v1"`, `"schema":null`, 1),
		"number version": strings.Replace(base, `"nix_version":"2.34.8"`, `"nix_version":2348`, 1),
		"null script":    strings.Replace(base, `"script":"true"`, `"script":null`, 1),
		"null capture":   strings.Replace(base, `"capture_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"capture_sha256":null`, 1),
		"null sh":        strings.Replace(base, `"attrs_sh":""`, `"attrs_sh":null`, 1),
		"null json":      strings.Replace(base, `"attrs_json":""`, `"attrs_json":null`, 1),
		"missing sh":     strings.Replace(base, `,"attrs_sh":""`, ``, 1),
		"missing json":   strings.Replace(base, `,"attrs_json":""`, ``, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMaterial([]byte(input)); err == nil {
				t.Fatal("accepted malformed material")
			}
		})
	}
}

func TestRejectUnsafeOutputAndKey(t *testing.T) {
	e, err := Parse(NixVersion, []byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	e.Variables["out"] = Variable{Type: "var", Value: []byte(`"/tmp/not-a-store-path"`)}
	if _, err := Render(e); err == nil {
		t.Fatal("accepted output outside store")
	}
	if _, err := (KeyInputs{}).Digest(); err == nil {
		t.Fatal("accepted incomplete key")
	}
	k := KeyInputs{AdapterVersion: AdapterVersion, NixVersion: NixVersion, System: "x86_64-linux", ProjectPath: "team/project", BaseFingerprint: "abc", RuntimeKitContract: "v1", CommittedInputsDigest: "source", DerivationPath: "/nix/store/demo.drv", NixPolicyDigest: "policy", ImageFormatVersion: "v1"}
	a, err := k.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.Digest()
	if err != nil || a != b {
		t.Fatal("nondeterministic key")
	}
	k.ProjectPath = "team/other"
	c, err := k.Digest()
	if err != nil || a == c {
		t.Fatal("project scope omitted from key")
	}
	for _, project := range []string{"", "/srv/p/project", "team/../other", "team//app", "team/", "team\\app", "team/\napp", strings.Repeat("a", 101), strings.Repeat("a/", 128) + "a"} {
		k.ProjectPath = project
		if _, err := k.Digest(); err == nil {
			t.Fatalf("accepted invalid project identity %q", project)
		}
	}
	k.ProjectPath = "team/other"
	k.NixVersion = "2.35.0"
	if _, err := k.Digest(); err == nil {
		t.Fatal("accepted unsupported Nix key version")
	}
}
