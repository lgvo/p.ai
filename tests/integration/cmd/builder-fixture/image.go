package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	runtimeincus "github.com/lgvo/p.ai/internal/runtimedriver/incus"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// runImageCase builds the commit's image through the image driver, starts
// two independent roots from it, and deletes it.
func runImageCase(ctx context.Context, source sourcedriver.Driver, binary, state string, b *runtimeincus.Backend, base, commit string) error {
	src, err := exportCommit(ctx, source, state, commit)
	if err != nil {
		return err
	}
	uuid, err := newUUID()
	if err != nil {
		return err
	}
	d := runtimeincus.NewDriver(b)
	labels := runtimedriver.Labels{
		runtimedriver.LabelProject: project, runtimedriver.LabelSourceCommit: commit,
		runtimedriver.LabelBaseImage: base, runtimedriver.LabelSystem: "x86_64-linux",
		runtimedriver.LabelBuilderVersion: d.Describe().ImageBuilder, runtimedriver.LabelBuild: uuid,
	}
	gates := 0
	img, err := d.BuildImage(ctx, runtimedriver.Build{Ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: uuid}, Labels: labels, Source: src}, func() error { gates++; return nil })
	if err != nil {
		return fmt.Errorf("commit image build: %w", err)
	}
	deleted := false
	defer func() {
		if !deleted {
			cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, _ = incus(cleanup, binary, state, "image", "delete", string(img.Ref))
		}
	}()
	if gates != 1 || img.Shell.Selection != runtimedriver.SelectionDevShell || img.Shell.MaterialSHA256 == "" || img.Size <= 0 || !maps.Equal(img.Labels, labels) {
		return fmt.Errorf("built image differs from its build: gates=%d %+v", gates, img)
	}
	r := runtimeincus.Builder{RequestUUID: uuid, ProjectPath: project, CommitOID: commit, BaseImageFingerprint: base, ContractVersion: "1"}
	if o, err := b.InspectBuilder(ctx, r); err != nil || o.Exists {
		return fmt.Errorf("builder left behind after its image: %+v %v", o, err)
	}
	listed, err := d.Images(ctx)
	if err != nil {
		return fmt.Errorf("list commit images: %w", err)
	}
	found := 0
	for _, got := range listed {
		if got.Ref == img.Ref {
			found++
			if !maps.Equal(got.Labels, labels) || got.Shell != img.Shell {
				return fmt.Errorf("listed image differs from the build: %+v", got)
			}
		}
	}
	if found != 1 {
		return fmt.Errorf("commit image listed %d times", found)
	}
	capture, err := checkImageProperties(ctx, binary, state, img, commit, uuid)
	if err != nil {
		return err
	}
	names := []string{"p-image-a-" + uuid, "p-image-b-" + uuid}
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
	// The hook's own store object, rooted only from the builder's workspace,
	// must not survive into the image. The first root finds its path.
	var hookObject string
	for i, name := range names {
		out, err := incus(ctx, binary, state, "init", string(img.Ref), name, "--profile", "default", "--storage", "default",
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
		if out, err := guestUser(ctx, binary, state, name, cloneCheck(commit)); err != nil {
			return fmt.Errorf("image root %s workspace is not the commit's clone: %w: %s", name, err, out)
		}
		// A printed environment output and its activation dependencies must
		// survive in both independent roots. The bundle, the builder's
		// scratch and session credentials must not be present.
		script := "test ! -e /opt/p/build && test ! -e /etc/p/git/identity && " +
			"test ! -e /etc/p/session.json && test ! -e /home/p/.p-devshell-capture && " +
			"test -L /nix/var/nix/gcroots/p-devshell && " +
			"test ! -e /workspace/p-hook-material-link && " +
			"/run/current-system/sw/bin/nix path-info " + capture + " >/dev/null && " +
			"source /etc/p/devshell/activate.sh && test \"$NATIVE_HOOK\" = ready && test \"$NATIVE_VALUE\" = ok && " +
			"test -e " + capture + " && test -L /workspace/p-hook-material-link && " +
			"printf 'P_HOOK_OBJECT=%s\\n' \"$(readlink /workspace/p-hook-root)\""
		if i > 0 {
			script = "test ! -e " + hookObject + " && ! /run/current-system/sw/bin/nix path-info " + hookObject + " >/dev/null 2>&1 && " + script
		}
		out, err = guestUser(ctx, binary, state, name, script)
		if err != nil {
			return fmt.Errorf("image root %s activation/store/scrub: %w: %s", name, err, out)
		}
		if i == 0 {
			if hookObject, err = markedStorePath(out, "P_HOOK_OBJECT="); err != nil {
				return err
			}
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
	for _, name := range names {
		if out, err := incus(ctx, binary, state, "delete", "--force", name); err != nil {
			return fmt.Errorf("delete image root %s: %w: %s", name, err, out)
		}
		created[name] = false
	}
	gates = 0
	if err := d.DeleteImage(ctx, img, func() error { gates++; return nil }); err != nil || gates != 1 {
		return fmt.Errorf("delete commit image: gates=%d %w", gates, err)
	}
	deleted = true
	if _, found, err := d.InspectImage(ctx, img.Ref); err != nil || found {
		return fmt.Errorf("commit image remains after its delete: found=%t %v", found, err)
	}
	return nil
}

// checkImageProperties reads the image as Incus lists it: private, with no
// alias, labeled with the commit, the build and the devShell outcome. It
// returns the captured environment's store path.
func checkImageProperties(ctx context.Context, binary, state string, img runtimedriver.Image, commit, build string) (string, error) {
	listed, err := incus(ctx, binary, state, "image", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("read private image inventory: %w: %s", err, listed)
	}
	var images []struct {
		Fingerprint string            `json:"fingerprint"`
		Public      bool              `json:"public"`
		Aliases     []json.RawMessage `json:"aliases"`
		Properties  map[string]string `json:"properties"`
	}
	if err := json.Unmarshal([]byte(listed), &images); err != nil {
		return "", err
	}
	var capture string
	matches := 0
	for _, got := range images {
		if got.Fingerprint != string(img.Ref) {
			continue
		}
		matches++
		p := got.Properties
		if got.Public || len(got.Aliases) != 0 || p["p.contract"] != "p.incus-commit-image/v1" || p["p.source_commit"] != commit ||
			p["p.build"] != build || p["p.selection"] != "devshell" || p["p.material"] != img.Shell.MaterialSHA256 {
			return "", errors.New("commit image is public, aliased or labeled differently from its build")
		}
		capture = p["p.capture_store_path"]
	}
	if matches != 1 || !strings.HasPrefix(capture, "/nix/store/") || !strings.HasSuffix(capture, "-env") || strings.ContainsAny(capture, " \t\r\n") {
		return "", fmt.Errorf("commit image missing, ambiguous or without its capture: %d %q", matches, capture)
	}
	return capture, nil
}

// markedStorePath takes the store path a guest script printed after marker.
func markedStorePath(out, marker string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), marker); ok {
			if !strings.HasPrefix(path, "/nix/store/") || strings.ContainsAny(path, " \t") {
				return "", fmt.Errorf("invalid store path %q", path)
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("guest printed no %s line: %s", marker, out)
}
