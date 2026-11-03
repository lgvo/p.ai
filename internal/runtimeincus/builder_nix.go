package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

const (
	builderNixBinary = "/run/current-system/sw/bin/nix"
	builderGitBinary = "/run/current-system/sw/bin/git"
	builderNixSocket = "/nix/var/nix/daemon-socket/socket"
	// Nix exports the flake from the clone's Git objects at the commit, so the
	// working tree cannot change what it evaluates.
	builderNixFlake       = "git+file://" + builderWorkspace
	builderCaptureProfile = "/home/p/.p-devshell-capture"
	// The base image lists this cache as its one trusted substituter.
	builderPublicCache = "https://cache.nixos.org"
	// builderNixRules are the Nix rules every builder follows whatever its
	// network: the flags builderNixExec passes and the sandbox setting
	// builderNixReady checks. They are part of how an image is built, so a
	// change to them must also change builderNixRulesVersion.
	builderNixRules        = "pure-eval;accept-flake-config=false;flake-registry=empty;no-lock-update;no-lock-write;sandbox=false"
	builderNixRulesVersion = "rules/1"
)

// builderNix is how a builder runs Nix. A builder without a NIC stays
// offline with no substituters; one with public egress fetches flake inputs
// and substitutes from the public cache.
type builderNix struct {
	policy       string
	substituters string
	offline      bool
}

var (
	offlineBuilderNix = builderNix{policy: "nix-" + nixenv.NixVersion + ";offline;" + builderNixRules + ";substituters=empty", offline: true}
	publicBuilderNix  = builderNix{policy: "nix-" + nixenv.NixVersion + ";public-egress;" + builderNixRules + ";substituters=" + builderPublicCache, substituters: builderPublicCache}
)

func builderNixFor(r Builder) builderNix {
	if r.PublicIPv4 != "" {
		return publicBuilderNix
	}
	return offlineBuilderNix
}

