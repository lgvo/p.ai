package runtimeincus

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"golang.org/x/crypto/ssh"
)

// incusWorld is a stubbed Incus as the session driver reaches it: one
// confined project with its default profile, the images it holds, and its
// instances. Each instance's files are served over a private Unix socket the
// way Incus's file and SFTP endpoints serve them, and `incus exec` gets the
// replies of the guest's systemd: the session host's unit and journal, and
// the session user's project services.
//
// Tests and fixtures change the world directly, as an administrator, a crash
// or the guest would: through the instance's fields, under mu.
type incusWorld struct {
	t  *testing.T
	mu sync.Mutex
	// socket is where Incus serves the current driver; stopServing closes
	// it. restarted closes once a restart closed it, releasing what a dead
	// driver left waiting.
	socket      string
	stopServing func()
	restarted   chan struct{}
	// public is the host's proven public egress substrate, if it has one.
	public *PublicEgressConfig
	images []worldImage
	// instances are listed in this order.
	instances []*worldInstance
	// commands is every Incus argv after the project scope, in order.
	commands [][]string
	// before, when set, runs before each Incus command; an error fails the
	// command before it has any effect.
	before func(args []string) error
	// after, when set, runs after each Incus command that had its effect,
	// and its error replaces the command's reply, as when Incus applies a
	// request and the client loses the answer.
	after func(args []string) error
	// write, when set, sees each write the file API receives before it
	// lands, under mu: it may change what lands, keep it from landing, or
	// hang the request unanswered until the next restart, as when the
	// driver dies waiting for the answer.
	write func(f *guestFile) (land, hang bool)
	// operations are the Incus operations the project lists.
	operations []worldOperation
	// endpointErr and grantErr fail the host checks of the session's
	// endpoint directory and grant sources.
	endpointErr, grantErr error
	// unsafeProfile gives the default profile a NIC, which breaks the
	// project's confinement.
	unsafeProfile bool
	// containerLimit is the project's limits.containers; "" leaves it unset.
	containerLimit string
	// partialList truncates the next listing of the whole project.
	partialList bool
	// initialized and transitions count, by session UUID, the inits that
	// created an instance and the starts and stops that reached one;
	// deletedWhileRunning holds those a delete reached while running.
	initialized, transitions map[string]int
	deletedWhileRunning      map[string]bool
	incarnations             int
	exitOne                  error
}

// worldOperation is an Incus operation as `incus operation list` shows it:
// its status code and the resources it acts on.
type worldOperation struct {
	StatusCode int                 `json:"status_code"`
	Resources  map[string][]string `json:"resources"`
}

// worldImage is an image the project holds and the root tree a new instance
// gets from it.
type worldImage struct {
	image builderImageJSON
	files map[string]guestFile
}

type worldInstance struct {
	name    string
	status  string // Running or Stopped
	config  map[string]string
	devices map[string]map[string]string
	files   map[string]guestFile
	// host is the session host's systemd unit once the runtime started:
	// ActiveState/SubState/Result, reached after hostReads reads of it that
	// see it activating, and its journal.
	host      string
	hostReads int
	journal   string
	reads     int
	// boot, when set, is the host's state for this boot only, and
	// failNextStart the journal of a host that fails on the next start.
	boot, failNextStart string
	// units are the project services the user manager has loaded, and
	// unitFiles the installed ones. reached are the units the service
	// manager was asked to act on or read the journal of; managerBroken
	// makes it unreachable.
	units         []worldUnit
	unitFiles     []string
	reached       []string
	managerBroken bool
}

// worldUnit is one project service: systemd's active and sub state, and its
// journal. A queued job leaves it activating or deactivating until
// settleServices; failing makes its next start fail.
type worldUnit struct {
	name, description, active, sub, logs string
	failing                              bool
}

const worldHostReady = "active/running/success"

