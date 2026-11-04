// Package gitnames is Git's syntax for the names P stores: branch and ref
// names, origin URLs and commit IDs, as P's drivers whose backend is Git
// accept them. Every rule decides from the value only, offline.
package gitnames

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Branch reports whether name is a P branch: refs/heads/<name> with P's
// narrow alphabet.
func Branch(name string) bool {
	if len(name) == 0 || len(name) > 200 || strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// RenameBranch reports whether name is a branch a rename may create or a
// retained-branch change may name: a P branch of at most 100 bytes that
// `git check-ref-format --branch` accepts. Of the names Branch accepts, that
// command refuses only a leading '-' and HEAD; a driver with the git
// executable may run the command as well.
func RenameBranch(name string) bool {
	return Branch(name) && len(name) <= 100 && !strings.HasPrefix(name, "-") && name != "HEAD"
}

// OriginRef reports whether ref is a full head or tag ref an origin may
// advertise: Git's ref syntax, without P's narrower branch alphabet.
func OriginRef(ref string) bool {
	if !utf8.ValidString(ref) {
		return false
	}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		name := ref[len(prefix):]
		if len(name) == 0 || len(name) > 512 || strings.Contains(name, "..") || strings.Contains(name, "@{") || name[len(name)-1] == '.' {
			return false
		}
		for _, part := range strings.Split(name, "/") {
			if len(part) == 0 || part[0] == '.' || strings.HasSuffix(part, ".lock") {
				return false
			}
			for i := 0; i < len(part); i++ {
				c := part[i]
				if c <= 0x20 || c == 0x7f || strings.IndexByte("~^:?*[\\", c) >= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

// OID reports whether s is a nonzero SHA-1 or SHA-256 object ID in lowercase
// hex.
func OID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return strings.Trim(s, "0") != ""
}

// OriginURL reports whether raw is one of the SSH URL forms P reaches an
// origin by: ssh://[user@]host[:port]/path or user@host:path. The allowlist
// keeps out URL helpers, URL options, SSH command injection and local paths;
// host aliases and ProxyCommand still come from the daemon account's trusted
// OpenSSH configuration.
func OriginURL(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.IndexFunc(raw, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return false
	}
	if strings.HasPrefix(raw, "ssh://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "ssh" || u.Hostname() == "" || u.Path == "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return false
		}
		if u.User != nil {
			if _, has := u.User.Password(); has || !safeUser(u.User.Username()) {
				return false
			}
		}
		if !safeHost(u.Hostname()) || !safeRemotePath(u.Path) || u.RawPath != "" {
			return false
		}
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				return false
			}
		}
		return true
	}
	if strings.Contains(raw, "://") {
		return false
	}
	userHost, path, ok := strings.Cut(raw, ":")
	if !ok || !safeRemotePath(path) || strings.Count(userHost, "@") != 1 {
		return false
	}
	user, host, _ := strings.Cut(userHost, "@")
	return safeUser(user) && safeHost(host)
}

func safeUser(s string) bool { return safeAtom(s, "._-") && s[0] != '-' }
func safeHost(s string) bool {
	return safeAtom(s, "._-:") && s[0] != '-' && !strings.Contains(s, "..")
}
func safeRemotePath(s string) bool {
	if s == "" || s[0] == '-' || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !asciiAlphaNum(r) && !strings.ContainsRune("/_-.~", r) {
			return false
		}
	}
	return true
}
func safeAtom(s, extra string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !asciiAlphaNum(r) && !strings.ContainsRune(extra, r) {
			return false
		}
	}
	return true
}
func asciiAlphaNum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
