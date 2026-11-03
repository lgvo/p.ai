// runtime-fixture drives the Incus runtime driver in process for VM
// scenarios, on the synthetic session identities a scenario supplies. It is
// not a public lifecycle API or an authorization entrypoint.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

const usage = "usage: runtime-fixture inspect|create|start|stop|delete|hostlog <session.json> [incarnation]\n" +
	"  create takes an incarnation to complete that runtime instead of creating one; delete requires one;\n" +
	"  hostlog prints the session host's last words, at the incarnation when one is given"

// sessionFile is a scenario's session: the driver's session spec, with the
// selected asset packages and the keys as files.
type sessionFile struct {
	Instance         string `json:"instance_uuid"` // the P instance whose labels the runtime carries
	Session          string `json:"session_uuid"`
	Project          string `json:"project"`
	Branch           string `json:"branch"`
	Commit           string `json:"commit,omitempty"` // empty for an unborn branch
	Image            string `json:"image"`
	Endpoints        string `json:"endpoints"`
	HostActivation   string `json:"host_activation"`
	SourceActivation string `json:"source_activation"`
	GitKeyFile       string `json:"git_key_file"`
	GitHostKeyFile   string `json:"git_host_key_file"`
	Socket           string `json:"socket,omitempty"` // default: the confined user socket
}

// observation is a runtimedriver.Observation as JSON. Gated says, for a
// create, whether the driver called its gate.
type observation struct {
	State       runtimedriver.State       `json:"state"`
	Incarnation runtimedriver.Incarnation `json:"incarnation"`
	Complete    bool                      `json:"complete"`
	Host        hostState                 `json:"host"`
	Gated       *bool                     `json:"gated,omitempty"`
}

type hostState struct {
	Ready      bool   `json:"ready"`
	Unknown    bool   `json:"unknown"`
	Diagnostic string `json:"diagnostic"`
}

// hostFailed is the exit status of a start whose session host did not
// become ready; the driver has stopped the runtime again.
const hostFailed = 3

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "runtime-fixture:", err)
	var host *runtimedriver.HostError
	if errors.As(err, &host) {
		os.Exit(hostFailed)
	}
	os.Exit(1)
}

func run(args []string) error {
	if len(args) != 2 && len(args) != 3 {
		return errors.New(usage)
	}
	command, at := args[0], runtimedriver.Incarnation("")
	if len(args) == 3 {
		at = runtimedriver.Incarnation(args[2])
	}
	if at != "" && command != "create" && command != "delete" && command != "hostlog" || command == "delete" && at == "" {
		return errors.New(usage)
	}
	in, err := readSessionFile(args[1])
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("incus")
	if err != nil {
		return err
	}
	backend, err := runtimeincus.New(runtimeincus.Config{
		Binary: binary, UserSocket: in.Socket, Project: "user-1000", PInstanceID: in.Instance,
		EndpointPrefix:     "/var/lib/p-vm/endpoints/pdev",
		DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints", "/var/lib/p-vm/grants"},
	})
	if err != nil {
		return err
	}
	spec, err := in.spec()
	if err != nil {
		return err
	}
	driver := runtimeincus.NewDriver(backend)
	// Start waits up to 100 s for the session host and up to 45 s more to
	// stop a runtime whose host failed.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var o runtimedriver.Observation
	var gated *bool
	switch command {
	case "inspect":
		o, err = driver.Inspect(ctx, spec)
	case "create":
		called := false
		gated = &called
		if at == "" {
			o, err = driver.Create(ctx, spec, "", func() error { called = true; return nil })
		} else {
			o, err = driver.Create(ctx, spec, at, nil)
		}
	case "start":
		o, err = driver.Start(ctx, spec)
	case "stop":
		o, err = driver.Stop(ctx, spec)
	case "delete":
		o, err = deleteSession(ctx, backend, driver, spec, at)
	case "hostlog":
		// A stopped host's last words come only from HostLog.
		log, err := driver.HostLog(ctx, runtimedriver.Target{Session: spec, At: at})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Log string `json:"log"`
		}{log})
	default:
		return errors.New(usage)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(observation{State: o.State, Incarnation: o.Incarnation, Complete: o.Complete,
		Host: hostState{Ready: o.Host.Ready, Unknown: o.Host.Unknown, Diagnostic: o.Host.Diagnostic}, Gated: gated})
}