func newIncusWorld(t *testing.T) *incusWorld {
	t.Helper()
	w := &incusWorld{t: t, initialized: map[string]int{}, transitions: map[string]int{}, deletedWhileRunning: map[string]bool{}, containerLimit: "8",
		restarted: make(chan struct{})}
	w.addImage(builderImageJSON{Fingerprint: testImage, Type: "container", Architecture: "x86_64", Size: 4096, Properties: map[string]string{"os": "NixOS"}},
		map[string]guestFile{"/": {typ: "directory", uid: 0, gid: 0, mode: 0755}})
	w.socket, w.stopServing = w.serve()
	return w
}

// restart serves Incus on a new socket and closes the old one, so that a
// driver made before it reaches nothing any more: its commands name the old
// socket, and its file requests find no server. What the old driver left
// waiting is released only then.
func (w *incusWorld) restart() {
	socket, stop := w.serve()
	w.mu.Lock()
	old, restarted := w.stopServing, w.restarted
	w.socket, w.stopServing, w.restarted = socket, stop, make(chan struct{})
	w.mu.Unlock()
	old()
	close(restarted)
}

func (w *incusWorld) addImage(image builderImageJSON, files map[string]guestFile) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.images = append(w.images, worldImage{image: image, files: files})
}

// backend is the Incus backend of this P instance over the world, with the
// session endpoint and grant ceilings of a configured host.
func (w *incusWorld) backend() *Backend {
	w.mu.Lock()
	socket := w.socket
	w.mu.Unlock()
	b := &Backend{
		config: Config{Binary: "/bin/incus", UserSocket: socket, Project: "user-1000", PInstanceID: testInstance,
			EndpointPrefix: "/var/lib/p-vm/endpoints", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints", "/var/lib/p-vm/grants"}, PublicEgress: w.public},
		run: w.run, validate: func(Config) error { return nil },
		checkEndpoint:    func(string) error { w.mu.Lock(); defer w.mu.Unlock(); return w.endpointErr },
		checkGrantSource: func(FilesystemGrant) error { w.mu.Lock(); defer w.mu.Unlock(); return w.grantErr },
	}
	if w.public != nil {
		b.proofRun = egressProof(w.t, w.public)
	}
	return b
}

func (w *incusWorld) driver() *Driver { return NewDriver(w.backend()) }

// instance is the instance named name, or nil.
func (w *incusWorld) instance(name string) *worldInstance {
	for _, in := range w.instances {
		if in.name == name {
			return in
		}
	}
	return nil
}

// change runs f on the instance named name under the world's lock.
func (w *incusWorld) change(name string, f func(*worldInstance)) {
	w.t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	in := w.instance(name)
	if in == nil {
		w.t.Fatalf("no instance %s", name)
	}
	f(in)
}

// count is how many Incus commands began with prefix.
func (w *incusWorld) count(prefix ...string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, args := range w.commands {
		if len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

// mutations are the commands that change an instance, by their verb.
func (w *incusWorld) mutations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var verbs []string
	for _, args := range w.commands {
		switch args[0] {
		case "init", "start", "stop", "delete":
			verbs = append(verbs, args[0])
		case "config":
			verbs = append(verbs, "device "+args[4])
		}
	}
	return verbs
}

// userExecs are the guest commands run as the session user, without the
// exec prefix.
func (w *incusWorld) userExecs() [][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var execs [][]string
	for _, args := range w.commands {
		if args[0] == "exec" && slices.Contains(args, "--user") {
			execs = append(execs, args[slices.Index(args, "--")+1:])
		}
	}
	return execs
}

func (w *incusWorld) run(_ context.Context, _ string, argv, env []string) ([]byte, error) {
	w.mu.Lock()
	if len(argv) < 4 || !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || !slices.Contains(env, "INCUS_SOCKET="+w.socket) {
		w.mu.Unlock()
		return nil, errors.New("unconfined invocation")
	}
	args := argv[3:]
	w.commands = append(w.commands, slices.Clone(args))
	before, after := w.before, w.after
	w.mu.Unlock()
	if before != nil {
		if err := before(args); err != nil {
			return nil, err
		}
	}
	w.mu.Lock()
	out, err := w.command(args)
	w.mu.Unlock()
	if err == nil && after != nil {
		if err := after(args); err != nil {
			return nil, err
		}
	}
	return out, err
}

var listNamePattern = regexp.MustCompile(`^\^(.+)\$$`)

func (w *incusWorld) command(args []string) ([]byte, error) {
	switch {
	case slices.Equal(args, []string{"project", "list", "--format", "json"}):
		config := map[string]string{"restricted": "true", "restricted.containers.privilege": "isolated", "restricted.containers.nesting": "block",
			"restricted.containers.lowlevel": "block", "restricted.devices.nic": "block", "restricted.devices.gpu": "block", "restricted.devices.disk": "allow",
			"features.profiles": "true", "features.networks": "false", "restricted.devices.disk.paths": "/var/lib/p-vm/endpoints,/var/lib/p-vm/grants"}
		if w.public != nil {
			config["restricted.devices.nic"], config["restricted.networks.access"] = "managed", w.public.Network
		}
		if w.containerLimit != "" {
			config["limits.containers"] = w.containerLimit
		}
		return json.Marshal([]projectJSON{{Name: "user-1000", Config: config}})
	case slices.Equal(args, []string{"profile", "list", "--format", "json"}):
		devices := map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}
		if w.unsafeProfile {
			devices["eth0"] = map[string]string{"type": "nic", "network": "incusbr0"}
		}
		return json.Marshal([]profileJSON{{Name: "default", Config: map[string]string{"security.idmap.isolated": "true"}, Devices: devices}})
	case slices.Equal(args, []string{"network", "list", "--format", "json"}) && w.public != nil:
		return json.Marshal([]egressNetworkJSON{{Name: w.public.Network, Project: "default", Type: "bridge", Status: "Created", Managed: true, Config: map[string]string{}}})
	case slices.Equal(args, []string{"image", "list", "--format", "json"}):
		images := make([]builderImageJSON, 0, len(w.images))
		for _, image := range w.images {
			images = append(images, image.image)
		}
		return json.Marshal(images)
	case slices.Equal(args, []string{"operation", "list", "--format", "json"}):
		return json.Marshal(append([]worldOperation{}, w.operations...))
	case len(args) == 3 && args[0] == "list" && args[1] == "--format":
		listing, err := w.list("")
		if w.partialList {
			w.partialList = false
			listing = listing[:len(listing)/2]
		}
		return listing, err
	case len(args) == 4 && args[0] == "list" && args[2] == "--format" && listNamePattern.MatchString(args[1]):
		return w.list(listNamePattern.FindStringSubmatch(args[1])[1])
	case args[0] == "init":
		return nil, w.init(args)
	case len(args) > 6 && slices.Equal(args[:3], []string{"config", "device", "add"}):
		return nil, w.addDevice(args[3], args[4], args[5], args[6:])
	case len(args) == 2 && args[0] == "start":
		return nil, w.setStatus(args[1], "Running")
	case len(args) >= 2 && args[0] == "stop":
		return nil, w.setStatus(args[1], "Stopped")
	case len(args) == 2 && args[0] == "delete":
		return nil, w.delete(args[1])
	case args[0] == "exec":
		return w.exec(args)
	}
	return nil, fmt.Errorf("unexpected Incus command %v", args)
}

// delete deletes a stopped instance with its files. Like Incus without
// --force, it refuses a running one; such a delete is recorded.
func (w *incusWorld) delete(name string) error {
	in := w.instance(name)
	if in == nil {
		return errors.New("Instance not found")
	}
	if in.status == "Running" {
		w.deletedWhileRunning[in.config["user.p.session_uuid"]] = true
		return errors.New("The instance is currently running, stop it first or use --force")
	}
	w.instances = slices.DeleteFunc(w.instances, func(other *worldInstance) bool { return other == in })
	return nil
}

func (w *incusWorld) list(name string) ([]byte, error) {
	instances := []instanceJSON{}
	for _, in := range w.instances {
		if name == "" || in.name == name {
			instances = append(instances, in.json())
		}
	}
	return json.Marshal(instances)
}

func (in *worldInstance) json() instanceJSON {
	expanded := maps.Clone(in.config)
	expanded["security.idmap.isolated"] = "true"
	devices := map[string]map[string]string{}
	expandedDevices := map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}
	for name, d := range in.devices {
		devices[name], expandedDevices[name] = maps.Clone(d), maps.Clone(d)
	}
	return instanceJSON{Name: in.name, Type: "container", Status: in.status, Config: maps.Clone(in.config), ExpandedConfig: expanded,
		Devices: devices, ExpandedDevices: expandedDevices, Profiles: []string{"default"}}
}

