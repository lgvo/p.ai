package runtimeincus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
)

const (
	builderNixBinary      = "/run/current-system/sw/bin/nix"
	builderNixSocket      = "/nix/var/nix/daemon-socket/socket"
	builderNixFlake       = "path:" + builderSource
	builderCaptureProfile = "/home/p/.p-devshell-capture"
	builderNixPolicy      = "nix-2.34.8;offline;pure-eval;accept-flake-config=false;flake-registry=empty;substituters=empty;no-lock-update;no-lock-write;sandbox=false"
)

var builderDrvPattern = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+\.drv$`)
var builderNarHashPattern = regexp.MustCompile(`^sha256-[A-Za-z0-9+/]{43}=$`)

// The commit is flake self.rev provenance. The NAR hash, derivation and
// committed-input digest remain the cache identity, so a source-only commit
// can reuse an otherwise identical environment image.
func builderLockedFlake(narHash, commit string) string {
	return builderNixFlake + "?narHash=" + url.QueryEscape(narHash) + "&rev=" + commit
}

// BuilderNixSelection is a core-owned resolution of a committed source. A
// base-only selection has no environment key or derivation to realize.
type BuilderNixSelection struct {
	Builder               Builder
	System                string
	BaseOnly              bool
	DevShellAttr          string
	DerivationPath        string
	SourceNarHash         string
	CommittedInputsDigest string
	KeyInputs             nixenv.KeyInputs
	KeyDigest             string
}

type BuilderNixResult struct {
	Selection        BuilderNixSelection
	Material         nixenv.Material
	MaterialDigest   string
	CaptureStorePath string
}

func validBuilderSystem(system string) bool {
	return system == "x86_64-linux" || system == "aarch64-linux"
}

// ResolveBuilderNix distinguishes an absent conventional devShell from a
// present invalid one using a boolean Nix expression, never error text.
func (b *Backend) ResolveBuilderNix(ctx context.Context, r Builder, system string) (BuilderNixSelection, error) {
	var out BuilderNixSelection
	if !validBuilderSystem(system) {
		return out, errors.New("unsupported builder Nix system")
	}
	out.Builder, out.System = r, system
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return BuilderNixSelection{}, err
	}
	if err := b.checkBuilderSystem(ctx, r, system); err != nil {
		return BuilderNixSelection{}, err
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}
	flake, exists, err := builderInput(ctx, api, builderSource+"/flake.nix")
	if err != nil {
		return BuilderNixSelection{}, err
	}
	if !exists {
		out.BaseOnly = true
		return out, nil
	}
	lock, lockExists, err := builderInput(ctx, api, builderSource+"/flake.lock")
	if err != nil {
		return BuilderNixSelection{}, err
	}
	if err := b.builderNixReady(ctx, r); err != nil {
		return BuilderNixSelection{}, err
	}
	return resolveBuilderNixInputs(r, system, flake, lock, lockExists, func(args ...string) ([]byte, error) {
		return b.builderNixExec(ctx, r, args...)
	})
}

func (b *Backend) checkBuilderSystem(ctx context.Context, r Builder, system string) error {
	if !validBuilderSystem(system) {
		return errors.New("unsupported requested builder system")
	}
	host, err := b.builderHostArchitecture(ctx)
	if err != nil {
		return err
	}
	if system != host+"-linux" {
		return errors.New("requested builder system differs from Incus host architecture")
	}
	images, err := b.imageList(ctx)
	if err != nil {
		return err
	}
	count := 0
	for _, image := range images {
		if image.Fingerprint != r.BaseImageFingerprint {
			continue
		}
		count++
		if image.Type != "container" || image.Architecture != strings.TrimSuffix(system, "-linux") {
			return errors.New("pinned builder base image architecture differs from requested system")
		}
	}
	if count != 1 {
		return errors.New("pinned builder base image architecture unavailable or ambiguous")
	}
	return nil
}

// The input bytes come from the sealed guest tree. The only executable
// operations supplied here are the fixed Nix commands below.
func resolveBuilderNixInputs(r Builder, system string, flake, lock guestFile, lockExists bool, execNix func(...string) ([]byte, error)) (BuilderNixSelection, error) {
	out := BuilderNixSelection{Builder: r, System: system, CommittedInputsDigest: digestBuilderInputs(flake, lock, lockExists)}
	raw, err := execNix("hash", "path", "--type", "sha256", "--format", "sri", builderSource)
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("builder source hash failed: %w", err)
	}
	out.SourceNarHash = strings.TrimSpace(string(raw))
	if !builderNarHashPattern.MatchString(out.SourceNarHash) {
		return BuilderNixSelection{}, errors.New("invalid builder source NAR hash")
	}
	lockedFlake := builderLockedFlake(out.SourceNarHash, r.CommitOID)
	attr := "devShells." + system + ".default"
	// All interpolated values are closed constants or a validated system.
	expr := `let flake = builtins.getFlake "` + lockedFlake + `"; shells = flake.devShells or {}; arch = shells."` + system + `" or {}; in builtins.hasAttr "default" arch`
	raw, err = execNix("eval", "--json", "--expr", expr)
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("default devShell presence evaluation failed: %w", err)
	}
	switch strings.TrimSpace(string(raw)) {
	case "false":
		out.BaseOnly = true
		return out, nil
	case "true":
	default:
		return BuilderNixSelection{}, errors.New("invalid Nix devShell presence response")
	}
	// A present value must evaluate as a derivation; its store path is the
	// immutable realization target, avoiding Nix CLI output fallback rules.
	drvExpr := `let flake = builtins.getFlake "` + lockedFlake + `"; shell = flake.devShells."` + system + `".default; in assert builtins.isAttrs shell && (shell.type or null) == "derivation" && (shell.system or null) == "` + system + `"; shell.drvPath`
	raw, err = execNix("eval", "--raw", "--expr", drvExpr)
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("present default devShell invalid: %w", err)
	}
	drv := strings.TrimSpace(string(raw))
	if !builderDrvPattern.MatchString(drv) {
		return BuilderNixSelection{}, errors.New("invalid resolved derivation path")
	}
	// The attribute can be forged by a flake. Require an actual store
	// derivation for the requested system before treating it as a shell.
	raw, err = execNix("derivation", "show", drv)
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("selected devShell derivation missing: %w", err)
	}
	if err := checkBuilderDerivation(raw, drv, system); err != nil {
		return BuilderNixSelection{}, err
	}
	out.DevShellAttr, out.DerivationPath = attr, drv
	policy := sha256.Sum256([]byte(builderNixPolicy))
	out.KeyInputs = nixenv.KeyInputs{
		AdapterVersion: nixenv.AdapterVersion, NixVersion: nixenv.NixVersion,
		System: system, ProjectPath: r.ProjectPath, BaseFingerprint: r.BaseImageFingerprint,
		RuntimeKitContract: "p.runtime-session/v2", CommittedInputsDigest: out.CommittedInputsDigest,
		DerivationPath: drv, NixPolicyDigest: hex.EncodeToString(policy[:]),
		ImageFormatVersion: builderImageFormatKey,
	}
	out.KeyDigest, err = out.KeyInputs.Digest()
	if err != nil {
		return BuilderNixSelection{}, err
	}
	return out, nil
}

// RealizeBuilderNix builds only the derivation selected above and captures
// pinned activation material. Publication, rooting and scrubbing are later
// stages; this method does not claim an image-ready builder.
func (b *Backend) RealizeBuilderNix(ctx context.Context, r Builder, selected BuilderNixSelection) (BuilderNixResult, error) {
	var out BuilderNixResult
	if selected.BaseOnly || selected.Builder != r || !validBuilderSystem(selected.System) ||
		selected.DevShellAttr != "devShells."+selected.System+".default" || !builderDrvPattern.MatchString(selected.DerivationPath) {
		return out, errors.New("invalid builder Nix selection")
	}
	current, err := b.ResolveBuilderNix(ctx, r, selected.System)
	if err != nil {
		return out, err
	}
	if current.BaseOnly || current.KeyDigest != selected.KeyDigest || current.DerivationPath != selected.DerivationPath || current.CommittedInputsDigest != selected.CommittedInputsDigest || current.SourceNarHash != selected.SourceNarHash {
		return out, errors.New("builder Nix selection changed before realization")
	}
	if _, err := b.builderNixExec(ctx, r, "build", "--no-link", selected.DerivationPath+"^*"); err != nil {
		return out, fmt.Errorf("builder Nix realization failed: %w", err)
	}
	// Recheck the selected derivation after build before any capture command.
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return out, err
	}
	flakeRef := builderLockedFlake(selected.SourceNarHash, r.CommitOID) + "#" + selected.DevShellAttr
	raw, err := b.builderNixExec(ctx, r, "print-dev-env", "--json", "--profile", builderCaptureProfile, flakeRef)
	if err != nil {
		return out, fmt.Errorf("builder Nix activation capture failed: %w", err)
	}
	env, err := nixenv.Parse(nixenv.NixVersion, raw)
	if err != nil {
		return out, err
	}
	material, err := nixenv.Render(env)
	if err != nil {
		return out, err
	}
	digest, err := material.Digest()
	if err != nil {
		return out, err
	}
	// Nix's --profile points through a generation to the actual -env output
	// produced by print-dev-env. This output, rather than the devShell out,
	// retains the activation closure. Prove the target is a live store object.
	path, err := b.builderGuestExec(ctx, r, "/run/current-system/sw/bin/readlink", "-f", builderCaptureProfile)
	if err != nil {
		return out, err
	}
	capture := strings.TrimSpace(string(path))
	if !builderStorePathPattern.MatchString(capture) || !strings.HasSuffix(capture, "-env") {
		return out, errors.New("invalid captured Nix environment store path")
	}
	if _, err := b.builderNixExec(ctx, r, "path-info", capture); err != nil {
		return out, fmt.Errorf("captured Nix environment is not valid in store: %w", err)
	}
	out = BuilderNixResult{Selection: current, Material: material, MaterialDigest: digest, CaptureStorePath: capture}
	return out, nil
}

var builderStorePathPattern = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+$`)

