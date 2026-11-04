package control

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/api"
)

// ProjectDeleteRequest is a project deletion's run as its operation records
// it: the project, the run's idempotency key and its change tag.
type ProjectDeleteRequest struct {
	Key     string `json:"key"`
	Project string `json:"project"`
	IfMatch string `json:"if_match"`
}

// CommitImageClaim is one reviewed commit image of a project deletion: its
// index row, whether the image was present and its observed size, and a digest
// of the sessions started from its commit. It is persisted before any image is
// deleted and never rewritten on retry.
type CommitImageClaim struct {
	Image         CommitImage `json:"image"`
	ImagePresent  bool        `json:"image_present"`
	ObservedSize  int64       `json:"observed_size"`
	RelatedDigest string      `json:"related_digest"`
}

// CommitImageClaimViews returns claims as the project deletion plan shows
// them, each image's labels in name order.
func CommitImageClaimViews(claims []CommitImageClaim) []api.CommitImageClaim {
	views := make([]api.CommitImageClaim, 0, len(claims))
	for _, c := range claims {
		img := c.Image
		labels := make([]api.ImageLabel, 0, len(img.Labels))
		for _, name := range slices.Sorted(maps.Keys(img.Labels)) {
			labels = append(labels, api.ImageLabel{Name: name, Value: img.Labels[name]})
		}
		views = append(views, api.CommitImageClaim{
			Image: api.CommitImage{Key: api.CommitImageKey(img.Key), Ref: img.Ref, Build: img.Build, Labels: labels,
				Selection: img.Selection, MaterialSHA256: img.MaterialSHA256, Size: img.Size, State: img.State},
			ImagePresent: c.ImagePresent, ObservedSize: c.ObservedSize, RelatedDigest: c.RelatedDigest})
	}
	return views
}