// newUUID is a fresh volatile UUID; Incus makes one for every instance and
// generation.
func (w *incusWorld) newUUID() string {
	w.incarnations++
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", w.incarnations, w.incarnations)
}

// addInstance adds an instance that P did not create, as an administrator
// would, with Incus's volatile identity added to config. Called with w.mu
// held.
func (w *incusWorld) addInstance(name, status string, config map[string]string) *worldInstance {
	config = maps.Clone(config)
	if config == nil {
		config = map[string]string{}
	}
	config["volatile.uuid"], config["volatile.uuid.generation"] = w.newUUID(), w.newUUID()
	in := &worldInstance{name: name, status: status, config: config, devices: map[string]map[string]string{}, files: map[string]guestFile{}}
	w.instances = append(w.instances, in)
	return in
}

func (w *incusWorld) init(args []string) error {
	if len(args) < 5 || args[3] != "--profile" || args[4] != "default" {
		return errors.New("unexpected init")
	}
	fingerprint, name := args[1], args[2]
	var image *worldImage
	for i := range w.images {
		if w.images[i].image.Fingerprint == fingerprint {
			image = &w.images[i]
		}
	}
	if image == nil || w.instance(name) != nil {
		return errors.New("Incus refused the init")
	}
	config := map[string]string{"volatile.base_image": fingerprint, "volatile.uuid": w.newUUID(), "volatile.uuid.generation": w.newUUID()}
	for i := 5; i+1 < len(args); i += 2 {
		key, value, ok := strings.Cut(args[i+1], "=")
		if args[i] != "--config" || !ok {
			return errors.New("unexpected init option")
		}
		config[key] = value
	}
	files := map[string]guestFile{}
	for path, f := range image.files {
		f.data = slices.Clone(f.data)
		files[path] = f
	}
	w.instances = append(w.instances, &worldInstance{name: name, status: "Stopped", config: config, devices: map[string]map[string]string{}, files: files, host: worldHostReady})
	w.initialized[config["user.p.session_uuid"]]++
	return nil
}

