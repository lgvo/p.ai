package nixenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Render turns a validated Nix 2.34.8 environment into guest-only Bash
// activation. It does not run the hook. Source Script at Bash top level in the
// session after trusted paths have been installed, then exec the host command.
func Render(e Environment) (Material, error) {
	return render(e, "/workspace", "/home/p")
}

func render(e Environment, workspace, home string) (Material, error) {
	if e.Variables == nil || e.Functions == nil {
		return Material{}, errors.New("incomplete environment")
	}
	// Validate a caller-created Environment by round-tripping through Parse.
	b, err := json.Marshal(e)
	if err != nil {
		return Material{}, err
	}
	if hexDigest(e.canonicalSHA256) {
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != e.canonicalSHA256 {
			return Material{}, errors.New("captured environment changed after parse")
		}
	}
	validated, err := Parse(NixVersion, b)
	if err != nil {
		return Material{}, err
	}
	captureDigest := e.captureSHA256
	if !hexDigest(captureDigest) {
		captureDigest = validated.captureSHA256
	}
	e = validated
	e.captureSHA256 = captureDigest
	rewrites, err := outputRewrites(e)
	if err != nil {
		return Material{}, err
	}
	if e.StructuredAttrs != nil {
		for _, name := range []string{"NIX_ATTRS_SH_FILE", "NIX_ATTRS_JSON_FILE"} {
			v, ok := e.Variables[name]
			if !ok || (v.Type != "var" && v.Type != "exported") {
				return Material{}, fmt.Errorf("missing %s for structured attrs", name)
			}
			var old string
			_ = json.Unmarshal(v.Value, &old)
			if old == "" || !strings.HasPrefix(old, "/") {
				return Material{}, fmt.Errorf("invalid %s", name)
			}
			suffix := ".attrs.sh"
			if name == "NIX_ATTRS_JSON_FILE" {
				suffix = ".attrs.json"
			}
			rewrites[old] = AttrsDir + "/" + suffix
		}
	}
	replace := replacer(rewrites)
	var s strings.Builder
	s.WriteString("# " + MaterialSchema + "; Nix " + NixVersion + "\n")
	s.WriteString("unset -v shellHook\n")
	s.WriteString("__p_nix_saved_PATH=${PATH:-}\n__p_nix_saved_XDG_DATA_DIRS=${XDG_DATA_DIRS:-}\n")
	for _, name := range names(e.Variables) {
		if protected(name) {
			continue
		}
		v := e.Variables[name]
		s.WriteString("unset -v " + name + "\n")
		switch v.Type {
		case "var", "exported":
			var value string
			_ = json.Unmarshal(v.Value, &value)
			s.WriteString(name + "=" + quote(replace(value)) + "\n")
			if v.Type == "exported" {
				s.WriteString("export " + name + "\n")
			} else {
				s.WriteString("export -n " + name + "\n")
			}
		case "array":
			var values []string
			_ = json.Unmarshal(v.Value, &values)
			s.WriteString("declare -a " + name + "=(")
			for _, value := range values {
				s.WriteString(" " + quote(replace(value)))
			}
			s.WriteString(" )\n")
		case "associative":
			var values map[string]string
			_ = json.Unmarshal(v.Value, &values)
			s.WriteString("declare -A " + name + "=(")
			for _, key := range names(values) {
				s.WriteString(" [" + quote(replace(key)) + "]=" + quote(replace(values[key])))
			}
			s.WriteString(" )\n")
		}
	}
	for _, name := range names(e.Functions) {
		if protected(name) {
			continue
		}
		if dangerousFunction(name) {
			return Material{}, fmt.Errorf("unsupported shell function %s", name)
		}
		s.WriteString(name + " ()\n{\n" + replace(e.Functions[name]) + "}\n")
	}
	for _, name := range []string{"PATH", "XDG_DATA_DIRS"} {
		s.WriteString(name + `="${` + name + `:-}${__p_nix_saved_` + name + `:+:${__p_nix_saved_` + name + `}}"` + "\n")
	}
	s.WriteString("unset -v __p_nix_saved_PATH __p_nix_saved_XDG_DATA_DIRS\n")
	s.WriteString("export NIX_BUILD_TOP=\"$(mktemp -d -t nix-shell.XXXXXX)\"\n")
	s.WriteString("export TMP=\"$NIX_BUILD_TOP\" TMPDIR=\"$NIX_BUILD_TOP\" TEMP=\"$NIX_BUILD_TOP\" TEMPDIR=\"$NIX_BUILD_TOP\"\n")
	s.WriteString("builtin eval \"${shellHook:-}\" || return $?\n")
	// The session user and workspace are fixed by the image contract. Hooks are
	// project code inside the guest; restore these shell values before the host.
	s.WriteString("builtin export HOME=" + quote(home) + " USER=p LOGNAME=p\nbuiltin cd " + quote(workspace) + " || return $?\n")
	if s.Len() > MaxScript {
		return Material{}, errors.New("activation script exceeds bounds")
	}
	m := Material{Schema: MaterialSchema, NixVersion: NixVersion, AdapterVersion: AdapterVersion, Script: s.String(), CaptureSHA256: e.captureSHA256}
	if e.StructuredAttrs != nil {
		m.HasStructuredAttrs = true
		m.AttrsSH = replace(e.StructuredAttrs[".attrs.sh"])
		m.AttrsJSON = replace(e.StructuredAttrs[".attrs.json"])
	}
	if !validValue(m.AttrsSH) || !validValue(m.AttrsJSON) {
		return Material{}, errors.New("structured attrs material exceeds bounds")
	}
	return m, nil
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func protected(name string) bool {
	if strings.HasPrefix(name, "P_") || strings.HasPrefix(name, "BASH_") || strings.HasPrefix(name, "COMP_") || strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "SSH_") || strings.HasPrefix(name, "__p_nix_") {
		return true
	}
	switch name {
	case "HOME", "USER", "LOGNAME", "PWD", "OLDPWD", "SHELL", "UID", "EUID", "PPID", "SHELLOPTS", "BASHOPTS", "NIX_REMOTE", "NIX_CONF_DIR", "NIX_USER_CONF_FILES", "NIX_ENFORCE_PURITY", "NIX_LOG_FD", "NIX_BUILD_TOP", "SSL_CERT_FILE", "TERM", "TZ", "TMP", "TMPDIR", "TEMP", "TEMPDIR", "DIRSTACK", "GROUPS", "FUNCNAME", "PIPESTATUS", "RANDOM", "SHLVL", "SECONDS", "EPOCHREALTIME", "EPOCHSECONDS", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME":
		return true
	}
	return false
}

