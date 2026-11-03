package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimekit"
	"golang.org/x/crypto/ssh"
)

type guestFile struct {
	typ            string
	uid, gid, mode int
	data           []byte
}
type fileAPI interface {
	get(context.Context, string) (guestFile, bool, error)
	put(context.Context, string, guestFile) error
}

// guestAssets are where the guest's systemd host, its Git transport and the
// agent adapter find each asset role, with the mode each file gets.
var guestAssets = map[string]guestInstall{
	"p-session.target":      {"/etc/p/assets/p-session.target", guestFile{"file", 0, 0, 0644, nil}},
	"p-interactive.service": {"/etc/p/assets/p-interactive.service", guestFile{"file", 0, 0, 0644, nil}},
	"p-attach":              {"/etc/p/assets/p-attach", guestFile{"file", 0, 0, 0555, nil}},
	"p-git-ssh":             {"/etc/p/assets/p-git-ssh", guestFile{"file", 0, 0, 0555, nil}},
	"p-codex-adapter":       {"/etc/p/assets/p-codex-adapter", guestFile{"file", 0, 0, 0555, nil}},
}

// assetInstall checks one selected asset file against its role's mode and
// its own digest, and says where it goes.
func assetInstall(role string, mode uint32, data []byte, digest string) (guestInstall, error) {
	want, ok := guestAssets[role]
	if !ok || mode != uint32(want.file.mode) || len(data) == 0 || len(data) > 1<<20 {
		return guestInstall{}, errors.New("asset role, destination, or mode changed")
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != digest {
		return guestInstall{}, errors.New("asset bytes differ from selected digest")
	}
	want.file.data = bytes.Clone(data)
	return want, nil
}

// sessionGuestFiles are a session's own guest files: what its runtime kit
// reads about the session and its workspace, and its Git identity with the
// pinned host key of P's Git endpoint.
func sessionGuestFiles(sessionConfig runtimekit.Config, workspaceConfig runtimekit.WorkspaceConfig, identity []byte, hostKey string) ([]guestInstall, error) {
	config, err := json.Marshal(sessionConfig)
	if err != nil {
		return nil, err
	}
	if _, err := runtimekit.ParseConfig(config); err != nil {
		return nil, err
	}
	workspace, err := json.Marshal(workspaceConfig)
	if err != nil {
		return nil, err
	}
	if _, err := runtimekit.ParseWorkspaceConfig(workspace); err != nil {
		return nil, err
	}
	// The commit image's devShell selection decides activation; the runtime
	// config carries the material digest the image must hold.
	if workspaceConfig.Schema == "p.workspace/v2" && (workspaceConfig.EnvironmentSelection == "devshell") != (sessionConfig.Activation == "devshell") {
		return nil, errors.New("workspace environment differs from runtime activation")
	}
	if len(identity) == 0 || len(identity) > 16<<10 || bytes.IndexByte(identity, 0) >= 0 {
		return nil, errors.New("invalid session identity")
	}
	block, rest := pem.Decode(identity)
	if block == nil || (block.Type != "PRIVATE KEY" && block.Type != "OPENSSH PRIVATE KEY") || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid session private key PEM")
	}
	signer, err := ssh.ParsePrivateKey(identity)
	if err != nil || signer.PublicKey().Type() != "ssh-ed25519" {
		return nil, errors.New("unsupported session private key")
	}
	key := strings.TrimSpace(hostKey)
	pub, _, options, restKey, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || pub == nil || len(options) != 0 || len(bytes.TrimSpace(restKey)) != 0 || pub.Type() != "ssh-ed25519" || len(key) > 4096 {
		return nil, errors.New("invalid pinned Git host key")
	}
	key = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	sshConfig := []byte("Host p\n  HostName p\n  User git\n  HostKeyAlias p\n  IdentityFile /etc/p/git/identity\n  IdentitiesOnly yes\n  IdentityAgent none\n  UserKnownHostsFile /etc/p/git/known_hosts\n  GlobalKnownHostsFile /dev/null\n  StrictHostKeyChecking yes\n  BatchMode yes\n  ConnectTimeout 5\n  ProxyCommand /usr/libexec/p/runtime-kit git-stream\n")
	return []guestInstall{
		{"/etc/p/session.json", guestFile{"file", 0, 0, 0644, config}},
		{"/etc/p/workspace.json", guestFile{"file", 0, 0, 0644, workspace}},
		{"/etc/p/git/ssh_config", guestFile{"file", 0, 0, 0644, sshConfig}},
		{"/etc/p/git/known_hosts", guestFile{"file", 0, 0, 0644, []byte("p " + key + "\n")}},
		{"/etc/p/git/identity", guestFile{"file", 1000, 1000, 0400, bytes.Clone(identity)}},
	}, nil
}