func (w *incusWorld) addDevice(instance, device, typ string, options []string) error {
	in := w.instance(instance)
	if in == nil || in.devices[device] != nil {
		return errors.New("Incus refused the device")
	}
	d := map[string]string{"type": typ}
	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok {
			return errors.New("malformed device option")
		}
		d[key] = value
	}
	in.devices[device] = d
	return nil
}

func (w *incusWorld) setStatus(name, status string) error {
	in := w.instance(name)
	if in == nil {
		return errors.New("instance not found")
	}
	w.transitions[in.config["user.p.session_uuid"]]++
	if in.status == status {
		return nil
	}
	in.status, in.reads, in.boot = status, 0, ""
	if status == "Running" && in.failNextStart != "" {
		in.boot, in.journal, in.failNextStart = "failed/failed/exit-code", in.failNextStart, ""
	}
	return nil
}

// exec answers the fixed guest commands: the session host's systemd unit and
// journal as root, and the project services as the session user.
func (w *incusWorld) exec(args []string) ([]byte, error) {
	in := w.instance(args[1])
	if in == nil || in.status != "Running" {
		return nil, errors.New("Instance is not running")
	}
	split := slices.Index(args, "--")
	prefix, command := args[2:split], args[split+1:]
	user := []string{"--mode", "non-interactive", "--user", "1000", "--group", "1000", "--env", "XDG_RUNTIME_DIR=/run/user/1000", "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	switch {
	case slices.Equal(prefix, []string{"--mode", "non-interactive"}) && slices.Equal(command, []string{"/usr/libexec/p/systemctl", "show", "p-interactive.service", "--property=ActiveState,SubState,Result", "--no-pager"}):
		state := "activating/start/success"
		if in.reads >= in.hostReads {
			state = in.host
		}
		if in.boot != "" {
			state = in.boot
		}
		in.reads++
		parts := strings.Split(state, "/")
		return []byte("ActiveState=" + parts[0] + "\nSubState=" + parts[1] + "\nResult=" + parts[2] + "\n"), nil
	case slices.Equal(prefix, []string{"--mode", "non-interactive"}) && slices.Equal(command, []string{"/usr/libexec/p/journalctl", "-u", "p-interactive.service", "-n", "20", "--no-pager", "--output=short-iso"}):
		return []byte(in.journal), nil
	case slices.Equal(prefix, user):
		return w.userCommand(in, command)
	}
	return nil, fmt.Errorf("unexpected exec %v", args)
}

