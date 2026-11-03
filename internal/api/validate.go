package api

import (
	"strings"

	"github.com/lgvo/p.ai/internal/plugin"
)

// The checks below are the bounds every request type applies in Validate.
// Control applies the same checks to the records it stores.

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

// ValidBranch reports whether branch is an ordinary P branch name.
func ValidBranch(branch string) bool {
	if len(branch) == 0 || len(branch) > 200 || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, c := range strings.Split(branch, "/") {
		if c == "" || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".") || strings.HasSuffix(c, ".lock") {
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

// ValidOID reports whether s is a nonzero SHA-1 or SHA-256 object ID in
// lowercase hex.
func ValidOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return lowerHex(s) && strings.Trim(s, "0") != ""
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

// ValidToken reports whether value is a confirmation token: 32 lowercase hex
// digits.
func ValidToken(value string) bool { return len(value) == 32 && lowerHex(value) }

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

// validKey accepts an idempotency key of 1 to 128 bytes.
func validKey(key string) bool { return len(key) >= 1 && len(key) <= 128 }

// validBranchCursor accepts an empty cursor or the full ref of a P branch.
func validBranchCursor(after string) bool {
	return after == "" || strings.HasPrefix(after, "refs/heads/") && ValidBranch(strings.TrimPrefix(after, "refs/heads/"))
}

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
	case !validBranchCursor(after):
		return invalid("after")
	}
	return nil
}

// validConfirmation checks a confirmation's key and token.
func validConfirmation(key, token string) error {
	switch {
	case !validKey(key):
		return invalid("key")
	case !ValidToken(token):
		return invalid("confirmation_token")
	}
	return nil
}

// validOriginRef is the external origin's branch or tag syntax.
func validOriginRef(ref string) bool { return plugin.ValidGitOriginRef(ref) }
