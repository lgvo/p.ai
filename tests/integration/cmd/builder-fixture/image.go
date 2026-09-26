package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/environmentnix"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func runPublishedImageCase(ctx context.Context, binary, state string, b *runtimeincus.Backend, r runtimeincus.Builder, pipeline *environmentnix.Pipeline, result runtimeincus.BuilderNixResult) error {
	builder := "p-builder-" + r.RequestUUID
	// This unrooted temporary store object must disappear under the native
	// image GC. It is unrelated to the selected devShell.
	temporary, err := incus(ctx, binary, state, "exec", builder, "--user", "1000", "--group", "1000",
		"--env", "HOME=/home/p", "--env", "NIX_REMOTE=daemon", "--", "/bin/sh", "-c",
		"printf hook-extra > /home/p/p-hook-source && /run/current-system/sw/bin/nix-store --add /home/p/p-hook-source")
	if err != nil {
		return fmt.Errorf("seed unrooted store object: %w: %s", err, temporary)
	}
	unrooted := strings.TrimSpace(temporary)
	if !strings.HasPrefix(unrooted, "/nix/store/") || strings.ContainsAny(unrooted, "\r\n") {
		return fmt.Errorf("invalid temporary store path %q", unrooted)
	}
	handle, image, err := pipeline.PublishPrivateImage(ctx)
	if err != nil {
		return fmt.Errorf("private image publication: %w", err)
	}
	if handle.TargetKind != "incus-system-image" || handle.Locator != image.Fingerprint ||
		handle.ContentIdentity != result.MaterialDigest || image.BuilderCleanupPending {
		return fmt.Errorf("image handle or builder cleanup invalid: %+v %+v", handle, image)
	}
	listed, err := incus(ctx, binary, state, "image", "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("read private image inventory: %w: %s", err, listed)
	}
	var images []struct {
		Fingerprint string            `json:"fingerprint"`
		Public      bool              `json:"public"`
		Aliases     []json.RawMessage `json:"aliases"`
		Properties  map[string]string `json:"properties"`
	}
	if err := json.Unmarshal([]byte(listed), &images); err != nil {
		return err
	}
	matches := 0
	for _, got := range images {
		if got.Fingerprint != handle.Locator {
			continue
		}
		matches++
		if got.Public || len(got.Aliases) != 0 || len(got.Properties) != len(image.Properties) {
			return errors.New("private image metadata changed after native verification")
		}
		for key, value := range image.Properties {
			if got.Properties[key] != value {
				return fmt.Errorf("private image label %s changed", key)
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("private image fingerprint ambiguity: %d", matches)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = incus(cleanup, binary, state, "image", "delete", handle.Locator)
	}()
	names := []string{"p-image-a-" + r.RequestUUID, "p-image-b-" + r.RequestUUID}
	created := map[string]bool{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		for _, name := range names {
			if created[name] {
				_, _ = incus(cleanup, binary, state, "delete", "--force", name)
			}
		}
	}()
	for _, name := range names {
		out, err := incus(ctx, binary, state, "init", handle.Locator, name, "--profile", "default", "--storage", "default",
			"--config", "security.idmap.isolated=true", "--config", "security.privileged=false", "--config", "security.nesting=false")
		if err != nil {
			return fmt.Errorf("create independent image root %s: %w: %s", name, err, out)
		}
		created[name] = true
		if out, err = incus(ctx, binary, state, "start", name); err != nil {
			return fmt.Errorf("start image root %s: %w: %s", name, err, out)
		}
		if err := waitGuestReady(ctx, binary, state, name); err != nil {
			return fmt.Errorf("image root %s boot: %w", name, err)
		}
		// A printed environment output and its activation dependencies must
		// survive in both independent roots. The staged checkout and session
		// credentials must not be present.
		script := "test ! -e " + unrooted + " && " +
			"! /run/current-system/sw/bin/nix path-info " + unrooted + " >/dev/null 2>&1 && " +
			"test ! -e /opt/p/build/source && test ! -e /etc/p/git/identity && " +
			"test ! -e /etc/p/session.json && test ! -e /home/p/.p-devshell-capture && " +
			"test -L /nix/var/nix/gcroots/p-devshell && " +
			"test ! -e /workspace/p-hook-material-link && " +
			"/run/current-system/sw/bin/nix path-info " + result.CaptureStorePath + " >/dev/null && " +
			"source /etc/p/devshell/activate.sh && test \"$NATIVE_HOOK\" = ready && test \"$NATIVE_VALUE\" = ok && " +
			"test -e " + result.CaptureStorePath + " && test -L /workspace/p-hook-material-link"
		out, err = incus(ctx, binary, state, "exec", name, "--user", "1000", "--group", "1000", "--cwd", "/workspace",
			"--env", "HOME=/home/p", "--env", "USER=p", "--env", "LOGNAME=p", "--env", "NIX_REMOTE=daemon",
			"--", "/run/current-system/sw/bin/bash", "-c", script)
		if err != nil {
			return fmt.Errorf("image root %s activation/store/scrub: %w: %s", name, err, out)
		}
	}
	out, err := incus(ctx, binary, state, "exec", names[0], "--user", "1000", "--group", "1000", "--",
		"/bin/sh", "-c", "printf independent > /workspace/independent")
	if err != nil {
		return fmt.Errorf("write first image root: %w: %s", err, out)
	}
	out, err = incus(ctx, binary, state, "exec", names[1], "--user", "1000", "--group", "1000", "--",
		"/bin/sh", "-c", "test ! -e /workspace/independent")
	if err != nil {
		return fmt.Errorf("image roots share writable state: %w: %s", err, out)
	}
	added, err := incus(ctx, binary, state, "exec", names[0], "--user", "1000", "--group", "1000",
		"--env", "NIX_REMOTE=daemon", "--", "/bin/sh", "-c",
		"printf private-store > /workspace/private-store-source && /run/current-system/sw/bin/nix-store --add /workspace/private-store-source")
	if err != nil {
		return fmt.Errorf("write first private Nix store: %w: %s", err, added)
	}
	privatePath := strings.TrimSpace(added)
	if !strings.HasPrefix(privatePath, "/nix/store/") || strings.ContainsAny(privatePath, "\r\n") {
		return fmt.Errorf("invalid private store path %q", privatePath)
	}
	for _, name := range names {
		probe := "test -e " + privatePath + " && /run/current-system/sw/bin/nix path-info " + privatePath + " >/dev/null"
		if name == names[1] {
			probe = "test ! -e " + privatePath + " && ! /run/current-system/sw/bin/nix path-info " + privatePath + " >/dev/null 2>&1"
		}
		out, err := incus(ctx, binary, state, "exec", name, "--user", "1000", "--group", "1000",
			"--env", "NIX_REMOTE=daemon", "--", "/bin/sh", "-c", probe)
		if err != nil {
			return fmt.Errorf("private Nix store isolation in %s: %w: %s", name, err, out)
		}
	}
	if out, err := incus(ctx, binary, state, "stop", names[0], "--timeout", "30"); err != nil {
		return fmt.Errorf("stop first private root: %w: %s", err, out)
	}
	if out, err := incus(ctx, binary, state, "start", names[0]); err != nil {
		return fmt.Errorf("restart first private root: %w: %s", err, out)
	}
	if err := waitGuestReady(ctx, binary, state, names[0]); err != nil {
		return err
	}
	out, err = incus(ctx, binary, state, "exec", names[0], "--user", "1000", "--group", "1000",
		"--env", "NIX_REMOTE=daemon", "--", "/bin/sh", "-c",
		"test -e "+privatePath+" && /run/current-system/sw/bin/nix path-info "+privatePath+" >/dev/null")
	if err != nil {
		return fmt.Errorf("first private Nix store lost across restart: %w: %s", err, out)
	}
	if _, _, ok := pipeline.Image(); !ok {
		return errors.New("accepted pipeline lost verified image")
	}
	return nil
}