type guestInstall struct {
	path string
	file guestFile
}

// guestPlan is what a session's stopped runtime gets before its first
// start: its guest files, and the digest of the devShell activation material
// its image must already hold, empty for the base activation.
type guestPlan struct {
	files    []guestInstall
	material string
}

// installGuestPlan writes plan into the session's stopped runtime at
// incarnation at, or at any incarnation when at is empty, checking the
// runtime again around every write.
func (b *Backend) installGuestPlan(ctx context.Context, s Session, plan guestPlan, at runtimedriver.Incarnation) (Observation, error) {
	if err := b.CheckConfinement(ctx); err != nil {
		return Observation{}, err
	}
	o, err := b.Inspect(ctx, s)
	if err != nil {
		return o, err
	}
	if !o.Exists || o.Status != "Stopped" || !o.EndpointMounted {
		return o, errors.New("assembly requires owned stopped instance with endpoints")
	}
	if at != "" && incarnationOf(o) != at {
		return o, mismatch("the runtime is not the incarnation being assembled")
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}
	check := func() (Observation, error) {
		if err := b.CheckConfinement(ctx); err != nil {
			return Observation{}, err
		}
		got, err := b.Inspect(ctx, s)
		if err != nil {
			return got, err
		}
		if !got.Exists || got.Status != "Stopped" || !got.EndpointMounted || at != "" && incarnationOf(got) != at {
			return got, errors.New("instance changed during assembly")
		}
		return got, nil
	}
	if _, err := check(); err != nil {
		return o, err
	}
	if plan.material != "" {
		if err := verifyStoppedDevShellImage(ctx, api, plan.material); err != nil {
			return o, fmt.Errorf("private environment image material: %w", err)
		}
	}
	if err := ensureGuestEtc(ctx, api); err != nil {
		return o, err
	}
	if _, err := check(); err != nil {
		return o, err
	}
	for _, path := range []string{"/etc/p", "/etc/p/assets", "/etc/p/git"} {
		if _, err := check(); err != nil {
			return o, err
		}
		if err := ensureGuestFile(ctx, api, path, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
			return o, err
		}
		if _, err := check(); err != nil {
			return o, err
		}
	}
	// NixOS owns the visible links. Their presence and exact destination are
	// checked by the fixed image and runtime validation; assembly only writes
	// the mutable /etc/p/assets targets.
	for _, f := range plan.files {
		if _, err := check(); err != nil {
			return o, err
		}
		if err := ensureGuestFile(ctx, api, f.path, f.file); err != nil {
			return o, err
		}
		if _, err := check(); err != nil {
			return o, err
		}
	}
	return b.Inspect(ctx, s)
}

type stoppedImageReader interface {
	lstat(context.Context, string) (guestFile, error)
	getBounded(context.Context, string, int64) (guestFile, bool, error)
}

