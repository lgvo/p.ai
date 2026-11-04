package gitclone

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

var safeGitToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// closedLayoutName reports whether name is a branch whose ref and reflog
// paths rename handles: a plain path of at most 100 bytes under
// refs/heads.
func closedLayoutName(name string) bool {
	if len(name) == 0 || len(name) > 100 || !safeGitToken.MatchString(name) ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

// renameConfig is raw, the clone's config, with branch old's section and
// upstream moved to next. It refuses an upstream of old other than origin's
// old as a mismatch, and every config safeGitConfig refuses.
func renameConfig(raw []byte, old, next string) ([]byte, error) {
	safe, err := safeGitConfig(raw)
	if err != nil {
		var upstream *gitUpstreamConfigError
		if errors.As(err, &upstream) && upstream.branch == old {
			return nil, &sourcedriver.WorkspaceMismatch{Field: "upstream", Expected: "origin:refs/heads/" + old, Actual: "<incomplete>"}
		}
		return nil, err
	}
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("rename Git config terminator unavailable")
	}
	oldHeader := "[branch \"" + old + "\"]"
	newHeader := "[branch \"" + next + "\"]"
	// Read only the sanitized configuration. Its parser validates names and
	// omits every URL, user identity, and executable setting.
	inside, remote, merge := false, "", ""
	for _, line := range strings.Split(string(safe), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inside = trimmed == oldHeader
		} else if inside {
			key, value, _ := strings.Cut(trimmed, "=")
			switch strings.TrimSpace(key) {
			case "remote":
				remote = strings.TrimSpace(value)
			case "merge":
				merge = strings.TrimSpace(value)
			}
		}
	}
	actual := "<unset>"
	if remote != "" && merge != "" {
		actual = remote + ":" + merge
	}
	if remote != "origin" || merge != "refs/heads/"+old {
		return nil, &sourcedriver.WorkspaceMismatch{Field: "upstream", Expected: "origin:refs/heads/" + old, Actual: actual}
	}
	seenOld := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == newHeader {
			return nil, errors.New("destination Git branch config occupied")
		}
		if trimmed == oldHeader {
			if seenOld {
				return nil, errors.New("source Git branch config ambiguous")
			}
			seenOld = true
		}
	}
	if !seenOld {
		return bytes.Clone(raw), nil
	}
	// safeGitConfig has already rejected includes, continuations, and unknown
	// executable controls. Preserve every other byte of the user's config.
	var out strings.Builder
	insideOld := false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			insideOld = trimmed == oldHeader
		}
		if trimmed == oldHeader {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out.WriteString(indent + newHeader + "\n")
		} else if insideOld && strings.HasPrefix(strings.ToLower(trimmed), "merge") {
			key, value, ok := strings.Cut(trimmed, "=")
			if !ok || strings.TrimSpace(strings.ToLower(key)) != "merge" || strings.TrimSpace(value) != "refs/heads/"+old {
				return nil, errors.New("source Git upstream config unsupported")
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out.WriteString(indent + "merge = refs/heads/" + next + "\n")
		} else {
			out.WriteString(line)
		}
	}
	return []byte(out.String()), nil
}

type gitUpstreamConfigError struct{ branch, reason string }

func (e *gitUpstreamConfigError) Error() string { return e.reason }