func readSessionFile(path string) (sessionFile, error) {
	var in sessionFile
	f, err := os.Open(path)
	if err != nil {
		return in, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return in, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return in, errors.New("trailing session file input")
	}
	for _, required := range []string{in.Instance, in.Session, in.Project, in.Branch, in.Image, in.Endpoints, in.HostActivation, in.SourceActivation, in.GitKeyFile, in.GitHostKeyFile} {
		if required == "" {
			return in, errors.New("session file incomplete")
		}
	}
	if in.Socket == "" {
		in.Socket = "/var/lib/incus/unix.socket.user"
	}
	return in, nil
}

// spec is the driver's session spec: the assets of the selected host and
// source packages by role, the session's Git key, and the pinned host key of
// P's Git endpoint.
func (in sessionFile) spec() (runtimedriver.Session, error) {
	host, err := assets(in.HostActivation, "interactive-host")
	if err != nil {
		return runtimedriver.Session{}, err
	}
	source, err := assets(in.SourceActivation, "source-git")
	if err != nil {
		return runtimedriver.Session{}, err
	}
	key, err := os.ReadFile(in.GitKeyFile)
	if err != nil {
		return runtimedriver.Session{}, err
	}
	hostKey, err := os.ReadFile(in.GitHostKeyFile)
	if err != nil {
		return runtimedriver.Session{}, err
	}
	return runtimedriver.Session{
		Ref:       runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: in.Session},
		Labels:    runtimedriver.Labels{runtimedriver.LabelProject: in.Project},
		Image:     runtimedriver.ImageRef(in.Image),
		Command:   []string{"/run/current-system/sw/bin/bash", "--noprofile", "--norc"},
		Workspace: runtimedriver.Workspace{Repository: in.Project, Branch: in.Branch, Commit: in.Commit},
		GitKey:    key, GitHostKey: string(hostKey),
		Endpoints: in.Endpoints,
		Assets:    append(host, source...),
	}, nil
}

// assets are the files of the one package an activation selects for
// capability, as its asset plan pins them.
func assets(activation, capability string) ([]runtimedriver.Asset, error) {
	selected, err := plugin.LoadActivation(activation)
	if err != nil {
		return nil, err
	}
	if len(selected) != 1 || selected[0].Package.Manifest.Capability != capability {
		return nil, fmt.Errorf("one %s package required", capability)
	}
	plan, err := plugin.PlanAssets(selected[0])
	if err != nil {
		return nil, err
	}
	files := make([]runtimedriver.Asset, 0, len(plan.Files))
	for _, f := range plan.Files {
		files = append(files, runtimedriver.Asset{Role: f.Role, Mode: f.Mode, Data: f.Data, SHA256: f.SHA256})
	}
	return files, nil
}

// deleteSession deletes the stopped runtime at incarnation at with the exact
// delete the daemon's discard uses, until the driver has its own Delete
// (migration step M4). An absent runtime needs no delete.
func deleteSession(ctx context.Context, backend *runtimeincus.Backend, driver *runtimeincus.Driver, spec runtimedriver.Session, at runtimedriver.Incarnation) (runtimedriver.Observation, error) {
	o, err := driver.Inspect(ctx, spec)
	if err != nil || o.State == runtimedriver.StateAbsent {
		return o, err
	}
	incusUUID, generation, ok := strings.Cut(string(at), "/")
	if !ok {
		return o, errors.New("an Incus incarnation is the instance UUID and its generation")
	}
	native, err := backend.NativeSession(spec)
	if err != nil {
		return o, err
	}
	if err := backend.DeleteForDiscardExact(ctx, native, incusUUID, generation, func() error { return nil }); err != nil {
		return o, err
	}
	return driver.Inspect(ctx, spec)
}