func dangerousFunction(name string) bool {
	switch name {
	case "eval", "cd", "export", "unset", "declare", "mktemp", "command", "builtin", "return":
		return true
	}
	return false
}

func outputRewrites(e Environment) (map[string]string, error) {
	v, ok := e.Variables["outputs"]
	if !ok {
		return nil, errors.New("missing outputs variable")
	}
	m := map[string]string{}
	add := func(name, from string) error {
		if !identifier.MatchString(name) || len(name) > 128 || !strings.HasPrefix(from, "/nix/store/") || path.Clean(from) != from || from == "/nix/store/" {
			return fmt.Errorf("invalid output %q", name)
		}
		if _, exists := m[from]; exists {
			return fmt.Errorf("duplicate output path %q", from)
		}
		m[from] = "/workspace/outputs/" + name
		return nil
	}
	if e.StructuredAttrs != nil {
		if v.Type != "associative" {
			return nil, errors.New("structured outputs must be associative")
		}
		var values map[string]string
		_ = json.Unmarshal(v.Value, &values)
		for name, from := range values {
			if err := add(name, from); err != nil {
				return nil, err
			}
		}
	} else {
		var namesOut []string
		switch v.Type {
		case "var", "exported":
			var value string
			_ = json.Unmarshal(v.Value, &value)
			namesOut = strings.Fields(value)
		case "array":
			_ = json.Unmarshal(v.Value, &namesOut)
		default:
			return nil, errors.New("unsupported outputs type")
		}
		for _, name := range namesOut {
			out, ok := e.Variables[name]
			if !ok || (out.Type != "var" && out.Type != "exported") {
				return nil, fmt.Errorf("missing output variable %s", name)
			}
			var from string
			_ = json.Unmarshal(out.Value, &from)
			if err := add(name, from); err != nil {
				return nil, err
			}
		}
	}
	if len(m) == 0 || len(m) > 32 {
		return nil, errors.New("invalid output count")
	}
	return m, nil
}

func replacer(m map[string]string) func(string) string {
	keys := names(m)
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		pairs = append(pairs, key, m[key])
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}
