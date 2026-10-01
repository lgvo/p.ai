// Package nixenv captures the pinned Nix print-dev-env JSON contract. It never
// invokes Nix or evaluates project source; callers must do that in a builder.
package nixenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	NixVersion     = "2.34.8"
	AdapterVersion = "p.nix-print-dev-env/1"
	MaterialSchema = "p.nix-activation/v1"
	MaxJSON        = 2 << 20
	MaxScript      = 3 << 20
	AttrsDir       = "/etc/p/devshell"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)

// Variable is one of the four value kinds emitted by Nix 2.34.8. See
// https://github.com/NixOS/nix/blob/2.34.8/src/nix/develop.cc and get-env.sh.
// An absent
// variable is absent; the upstream JSON has no explicit unset type.
type Variable struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type Environment struct {
	Variables       map[string]Variable `json:"variables"`
	Functions       map[string]string   `json:"bashFunctions"`
	StructuredAttrs map[string]string   `json:"structuredAttrs,omitempty"`
	captureSHA256   string
	canonicalSHA256 string
}

// Material is a builder-produced, versioned immutable image asset. Attrs files
// are installed at AttrsDir with root ownership before a guest sources Script.
type Material struct {
	Schema             string `json:"schema"`
	NixVersion         string `json:"nix_version"`
	AdapterVersion     string `json:"adapter_version"`
	Script             string `json:"script"`
	HasStructuredAttrs bool   `json:"has_structured_attrs"`
	CaptureSHA256      string `json:"capture_sha256"`
	AttrsSH            string `json:"attrs_sh"`
	AttrsJSON          string `json:"attrs_json"`
}