// ProjectDeleteResource is one resource a project deletion removes, and its
// progress. StopIssued and DeleteIssued record a native stop or delete as
// issued right before it starts. A delete is never issued again; a stop,
// which is idempotent, is sent again while the runtime still runs.
type ProjectDeleteResource struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Status       string `json:"status"` // remaining, unreachable, deleted, already_absent
	StopIssued   bool   `json:"stop_issued,omitempty"`
	DeleteIssued bool   `json:"delete_issued,omitempty"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

// Only exact known resource identities and a review digest survive acceptance;
// loss snapshots are historical display evidence, not a rollback workflow.
type ProjectDeleteEvidence struct {
	InstanceUUID    string                     `json:"instance_uuid"`
	BaseFingerprint string                     `json:"base_fingerprint"`
	StateSHA256     string                     `json:"state_sha256"`
	ReviewSHA256    string                     `json:"review_sha256"`
	Sessions        []api.ProjectDeleteSession `json:"sessions"`
	Images          []CommitImageClaim         `json:"images"`
	// RepositoryIncarnation is the incarnation of the project's repository
	// the deletion reviewed; only that one is deleted.
	RepositoryIncarnation string                  `json:"repository_incarnation"`
	Resources             []ProjectDeleteResource `json:"resources"`
}

// ProjectDeleteAPI deletes projects, a risky change: a dry run plans it, and
// a run with the plan's change tag records its operation.
type ProjectDeleteAPI interface {
	DeleteProject(context.Context, string, api.ChangeRequest) (api.ChangeResult[api.ProjectDeleteDetails], error)
}

// maxProjectDeleteReviewBytes bounds the encoded evidence a project deletion
// is confirmed with; maxProjectDeleteEvidenceBytes bounds it later, when each
// of its at most 29 resources may carry a diagnostic of up to
// api.MaxDiagnosticBytes, also once encoded (ResourceDiagnostic).
// operation.inspect of the largest still fits a response frame.
const (
	maxProjectDeleteReviewBytes   = 24 << 10
	maxProjectDeleteEvidenceBytes = 56 << 10
)

// ProjectDeleteReviewFits reports whether a deletion can run with ev: its
// encoding is within maxProjectDeleteReviewBytes, which leaves room for a
// diagnostic on every resource. A plan that does not fit is refused before
// its change tag is issued.
func ProjectDeleteReviewFits(ev ProjectDeleteEvidence) bool {
	raw, err := json.Marshal(ev)
	return err == nil && len(raw) <= maxProjectDeleteReviewBytes
}

// ResourceDiagnostic is d bounded for a resource in project deletion
// evidence: at most api.MaxDiagnosticBytes, and at most that many once JSON
// encodes it, which turns some characters into six bytes, so the evidence
// stays within its bound whatever the detail holds.
func ResourceDiagnostic(d string) string {
	d = api.BoundDiagnostic(d)
	encoded := 0
	for i, r := range d {
		raw, _ := json.Marshal(string(r))
		if encoded += len(raw) - 2; encoded > api.MaxDiagnosticBytes {
			return d[:i]
		}
	}
	return d
}

// ValidRepositoryIncarnation reports whether s can be a repository's
// incarnation as P records it: opaque to P, so only bounded, as SQLite and
// the logs need.
func ValidRepositoryIncarnation(s string) bool {
	return s != "" && len(s) <= maxRepositoryIncarnation && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// maxRepositoryIncarnation bounds a repository's incarnation.
const maxRepositoryIncarnation = 128

func projectDeleteResourceAbsent(r ProjectDeleteResource) bool {
	return r.Status == "deleted" || r.Status == "already_absent"
}
func validProjectDeleteEvidence(ev ProjectDeleteEvidence) bool {
	if !validUUID(ev.InstanceUUID) || !validFingerprint(ev.BaseFingerprint) || !validFingerprint(ev.StateSHA256) || !validFingerprint(ev.ReviewSHA256) || len(ev.Sessions) > 4 || len(ev.Images) > MaxProjectDeleteImages || len(ev.Resources) != 2*len(ev.Sessions)+len(ev.Images)+1 ||
		!ValidRepositoryIncarnation(ev.RepositoryIncarnation) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range ev.Sessions {
		if !validUUID(s.Session.UUID) || s.Session.Registry != "established" || !validProject(s.Session.Project) || !validBranch(s.Session.Branch) || !validFingerprint(s.Session.PolicySHA256) || !s.Local.Valid() || s.Local.OldUUID != s.Session.UUID || seen[s.Session.UUID] {
			return false
		}
		seen[s.Session.UUID] = true
		r := s.Runtime
		if r.Condition == "present" {
			if !validIncarnation(r.Incarnation) || !validFingerprint(r.ImageFingerprint) || r.OriginalStatus != "Running" && r.OriginalStatus != "Stopped" {
				return false
			}
		} else if r.Condition != "missing" {
			return false
		}
	}
	for _, c := range ev.Images {
		if !c.Image.valid() || c.Image.Ref == ev.BaseFingerprint {
			return false
		}
	}
	seen = map[string]bool{}
	for _, r := range ev.Resources {
		key := r.Kind + ":" + r.ID
		if seen[key] || r.ID == "" || ResourceDiagnostic(r.Diagnostic) != r.Diagnostic || r.Status != "remaining" && r.Status != "unreachable" && !projectDeleteResourceAbsent(r) {
			return false
		}
		seen[key] = true
	}
	for _, s := range ev.Sessions {
		if !seen["runtime:"+s.Session.UUID] || !seen["local:"+s.Session.UUID] {
			return false
		}
	}
	for _, c := range ev.Images {
		if !seen["image:"+c.Image.Ref] {
			return false
		}
	}
	return seen["repository:bare"]
}
func projectDeleteIdentities(ev ProjectDeleteEvidence) string {
	copy := ev
	copy.Resources = nil
	raw, _ := json.Marshal(copy)
	return digest(raw)
}
