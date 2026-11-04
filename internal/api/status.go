package api

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// StatusReport is an agent adapter's status.report notification on the
// private session socket.
type StatusReport struct {
	V              int    `json:"v"`
	Source         string `json:"source"`
	Condition      string `json:"condition"`
	Reason         string `json:"reason"`
	Adapter        string `json:"adapter"`
	AdapterVersion string `json:"adapter_version"`
}

func (r StatusReport) Validate() error {
	if r.V != SessionRPCVersion || len(r.Source) > 128 || len(r.Reason) > 256 || len(r.Adapter) == 0 || len(r.Adapter) > 128 || len(r.AdapterVersion) == 0 || len(r.AdapterVersion) > 64 {
		return invalid("")
	}
	switch r.Condition {
	case "running", "attention", "idle", "failed", "unknown":
	default:
		return invalid("condition")
	}
	for _, s := range []string{r.Source, r.Reason, r.Adapter, r.AdapterVersion} {
		if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return invalid("")
		}
	}
	return nil
}