var builderDrvPattern = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+\.drv$`)
var builderNarHashPattern = regexp.MustCompile(`^sha256-[A-Za-z0-9+/]{43}=$`)

// builderFlakeAt names the commit in the workspace clone before its NAR
// hash is known; builderLockedFlake pins both.
func builderFlakeAt(commit string) string { return builderNixFlake + "?rev=" + commit }

func builderLockedFlake(narHash, commit string) string {
	return builderNixFlake + "?narHash=" + url.QueryEscape(narHash) + "&rev=" + commit
}

// BuilderNixSelection is what a builder resolved at the commit: no flake, a
// flake without a default devShell for the system, or that devShell's
// derivation.
type BuilderNixSelection struct {
	Builder        Builder
	System         string
	Selection      runtimedriver.Selection
	DevShellAttr   string // devshell only
	DerivationPath string // devshell only
	SourceNarHash  string // empty for no-flake
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

// ResolveBuilderNix reads the commit's flake from the workspace clone. It
// distinguishes an absent conventional devShell from a present invalid one
// using a boolean Nix expression, never error text.
func (b *Backend) ResolveBuilderNix(ctx context.Context, r Builder, system string) (BuilderNixSelection, error) {
	if !validBuilderSystem(system) {
		return BuilderNixSelection{}, errors.New("unsupported builder Nix system")
	}
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return BuilderNixSelection{}, err
	}
	if err := b.checkBuilderSystem(ctx, r, system); err != nil {
		return BuilderNixSelection{}, err
	}
	entry, err := b.builderGit(ctx, r, "ls-tree", "-z", r.CommitOID, "--", "flake.nix")
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("read the commit's flake.nix entry: %w", err)
	}
	present, err := parseFlakeEntry(entry)
	if err != nil {
		return BuilderNixSelection{}, err
	}
	if !present {
		return BuilderNixSelection{Builder: r, System: system, Selection: runtimedriver.SelectionNoFlake}, nil
	}
	if err := b.builderNixReady(ctx, r); err != nil {
		return BuilderNixSelection{}, err
	}
	return resolveBuilderNixInputs(r, system, func(args ...string) ([]byte, error) {
		return b.builderNixExec(ctx, r, args...)
	})
}

// parseFlakeEntry reads `git ls-tree -z <commit> -- flake.nix`: nothing, or
// the one top-level entry, which must be a file or a symlink.
func parseFlakeEntry(raw []byte) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	line, ok := strings.CutSuffix(string(raw), "\x00")
	meta, name, tab := strings.Cut(line, "\t")
	fields := strings.Fields(meta)
	if !ok || !tab || name != "flake.nix" || len(fields) != 3 || !validBuilderOID(fields[2]) {
		return false, errors.New("invalid flake.nix tree entry")
	}
	switch fields[0] + " " + fields[1] {
	case "100644 blob", "100755 blob", "120000 blob":
		return true, nil
	}
	return false, errors.New("the commit's flake.nix is not a file")
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

// The only executable operations supplied here are the fixed Nix commands
// below, all of them on the commit in the workspace clone.
func resolveBuilderNixInputs(r Builder, system string, execNix func(...string) ([]byte, error)) (BuilderNixSelection, error) {
	out := BuilderNixSelection{Builder: r, System: system}
	raw, err := execNix("flake", "metadata", "--json", builderFlakeAt(r.CommitOID))
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("builder flake metadata failed: %w", err)
	}
	out.SourceNarHash, err = parseFlakeNarHash(raw, r.CommitOID)
	if err != nil {
		return BuilderNixSelection{}, err
	}
	lockedFlake := builderLockedFlake(out.SourceNarHash, r.CommitOID)
	attr := "devShells." + system + ".default"
	// All interpolated values are closed constants, a validated system, a
	// validated commit and an SRI hash.
	expr := `let flake = builtins.getFlake "` + lockedFlake + `"; shells = flake.devShells or {}; arch = shells."` + system + `" or {}; in builtins.hasAttr "default" arch`
	raw, err = execNix("eval", "--json", "--expr", expr)
	if err != nil {
		return BuilderNixSelection{}, fmt.Errorf("default devShell presence evaluation failed: %w", err)
	}
	switch strings.TrimSpace(string(raw)) {
	case "false":
		out.Selection = runtimedriver.SelectionNoDefault
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
	out.Selection, out.DevShellAttr, out.DerivationPath = runtimedriver.SelectionDevShell, attr, drv
	return out, nil
}

// parseFlakeNarHash takes the NAR hash Nix locked the commit's flake to,
// and requires the lock to name the commit.
func parseFlakeNarHash(raw []byte, commit string) (string, error) {
	var metadata struct {
		Locked struct {
			NarHash string `json:"narHash"`
			Rev     string `json:"rev"`
		} `json:"locked"`
	}
	if len(raw) == 0 || len(raw) > 2<<20 || json.Unmarshal(raw, &metadata) != nil {
		return "", errors.New("invalid builder flake metadata")
	}
	if !builderNarHashPattern.MatchString(metadata.Locked.NarHash) || metadata.Locked.Rev != commit {
		return "", errors.New("builder flake metadata does not lock the commit to an SRI NAR hash")
	}
	return metadata.Locked.NarHash, nil
}

// RealizeBuilderNix builds only the derivation selected above and captures
// pinned activation material. Imaging, rooting and scrubbing are later
// stages; this method does not claim an image-ready builder.
func (b *Backend) RealizeBuilderNix(ctx context.Context, r Builder, selected BuilderNixSelection) (BuilderNixResult, error) {
	var out BuilderNixResult
	if selected.Selection != runtimedriver.SelectionDevShell || selected.Builder != r || !validBuilderSystem(selected.System) ||
		selected.DevShellAttr != "devShells."+selected.System+".default" || !builderDrvPattern.MatchString(selected.DerivationPath) ||
		!builderNarHashPattern.MatchString(selected.SourceNarHash) {
		return out, errors.New("invalid builder Nix selection")
	}
	current, err := b.ResolveBuilderNix(ctx, r, selected.System)
	if err != nil {
		return out, err
	}
	if current != selected {
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
	return b.checkBuilderBundle(ctx, r)
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
	mode := builderNixFor(r)
	args := []string{builderNixBinary, "--extra-experimental-features", "nix-command flakes", "--option", "accept-flake-config", "false", "--option", "pure-eval", "true", "--option", "flake-registry", "", "--option", "substituters", mode.substituters}
	args = append(args, command...)
	if len(command) > 0 && (command[0] == "flake" || command[0] == "eval" || command[0] == "build" || command[0] == "print-dev-env") {
		if mode.offline {
			args = append(args, "--offline")
		}
		args = append(args, "--no-update-lock-file", "--no-write-lock-file")
	}
	return b.builderGuestExec(ctx, r, args...)
}

// builderGitEnv keeps system and global Git configuration, replace refs,
// system attributes and prompts out of every builder Git command.
var builderGitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
	"GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
}

// builderGit runs one fixed Git command in the workspace as the builder
// user, with no hooks, fsmonitor, credential helper or background
// maintenance.
func (b *Backend) builderGit(ctx context.Context, r Builder, args ...string) ([]byte, error) {
	argv := append([]string{builderGitBinary, "--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	return b.builderGuestExecEnv(ctx, r, builderGitEnv, argv...)
}

func (b *Backend) builderGuestExec(ctx context.Context, r Builder, guestArgv ...string) ([]byte, error) {
	return b.builderGuestExecEnv(ctx, r, nil, guestArgv...)
}

// builderGuestExecEnv runs a fixed command as the unprivileged builder user
// in the workspace, with a fixed environment and any extra variables given.
func (b *Backend) builderGuestExecEnv(ctx context.Context, r Builder, env []string, guestArgv ...string) ([]byte, error) {
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return nil, err
	}
	args := []string{"exec", builderName(r), "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--cwd", builderWorkspace,
		"--env", "HOME=/home/p", "--env", "USER=p", "--env", "LOGNAME=p", "--env", "PATH=/run/current-system/sw/bin:/usr/bin:/bin", "--env", "LANG=C", "--env", "LC_ALL=C", "--env", "TERM=dumb",
		"--env", "NIX_PATH=", "--env", "NIX_CONFIG=", "--env", "NIX_USER_CONF_FILES=/dev/null", "--env", "NIX_REMOTE=daemon", "--env", "LD_PRELOAD=", "--env", "LD_LIBRARY_PATH=", "--env", "BASH_ENV="}
	for _, variable := range env {
		args = append(args, "--env", variable)
	}
	args = append(args, "--")
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