// Verify the immutable image material before installing any session-specific
// files. Incus HTTP GET follows RealPath, so SFTP LSTAT first rejects a
// symlink at every ancestor and leaf while the instance is stopped.
func verifyStoppedDevShellImage(ctx context.Context, api stoppedImageReader, digest string) error {
	for _, path := range []string{"/", "/etc", "/etc/p", "/etc/p/devshell"} {
		f, err := api.lstat(ctx, path)
		if err != nil || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode&022 != 0 || f.mode&0005 != 0005 {
			return errors.Join(err, fmt.Errorf("unsafe activation ancestor %s", path))
		}
	}
	read := func(path string, limit int64) ([]byte, error) {
		meta, err := api.lstat(ctx, path)
		if err != nil || meta.typ != "file" || meta.uid != 0 || meta.gid != 0 || meta.mode != 0444 {
			return nil, errors.Join(err, fmt.Errorf("unsafe activation file %s", path))
		}
		file, found, err := api.getBounded(ctx, path, limit)
		if err != nil || !found || file.typ != "file" || file.uid != 0 || file.gid != 0 || file.mode != 0444 {
			return nil, errors.Join(err, fmt.Errorf("activation file %s changed during read", path))
		}
		return file.data, nil
	}
	raw, err := read("/etc/p/devshell/material.json", int64(nixenv.MaxScript+nixenv.MaxJSON))
	if err != nil {
		return err
	}
	material, err := nixenv.ParseMaterial(raw)
	if err != nil {
		return err
	}
	got, err := material.Digest()
	if err != nil || got != digest {
		return errors.Join(err, errors.New("activation material digest changed"))
	}
	for _, expected := range []struct {
		path string
		data string
		max  int64
	}{
		{"/etc/p/devshell/activate.sh", material.Script, nixenv.MaxScript},
		{"/etc/p/devshell/.attrs.sh", material.AttrsSH, nixenv.MaxJSON},
		{"/etc/p/devshell/.attrs.json", material.AttrsJSON, nixenv.MaxJSON},
	} {
		data, err := read(expected.path, expected.max)
		if err != nil || !bytes.Equal(data, []byte(expected.data)) {
			return errors.Join(err, fmt.Errorf("activation asset %s changed", expected.path))
		}
	}
	return nil
}

// The pinned NixOS Incus image contains no /etc before its first boot.
// Create only that missing image bootstrap directory, beneath a verified
// root. An existing /etc must already be a safe real directory.
func ensureGuestEtc(ctx context.Context, api fileAPI) error {
	etc, exists, err := api.get(ctx, "/etc")
	if err != nil {
		return fmt.Errorf("inspect guest /etc: %w", err)
	}
	if !exists {
		root, rootExists, err := api.get(ctx, "/")
		if err != nil {
			return fmt.Errorf("inspect guest /: %w", err)
		}
		if !rootExists || root.typ != "directory" || root.uid != 0 || root.gid != 0 || root.mode != 0755 {
			return fmt.Errorf("guest / must match the image root before creating /etc (observed %s)", guestFileState(root, rootExists))
		}
		if err := ensureGuestFile(ctx, api, "/etc", guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
			return err
		}
		return nil
	}
	if etc.typ != "directory" || etc.uid != 0 || etc.gid != 0 || etc.mode&022 != 0 {
		return fmt.Errorf("guest /etc must be a root-owned real directory (observed %s)", guestFileState(etc, true))
	}
	return nil
}

func guestFileState(f guestFile, exists bool) string {
	if !exists {
		return "absent"
	}
	return fmt.Sprintf("type=%s uid=%d gid=%d mode=%04o", f.typ, f.uid, f.gid, f.mode)
}

func ensureGuestFile(ctx context.Context, api fileAPI, path string, want guestFile) error {
	current, exists, err := api.get(ctx, path)
	if err != nil {
		return fmt.Errorf("inspect guest %s: %w", path, err)
	}
	if exists {
		if current.typ != want.typ || current.uid != want.uid || current.gid != want.gid || current.mode != want.mode || want.typ == "file" && !bytes.Equal(current.data, want.data) {
			return fmt.Errorf("guest %s differs from trusted assembly", path)
		}
		return nil
	}
	if err := api.put(ctx, path, want); err != nil {
		return fmt.Errorf("install guest %s: %w", path, err)
	}
	current, exists, err = api.get(ctx, path)
	if err != nil || !exists || current.typ != want.typ || current.uid != want.uid || current.gid != want.gid || current.mode != want.mode || want.typ == "file" && !bytes.Equal(current.data, want.data) {
		return errors.Join(err, fmt.Errorf("guest %s installation postcondition failed", path))
	}
	return nil
}

// unixFileAPI uses only Incus's confined Unix socket and the fixed instance
// file endpoint. HTTP 404 alone means absent; permission, daemon and transport
// failures never become permission to create or replace a guest file.
type unixFileAPI struct{ socket, project, instance string }