func (b *Backend) checkBuilderRunning(ctx context.Context, r Builder) error {
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	o, err := b.InspectBuilder(ctx, r)
	if err != nil {
		return err
	}
	if !o.Exists || !o.Ready || o.Status != "Running" {
		return errors.New("verified running builder required")
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}
	f, exists, err := api.head(ctx, builderSource, 0)
	if err != nil || !exists || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode != 0555 {
		return errors.Join(err, errors.New("builder source seal changed"))
	}
	return nil
}

func (b *Backend) builderNixReady(ctx context.Context, r Builder) error {
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastDaemonErr error
	for {
		if _, err := b.builderGuestExec(ready, r, "/bin/sh", "-c", "test -S "+builderNixSocket+" && test -x "+builderNixBinary); err == nil {
			// Socket existence alone can precede the daemon accepting
			// connections after Incus reports Running. The pinned Nix store
			// info command calls Store::connect(), a real daemon RPC.
			raw, probeErr := b.builderNixExec(ready, r, "store", "info", "--json")
			if probeErr == nil {
				var info struct {
					URL     string `json:"url"`
					Version string `json:"version"`
				}
				if json.Unmarshal(raw, &info) != nil || info.URL == "" || info.Version != nixenv.NixVersion {
					return errors.New("builder Nix daemon identity changed")
				}
				break
			}
			lastDaemonErr = probeErr
		}
		select {
		case <-ready.Done():
			return errors.Join(lastDaemonErr, errors.New("builder Nix daemon or binary unavailable after boot"))
		case <-ticker.C:
		}
	}
	raw, err := b.builderGuestExec(ctx, r, builderNixBinary, "--version")
	if err != nil || strings.TrimSpace(string(raw)) != "nix (Nix) "+nixenv.NixVersion {
		return errors.Join(err, errors.New("unsupported builder Nix version"))
	}
	raw, err = b.builderNixExec(ctx, r, "config", "show", "--json")
	if err != nil {
		return err
	}
	var settings map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &settings) != nil || string(settings["sandbox"].Value) != "false" {
		return errors.New("builder Nix sandbox policy changed")
	}
	return nil
}

