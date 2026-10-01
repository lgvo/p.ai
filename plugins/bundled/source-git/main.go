//go:build wasip1

// Build with GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o git.wasm
// ./plugins/bundled/source-git. Stage only plugin.json, git.wasm, and p-git-ssh.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func brokerCall(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

type command struct {
	Schema         string   `json:"schema"`
	Kind           string   `json:"kind"`
	Scope          string   `json:"scope"`
	Project        string   `json:"project"`
	InitialHead    string   `json:"initial_head,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Service        string   `json:"service,omitempty"`
	Ceilings       *budgets `json:"ceilings,omitempty"`
	Source         *source  `json:"source,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	CommitOID      string   `json:"commit_oid,omitempty"`
	OriginRef      string   `json:"origin_ref,omitempty"`
	SourceRef      string   `json:"source_ref,omitempty"`
	DestinationRef string   `json:"destination_ref,omitempty"`
}

type source struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type budgets struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

type brokerRequest struct {
	Schema   string `json:"schema"`
	Method   string `json:"method"`
	Scope    string `json:"scope"`
	PageSize int    `json:"page_size,omitempty"`
}

type brokerReply struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
	Exists *bool  `json:"exists,omitempty"`
	Head   string `json:"head,omitempty"`
	Refs   []struct {
		Ref string `json:"ref"`
		OID string `json:"oid"`
	} `json:"refs,omitempty"`
	Exhausted    *bool  `json:"exhausted,omitempty"`
	PageComplete *bool  `json:"page_complete,omitempty"`
	CommitOID    string `json:"commit_oid,omitempty"`
}

func decode(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func call(scope, method string, pageSize int) (brokerReply, error) {
	var reply brokerReply
	request, err := json.Marshal(brokerRequest{Schema: "p.broker/v1", Method: method, Scope: scope, PageSize: pageSize})
	if err != nil {
		return reply, err
	}
	buffer := make([]byte, 4096)
	n := brokerCall(uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)), uint32(uintptr(unsafe.Pointer(&buffer[0]))), uint32(len(buffer)))
	if n < 0 || n > int32(len(buffer)) {
		return reply, errors.New("broker refused operation")
	}
	if err := decode(buffer[:n], &reply); err != nil || reply.Schema != "p.broker/v1" || reply.Status != "ok" {
		return reply, errors.New("invalid broker reply")
	}
	return reply, nil
}

func run() error {
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(input) > 4096 {
		return errors.New("invalid command input")
	}
	var request command
	if err := decode(input, &request); err != nil || request.Schema != "p.command/v1" || request.Scope == "" || len(request.Scope) > 64 || request.Project == "" {
		return errors.New("invalid source command")
	}
	switch request.Kind {
	case "git.project.ensure":
		if request.InitialHead != "refs/heads/main" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil || request.Source != nil || request.Branch != "" || request.CommitOID != "" {
			return errors.New("invalid ensure request")
		}
		state, err := call(request.Scope, "git.repo.inspect", 0)
		if err != nil {
			return err
		}
		if state.Exists == nil {
			return errors.New("missing repository observation")
		}
		if *state.Exists {
			if state.Head != request.InitialHead {
				return errors.New("incompatible existing repository")
			}
			return nil
		}
		if _, err := call(request.Scope, "git.repo.init", 0); err != nil {
			return err
		}
		_, err = call(request.Scope, "git.repo.set-head", 0)
		return err
	case "git.refs.list":
		if request.Limit < 1 || request.Limit > 8 || request.InitialHead != "" || request.Service != "" || request.Ceilings != nil || request.Source != nil || request.Branch != "" || request.CommitOID != "" {
			return errors.New("invalid list request")
		}
		for range 8 {
			page, err := call(request.Scope, "git.refs.next", 8)
			if err != nil {
				return err
			}
			if page.PageComplete == nil || page.Exhausted == nil {
				return errors.New("missing page observation")
			}
			if *page.PageComplete {
				return nil
			}
		}
		return errors.New("incomplete ref page")
	case "git.transport.plan":
		if request.InitialHead != "" || request.Limit != 0 || (request.Service != "upload" && request.Service != "receive") || request.Ceilings == nil || request.Ceilings.MaxInputBytes < 1 || request.Ceilings.MaxDurationMS < 1 || request.Source != nil || request.Branch != "" || request.CommitOID != "" {
			return errors.New("invalid transport request")
		}
		_, err := call(request.Scope, "git.transport.prepare", 0)
		return err
	case "git.source.observe":
		if request.Source == nil || request.Branch != "" || request.CommitOID != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid source observation request")
		}
		observed, err := call(request.Scope, "git.source.observe", 0)
		if err != nil || observed.CommitOID == "" {
			return errors.New("missing source observation")
		}
		return nil
	case "git.branch.create":
		if request.Branch == "" || request.CommitOID == "" || request.Source != nil || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid branch creation request")
		}
		_, err := call(request.Scope, "git.branch.create", 0)
		return err
	case "git.branch.delete":
		if request.Branch == "" || request.Source != nil || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid branch deletion request")
		}
		_, err := call(request.Scope, "git.branch.delete", 0)
		return err
	case "git.origin.observe":
		if request.OriginRef != "" || request.CommitOID != "" || request.Source != nil || request.Branch != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid origin observation request")
		}
		_, err := call(request.Scope, "git.origin.observe", 0)
		return err
	case "git.origin.fetch":
		if request.OriginRef == "" || request.CommitOID == "" || request.Source != nil || request.Branch != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid origin fetch request")
		}
		_, err := call(request.Scope, "git.origin.fetch", 0)
		return err
	case "git.origin.publish":
		if request.SourceRef == "" || request.DestinationRef == "" || request.CommitOID == "" || request.OriginRef != "" || request.Source != nil || request.Branch != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return errors.New("invalid origin publication request")
		}
		_, err := call(request.Scope, "git.origin.publish", 0)
		return err
	default:
		return errors.New("unsupported source command")
	}
}

func main() {
	if err := run(); err != nil {
		message := err.Error()
		if len(message) > 256 {
			message = message[:256]
		}
		result, _ := json.Marshal(struct {
			Schema  string `json:"schema"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}{"p.command-result/v1", "refused", message})
		fmt.Println(string(result))
		return
	}
	fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
}
