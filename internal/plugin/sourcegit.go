package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const GitCommandSchema = "p.command/v1"
const GitResultSchema = "p.command-result/v1"
const GitBrokerSchema = "p.broker/v1"

type GitRef struct {
	Ref string `json:"ref"`
	OID string `json:"oid"`
}

// GitOriginRef is one advertised branch or tag. CommitOID is the advertised
// peeled target for an annotated tag, or OID otherwise. Fetch validates type.
type GitOriginRef struct {
	Ref       string `json:"ref"`
	OID       string `json:"oid"`
	CommitOID string `json:"commit_oid"`
}

// GitOriginBroker is implemented only by an origin-scoped host broker. A
// package cannot supply a URL, repository path, expected OID, or Git argv.
type GitOriginBroker interface {
	ObserveOrigin(context.Context) ([]GitOriginRef, error)
	FetchOrigin(context.Context) (string, error)
}

type GitOriginPublisher interface {
	PublishOrigin(context.Context) (string, error)
}

// GitSourceSelector is selected by core from a validated create request.
// Branch values are full ordinary P refs; commit values are full object IDs.
type GitSourceSelector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type GitInspection struct {
	Exists bool   `json:"exists"`
	Head   string `json:"head,omitempty"`
}

type GitTransportPlan struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

// GitBroker is the typed host effect surface of a source-git WASI package.
// Its methods operate only on the invocation's core-selected project.
type GitBroker interface {
	Inspect(context.Context) (GitInspection, error)
	Init(context.Context) error
	SetHead(context.Context) error
	NextRefs(context.Context, int, int) ([]GitRef, bool, error)
	ObserveSource(context.Context) (string, error)
	CreateBranch(context.Context) error
}

// GitBranchDeleter is available only for a core-pinned, expected-old branch
// deletion. The selected package can request this one effect, never supply a
// ref or object ID through the broker request.
type GitBranchDeleter interface{ DeleteBranch(context.Context) error }