func (b *Backend) builderNixExec(ctx context.Context, r Builder, command ...string) ([]byte, error) {
	args := []string{builderNixBinary, "--extra-experimental-features", "nix-command flakes", "--option", "accept-flake-config", "false", "--option", "pure-eval", "true", "--option", "flake-registry", "", "--option", "substituters", ""}
	args = append(args, command...)
	if len(command) > 0 && (command[0] == "eval" || command[0] == "build" || command[0] == "print-dev-env") {
		args = append(args, "--offline", "--no-update-lock-file", "--no-write-lock-file")
	}
	return b.builderGuestExec(ctx, r, args...)
}

func (b *Backend) builderGuestExec(ctx context.Context, r Builder, guestArgv ...string) ([]byte, error) {
	return b.builderGuestExecAt(ctx, r, builderSource, guestArgv...)
}

func (b *Backend) builderGuestExecAt(ctx context.Context, r Builder, cwd string, guestArgv ...string) ([]byte, error) {
	if cwd != builderSource && cwd != "/workspace" {
		return nil, errors.New("unsupported builder guest working directory")
	}
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return nil, err
	}
	args := []string{"exec", builderName(r), "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--cwd", cwd,
		"--env", "HOME=/home/p", "--env", "USER=p", "--env", "LOGNAME=p", "--env", "PATH=/run/current-system/sw/bin:/usr/bin:/bin", "--env", "LANG=C", "--env", "LC_ALL=C", "--env", "TERM=dumb",
		"--env", "NIX_PATH=", "--env", "NIX_CONFIG=", "--env", "NIX_USER_CONF_FILES=/dev/null", "--env", "NIX_REMOTE=daemon", "--env", "LD_PRELOAD=", "--env", "LD_LIBRARY_PATH=", "--env", "BASH_ENV=", "--"}
	args = append(args, guestArgv...)
	out, err := b.builderCommand(ctx, args...)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// Incus exec disconnect does not prove its guest process exited. Stop the
		// verified builder so a retry cannot overlap a prior Nix realization.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, stopErr := b.StopBuilder(cleanupCtx, r)
		if stopErr != nil {
			return nil, errors.Join(err, fmt.Errorf("builder may still be running; verified stop failed: %w", stopErr))
		}
		return nil, errors.Join(err, errors.New("builder stopped after interrupted guest command"))
	}
	return out, err
}