func (w *incusWorld) userCommand(in *worldInstance, command []string) ([]byte, error) {
	if in.managerBroken {
		return nil, errors.New("Failed to connect to bus: No such file or directory")
	}
	systemctl := func(args ...string) bool {
		return slices.Equal(command, append([]string{"/usr/libexec/p/systemctl", "--user"}, args...))
	}
	switch {
	case systemctl("list-units", "--all", "--type=service", "--no-pager", "--plain", "--output=json", "p-project-*.service"):
		rows := []map[string]string{}
		for _, u := range in.units {
			rows = append(rows, map[string]string{"unit": u.name, "load": "loaded", "active": u.active, "sub": u.sub, "description": u.description})
		}
		return json.Marshal(rows)
	case systemctl("list-unit-files", "--type=service", "--no-pager", "--output=json", "p-project-*.service"):
		if len(in.unitFiles) == 0 {
			return []byte("[]"), w.exitStatusOne()
		}
		rows := []map[string]string{}
		for _, name := range in.unitFiles {
			rows = append(rows, map[string]string{"unit_file": "/home/p/.config/systemd/user/" + name, "state": "disabled"})
		}
		return json.Marshal(rows)
	case len(command) == 6 && systemctl("--no-pager", "--no-block", command[4], command[5]):
		return nil, in.queue(command[4], command[5])
	case len(command) == 6 && command[0] == "/usr/libexec/p/journalctl" && slices.Equal(command[2:], []string{"--no-pager", "--output=short-iso", "-n", "100"}):
		unit := strings.TrimPrefix(command[1], "--user-unit=")
		in.reached = append(in.reached, unit)
		for _, u := range in.units {
			if u.name == unit {
				return []byte(newestLines(u.logs, 100)), nil
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected user command %v", command)
}

// exitStatusOne is the error of a command that exited with status 1, as
// systemctl list-unit-files does for an empty list.
func (w *incusWorld) exitStatusOne() error {
	if w.exitOne == nil {
		_, w.exitOne = serviceCommandFixture(w.t, "one")
	}
	return w.exitOne
}

// queue takes a start, stop or restart job for a unit, loading an installed
// one first.
func (in *worldInstance) queue(action, name string) error {
	in.reached = append(in.reached, name)
	i := slices.IndexFunc(in.units, func(u worldUnit) bool { return u.name == name })
	if i < 0 {
		if !slices.Contains(in.unitFiles, name) {
			return errors.New("Unit not found")
		}
		in.units = append(in.units, worldUnit{name: name, active: "inactive", sub: "dead"})
		i = len(in.units) - 1
	}
	u := &in.units[i]
	// systemd finishes a start of an active unit and a stop of an
	// inactive one at once, changing nothing.
	switch {
	case action == "start" && (u.active == "active" || u.active == "activating"):
	case action == "stop" && (u.active == "inactive" || u.active == "failed"):
	case action == "start" || action == "restart":
		u.active, u.sub = "activating", "start"
	case action == "stop":
		u.active, u.sub = "deactivating", "stop"
	default:
		return errors.New("Unknown command verb")
	}
	return nil
}

// newestLines is the last n lines of log, as journalctl -n shows them.
func newestLines(log string, n int) string {
	lines := strings.SplitAfter(log, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines[max(0, len(lines)-n):], "")
}

// settleServices finishes every queued job.
func (w *incusWorld) settleServices(name string) {
	w.change(name, func(in *worldInstance) {
		for i := range in.units {
			in.units[i].settle()
		}
	})
}

func (u *worldUnit) settle() {
	switch {
	case u.active == "activating" && u.failing:
		u.active, u.sub = "failed", "failed"
	case u.active == "activating":
		u.active, u.sub = "active", "running"
	case u.active == "deactivating":
		u.active, u.sub = "inactive", "dead"
	}
}

// serve answers Incus's file and SFTP endpoints of every instance on a new
// private Unix socket, until stop.
func (w *incusWorld) serve() (socket string, stop func()) {
	// A socket path holds at most 107 bytes, too few for a test's own
	// temporary directory.
	dir, err := os.MkdirTemp("", "incus")
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "unix.socket.user")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		w.t.Fatal(err)
	}
	files := regexp.MustCompile(`^/1\.0/instances/([^/]+)/(files|sftp)$`)
	server := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		match := files.FindStringSubmatch(r.URL.Path)
		if match == nil || r.URL.Query().Get("project") != "user-1000" {
			w.t.Errorf("unconfined file request %s %s", r.Method, r.URL)
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		if match[2] == "sftp" {
			w.serveSFTP(rw, match[1])
			return
		}
		w.serveFile(rw, r, match[1])
	})}
	go func() { _ = server.Serve(listener) }()
	w.t.Cleanup(func() { _ = server.Close() })
	return socket, func() { _ = server.Close() }
}

