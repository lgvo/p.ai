package runtimeincus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// AttachmentFailure carries a fixed diagnostic code for the trusted host log.
// Native transport errors and guest content stay out of that log and the RPC.
type AttachmentFailure struct {
	Code string
	Err  error
}

func (e *AttachmentFailure) Error() string { return "attachment verification failed: " + e.Code }
func (e *AttachmentFailure) Unwrap() error { return e.Err }

func attachmentFailure(code string, err error) error {
	return &AttachmentFailure{Code: code, Err: err}
}

// AttachSpec freshly checks ownership, host readiness, and the pinned trusted
// unit/adapter files. No repository or caller data becomes executable argv.
func (b *Backend) AttachSpec(ctx context.Context, s Session, plan plugin.AssetPlan) (plugin.AttachSpec, error) {
	var zero plugin.AttachSpec
	h, err := b.ObserveHost(ctx, s)
	if err != nil {
		return zero, attachmentFailure("initial-host-observation", err)
	}
	if !h.Exists || h.Status != "Running" || !h.Ready {
		return zero, attachmentFailure("host-not-ready", errors.New("interactive host is not ready"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}
	if err = verifyAttachAssets(ctx, attachmentFileAPI{api}, plan); err != nil {
		return zero, err
	}
	// Inspect again after the asset reads; it checks current Incus ownership.
	if final, e := b.ObserveHost(ctx, s); e != nil || !final.Ready {
		if e == nil {
			e = errors.New("host changed during attachment verification")
		}
		err = e
		return zero, attachmentFailure("final-host-observation", err)
	}
	return plugin.AttachSpec{Project: b.config.Project, Instance: b.name(s), Argv: []string{"/usr/libexec/p/attach"}}, nil
}

func verifyAttachAssets(ctx context.Context, api fileAPI, plan plugin.AssetPlan) error {
	for _, role := range []string{"p-interactive.service", "p-attach"} {
		var expected *plugin.AssetFile
		for i := range plan.Files {
			if plan.Files[i].Role == role {
				expected = &plan.Files[i]
			}
		}
		if expected == nil {
			return attachmentFailure(role+":plan-missing", errors.New("required host asset missing"))
		}
		f, resolved, e := resolveTrustedGuestFile(ctx, api, expected.Destination)
		if e != nil {
			var failure *AttachmentFailure
			if errors.As(e, &failure) {
				return attachmentFailure(role+":"+failure.Code, e)
			}
			return attachmentFailure(role+":file-api", e)
		}
		for _, check := range []struct {
			ok   bool
			code string
		}{
			{resolved == "/etc/p/assets/"+role, "destination"},
			{f.typ == "file", "type"},
			{f.uid == 0 && f.gid == 0, "owner"},
			{f.mode == int(expected.Mode), "mode"},
			{bytes.Equal(f.data, expected.Data), "content"},
		} {
			if !check.ok {
				return attachmentFailure(role+":"+check.code, errors.New("fixed host asset changed; repair required"))
			}
		}
	}
	return nil
}

// Resolve the image's fixed links (including NixOS's generated unit tree),
// checking every traversed parent. The API returns absolute resolved symlink
// targets; these are re-walked from root and cannot bypass ownership checks.
func resolveTrustedGuestFile(ctx context.Context, api fileAPI, name string) (guestFile, string, error) {
	current := path.Clean(name)
	for links := 0; links < 16; links++ {
		if !strings.HasPrefix(current, "/") {
			return guestFile{}, "", attachmentFailure("relative-path", errors.New("relative host asset"))
		}
		parts := strings.Split(strings.TrimPrefix(current, "/"), "/")
		prefix := "/"
		root, exists, e := api.get(ctx, prefix)
		if e != nil || !exists || root.typ != "directory" || root.uid != 0 || root.gid != 0 || root.mode&022 != 0 {
			return guestFile{}, "", attachmentFailure("root", errors.New("untrusted guest root"))
		}
		redirected := false
		for i, part := range parts {
			prefix = path.Join(prefix, part)
			f, exists, e := api.get(ctx, prefix)
			if e != nil {
				return guestFile{}, "", e
			}
			if !exists || f.uid != 0 {
				return guestFile{}, "", attachmentFailure("path-owner", errors.New("host asset path is not root-owned"))
			}
			if f.typ == "symlink" {
				target := string(f.data)
				if !strings.HasPrefix(target, "/") || len(target) > 4096 || strings.ContainsRune(target, 0) {
					return guestFile{}, "", attachmentFailure("link-target", errors.New("invalid fixed asset link"))
				}
				current = path.Join(append([]string{target}, parts[i+1:]...)...)
				redirected = true
				break
			}
			if i == len(parts)-1 {
				return f, prefix, nil
			}
			writable := f.mode&022 != 0
			if prefix == "/nix/store" && f.mode&01000 != 0 && f.mode&0002 == 0 {
				writable = false
			}
			if f.typ != "directory" || writable {
				return guestFile{}, "", attachmentFailure("ancestor", errors.New("untrusted host asset ancestor"))
			}
		}
		if !redirected {
			break
		}
	}
	return guestFile{}, "", attachmentFailure("link-cycle", errors.New("fixed host asset link cycle"))
}

// Attachment verification needs the fixed image symlink target. Keep the
// assembly API's conservative metadata-only handling of symlinks unchanged.
type attachmentFileAPI struct{ *unixFileAPI }

func (a attachmentFileAPI) get(ctx context.Context, name string) (guestFile, bool, error) {
	meta, exists, err := a.unixFileAPI.get(ctx, name)
	if err == nil && exists && name == "/nix/store" {
		// Incus's HTTP file endpoint serializes Mode().Perm(), which drops the
		// sticky bit on NixOS's 1775 store. Read this one ancestor through the
		// same confined instance's native SFTP LSTAT before accepting it.
		full, e := a.storeDirectoryMode(ctx)
		if e != nil || full.typ != meta.typ || full.uid != meta.uid || full.gid != meta.gid || full.mode&0777 != meta.mode {
			return guestFile{}, false, attachmentFailure("store-metadata", errors.New("Nix store metadata differs between Incus APIs"))
		}
		meta.mode = full.mode
	}
	if err != nil || !exists || meta.typ != "symlink" {
		return meta, exists, err
	}
	resp, err := a.request(ctx, http.MethodGet, name, guestFile{})
	if err != nil {
		return guestFile{}, false, err
	}
	defer resp.Body.Close()
	current, e := guestMetadata(resp.Header)
	if resp.StatusCode != http.StatusOK || e != nil || current.typ != meta.typ || current.uid != meta.uid || current.gid != meta.gid || current.mode != meta.mode {
		return guestFile{}, false, errors.New("fixed asset symlink changed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 {
		return guestFile{}, false, errors.New("fixed asset symlink target exceeds bound")
	}
	after, exists, e := a.head(ctx, name, 4096)
	if e != nil || !exists || after.typ != meta.typ || after.uid != meta.uid || after.gid != meta.gid || after.mode != meta.mode {
		return guestFile{}, false, errors.New("fixed asset symlink changed")
	}
	meta.data = data
	return meta, true, nil
}

// ValidateAttachmentOperation repeats the live-channel check at the final
// daemon promotion boundary, after potentially slow readiness/asset reads.
func (b *Backend) ValidateAttachmentOperation(ctx context.Context, s Session, operation string) error {
	if !regexp.MustCompile(`^/1\.0/operations/[0-9a-f-]{36}$`).MatchString(operation) {
		return errors.New("invalid attachment operation")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", b.config.UserSocket)
	}}
	defer transport.CloseIdleConnections()
	u := url.URL{Scheme: "http", Host: "incus", Path: operation}
	q := u.Query()
	q.Set("project", b.config.Project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Incus redirect refused") }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return err
	}
	var result struct {
		Type     string `json:"type"`
		Metadata struct {
			Class      string              `json:"class"`
			StatusCode int                 `json:"status_code"`
			Resources  map[string][]string `json:"resources"`
			Metadata   struct {
				Command     []string `json:"command"`
				Interactive bool     `json:"interactive"`
			} `json:"metadata"`
		} `json:"metadata"`
	}
	if resp.StatusCode != http.StatusOK || len(raw) > 65536 || json.Unmarshal(raw, &result) != nil || result.Type != "sync" || result.Metadata.Class != "websocket" || result.Metadata.StatusCode != 103 {
		return errors.New("attachment operation is not running")
	}
	if !result.Metadata.Metadata.Interactive || len(result.Metadata.Metadata.Command) != 1 || result.Metadata.Metadata.Command[0] != "/usr/libexec/p/attach" {
		return errors.New("attachment operation command changed")
	}
	instances := result.Metadata.Resources["instances"]
	if len(instances) != 1 {
		return errors.New("attachment operation scope changed")
	}
	resource, err := url.Parse(instances[0])
	if err != nil || resource.IsAbs() || resource.Host != "" || resource.Path != "/1.0/instances/"+b.name(s) || resource.Query().Get("project") != "" && resource.Query().Get("project") != b.config.Project {
		return errors.New("attachment operation belongs to another runtime")
	}
	return nil
}
