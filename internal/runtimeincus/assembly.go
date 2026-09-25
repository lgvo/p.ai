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
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimekit"
	"golang.org/x/crypto/ssh"
)

// Assembly is core-selected immutable input for one stopped instance. The
// plugin sees none of these bytes or paths and cannot choose installation
// targets. No origin credential belongs here.
type Assembly struct {
	HostAssets      plugin.AssetPlan
	SourceAssets    plugin.AssetPlan
	AgentAssets     *plugin.AssetPlan
	SessionConfig   runtimekit.Config
	Workspace       runtimekit.WorkspaceConfig
	Identity        []byte
	ServerPublicKey string
}

type guestFile struct {
	typ            string
	uid, gid, mode int
	data           []byte
}
type fileAPI interface {
	get(context.Context, string) (guestFile, bool, error)
	put(context.Context, string, guestFile) error
}

func validateAssembly(a Assembly) ([]guestInstall, error) {
	if a.HostAssets.Schema != "p.asset-plan/v1" || a.SourceAssets.Schema != "p.asset-plan/v1" || a.HostAssets.PackageID == "" || a.SourceAssets.PackageID == "" || a.HostAssets.PackageID == a.SourceAssets.PackageID || a.HostAssets.Scope != "internal-session" || a.SourceAssets.Scope != "internal-session" {
		return nil, errors.New("invalid selected asset plans")
	}
	if !fingerprintPattern.MatchString(a.HostAssets.PackageSHA256) || !fingerprintPattern.MatchString(a.SourceAssets.PackageSHA256) {
		return nil, errors.New("invalid selected package digest")
	}
	if a.AgentAssets != nil && (a.AgentAssets.Schema != "p.asset-plan/v1" || a.AgentAssets.Scope != "internal-session" || a.AgentAssets.PackageID == "" || a.AgentAssets.PackageID == a.HostAssets.PackageID || a.AgentAssets.PackageID == a.SourceAssets.PackageID || !fingerprintPattern.MatchString(a.AgentAssets.PackageSHA256)) {
		return nil, errors.New("invalid selected agent asset plan")
	}
	config, err := json.Marshal(a.SessionConfig)
	if err != nil {
		return nil, err
	}
	if _, err := runtimekit.ParseConfig(config); err != nil {
		return nil, err
	}
	if a.AgentAssets == nil && a.SessionConfig.AgentSHA256 != "" || a.AgentAssets != nil && (len(a.AgentAssets.Files) != 1 || a.SessionConfig.AgentSHA256 != a.AgentAssets.Files[0].SHA256) {
		return nil, errors.New("selected agent asset differs from runtime config")
	}
	workspace, err := json.Marshal(a.Workspace)
	if err != nil {
		return nil, err
	}
	if _, err := runtimekit.ParseWorkspaceConfig(workspace); err != nil {
		return nil, err
	}
	if a.Workspace.Schema == "p.workspace/v2" || a.Workspace.Schema == "p.workspace/v3" {
		if a.Workspace.EnvironmentSelection == "devshell" && a.SessionConfig.Activation != "devshell" ||
			(a.Workspace.EnvironmentSelection == "base-no-flake" || a.Workspace.EnvironmentSelection == "base-no-default" || a.Workspace.EnvironmentSelection == "base-recorded") && a.SessionConfig.Activation != "base" {
			return nil, errors.New("workspace environment differs from runtime activation")
		}
	}
	if len(a.Identity) == 0 || len(a.Identity) > 16<<10 || bytes.IndexByte(a.Identity, 0) >= 0 {
		return nil, errors.New("invalid session identity")
	}
	block, rest := pem.Decode(a.Identity)
	if block == nil || (block.Type != "PRIVATE KEY" && block.Type != "OPENSSH PRIVATE KEY") || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid session private key PEM")
	}
	signer, err := ssh.ParsePrivateKey(a.Identity)
	if err != nil || signer.PublicKey().Type() != "ssh-ed25519" {
		return nil, errors.New("unsupported session private key")
	}
	key := strings.TrimSpace(a.ServerPublicKey)
	pub, _, options, restKey, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || pub == nil || len(options) != 0 || len(bytes.TrimSpace(restKey)) != 0 || pub.Type() != "ssh-ed25519" || len(key) > 4096 {
		return nil, errors.New("invalid pinned Git host key")
	}
	key = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	sshConfig := []byte("Host p\n  HostName p\n  User git\n  HostKeyAlias p\n  IdentityFile /etc/p/git/identity\n  IdentitiesOnly yes\n  IdentityAgent none\n  UserKnownHostsFile /etc/p/git/known_hosts\n  GlobalKnownHostsFile /dev/null\n  StrictHostKeyChecking yes\n  BatchMode yes\n  ConnectTimeout 5\n  ProxyCommand /usr/libexec/p/runtime-kit git-stream\n")
	files := []guestInstall{}
	appendPlan := func(plan plugin.AssetPlan, expected map[string]guestInstall) error {
		if len(plan.Files) != len(expected) {
			return errors.New("asset role set incomplete")
		}
		seen := map[string]bool{}
		visible := map[string]string{"p-session.target": "/etc/systemd/system/p-session.target", "p-interactive.service": "/etc/systemd/system/p-interactive.service", "p-attach": "/usr/libexec/p/attach", "p-git-ssh": "/usr/libexec/p/git-ssh", "p-codex-adapter": "/usr/libexec/p/codex-adapter"}
		for _, f := range plan.Files {
			want, ok := expected[f.Role]
			if !ok || seen[f.Role] || f.Destination != visible[f.Role] || f.Mode != uint32(want.file.mode) || len(f.Data) == 0 || len(f.Data) > 1<<20 {
				return errors.New("asset role, destination, or mode changed")
			}
			h := sha256.Sum256(f.Data)
			if hex.EncodeToString(h[:]) != f.SHA256 {
				return errors.New("asset bytes differ from selected digest")
			}
			want.file.data = bytes.Clone(f.Data)
			files = append(files, want)
			seen[f.Role] = true
		}
		return nil
	}
	host := map[string]guestInstall{
		"p-session.target":      {"/etc/p/assets/p-session.target", guestFile{"file", 0, 0, 0644, nil}},
		"p-interactive.service": {"/etc/p/assets/p-interactive.service", guestFile{"file", 0, 0, 0644, nil}},
		"p-attach":              {"/etc/p/assets/p-attach", guestFile{"file", 0, 0, 0555, nil}},
	}
	source := map[string]guestInstall{"p-git-ssh": {"/etc/p/assets/p-git-ssh", guestFile{"file", 0, 0, 0555, nil}}}
	if err := appendPlan(a.HostAssets, host); err != nil {
		return nil, err
	}
	if err := appendPlan(a.SourceAssets, source); err != nil {
		return nil, err
	}
	if a.AgentAssets != nil {
		agent := map[string]guestInstall{"p-codex-adapter": {"/etc/p/assets/p-codex-adapter", guestFile{"file", 0, 0, 0555, nil}}}
		if err := appendPlan(*a.AgentAssets, agent); err != nil {
			return nil, err
		}
	}
	files = append(files,
		guestInstall{"/etc/p/session.json", guestFile{"file", 0, 0, 0644, config}},
		guestInstall{"/etc/p/workspace.json", guestFile{"file", 0, 0, 0644, workspace}},
		guestInstall{"/etc/p/git/ssh_config", guestFile{"file", 0, 0, 0644, sshConfig}},
		guestInstall{"/etc/p/git/known_hosts", guestFile{"file", 0, 0, 0644, []byte("p " + key + "\n")}},
		guestInstall{"/etc/p/git/identity", guestFile{"file", 1000, 1000, 0400, bytes.Clone(a.Identity)}},
	)
	return files, nil
}

