package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// attachmentError refuses a terminal with a fixed diagnostic code. Native
// transport errors and guest content stay out of its text.
type attachmentError struct {
	code string
	err  error
}

func (e *attachmentError) Error() string { return "attachment verification failed: " + e.code }
func (e *attachmentError) Unwrap() error { return e.err }

func attachmentFailure(code string, err error) error {
	return &attachmentError{code: code, err: err}
}

// attachLinks are the image's fixed links a terminal relies on, by asset
// role: the session host's unit and its attach program.
var attachLinks = []struct{ role, link string }{
	{"p-interactive.service", "/etc/systemd/system/p-interactive.service"},
	{"p-attach", attachProgram},
}

// verifyAttachAssets checks that each of the image's attach links
// resolves, through root-owned paths only, to the session's asset of its
// role, installed unchanged.
func verifyAttachAssets(ctx context.Context, api fileAPI, assets []runtimedriver.Asset) error {
	for _, l := range attachLinks {
		i := slices.IndexFunc(assets, func(a runtimedriver.Asset) bool { return a.Role == l.role })
		if i < 0 {
			return attachmentFailure(l.role+":asset-missing", errors.New("required host asset missing"))
		}
		want, err := assetInstall(l.role, assets[i].Mode, assets[i].Data, assets[i].SHA256)
		if err != nil {
			return attachmentFailure(l.role+":asset-invalid", err)
		}
		f, resolved, err := resolveTrustedGuestFile(ctx, api, l.link)
		if err != nil {
			var failure *attachmentError
			if errors.As(err, &failure) {
				return attachmentFailure(l.role+":"+failure.code, err)
			}
			return attachmentFailure(l.role+":file-api", err)
		}
		for _, check := range []struct {
			ok   bool
			code string
		}{
			{resolved == want.path, "destination"},
			{f.typ == "file", "type"},
			{f.uid == 0 && f.gid == 0, "owner"},
			{f.mode == want.file.mode, "mode"},
			{bytes.Equal(f.data, want.file.data), "content"},
		} {
			if !check.ok {
				return attachmentFailure(l.role+":"+check.code, errors.New("fixed host asset changed; repair required"))
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