// serveFile answers the file endpoint as Incus does: the components above
// the last resolve through symlinks inside the instance's root, and a file
// write follows a symlink at the path too; HEAD and GET report the last
// component itself, with only its permission bits.
func (w *incusWorld) serveFile(rw http.ResponseWriter, r *http.Request, instance string) {
	name := r.URL.Query().Get("path")
	if r.Method == http.MethodPost {
		w.receiveFile(rw, r, instance, name)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	in := w.instance(instance)
	if in == nil {
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	var f guestFile
	ok := false
	if resolved, found := in.resolve(name, false); found {
		f, ok = in.files[resolved]
	}
	if name == guestSystemdSocket {
		// A running guest's systemd has its socket, which Incus reports
		// as an empty file.
		f, ok = guestFile{typ: "file", uid: 0, gid: 0, mode: 0700}, in.status == "Running"
	}
	if !ok {
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	rw.Header().Set("X-Incus-type", f.typ)
	rw.Header().Set("X-Incus-uid", strconv.Itoa(f.uid))
	rw.Header().Set("X-Incus-gid", strconv.Itoa(f.gid))
	rw.Header().Set("X-Incus-mode", fmt.Sprintf("%04o", f.mode&0777))
	if f.typ == "file" {
		rw.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
	}
	if r.Method == http.MethodGet {
		_, _ = rw.Write(f.data)
	}
}

// receiveFile lands a file write, unless the write hook keeps it from
// landing or hangs it.
func (w *incusWorld) receiveFile(rw http.ResponseWriter, r *http.Request, instance, name string) {
	mode, e1 := strconv.ParseInt(r.Header.Get("X-Incus-mode"), 8, 32)
	uid, e2 := strconv.Atoi(r.Header.Get("X-Incus-uid"))
	gid, e3 := strconv.Atoi(r.Header.Get("X-Incus-gid"))
	data, e4 := io.ReadAll(r.Body)
	if errors.Join(e1, e2, e3, e4) != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	f := guestFile{typ: r.Header.Get("X-Incus-type"), uid: uid, gid: gid, mode: int(mode), data: data}
	w.mu.Lock()
	in := w.instance(instance)
	if in == nil {
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	land, hang := true, false
	if w.write != nil {
		land, hang = w.write(&f)
	}
	if land {
		resolved, ok := in.resolve(name, f.typ == "file")
		if !ok {
			w.mu.Unlock()
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		in.files[resolved] = f
	}
	restarted := w.restarted
	w.mu.Unlock()
	if hang {
		<-restarted
	}
}

// resolve finds the path name names in the instance's root as the kernel
// would inside it: every component above the last through symlinks, and
// the last one too when follow is set. It is false when a component above
// the last is missing or not a directory.
func (in *worldInstance) resolve(name string, follow bool) (string, bool) {
	pending := strings.Split(path.Clean("/"+name), "/")
	current := "/"
	for hops := 0; len(pending) != 0; {
		part := pending[0]
		pending = pending[1:]
		if part == "" {
			continue
		}
		next := path.Join(current, part)
		f, ok := in.files[next]
		if ok && f.typ == "symlink" && (len(pending) != 0 || follow) {
			if hops++; hops > 40 {
				return "", false
			}
			target := string(f.data)
			if !path.IsAbs(target) {
				target = path.Join(current, target)
			}
			pending = append(strings.Split(path.Clean(target), "/"), pending...)
			current = "/"
			continue
		}
		if len(pending) != 0 && (!ok || f.typ != "directory") {
			return "", false
		}
		current = next
	}
	return current, true
}

// serveSFTP answers the SFTP requests the driver makes: LSTAT with the full
// mode of a path, a directory's listing, and the removal of a path.
func (w *incusWorld) serveSFTP(rw http.ResponseWriter, instance string) {
	conn, buffered, err := rw.(http.Hijacker).Hijack()
	if err != nil {
		w.t.Error(err)
		return
	}
	defer conn.Close()
	fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: sftp\r\n\r\n")
	reader := bufio.NewReader(buffered)
	if init, err := readSFTPPacket(reader); err != nil || init[0] != 1 {
		return
	}
	if writePacket(conn, []byte{0, 0, 0, 5, 2, 0, 0, 0, 3}) != nil {
		return
	}
	// listed holds the open directory handles, true once read to the end.
	listed := map[string]bool{}
	for {
		request, err := readSFTPPacket(reader)
		if err != nil || len(request) < 9 {
			return
		}
		w.mu.Lock()
		reply := sftpStatus(request[1:5], 2) // no such file
		if in := w.instance(instance); in != nil {
			reply = in.sftp(request[0], request[1:5], string(request[9:]), listed)
		}
		w.mu.Unlock()
		if writePacket(conn, reply) != nil {
			return
		}
	}
}

// sftp answers one SFTP request of op with id on arg, a path or a directory
// handle.
func (in *worldInstance) sftp(op byte, id []byte, arg string, listed map[string]bool) []byte {
	const ok, eof, missing, failure = 0, 1, 2, 4
	lookup := func(follow bool) (string, guestFile, bool) {
		resolved, found := in.resolve(arg, follow)
		f, exists := in.files[resolved]
		return resolved, f, found && exists
	}
	switch op {
	case 7: // SSH_FXP_LSTAT
		_, f, exists := lookup(false)
		if !exists {
			return sftpStatus(id, missing)
		}
		return sftpAttrs(id, f)
	case 11: // SSH_FXP_OPENDIR
		dir, f, exists := lookup(true)
		if !exists || f.typ != "directory" {
			return sftpStatus(id, missing)
		}
		listed[dir] = false
		return sftpPacket(append(append([]byte{102}, id...), sftpString(dir)...))
	case 12: // SSH_FXP_READDIR
		done, open := listed[arg]
		if !open {
			return sftpStatus(id, failure)
		}
		listed[arg] = true
		names := in.children(arg)
		if done || len(names) == 0 {
			return sftpStatus(id, eof)
		}
		reply := binary.BigEndian.AppendUint32(append([]byte{104}, id...), uint32(len(names)))
		for _, name := range names {
			reply = binary.BigEndian.AppendUint32(append(append(reply, sftpString(name)...), sftpString(name)...), 0)
		}
		return sftpPacket(reply)
	case 4: // SSH_FXP_CLOSE
		delete(listed, arg)
		return sftpStatus(id, ok)
	case 13, 15: // SSH_FXP_REMOVE, SSH_FXP_RMDIR
		name, f, exists := lookup(false)
		switch {
		case !exists:
			return sftpStatus(id, missing)
		case (f.typ == "directory") != (op == 15) || len(in.children(name)) != 0:
			return sftpStatus(id, failure)
		}
		delete(in.files, name)
		return sftpStatus(id, ok)
	}
	return sftpStatus(id, 8) // unsupported
}

// children are the names directly inside the directory dir.
func (in *worldInstance) children(dir string) []string {
	var names []string
	for name := range in.files {
		if name != dir && path.Dir(name) == dir {
			names = append(names, path.Base(name))
		}
	}
	slices.Sort(names)
	return names
}

// sftpAttrs is the LSTAT reply for f: its owner and full mode. A path that
// is neither a directory, a regular file nor a symlink is a FIFO.
func sftpAttrs(id []byte, f guestFile) []byte {
	be := binary.BigEndian
	kind := map[string]uint32{"directory": 0040000, "file": 0100000, "symlink": 0120000, "other": 0010000}[f.typ]
	reply := append([]byte{105}, id...)
	reply = be.AppendUint32(reply, 6) // uid, gid and permissions
	reply = be.AppendUint32(be.AppendUint32(be.AppendUint32(reply, uint32(f.uid)), uint32(f.gid)), kind|uint32(f.mode))
	return sftpPacket(reply)
}

func sftpStatus(id []byte, code uint32) []byte {
	be := binary.BigEndian
	return sftpPacket(be.AppendUint32(be.AppendUint32(be.AppendUint32(append([]byte{101}, id...), code), 0), 0))
}

func sftpString(s string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...)
}

// sftpPacket prefixes body with its length.
func sftpPacket(body []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)
}

// testSessionSpec is a valid spec of a new session of the project team/app on
// unborn main, created from the base image, with the session assets every
// session gets and a fresh Git key.
func testSessionSpec(t *testing.T) runtimedriver.Session {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return runtimedriver.Session{
		Ref:    runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: testUUID},
		Labels: runtimedriver.Labels{runtimedriver.LabelProject: "team/app"},
		Image:  testImage, Command: []string{"/bin/bash", "-l"},
		Workspace:  runtimedriver.Workspace{Repository: "team/app", Branch: "main"},
		GitKey:     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		GitHostKey: string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pub))),
		Endpoints:  "/var/lib/p-vm/endpoints/" + testUUID,
		Assets: []runtimedriver.Asset{testAsset("p-session.target", 0644, nil), testAsset("p-interactive.service", 0644, nil),
			testAsset("p-attach", 0555, nil), testAsset("p-git-ssh", 0555, nil)},
	}
}

// testAsset is the session asset of role with mode; its bytes are data, or
// the role's name when data is nil.
func testAsset(role string, mode uint32, data []byte) runtimedriver.Asset {
	if data == nil {
		data = []byte(role + "\n")
	}
	sum := sha256.Sum256(data)
	return runtimedriver.Asset{Role: role, Mode: mode, Data: data, SHA256: hex.EncodeToString(sum[:])}
}

// testGrant is a read-only directory grant under the host's grant ceiling.
func testGrant() runtimedriver.Grant {
	return runtimedriver.Grant{Name: "data", Source: "/var/lib/p-vm/grants/team/data", Type: "directory", Access: "read-only", Device: 7, Inode: 11, OwnerUID: 1000, OwnerGID: 1000}
}