type GitCommand struct {
	Schema         string             `json:"schema"`
	Kind           string             `json:"kind"`
	Scope          string             `json:"scope"`
	Project        string             `json:"project"`
	InitialHead    string             `json:"initial_head,omitempty"`
	Limit          int                `json:"limit,omitempty"`
	Service        string             `json:"service,omitempty"`
	Ceilings       *GitTransportPlan  `json:"ceilings,omitempty"`
	Source         *GitSourceSelector `json:"source,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	CommitOID      string             `json:"commit_oid,omitempty"`
	OriginRef      string             `json:"origin_ref,omitempty"`
	SourceRef      string             `json:"source_ref,omitempty"`
	DestinationRef string             `json:"destination_ref,omitempty"`
}

type GitResult struct {
	Schema  string `json:"schema"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type GitBrokerRequest struct {
	Schema        string `json:"schema"`
	Method        string `json:"method"`
	Scope         string `json:"scope"`
	PageSize      *int   `json:"page_size,omitempty"`
	MaxInputBytes *int64 `json:"max_input_bytes,omitempty"`
	MaxDurationMS *int64 `json:"max_duration_ms,omitempty"`
}

type GitBrokerResponse struct {
	Schema       string    `json:"schema"`
	Status       string    `json:"status"`
	Exists       *bool     `json:"exists,omitempty"`
	Head         string    `json:"head,omitempty"`
	Refs         *[]GitRef `json:"refs,omitempty"`
	Exhausted    *bool     `json:"exhausted,omitempty"`
	PageComplete *bool     `json:"page_complete,omitempty"`
	CommitOID    string    `json:"commit_oid,omitempty"`
}

type GitOutcome struct {
	Refs              []GitRef
	Plan              GitTransportPlan
	BrokerCalls       int
	CommitOID         string
	OriginRefs        []GitOriginRef
	PublicationStatus string
}

const GitCoreInputCeiling int64 = 1 << 30
const GitCoreDurationMS int64 = 600000

// RunSourceGit runs a package-authored operation sequence. The returned refs
// and plan come only from broker observations, never from module output.
func RunSourceGit(parent context.Context, selected Active, command GitCommand, broker GitBroker) (GitOutcome, error) {
	var outcome GitOutcome
	originCommand := command.Kind == "git.origin.observe" || command.Kind == "git.origin.fetch" || command.Kind == "git.origin.publish"
	deadline := 2 * time.Second
	slots := wasiSlots
	if originCommand {
		deadline = 35 * time.Second
		slots = originWASISlots
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return outcome, errors.New("source-git execution timed out")
	}
	m := selected.Package.Manifest
	if m.Capability != "source-git" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"git.project"}) || broker == nil {
		return outcome, errors.New("unsupported source-git capability")
	}
	if err := validateConfig(m, selected.Config); err != nil {
		return outcome, err
	}
	if command.Schema != "" || command.Scope != "" || command.Project == "" || len(command.Project) > 255 {
		return outcome, errors.New("invalid source-git command")
	}
	if command.Kind != "git.origin.fetch" && command.OriginRef != "" {
		return outcome, errors.New("unexpected origin ref")
	}
	if command.Kind != "git.origin.publish" && (command.SourceRef != "" || command.DestinationRef != "") {
		return outcome, errors.New("unexpected publication ref")
	}
	switch command.Kind {
	case "git.project.ensure":
		if command.InitialHead != "refs/heads/main" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil || command.Branch != "" || command.CommitOID != "" {
			return outcome, errors.New("invalid ensure request")
		}
	case "git.refs.list":
		if command.Limit < 1 || command.Limit > 8 || command.InitialHead != "" || command.Service != "" || command.Ceilings != nil || command.Source != nil || command.Branch != "" || command.CommitOID != "" {
			return outcome, errors.New("invalid ref list request")
		}
	case "git.transport.plan":
		if (command.Service != "upload" && command.Service != "receive") || command.InitialHead != "" || command.Limit != 0 || command.Ceilings == nil || command.Ceilings.MaxInputBytes <= 0 || command.Ceilings.MaxInputBytes > GitCoreInputCeiling || command.Ceilings.MaxDurationMS <= 0 || command.Ceilings.MaxDurationMS > GitCoreDurationMS || command.Source != nil || command.Branch != "" || command.CommitOID != "" {
			return outcome, errors.New("invalid transport request")
		}
	case "git.source.observe":
		if command.Source == nil || !validGitSource(*command.Source) || command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Branch != "" || command.CommitOID != "" {
			return outcome, errors.New("invalid source observation request")
		}
	case "git.branch.create":
		if !validGitBranch(command.Branch) || !validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID) || command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil {
			return outcome, errors.New("invalid branch creation request")
		}
	case "git.branch.delete":
		if !validGitBranch(command.Branch) || command.CommitOID != "" && (!validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID)) || command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil {
			return outcome, errors.New("invalid branch deletion request")
		}
	case "git.origin.observe":
		if command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil || command.Branch != "" || command.CommitOID != "" || command.OriginRef != "" {
			return outcome, errors.New("invalid origin observation request")
		}
	case "git.origin.fetch":
		if !ValidGitOriginRef(command.OriginRef) || !validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID) || command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil || command.Branch != "" {
			return outcome, errors.New("invalid origin fetch request")
		}
	case "git.origin.publish":
		if !validGitBranchRef(command.SourceRef) || !validGitBranchRef(command.DestinationRef) || !validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID) || command.InitialHead != "" || command.Limit != 0 || command.Service != "" || command.Ceilings != nil || command.Source != nil || command.Branch != "" {
			return outcome, errors.New("invalid origin publication request")
		}
	default:
		return outcome, errors.New("unknown source-git method")
	}
	current, retained, err := packageSnapshot(ctx, selected.Package.Path, map[string]bool{m.Runtime.Entry: true})
	if err != nil || current.SHA256 != selected.Package.SHA256 || !reflect.DeepEqual(current.Manifest, m) {
		return outcome, errors.New("source-git package changed")
	}
	moduleBytes := retained[m.Runtime.Entry]
	if len(moduleBytes) == 0 {
		return outcome, errors.New("source-git module is missing")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return outcome, err
	}
	command.Schema, command.Scope = GitCommandSchema, hex.EncodeToString(nonce[:])
	input, err := json.Marshal(command)
	if err != nil || len(input) > maxCommandBytes {
		return outcome, errors.New("source-git command exceeds limit")
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(256).WithCloseOnContextDone(true))
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return outcome, errors.New("WASI initialization failed")
	}
	var brokerFailure error
	callCount, offset, prepared := 0, 0, false
	inspected, initialized, headSet := false, false, false
	observed, created, deleteAttempted, deleted, observedOID := false, false, false, false, ""
	originObserved, originFetched, originPublished := false, false, false
	publicationStatus := ""
	var originRefs []GitOriginRef
	var accumulated []GitRef
	var exhausted bool
	plan := GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}
	mod := runtime.NewHostModuleBuilder("p_broker_v1")
	mod.NewFunctionBuilder().WithFunc(func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
		callCount++
		if callCtx.Err() != nil || callCount > 16 || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
			brokerFailure = errors.New("source-git broker bounds exceeded")
			return -1
		}
		memory := module.ExportedMemory("memory")
		if memory == nil {
			brokerFailure = errors.New("source-git memory missing")
			return -1
		}
		raw, ok := memory.Read(reqPtr, reqLen)
		if !ok {
			brokerFailure = errors.New("source-git broker pointer invalid")
			return -1
		}
		var req GitBrokerRequest
		if strictJSON(raw, &req) != nil || req.Schema != GitBrokerSchema || req.Scope != command.Scope {
			brokerFailure = errors.New("source-git broker request refused")
			return -1
		}
		reply := GitBrokerResponse{Schema: GitBrokerSchema, Status: "ok"}
		switch req.Method {
		case "git.repo.inspect":
			if command.Kind != "git.project.ensure" || inspected || req.PageSize != nil || req.MaxInputBytes != nil || req.MaxDurationMS != nil {
				brokerFailure = errors.New("inspect refused")
				return -1
			}
			observation, e := broker.Inspect(callCtx)
			if e != nil {
				brokerFailure = e
				return -1
			}
			inspected = true
			reply.Exists, reply.Head = &observation.Exists, observation.Head
		case "git.repo.init":
			if command.Kind != "git.project.ensure" || !inspected || initialized || req.PageSize != nil || req.MaxInputBytes != nil || req.MaxDurationMS != nil {
				brokerFailure = errors.New("init refused")
				return -1
			}
			if e := broker.Init(callCtx); e != nil {
				brokerFailure = e
				return -1
			}
			initialized = true
		case "git.repo.set-head":
			if command.Kind != "git.project.ensure" || !initialized || headSet || req.PageSize != nil || req.MaxInputBytes != nil || req.MaxDurationMS != nil {
				brokerFailure = errors.New("set-head refused")
				return -1
			}
			if e := broker.SetHead(callCtx); e != nil {
				brokerFailure = e
				return -1
			}
			headSet = true
		case "git.refs.next":
			if command.Kind != "git.refs.list" || exhausted || offset >= command.Limit || req.PageSize == nil || *req.PageSize < 1 || *req.PageSize > 8 || req.MaxInputBytes != nil || req.MaxDurationMS != nil {
				brokerFailure = errors.New("ref page refused")
				return -1
			}
			size := *req.PageSize
			if size > command.Limit-offset {
				size = command.Limit - offset
			}
			refs, done, e := broker.NextRefs(callCtx, offset, size)
			if e != nil || len(refs) > size {
				brokerFailure = errors.New("ref page failed")
				return -1
			}
			accumulated = append(accumulated, refs...)
			offset += len(refs)
			exhausted = done || len(refs) == 0
			pageComplete := offset >= command.Limit || exhausted
			reply.Refs, reply.Exhausted, reply.PageComplete = &refs, &exhausted, &pageComplete
		case "git.transport.prepare":
			if command.Kind != "git.transport.plan" || prepared || req.PageSize != nil || req.MaxInputBytes != nil && (*req.MaxInputBytes <= 0 || *req.MaxInputBytes > command.Ceilings.MaxInputBytes) || req.MaxDurationMS != nil && (*req.MaxDurationMS <= 0 || *req.MaxDurationMS > command.Ceilings.MaxDurationMS) {
				brokerFailure = errors.New("transport plan refused")
				return -1
			}
			if req.MaxInputBytes != nil {
				plan.MaxInputBytes = *req.MaxInputBytes
			}
			if req.MaxDurationMS != nil {
				plan.MaxDurationMS = *req.MaxDurationMS
			}
			prepared = true
		case "git.source.observe":
			if command.Kind != "git.source.observe" || observed || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("source observation refused")
				return -1
			}
			oid, e := broker.ObserveSource(callCtx)
			if e != nil {
				brokerFailure = e
				return -1
			}
			if !validGitOID(oid) {
				brokerFailure = errors.New("source observation failed")
				return -1
			}
			observed, observedOID, reply.CommitOID = true, oid, oid
		case "git.branch.create":
			if command.Kind != "git.branch.create" || created || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("branch creation refused")
				return -1
			}
			if e := broker.CreateBranch(callCtx); e != nil {
				brokerFailure = e
				return -1
			}
			created = true
		case "git.branch.delete":
			deletion, ok := broker.(GitBranchDeleter)
			if command.Kind != "git.branch.delete" || deleteAttempted || !ok || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("branch deletion refused")
				return -1
			}
			deleteAttempted = true // never reissue after an uncertain first effect
			if e := deletion.DeleteBranch(callCtx); e != nil {
				brokerFailure = e
				return -1
			}
			deleted = true
		case "git.origin.observe":
			origin, ok := broker.(GitOriginBroker)
			if command.Kind != "git.origin.observe" || originObserved || !ok || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("origin observation refused")
				return -1
			}
			refs, e := origin.ObserveOrigin(callCtx)
			if e != nil || len(refs) > 1024 {
				brokerFailure = errors.New("origin observation failed")
				return -1
			}
			originRefs, originObserved = refs, true
		case "git.origin.fetch":
			origin, ok := broker.(GitOriginBroker)
			if command.Kind != "git.origin.fetch" || originFetched || !ok || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("origin fetch refused")
				return -1
			}
			oid, e := origin.FetchOrigin(callCtx)
			if e != nil || oid != command.CommitOID {
				brokerFailure = errors.New("origin fetch failed or changed")
				return -1
			}
			observedOID, originFetched = oid, true
		case "git.origin.publish":
			origin, ok := broker.(GitOriginPublisher)
			if command.Kind != "git.origin.publish" || originPublished || !ok || !plainGitBrokerRequest(req) {
				brokerFailure = errors.New("origin publication refused")
				return -1
			}
			status, e := origin.PublishOrigin(callCtx)
			if e != nil || !validPublicationStatus(status) {
				brokerFailure = errors.New("origin publication failed")
				return -1
			}
			publicationStatus, originPublished = status, true
		default:
			brokerFailure = errors.New("unknown source-git broker method")
			return -1
		}
		encoded, _ := json.Marshal(reply)
		if uint32(len(encoded)) > replyCap {
			brokerFailure = errors.New("source-git broker reply exceeds capacity")
			return -1
		}
		if _, ok := memory.Read(replyPtr, uint32(len(encoded))); !ok {
			brokerFailure = errors.New("source-git reply pointer invalid")
			return -1
		}
		memory.Write(replyPtr, encoded)
		return int32(len(encoded))
	}).Export("call")
	if _, err := mod.Instantiate(ctx); err != nil {
		return outcome, errors.New("source-git broker initialization failed")
	}
	var output, diagnostic boundedOutput
	config := wazero.NewModuleConfig().WithStdin(bytes.NewReader(append(input, '\n'))).WithStdout(&output).WithStderr(&diagnostic)
	_, err = runtime.InstantiateWithConfig(ctx, moduleBytes, config)
	if brokerFailure != nil {
		return outcome, brokerFailure
	}
	if ctx.Err() != nil {
		return outcome, errors.New("source-git execution timed out")
	}
	if output.overflow || diagnostic.overflow {
		return outcome, errors.New("source-git output exceeded limit")
	}
	if err != nil {
		var exit *sys.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 {
			return outcome, errors.New("source-git execution failed")
		}
	}
	var result GitResult
	if strictJSON(bytes.TrimSpace(output.Bytes()), &result) != nil || result.Schema != GitResultSchema || len(result.Message) > 256 {
		return outcome, errors.New("invalid source-git result")
	}
	if result.Status == "refused" {
		return outcome, errors.New("source-git operation refused")
	}
	if result.Status != "ready" {
		return outcome, errors.New("invalid source-git status")
	}
	outcome.BrokerCalls = callCount
	switch command.Kind {
	case "git.project.ensure":
		if !inspected {
			return outcome, errors.New("source-git skipped inspection")
		}
		observation, e := broker.Inspect(ctx)
		if e != nil || !observation.Exists || observation.Head != "refs/heads/main" {
			return outcome, errors.New("source-git ensure postcondition failed")
		}
	case "git.refs.list":
		if !exhausted && offset < command.Limit {
			return outcome, errors.New("source-git ref list incomplete")
		}
		outcome.Refs = accumulated
	case "git.transport.plan":
		if !prepared {
			return outcome, errors.New("source-git transport plan missing")
		}
		outcome.Plan = plan
	case "git.source.observe":
		if !observed {
			return outcome, errors.New("source-git skipped source observation")
		}
		outcome.CommitOID = observedOID
	case "git.branch.create":
		if !created {
			return outcome, errors.New("source-git skipped branch creation")
		}
	case "git.branch.delete":
		if !deleted {
			return outcome, errors.New("source-git skipped branch deletion")
		}
	case "git.origin.observe":
		if !originObserved {
			return outcome, errors.New("source-git skipped origin observation")
		}
		outcome.OriginRefs = originRefs
	case "git.origin.fetch":
		if !originFetched {
			return outcome, errors.New("source-git skipped origin fetch")
		}
		outcome.CommitOID = observedOID
	case "git.origin.publish":
		if !originPublished {
			return outcome, errors.New("source-git skipped origin publication")
		}
		outcome.PublicationStatus = publicationStatus
	}
	return outcome, nil
}