func (a *unixFileAPI) request(ctx context.Context, method, path string, f guestFile) (*http.Response, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", a.socket)
	}}
	defer transport.CloseIdleConnections()
	u := url.URL{Scheme: "http", Host: "incus", Path: "/1.0/instances/" + url.PathEscape(a.instance) + "/files"}
	q := u.Query()
	q.Set("project", a.project)
	q.Set("path", path)
	u.RawQuery = q.Encode()
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader(f.data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("X-Incus-type", f.typ)
		req.Header.Set("X-Incus-uid", strconv.Itoa(f.uid))
		req.Header.Set("X-Incus-gid", strconv.Itoa(f.gid))
		req.Header.Set("X-Incus-mode", fmt.Sprintf("%04o", f.mode))
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	return client.Do(req)
}
func fileHeader(h http.Header, key string) string {
	if x := h.Get("X-Incus-" + key); x != "" {
		return x
	}
	return h.Get("X-LXD-" + key)
}
func (a *unixFileAPI) get(ctx context.Context, path string) (guestFile, bool, error) {
	return a.getBounded(ctx, path, 1<<20)
}
func (a *unixFileAPI) getBounded(ctx context.Context, path string, maxBytes int64) (guestFile, bool, error) {
	meta, exists, err := a.head(ctx, path, maxBytes)
	if err != nil || !exists || meta.typ != "file" {
		return meta, exists, err
	}
	resp, err := a.request(ctx, http.MethodGet, path, guestFile{})
	if err != nil {
		return guestFile{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return guestFile{}, false, fmt.Errorf("Incus file GET status %d", resp.StatusCode)
	}
	current, err := guestMetadata(resp.Header)
	if err != nil || current.typ != "file" || current.uid != meta.uid || current.gid != meta.gid || current.mode != meta.mode {
		return guestFile{}, false, errors.New("Incus file changed between HEAD and GET")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return guestFile{}, false, errors.New("Incus file exceeds bound")
	}
	meta2, exists, err := a.head(ctx, path, maxBytes)
	if err != nil || !exists || meta2.typ != meta.typ || meta2.uid != meta.uid || meta2.gid != meta.gid || meta2.mode != meta.mode {
		return guestFile{}, false, errors.New("Incus file changed during read")
	}
	meta.data = data
	return meta, true, nil
}
func guestMetadata(h http.Header) (guestFile, error) {
	typ := fileHeader(h, "type")
	uid, e1 := strconv.Atoi(fileHeader(h, "uid"))
	gid, e2 := strconv.Atoi(fileHeader(h, "gid"))
	mode, e3 := strconv.ParseInt(fileHeader(h, "mode"), 8, 32)
	if e1 != nil || e2 != nil || e3 != nil || (typ != "file" && typ != "directory" && typ != "symlink") {
		return guestFile{}, errors.New("Incus file metadata invalid")
	}
	return guestFile{typ, uid, gid, int(mode), nil}, nil
}
func (a *unixFileAPI) head(ctx context.Context, path string, maxBytes int64) (guestFile, bool, error) {
	resp, err := a.request(ctx, http.MethodHead, path, guestFile{})
	if err != nil {
		return guestFile{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return guestFile{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return guestFile{}, false, fmt.Errorf("Incus file HEAD status %d", resp.StatusCode)
	}
	meta, err := guestMetadata(resp.Header)
	if err != nil {
		return guestFile{}, false, err
	}
	if meta.typ == "file" {
		size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil || size < 0 || size > maxBytes {
			return guestFile{}, false, errors.New("Incus file size exceeds bound")
		}
	}
	return meta, true, nil
}
func (a *unixFileAPI) put(ctx context.Context, path string, f guestFile) error {
	resp, err := a.request(ctx, http.MethodPost, path, f)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("Incus file POST status %d", resp.StatusCode)
	}
	return nil
}

// deleteTree is used only by the builder image scrub with fixed, verified
// paths after the guest has stopped. Incus maps force to SFTP RemoveAll.
func (a *unixFileAPI) deleteTree(ctx context.Context, path string) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", a.socket)
	}}
	defer transport.CloseIdleConnections()
	u := url.URL{Scheme: "http", Host: "incus", Path: "/1.0/instances/" + url.PathEscape(a.instance) + "/files"}
	q := u.Query()
	q.Set("project", a.project)
	q.Set("path", path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Incus-force", "true")
	resp, err := (&http.Client{Transport: transport, Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("Incus file DELETE status %d", resp.StatusCode)
	}
	return nil
}