// ParseMaterial is for trusted image material before guest use. The image
// assembler must separately enforce root ownership and file immutability.
func ParseMaterial(data []byte) (Material, error) {
	var m Material
	if len(data) == 0 || len(data) > MaxScript+MaxJSON {
		return m, errors.New("activation material exceeds bounds")
	}
	if err := uniqueKeys(data); err != nil {
		return m, err
	}
	if err := exactKeys(data, "schema", "nix_version", "adapter_version", "script", "has_structured_attrs", "capture_sha256", "attrs_sh", "attrs_json"); err != nil {
		return m, err
	}
	if !utf8.Valid(data) {
		return m, errors.New("invalid UTF-8 in activation material")
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	for _, key := range []string{"schema", "nix_version", "adapter_version", "script", "has_structured_attrs", "capture_sha256"} {
		if _, present := raw[key]; !present {
			return m, fmt.Errorf("missing material field %q", key)
		}
	}
	for _, key := range []string{"schema", "nix_version", "adapter_version", "script", "capture_sha256"} {
		if value := bytes.TrimSpace(raw[key]); len(value) == 0 || value[0] != '"' {
			return m, fmt.Errorf("material field %q must be a string", key)
		}
	}
	flag := bytes.TrimSpace(raw["has_structured_attrs"])
	if !bytes.Equal(flag, []byte("true")) && !bytes.Equal(flag, []byte("false")) {
		return m, errors.New("material has_structured_attrs must be a boolean")
	}
	for _, key := range []string{"attrs_sh", "attrs_json"} {
		value, present := raw[key]
		if !present {
			if bytes.Equal(flag, []byte("true")) {
				return m, fmt.Errorf("missing structured attrs field %q", key)
			}
			continue
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '"' {
			return m, fmt.Errorf("material field %q must be a string", key)
		}
	}
	if err := decodeStrict(data, &m); err != nil {
		return m, err
	}
	if m.Schema != MaterialSchema || m.NixVersion != NixVersion || m.AdapterVersion != AdapterVersion ||
		len(m.Script) == 0 || len(m.Script) > MaxScript || !validValue(m.AttrsSH) || !validValue(m.AttrsJSON) ||
		strings.ContainsRune(m.Script, 0) || !hexDigest(m.CaptureSHA256) {
		return m, errors.New("unsupported activation material")
	}
	if !m.HasStructuredAttrs && (m.AttrsSH != "" || m.AttrsJSON != "") {
		return m, errors.New("unexpected structured attrs material")
	}
	return m, nil
}

// Digest binds the exact canonical material fields for a future trusted
// image assembler and Handle.ContentIdentity. It does not authenticate the
// material by itself; the assembler must verify its trusted image ownership.
func (m Material) Digest() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if _, err := ParseMaterial(b); err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// KeyInputs are canonical environment identity inputs. The image locator is
// deliberately separate: only an Incus image adapter interprets it.
type KeyInputs struct {
	AdapterVersion        string `json:"adapter_version"`
	NixVersion            string `json:"nix_version"`
	System                string `json:"system"`
	ProjectPath           string `json:"project_path"`
	BaseFingerprint       string `json:"base_fingerprint"`
	RuntimeKitContract    string `json:"runtime_kit_contract"`
	CommittedInputsDigest string `json:"committed_inputs_digest"`
	DerivationPath        string `json:"derivation_path"`
	NixPolicyDigest       string `json:"nix_policy_digest"`
	ImageFormatVersion    string `json:"image_format_version"`
}

// Plan binds a committed source snapshot to the identity used by a future
// isolated builder. SourceCommit is provenance, not itself a cache key.
type Plan struct {
	SourceCommit string    `json:"source_commit"`
	DevShellAttr string    `json:"dev_shell_attr"`
	KeyInputs    KeyInputs `json:"key_inputs"`
}

type Handle struct {
	TargetKind      string `json:"target_kind"`
	ContractVersion string `json:"contract_version"`
	ContentIdentity string `json:"content_identity"`
	Locator         string `json:"locator"`
}

func (k KeyInputs) Digest() (string, error) {
	if k.AdapterVersion != AdapterVersion || k.NixVersion != NixVersion || (k.System != "x86_64-linux" && k.System != "aarch64-linux") ||
		!validProjectIdentity(k.ProjectPath) || k.BaseFingerprint == "" ||
		k.RuntimeKitContract == "" || k.CommittedInputsDigest == "" || k.DerivationPath == "" ||
		k.NixPolicyDigest == "" || k.ImageFormatVersion == "" {
		return "", errors.New("incomplete environment key inputs")
	}
	b, _ := json.Marshal(k)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// Environment cache scope is P's logical project identity, not a host path.
func validProjectIdentity(project string) bool {
	if project == "" || len(project) > 255 {
		return false
	}
	for _, part := range strings.Split(project, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

// Parse accepts only the complete pinned schema. Version comes from the
// trusted builder's Nix binary check, never from project JSON.
func Parse(version string, data []byte) (Environment, error) {
	var e Environment
	if version != NixVersion {
		return e, fmt.Errorf("unsupported Nix version %q", version)
	}
	if len(data) == 0 || len(data) > MaxJSON {
		return e, errors.New("Nix environment JSON exceeds bounds")
	}
	if !utf8.Valid(data) {
		return e, errors.New("invalid UTF-8 in Nix environment JSON")
	}
	if err := uniqueKeys(data); err != nil {
		return e, err
	}
	if err := exactKeys(data, "variables", "bashFunctions", "structuredAttrs"); err != nil {
		return e, err
	}
	var raw struct {
		Variables       map[string]json.RawMessage `json:"variables"`
		Functions       map[string]string          `json:"bashFunctions"`
		StructuredAttrs map[string]string          `json:"structuredAttrs,omitempty"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return e, err
	}
	var rootFields map[string]json.RawMessage
	_ = json.Unmarshal(data, &rootFields)
	if attrs, present := rootFields["structuredAttrs"]; present && (len(attrs) == 0 || attrs[0] != '{') {
		return e, errors.New("invalid structuredAttrs type")
	}
	h := sha256.Sum256(data)
	e = Environment{Variables: map[string]Variable{}, Functions: raw.Functions, StructuredAttrs: raw.StructuredAttrs, captureSHA256: hex.EncodeToString(h[:])}
	for name, item := range raw.Variables {
		if err := exactKeys(item, "type", "value"); err != nil {
			return e, fmt.Errorf("variable %s: %w", name, err)
		}
		var v Variable
		if err := decodeStrict(item, &v); err != nil {
			return e, fmt.Errorf("variable %s: %w", name, err)
		}
		e.Variables[name] = v
	}
	if raw.Variables == nil || e.Functions == nil {
		return e, errors.New("missing Nix environment fields")
	}
	if len(e.Variables) > 4096 || len(e.Functions) > 512 {
		return e, errors.New("Nix environment item count exceeds bounds")
	}
	for name, v := range e.Variables {
		if !identifier.MatchString(name) || len(name) > 128 {
			return e, fmt.Errorf("invalid variable name %q", name)
		}
		if len(v.Value) == 0 {
			return e, fmt.Errorf("variable %s missing value", name)
		}
		switch v.Type {
		case "var", "exported":
			var s string
			if v.Value[0] != '"' {
				return e, fmt.Errorf("invalid scalar %s", name)
			}
			if err := json.Unmarshal(v.Value, &s); err != nil || !validValue(s) {
				return e, fmt.Errorf("invalid scalar %s", name)
			}
		case "array":
			var a []string
			if err := json.Unmarshal(v.Value, &a); err != nil || a == nil || len(a) > 4096 {
				return e, fmt.Errorf("invalid array %s", name)
			}
			var rawItems []json.RawMessage
			_ = json.Unmarshal(v.Value, &rawItems)
			for _, raw := range rawItems {
				if len(raw) == 0 || raw[0] != '"' {
					return e, fmt.Errorf("invalid array element %s", name)
				}
			}
			for _, s := range a {
				if !validValue(s) {
					return e, fmt.Errorf("invalid array element %s", name)
				}
			}
		case "associative":
			var a map[string]string
			if err := json.Unmarshal(v.Value, &a); err != nil || a == nil || len(a) > 4096 {
				return e, fmt.Errorf("invalid associative array %s", name)
			}
			var rawEntries map[string]json.RawMessage
			_ = json.Unmarshal(v.Value, &rawEntries)
			for _, raw := range rawEntries {
				if len(raw) == 0 || raw[0] != '"' {
					return e, fmt.Errorf("invalid associative entry %s", name)
				}
			}
			for key, value := range a {
				if !validValue(key) || !validValue(value) {
					return e, fmt.Errorf("invalid associative entry %s", name)
				}
			}
		default:
			return e, fmt.Errorf("unsupported variable type %q", v.Type)
		}
	}
	for _, name := range []string{"shellHook", "PATH", "XDG_DATA_DIRS"} {
		if v, ok := e.Variables[name]; ok && v.Type != "var" && v.Type != "exported" {
			return e, fmt.Errorf("%s must be scalar", name)
		}
	}
	for name, body := range e.Functions {
		if !identifier.MatchString(name) || len(name) > 128 || !validValue(body) {
			return e, fmt.Errorf("invalid function %q", name)
		}
	}
	var rawFunctions map[string]json.RawMessage
	_ = json.Unmarshal(rootFields["bashFunctions"], &rawFunctions)
	for name, value := range rawFunctions {
		if len(value) == 0 || value[0] != '"' {
			return e, fmt.Errorf("invalid function body %q", name)
		}
	}
	if e.StructuredAttrs != nil {
		if err := exactKeys(rootFields["structuredAttrs"], ".attrs.sh", ".attrs.json"); err != nil {
			return e, err
		}
		var rawAttrs map[string]json.RawMessage
		_ = json.Unmarshal(rootFields["structuredAttrs"], &rawAttrs)
		for name, value := range rawAttrs {
			if len(value) == 0 || value[0] != '"' {
				return e, fmt.Errorf("invalid structured attr %q", name)
			}
		}
		if len(e.StructuredAttrs) != 2 {
			return e, errors.New("invalid structuredAttrs fields")
		}
		for _, key := range []string{".attrs.sh", ".attrs.json"} {
			if _, ok := e.StructuredAttrs[key]; !ok || !validValue(e.StructuredAttrs[key]) {
				return e, errors.New("invalid structuredAttrs content")
			}
		}
	}
	canonical, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	canonicalHash := sha256.Sum256(canonical)
	e.canonicalSHA256 = hex.EncodeToString(canonicalHash[:])
	return e, nil
}

func validValue(s string) bool { return len(s) <= 128<<10 && !strings.ContainsRune(s, 0) }

func hexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func exactKeys(data []byte, allowed ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("expected JSON object")
	}
	valid := map[string]bool{}
	for _, key := range allowed {
		valid[key] = true
	}
	for key := range fields {
		if !valid[key] {
			return fmt.Errorf("unknown JSON field %q", key)
		}
	}
	return nil
}

func decodeStrict(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func uniqueKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		mark, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch mark {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, err := d.Token()
				if err != nil {
					return err
				}
				key := t.(string)
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func names[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