var originWASISlots = make(chan struct{}, 2)

func validGitBranchRef(ref string) bool {
	return len(ref) > len("refs/heads/") && bytes.HasPrefix([]byte(ref), []byte("refs/heads/")) && ValidGitOriginRef(ref)
}

func validPublicationStatus(status string) bool {
	switch status {
	case "created", "advanced", "satisfied", "refused", "outcome_unknown":
		return true
	}
	return false
}

// ValidGitOriginRef applies Git's branch/tag ref syntax without P's narrower
// ordinary-branch naming rule to an external repository.
func ValidGitOriginRef(ref string) bool {
	if !utf8.ValidString(ref) {
		return false
	}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if bytes.HasPrefix([]byte(ref), []byte(prefix)) {
			name := ref[len(prefix):]
			if len(name) == 0 || len(name) > 512 || bytes.Contains([]byte(name), []byte("..")) || bytes.Contains([]byte(name), []byte("@{")) || name[len(name)-1] == '.' {
				return false
			}
			for _, part := range bytes.Split([]byte(name), []byte{'/'}) {
				if len(part) == 0 || part[0] == '.' || bytes.HasSuffix(part, []byte(".lock")) {
					return false
				}
				for _, c := range part {
					if c <= 0x20 || c == 0x7f || bytes.ContainsRune([]byte("~^:?*[\\"), rune(c)) {
						return false
					}
				}
			}
			return true
		}
	}
	return false
}

func plainGitBrokerRequest(req GitBrokerRequest) bool {
	return req.PageSize == nil && req.MaxInputBytes == nil && req.MaxDurationMS == nil
}

func validGitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func allZeroGitOID(oid string) bool {
	for _, c := range oid {
		if c != '0' {
			return false
		}
	}
	return true
}

func validGitBranch(branch string) bool {
	if branch == "" || len(branch) > 200 {
		return false
	}
	for _, part := range bytes.Split([]byte(branch), []byte{'/'}) {
		if len(part) == 0 || part[0] == '.' || part[len(part)-1] == '.' || bytes.HasSuffix(part, []byte(".lock")) {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return !bytes.Contains([]byte(branch), []byte(".."))
}

func validGitSource(source GitSourceSelector) bool {
	switch source.Kind {
	case "branch":
		return len(source.Value) > len("refs/heads/") && bytes.HasPrefix([]byte(source.Value), []byte("refs/heads/")) && validGitBranch(source.Value[len("refs/heads/"):])
	case "commit":
		return validGitOID(source.Value) && !allZeroGitOID(source.Value)
	default:
		return false
	}
}
