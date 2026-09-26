// runtime-fixture binds production runtime effects to explicit synthetic VM
// identities. It is not a public lifecycle API or authorization entrypoint.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"github.com/lgvo/p.ai/internal/runtimekit"
)

type request struct {
	Instance            string `json:"instance_uuid"`
	Session             string `json:"session_uuid"`
	Image               string `json:"image"`
	Endpoint            string `json:"endpoint"`
	Socket              string `json:"socket,omitempty"`
	Repository          string `json:"repository,omitempty"`
	Branch              string `json:"branch,omitempty"`
	InitialOID          string `json:"initial_oid,omitempty"`
	HostActivation      string `json:"host_activation,omitempty"`
	SourceActivation    string `json:"source_activation,omitempty"`
	IdentityFile        string `json:"identity_file,omitempty"`
	ServerPublicKeyFile string `json:"server_public_key_file,omitempty"`
	WorkspaceRepository string `json:"workspace_repository,omitempty"`
	WorkspaceInitialOID string `json:"workspace_initial_oid,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runtime-fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: runtime-fixture <activation> <runtime.method> <request.json>")
	}
	f, err := os.Open(args[2])
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	var req request
	if err = dec.Decode(&req); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("trailing fixture input")
	}
	if req.Socket == "" {
		req.Socket = "/var/lib/incus/unix.socket.user"
	}
	if req.Repository == "" {
		req.Repository = "runtime-fixture"
	}
	binary, err := exec.LookPath("incus")
	if err != nil {
		return err
	}
	backend, err := runtimeincus.New(runtimeincus.Config{
		Binary: binary, UserSocket: req.Socket, Project: "user-1000",
		EndpointPrefix:     "/var/lib/p-vm/endpoints/pdev",
		DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"},
	})
	if err != nil {
		return err
	}
	selected, err := plugin.LoadActivation(args[0])
	if err != nil {
		return err
	}
	if len(selected) != 1 || selected[0].Package.Manifest.Capability != "runtime" {
		return errors.New("one runtime provider required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
	defer cancel()
	var assembly *runtimeincus.Assembly
	if args[1] == "runtime.assemble" {
		assembly, err = selectedAssembly(req)
		if err != nil {
			return err
		}
	}
	state, err := plugin.RunRuntime(ctx, selected[0], args[1], runtimeincus.Scoped{
		Backend: backend,
		Session: runtimeincus.Session{InstanceUUID: req.Instance, SessionUUID: req.Session,
			ProjectPath: req.Repository, AssignedBranch: req.Branch, InitialOID: req.InitialOID,
			ContractVersion: "1", ImageFingerprint: req.Image, EndpointSource: req.Endpoint},
		Assembly: assembly,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}

func selectedAssembly(req request) (*runtimeincus.Assembly, error) {
	if req.HostActivation == "" || req.SourceActivation == "" || req.IdentityFile == "" || req.ServerPublicKeyFile == "" {
		return nil, errors.New("assembly fixture input incomplete")
	}
	plan := func(path, capability string) (plugin.AssetPlan, error) {
		selected, err := plugin.LoadActivation(path)
		if err != nil {
			return plugin.AssetPlan{}, err
		}
		if len(selected) != 1 || selected[0].Package.Manifest.Capability != capability {
			return plugin.AssetPlan{}, errors.New("one selected asset provider required")
		}
		return plugin.PlanAssets(selected[0])
	}
	host, err := plan(req.HostActivation, "interactive-host")
	if err != nil {
		return nil, err
	}
	source, err := plan(req.SourceActivation, "source-git")
	if err != nil {
		return nil, err
	}
	identity, err := os.ReadFile(req.IdentityFile)
	if err != nil {
		return nil, err
	}
	serverKey, err := os.ReadFile(req.ServerPublicKeyFile)
	if err != nil {
		return nil, err
	}
	workspaceRepository := req.Repository
	if req.WorkspaceRepository != "" {
		workspaceRepository = req.WorkspaceRepository
	}
	workspaceOID := req.InitialOID
	if req.WorkspaceInitialOID != "" {
		workspaceOID = req.WorkspaceInitialOID
	}
	return &runtimeincus.Assembly{
		HostAssets: host, SourceAssets: source,
		SessionConfig: runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: []string{"/run/current-system/sw/bin/bash", "--noprofile", "--norc"}},
		Workspace:     runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: workspaceRepository, Branch: req.Branch, InitialOID: workspaceOID},
		Identity:      identity, ServerPublicKey: string(serverKey),
	}, nil
}