// safeGitConfig accepts only inert facts needed to interpret an ordinary
// standalone worktree, and returns them without URLs or user identity.
// Includes, extensions, filters, monitors, external diff and credential
// helpers are unsupported regardless of where they appear.
func safeGitConfig(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxConfig || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
		return nil, errors.New("Git config size or encoding unavailable")
	}
	section, subject := "", ""
	seenCore := map[string]bool{}
	coreValues := map[string]string{}
	branches := map[string]map[string]string{}
	remoteFetch := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, errors.New("Git config section malformed")
			}
			content := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			section, subject = "", ""
			if content == "core" || content == "user" {
				section = content
				continue
			}
			for _, prefix := range []string{"branch", "remote"} {
				if strings.HasPrefix(content, prefix+" \"") && strings.HasSuffix(content, "\"") {
					name := strings.TrimSuffix(strings.TrimPrefix(content, prefix+" \""), "\"")
					if !safeGitToken.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, ".") {
						return nil, errors.New("Git config subsection unavailable")
					}
					section, subject = prefix, name
					break
				}
			}
			if section == "" {
				return nil, errors.New("Git config section unsupported")
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return nil, errors.New("Git config entry malformed")
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if strings.ContainsAny(value, "\x00\n\r\\\"") || len(value) > 1024 {
			return nil, errors.New("Git config value unsupported")
		}
		switch section {
		case "core":
			if seenCore[key] {
				return nil, errors.New("Git core config repeated")
			}
			seenCore[key] = true
			switch key {
			case "repositoryformatversion":
				if value != "0" {
					return nil, errors.New("Git repository format unsupported")
				}
			case "bare":
				if value != "false" {
					return nil, errors.New("bare Git repository unavailable")
				}
			case "filemode", "logallrefupdates", "ignorecase", "precomposeunicode":
				if value != "true" && value != "false" {
					return nil, errors.New("Git core boolean unsupported")
				}
			default:
				return nil, errors.New("Git core behavior unsupported")
			}
			coreValues[key] = value
		case "user":
			if key != "name" && key != "email" {
				return nil, errors.New("Git user setting unsupported")
			}
			// Identity is intentionally omitted.
		case "remote":
			if key != "url" && key != "fetch" {
				return nil, errors.New("Git remote setting unsupported")
			}
			if key == "fetch" {
				want := "+refs/heads/*:refs/remotes/" + subject + "/*"
				if value != want || remoteFetch[subject] != "" {
					return nil, errors.New("Git remote refspec unsupported")
				}
				remoteFetch[subject] = value
			}
			// URLs are deliberately omitted.
		case "branch":
			if key != "remote" && key != "merge" {
				return nil, errors.New("Git branch setting unsupported")
			}
			if branches[subject] == nil {
				branches[subject] = map[string]string{}
			}
			if branches[subject][key] != "" {
				return nil, errors.New("Git branch setting repeated")
			}
			if key == "remote" && (!safeGitToken.MatchString(value) || strings.Contains(value, "..")) {
				return nil, errors.New("Git branch remote unsupported")
			}
			if key == "merge" && (!strings.HasPrefix(value, "refs/heads/") || !safeGitToken.MatchString(strings.TrimPrefix(value, "refs/heads/"))) {
				return nil, errors.New("Git branch merge ref unsupported")
			}
			branches[subject][key] = value
		}
	}
	if !seenCore["repositoryformatversion"] || !seenCore["bare"] {
		return nil, errors.New("Git core identity incomplete")
	}
	var out strings.Builder
	out.WriteString("[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
	for _, key := range []string{"filemode", "ignorecase", "precomposeunicode"} {
		if value := coreValues[key]; value != "" {
			fmt.Fprintf(&out, "\t%s = %s\n", key, value)
		}
	}
	out.WriteString("\tlogallrefupdates = false\n")
	remotes := make([]string, 0, len(remoteFetch))
	for name := range remoteFetch {
		remotes = append(remotes, name)
	}
	sort.Strings(remotes)
	for _, name := range remotes {
		fmt.Fprintf(&out, "[remote %q]\n\tfetch = %s\n", name, remoteFetch[name])
	}
	keys := make([]string, 0, len(branches))
	for key := range branches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		values := branches[name]
		if values["remote"] == "" || values["merge"] == "" {
			return nil, &gitUpstreamConfigError{branch: name, reason: "Git upstream setting incomplete"}
		}
		if remoteFetch[values["remote"]] == "" {
			return nil, &gitUpstreamConfigError{branch: name, reason: "Git upstream fetch mapping unavailable"}
		}
		fmt.Fprintf(&out, "[branch %q]\n\tremote = %s\n\tmerge = %s\n", name, values["remote"], values["merge"])
	}
	return []byte(out.String()), nil
}
