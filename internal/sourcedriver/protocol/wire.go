package protocol

import (
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// The wire forms of sourcedriver's types: the same fields, named for the
// protocol. Nil and empty slices and maps keep their difference, so a value
// crosses the connection unchanged: LosingCommits' nil removes every head,
// and an empty list none.

type noParams struct{}

type wireDescription struct {
	Backend  string   `json:"backend"`
	Version  string   `json:"version"`
	Commands []string `json:"commands"`
}

type wireName struct {
	Kind  sourcedriver.NameKind `json:"kind"`
	Value string                `json:"value"`
}

type wireRepository struct {
	Project     string `json:"project"`
	Exists      bool   `json:"exists"`
	Incarnation string `json:"incarnation"`
}

type wireHead struct {
	Name string           `json:"name"`
	Tip  sourcedriver.OID `json:"tip"`
}

type wireCommit struct {
	Present   bool `json:"present"`
	Reachable bool `json:"reachable"`
}

type wireLoss struct {
	Heads   []wireHead         `json:"heads"`
	Commits []sourcedriver.OID `json:"commits"`
}

type wireExport struct {
	Bundle string           `json:"bundle"`
	Ref    string           `json:"ref"`
	Commit sourcedriver.OID `json:"commit"`
}

type wireRename struct {
	ID   string            `json:"id"`
	Old  string            `json:"old"`
	New  string            `json:"new"`
	Goal sourcedriver.Goal `json:"goal"`
	Plan string            `json:"plan"`
	User int               `json:"user"`
}

type wireFile struct {
	Type sourcedriver.FileType `json:"type"`
	UID  int                   `json:"uid"`
	GID  int                   `json:"gid"`
	Mode uint32                `json:"mode"`
	Data []byte                `json:"data"`
}

type wireWorkspaceFile struct {
	Path string   `json:"path"`
	File wireFile `json:"file"`
}

type wireEdits struct {
	Read  map[string]int64    `json:"read"`
	Edits []wireWorkspaceFile `json:"edits"`
	Plan  string              `json:"plan"`
}

type wireService struct {
	ID       string                   `json:"id"`
	Project  string                   `json:"project"`
	Kind     sourcedriver.ServiceKind `json:"kind"`
	Branch   string                   `json:"branch"`
	MaxInput int64                    `json:"max_input"`
	Deadline time.Duration            `json:"deadline"` // nanoseconds
}

type wireOriginRef struct {
	Ref    string           `json:"ref"`
	OID    sourcedriver.OID `json:"oid"`
	Target sourcedriver.OID `json:"target"`
}

type wirePublication struct {
	URL         string                `json:"url"`
	Branch      string                `json:"branch"`
	Commit      sourcedriver.OID      `json:"commit"`
	Destination string                `json:"destination"`
	Relation    sourcedriver.Relation `json:"relation"`
}

type wireEffect struct {
	Kind    sourcedriver.EffectKind `json:"kind"`
	Project string                  `json:"project"`
	Branch  string                  `json:"branch"`
	Old     sourcedriver.OID        `json:"old"`
	New     sourcedriver.OID        `json:"new"`
	Service string                  `json:"service"`
}

// The parameters and results of the methods.

type checkNamesParams struct {
	Names []wireName `json:"names"`
}

type projectParams struct {
	Project string `json:"project"`
}

type deleteRepositoryParams struct {
	Repository wireRepository `json:"repository"`
	Gate       bool           `json:"gate"`
}

type inspectBranchParams struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

type inspectBranchResult struct {
	Tip   sourcedriver.OID `json:"tip"`
	Found bool             `json:"found"`
}

type createBranchParams struct {
	Project string           `json:"project"`
	Name    string           `json:"name"`
	At      sourcedriver.OID `json:"at"`
	Gate    bool             `json:"gate"`
}

type deleteBranchParams struct {
	Project string           `json:"project"`
	Name    string           `json:"name"`
	Tip     sourcedriver.OID `json:"tip"`
	Gate    bool             `json:"gate"`
}

type inspectCommitParams struct {
	Project string           `json:"project"`
	ID      sourcedriver.OID `json:"id"`
}

type losingCommitsParams struct {
	Project string   `json:"project"`
	Removed []string `json:"removed"`
}

type exportParams struct {
	Project string           `json:"project"`
	ID      sourcedriver.OID `json:"id"`
	Dir     string           `json:"dir"`
}

type renameWorkspaceParams struct {
	Rename wireRename          `json:"rename"`
	Files  []wireWorkspaceFile `json:"files"`
}

type serveGitParams struct {
	Command string `json:"command"`
}

type openParams struct {
	Kind    sourcedriver.ServiceKind `json:"kind"`
	Project string                   `json:"project"`
}

type approveParams struct {
	Branch string           `json:"branch"`
	Old    sourcedriver.OID `json:"old"`
	New    sourcedriver.OID `json:"new"`
}

type observeOriginParams struct {
	URL string `json:"url"`
}

type fetchOriginParams struct {
	Project string        `json:"project"`
	URL     string        `json:"url"`
	Ref     wireOriginRef `json:"ref"`
}

type oidResult struct {
	OID sourcedriver.OID `json:"oid"`
}

type comparePublicationParams struct {
	Project string           `json:"project"`
	URL     string           `json:"url"`
	Source  sourcedriver.OID `json:"source"`
	Dest    *wireOriginRef   `json:"dest"`
}

type relationResult struct {
	Relation sourcedriver.Relation `json:"relation"`
}

type publishParams struct {
	Project     string          `json:"project"`
	Publication wirePublication `json:"publication"`
	Gate        bool            `json:"gate"`
}

type publishResult struct {
	Status sourcedriver.PublishStatus `json:"status"`
}

type settleParams struct {
	Effect wireEffect `json:"effect"`
}

type settleResult struct {
	Settlement string `json:"settlement"`
}

// each maps a slice, keeping nil.
func each[T, U any](in []T, f func(T) U) []U {
	if in == nil {
		return nil
	}
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func toNames(names []sourcedriver.Name) []wireName {
	return each(names, func(n sourcedriver.Name) wireName { return wireName(n) })
}

func fromNames(names []wireName) []sourcedriver.Name {
	return each(names, func(n wireName) sourcedriver.Name { return sourcedriver.Name(n) })
}

func toHeads(heads []sourcedriver.Head) []wireHead {
	return each(heads, func(h sourcedriver.Head) wireHead { return wireHead(h) })
}

func fromHeads(heads []wireHead) []sourcedriver.Head {
	return each(heads, func(h wireHead) sourcedriver.Head { return sourcedriver.Head(h) })
}

func toWorkspaceFiles(files []sourcedriver.WorkspaceFile) []wireWorkspaceFile {
	return each(files, func(f sourcedriver.WorkspaceFile) wireWorkspaceFile {
		return wireWorkspaceFile{Path: f.Path, File: wireFile(f.File)}
	})
}

func fromWorkspaceFiles(files []wireWorkspaceFile) []sourcedriver.WorkspaceFile {
	return each(files, func(f wireWorkspaceFile) sourcedriver.WorkspaceFile {
		return sourcedriver.WorkspaceFile{Path: f.Path, File: sourcedriver.File(f.File)}
	})
}

func toOriginRefs(refs []sourcedriver.OriginRef) []wireOriginRef {
	return each(refs, func(r sourcedriver.OriginRef) wireOriginRef { return wireOriginRef(r) })
}

func fromOriginRefs(refs []wireOriginRef) []sourcedriver.OriginRef {
	return each(refs, func(r wireOriginRef) sourcedriver.OriginRef { return sourcedriver.OriginRef(r) })
}

func toEdits(e sourcedriver.WorkspaceEdits) wireEdits {
	return wireEdits{Read: e.Read, Edits: toWorkspaceFiles(e.Edits), Plan: e.Plan}
}

func (e wireEdits) edits() sourcedriver.WorkspaceEdits {
	return sourcedriver.WorkspaceEdits{Read: e.Read, Edits: fromWorkspaceFiles(e.Edits), Plan: e.Plan}
}

func toLoss(l sourcedriver.Loss) wireLoss {
	return wireLoss{Heads: toHeads(l.Heads), Commits: l.Commits}
}

func (l wireLoss) loss() sourcedriver.Loss {
	return sourcedriver.Loss{Heads: fromHeads(l.Heads), Commits: l.Commits}
}

// The names of the settlements on the wire, which are their strings.
var settlements = map[string]sourcedriver.Settlement{
	sourcedriver.Unknown.String():    sourcedriver.Unknown,
	sourcedriver.Applied.String():    sourcedriver.Applied,
	sourcedriver.NotApplied.String(): sourcedriver.NotApplied,
}

func (r settleResult) settlement() (sourcedriver.Settlement, error) {
	s, ok := settlements[r.Settlement]
	if !ok {
		return sourcedriver.Unknown, fmt.Errorf("%s: the driver answered settlement %q", methodSettle, r.Settlement)
	}
	return s, nil
}

func toSettlement(s sourcedriver.Settlement) (settleResult, error) {
	if settlements[s.String()] != s {
		return settleResult{}, fmt.Errorf("the driver settled with %d, which is no settlement", s)
	}
	return settleResult{Settlement: s.String()}, nil
}
