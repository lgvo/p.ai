package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"
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

type GitTransportPlan struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

type GitCommand struct {
	Schema         string            `json:"schema"`
	Kind           string            `json:"kind"`
	Scope          string            `json:"scope"`
	Project        string            `json:"project"`
	Service        string            `json:"service,omitempty"`
	Ceilings       *GitTransportPlan `json:"ceilings,omitempty"`
	CommitOID      string            `json:"commit_oid,omitempty"`
	OriginRef      string            `json:"origin_ref,omitempty"`
	SourceRef      string            `json:"source_ref,omitempty"`
	DestinationRef string            `json:"destination_ref,omitempty"`
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
	MaxInputBytes *int64 `json:"max_input_bytes,omitempty"`
	MaxDurationMS *int64 `json:"max_duration_ms,omitempty"`
}

type GitBrokerResponse struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
}

type GitOutcome struct {
	Plan              GitTransportPlan
	CommitOID         string
	OriginRefs        []GitOriginRef
	PublicationStatus string
}

const GitCoreInputCeiling int64 = 1 << 30
const GitCoreDurationMS int64 = 600000

// RunSourceGit runs a package-authored operation sequence. The returned refs,
// commit and plan come only from broker observations, never from module
// output. broker holds the host's effects for the command, each operating
// only on the invocation's core-selected project: a GitOriginBroker for
// origin observation and fetch, a GitOriginPublisher for publication;
// transport planning uses none.
func RunSourceGit(parent context.Context, selected Active, command GitCommand, broker any) (GitOutcome, error) {
	var outcome GitOutcome
	release, leaseErr := LeasePackage(selected.Package.Path)
	if leaseErr != nil {
		return outcome, leaseErr
	}
	defer release()
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
	if m.Capability != "source-git" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"git.project"}) {
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
	case "git.transport.plan":
		if (command.Service != "upload" && command.Service != "receive") || command.Ceilings == nil || command.Ceilings.MaxInputBytes <= 0 || command.Ceilings.MaxInputBytes > GitCoreInputCeiling || command.Ceilings.MaxDurationMS <= 0 || command.Ceilings.MaxDurationMS > GitCoreDurationMS || command.CommitOID != "" {
			return outcome, errors.New("invalid transport request")
		}
	case "git.origin.observe":
		if command.Service != "" || command.Ceilings != nil || command.CommitOID != "" || command.OriginRef != "" {
			return outcome, errors.New("invalid origin observation request")
		}
	case "git.origin.fetch":
		if !ValidGitOriginRef(command.OriginRef) || !validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID) || command.Service != "" || command.Ceilings != nil {
			return outcome, errors.New("invalid origin fetch request")
		}
	case "git.origin.publish":
		if !validGitBranchRef(command.SourceRef) || !validGitBranchRef(command.DestinationRef) || !validGitOID(command.CommitOID) || allZeroGitOID(command.CommitOID) || command.Service != "" || command.Ceilings != nil {
			return outcome, errors.New("invalid origin publication request")
		}
	default:
		return outcome, errors.New("unknown source-git method")
	}
	moduleBytes, unavailable := selectedModule(ctx, selected)
	switch unavailable {
	case moduleUnreadable, moduleChanged:
		return outcome, errors.New("source-git package changed")
	case moduleMissing:
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
	runtime, err := newSandbox(ctx)
	if err != nil {
		return outcome, errors.New("WASI initialization failed")
	}
	defer runtime.Close(ctx)
	var brokerFailure error
	callCount, prepared, observedOID := 0, false, ""
	originObserved, originFetched, originPublished := false, false, false
	publicationStatus := ""
	var originRefs []GitOriginRef
	plan := GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}
	mod := runtime.NewHostModuleBuilder("p_broker_v1")
	mod.NewFunctionBuilder().WithFunc(func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
		callCount++
		if callCtx.Err() != nil || callCount > 16 || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
			brokerFailure = errors.New("source-git broker bounds exceeded")
			return -1
		}
		memory := guestMemory(module)
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
		case "git.transport.prepare":
			if command.Kind != "git.transport.plan" || prepared || req.MaxInputBytes != nil && (*req.MaxInputBytes <= 0 || *req.MaxInputBytes > command.Ceilings.MaxInputBytes) || req.MaxDurationMS != nil && (*req.MaxDurationMS <= 0 || *req.MaxDurationMS > command.Ceilings.MaxDurationMS) {
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
	output, failed := runModule(ctx, runtime, moduleBytes, input)
	if brokerFailure != nil {
		return outcome, brokerFailure
	}
	switch failed {
	case runTimedOut:
		return outcome, errors.New("source-git execution timed out")
	case runOutputExceeded:
		return outcome, errors.New("source-git output exceeded limit")
	case runFailed:
		return outcome, errors.New("source-git execution failed")
	}
	var result GitResult
	if strictJSON(bytes.TrimSpace(output), &result) != nil || result.Schema != GitResultSchema || len(result.Message) > 256 {
		return outcome, errors.New("invalid source-git result")
	}
	if result.Status == "refused" {
		return outcome, errors.New("source-git operation refused")
	}
	if result.Status != "ready" {
		return outcome, errors.New("invalid source-git status")
	}
	switch command.Kind {
	case "git.transport.plan":
		if !prepared {
			return outcome, errors.New("source-git transport plan missing")
		}
		outcome.Plan = plan
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
	return req.MaxInputBytes == nil && req.MaxDurationMS == nil
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
