package api

import (
	"strings"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// The checks below are the bounds every request type applies in Validate.
// Control applies the same checks to the records it stores.

// The bounds of the values whose syntax the source driver decides: branch
// and ref names, creation sources, commit IDs and origin URLs. Core only
// stores and compares them; a handler has the driver check the rest
// (CheckNames) after its replay lookup.
const (
	MaxBranchBytes = 200  // a P branch's name
	MaxRefBytes    = 523  // a full ref: an origin's, or a page cursor of refs
	MaxSourceBytes = 255  // a creation source
	MaxCommitBytes = 64   // a commit ID
	MaxURLBytes    = 2048 // an origin's URL
)

// BoundedName reports whether value is within core's bounds for a name of
// at most max bytes: valid UTF-8 without ASCII control characters, 1 to max
// bytes long. Whether it is a valid name is the source driver's to say.
func BoundedName(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) &&
		strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// ValidProject reports whether path is a P project path: up to 255 bytes of
// slash-separated names of letters, digits, '-', '_' and '.'.
func ValidProject(path string) bool {
	if len(path) == 0 || len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, c := range strings.Split(path, "/") {
		if c == "" || c == "." || c == ".." || len(c) > 100 {
			return false
		}
		for _, r := range c {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// ValidUUID reports whether value is a UUID in lowercase canonical form.
func ValidUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidFingerprint reports whether s is a SHA-256 digest in lowercase hex.
func ValidFingerprint(s string) bool { return len(s) == 64 && lowerHex(s) }

func lowerHex(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validURL accepts an empty URL, where a request may omit one, or one
// within core's bounds.
func validURL(url string) bool { return url == "" || BoundedName(url, MaxURLBytes) }

// validKey accepts an idempotency key of 1 to 128 bytes.
func validKey(key string) bool { return len(key) >= 1 && len(key) <= 128 }

// validRefCursor accepts an empty cursor or one within a ref's bounds: a
// cursor is compared, never recorded.
func validRefCursor(after string) bool { return after == "" || BoundedName(after, MaxRefBytes) }

// invalid is the error a request's Validate returns for field, or for no
// single field when field is empty.
func invalid(field string) error { return InvalidParam(field) }

// validPage checks a list request's limit, from 1 to max, and cursor.
func validPage(limit, max int, after string) error {
	switch {
	case limit < 1 || limit > max:
		return invalid("limit")
	case len(after) > 128:
		return invalid("after")
	}
	return nil
}

// validBranchPage checks a project's branch list request.
func validBranchPage(project, after string, limit int) error {
	switch {
	case !ValidProject(project):
		return invalid("project")
	case limit < 1 || limit > 8:
		return invalid("limit")
	case !validRefCursor(after):
		return invalid("after")
	}
	return nil
}

// ValidBranch and ValidOID are the client's parsing of P's default names:
// the CLI parses the branch names and commit IDs of its arguments by the
// bundled Git driver's rules. Core never uses them; the source driver checks
// names (CheckNames).

// ValidBranch reports whether branch is a P branch name by the bundled
// driver's rule.
func ValidBranch(branch string) bool { return gitnames.Branch(branch) }

// ValidOID reports whether s is a commit ID by the bundled driver's rule: a
// nonzero SHA-1 or SHA-256 object ID in lowercase hex.
func ValidOID(s string) bool { return gitnames.OID(s) }