func builderInput(ctx context.Context, api *unixFileAPI, path string) (guestFile, bool, error) {
	f, exists, err := api.head(ctx, path, 1<<20)
	if err != nil || !exists {
		return f, exists, err
	}
	switch f.typ {
	case "file":
		return api.getBounded(ctx, path, 1<<20)
	case "symlink":
		return api.getSymlinkBounded(ctx, path, 4096)
	default:
		return guestFile{}, false, errors.New("builder flake input is not a file or symlink")
	}
}

func digestBuilderInputs(flake, lock guestFile, hasLock bool) string {
	h := sha256.New()
	for _, f := range []guestFile{flake, lock} {
		_, _ = h.Write([]byte(f.typ + "\x00"))
		_, _ = h.Write(f.data)
		_, _ = h.Write([]byte{0})
	}
	if !hasLock {
		_, _ = h.Write([]byte("no-lock"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func checkBuilderDerivation(raw []byte, path, system string) error {
	var shown struct {
		Version     int `json:"version"`
		Derivations map[string]struct {
			Version int                        `json:"version"`
			System  string                     `json:"system"`
			Outputs map[string]json.RawMessage `json:"outputs"`
		} `json:"derivations"`
	}
	if len(raw) == 0 || len(raw) > 2<<20 || json.Unmarshal(raw, &shown) != nil || shown.Version != 4 || len(shown.Derivations) != 1 {
		return errors.New("invalid selected devShell derivation response")
	}
	// StorePath::to_string() in Nix 2.34.8 emits the store basename in
	// derivation-show JSON, even when queried by absolute /nix/store path.
	derivation, ok := shown.Derivations[filepath.Base(path)]
	if !ok || derivation.Version != 4 || derivation.System != system || len(derivation.Outputs) == 0 {
		return errors.New("selected devShell is not a derivation for the requested system")
	}
	return nil
}