type guestInstall struct {
	path string
	file guestFile
}

// Assemble never writes a running or unowned instance. Missing guest files
// are installed; identical complete files are retained; any other state is
// refused without deleting or overwriting it.
func (b *Backend) Assemble(ctx context.Context, s Session, a Assembly) (Observation, error) {
	files, err := validateAssembly(a)
	if err != nil {
		return Observation{}, err
	}
	if s.ProjectPath != a.Workspace.Repository || s.AssignedBranch != a.Workspace.Branch || s.InitialOID != a.Workspace.InitialOID {
		return Observation{}, errors.New("assembly workspace differs from bound session intent")
	}
	if a.Workspace.Schema == "p.workspace/v3" && a.Workspace.ImageFingerprint != s.ImageFingerprint {
		return Observation{}, errors.New("repair workspace image differs from bound runtime")
	}
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
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}
	check := func() (Observation, error) {
		if err := b.CheckConfinement(ctx); err != nil {
			return Observation{}, err
		}
		got, err := b.Inspect(ctx, s)
		if err != nil {
			return got, err
		}
		if !got.Exists || got.Status != "Stopped" || !got.EndpointMounted {
			return got, errors.New("instance changed during assembly")
		}
		return got, nil
	}
	if _, err := check(); err != nil {
		return o, err
	}
	if a.SessionConfig.Activation == "devshell" {
		if err := verifyStoppedDevShellImage(ctx, api, a.SessionConfig.MaterialSHA256); err != nil {
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
	for _, f := range files {
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
