package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

const nativeValidFlake = `{
  outputs = { self }: {
    devShells.x86_64-linux.default = builtins.derivation {
      name = "p-native-shell-fixture";
      system = "x86_64-linux";
      builder = "/run/current-system/sw/bin/bash";
      args = [ "-c" "printf built > \"$out\"" ];
      outputs = [ "out" ];
      stdenv = ./stdenv;
      NATIVE_VALUE = "ok";
	  NATIVE_REV = self.rev;
      shellHook = "export NATIVE_HOOK=ready";
    };
  };
}
`

// The hook writes into the workspace clone and roots a store object there:
// the image must hold neither.
const nativeImageFlake = `{
  outputs = { self }: {
    devShells.x86_64-linux.default = builtins.derivation {
      name = "p-native-shell-fixture";
      system = "x86_64-linux";
      builder = "/run/current-system/sw/bin/bash";
      args = [ "-c" "printf built > \"$out\"" ];
      outputs = [ "out" ];
      stdenv = ./stdenv;
      NATIVE_VALUE = "ok";
	  NATIVE_REV = self.rev;
      shellHook = ''
        export NATIVE_HOOK=ready
        printf hook-extra > /workspace/p-hook-source
        _p_hook_path=$(/run/current-system/sw/bin/nix-store --add /workspace/p-hook-source)
        /run/current-system/sw/bin/nix-store --realise --add-root /workspace/p-hook-root "$_p_hook_path" >/dev/null
        ln -s /etc/p/devshell/activate.sh /workspace/p-hook-material-link
        test -e "$_p_hook_path" && test -L /workspace/p-hook-root && test -e /workspace/p-hook-material-link
      '';
    };
  };
}
`

const nativeInvalidFlake = `{ outputs = _: { devShells.x86_64-linux.default = 42; }; }
`

// Its input is in the clone but not in a lock file: the builder must refuse
// to lock it rather than update or write a lock.
const nativeUnlockedFlake = `{ inputs.dep.url = "path:/workspace/dep"; outputs = { self, dep }: { packages = dep.packages or {}; }; }
`

const nixInstance = "a0000000-0000-4000-8000-000000000024"

func runNixFixture(args []string, image bool) error {
	if len(args) != 2 {
		return errors.New("usage: builder-fixture ACTIVATION BASE-FINGERPRINT nix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Minute)
	defer cancel()
	state, err := os.MkdirTemp("", "p-builder-nix-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	store, git, repo, err := openRepository(ctx, state, args[0])
	if err != nil {
		return err
	}
	defer store.Close()
	binary, err := exec.LookPath("incus")
	if err != nil {
		return err
	}
	b, err := runtimeincus.New(runtimeincus.Config{Binary: binary, UserSocket: "/var/lib/incus/unix.socket.user", Project: "user-1000", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints", "/var/lib/p-vm/grants"}, BuilderStoragePool: "builders", PInstanceID: nixInstance})
	if err != nil {
		return err
	}
	base := strings.TrimSpace(args[1])
	if image {
		commit, err := seedNativeCommit(ctx, repo, nativeImageFlake, "valid-offline")
		if err != nil {
			return fmt.Errorf("image seed: %w", err)
		}
		if err := runImageCase(ctx, git, binary, state, b, base, commit); err != nil {
			return fmt.Errorf("image: %w", err)
		}
		fmt.Println("P_BUILDER_PRIVATE_IMAGE_PASS")
		return nil
	}
	cases := []struct {
		name, flake, want string
	}{
		{"no-flake", "", string(runtimedriver.SelectionNoFlake)},
		{"no-default", `{ outputs = _: { packages.x86_64-linux.default = 42; }; }`, string(runtimedriver.SelectionNoDefault)},
		{"present-invalid", nativeInvalidFlake, "error"},
		{"missing-lock-input", nativeUnlockedFlake, "lock-error"},
		{"valid-offline", nativeValidFlake, string(runtimedriver.SelectionDevShell)},
	}
	for _, tc := range cases {
		commit, err := seedNativeCommit(ctx, repo, tc.flake, tc.name)
		if err != nil {
			return fmt.Errorf("%s seed: %w", tc.name, err)
		}
		if err := runNixCase(ctx, git, binary, state, b, base, commit, tc.want); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	fmt.Println("P_BUILDER_NATIVE_NIX_PASS")
	return nil
}

func seedNativeCommit(ctx context.Context, repo, flake, name string) (string, error) {
	readme, err := gitCommand(ctx, repo, []byte("fixture: "+name+"\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	entries := "100644 blob " + readme + "\tREADME\x00"
	if flake != "" {
		hash, err := gitCommand(ctx, repo, []byte(flake), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		entries += "100644 blob " + hash + "\tflake.nix\x00"
	}
	if name == "valid-offline" {
		setup, err := gitCommand(ctx, repo, []byte("export NATIVE_VALUE=ok\n"), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		setupTree, err := gitCommand(ctx, repo, []byte("100644 blob "+setup+"\tsetup\x00"), "mktree", "-z")
		if err != nil {
			return "", err
		}
		entries += "040000 tree " + setupTree + "\tstdenv\x00"
	}
	if name == "missing-lock-input" {
		depFlake, err := gitCommand(ctx, repo, []byte(`{ outputs = _: { packages = {}; }; }`), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		depTree, err := gitCommand(ctx, repo, []byte("100644 blob "+depFlake+"\tflake.nix\x00"), "mktree", "-z")
		if err != nil {
			return "", err
		}
		entries += "040000 tree " + depTree + "\tdep\x00"
	}
	tree, err := gitCommand(ctx, repo, []byte(entries), "mktree", "-z")
	if err != nil {
		return "", err
	}
	commit, err := gitCommand(ctx, repo, []byte("native "+name+"\n"), "commit-tree", tree)
	if err != nil {
		return "", err
	}
	if _, err := gitCommand(ctx, repo, nil, "update-ref", "refs/heads/main", commit); err != nil {
		return "", err
	}
	return commit, nil
}

// runNixCase takes one commit through the builder's lower-level steps:
// create, transfer the bundle, start, clone, resolve and realize.
func runNixCase(ctx context.Context, git *gitservice.Backend, binary, state string, b *runtimeincus.Backend, base, commit, want string) error {
	src, err := exportCommit(ctx, git, state, commit)
	if err != nil {
		return err
	}
	uuid, err := newUUID()
	if err != nil {
		return err
	}
	r := runtimeincus.Builder{RequestUUID: uuid, ProjectPath: project, CommitOID: commit, BaseImageFingerprint: base, ContractVersion: "1"}
	created := false
	defer func() {
		if created {
			cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, _ = b.DeleteBuilder(cleanup, r)
		}
	}()
	if _, err := b.CreateBuilder(ctx, r); err != nil {
		return err
	}
	created = true
	if err := b.TransferBuilderSource(ctx, r, src); err != nil {
		return err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return err
	}
	if err := b.CloneBuilderSource(ctx, r, src); err != nil {
		return err
	}
	selected, err := b.ResolveBuilderNix(ctx, r, "x86_64-linux")
	switch want {
	case "error", "lock-error":
		if err == nil {
			return fmt.Errorf("invalid present/default input selected %+v", selected)
		}
		if want == "error" && !strings.Contains(err.Error(), "present default devShell invalid") {
			return fmt.Errorf("present-invalid case failed before typed default validation: %w", err)
		}
		// Nix refuses while locking the input, or when the lock would change;
		// either way the builder never updates a lock.
		if want == "lock-error" && !strings.Contains(err.Error(), "unlocked flake input") && !strings.Contains(err.Error(), "lock file changes") && !strings.Contains(err.Error(), "--no-update-lock-file") {
			return fmt.Errorf("missing lock failed for another reason: %w", err)
		}
	case string(runtimedriver.SelectionNoFlake), string(runtimedriver.SelectionNoDefault):
		if err != nil {
			return err
		}
		if string(selected.Selection) != want || selected.DerivationPath != "" || (want == string(runtimedriver.SelectionNoFlake)) != (selected.SourceNarHash == "") {
			return fmt.Errorf("expected %s, got %+v", want, selected)
		}
	default:
		if err != nil {
			return err
		}
		if selected.Selection != runtimedriver.SelectionDevShell || selected.DerivationPath == "" || selected.SourceNarHash == "" || selected.Builder.CommitOID != commit {
			return fmt.Errorf("invalid shell selection %+v", selected)
		}
		other, err := b.ResolveBuilderNix(ctx, r, "aarch64-linux")
		// A missing default may fall back only for this host's actual system.
		// A foreign system must be rejected before evaluating that fallback.
		if err == nil || !strings.Contains(err.Error(), "requested builder system differs from Incus host architecture") {
			return fmt.Errorf("foreign system did not receive the architecture refusal: %+v %v", other, err)
		}
		result, err := b.RealizeBuilderNix(ctx, r, selected)
		if err != nil || result.MaterialDigest == "" || !strings.Contains(result.Material.Script, "NATIVE_VALUE='ok'") || !strings.Contains(result.Material.Script, "NATIVE_REV='"+commit+"'") {
			return fmt.Errorf("offline build/capture: %+v %w", result, err)
		}
		name := "p-builder-" + r.RequestUUID
		if _, err := incus(ctx, binary, state, "config", "set", name, "user.p.builder_request_uuid=00000000-0000-4000-8000-000000000000"); err != nil {
			return err
		}
		tampered := true
		defer func() {
			if tampered {
				restore, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				_, _ = incus(restore, binary, state, "config", "set", name, "user.p.builder_request_uuid="+r.RequestUUID)
			}
		}()
		if _, err := b.ResolveBuilderNix(ctx, r, "x86_64-linux"); err == nil {
			return errors.New("foreign builder identity reached native Nix")
		}
		if _, err := incus(ctx, binary, state, "config", "set", name, "user.p.builder_request_uuid="+r.RequestUUID); err != nil {
			return err
		}
		tampered = false
	}
	// Nix read the commit from the clone's objects and wrote nothing there.
	if out, err := guestUser(ctx, binary, state, "p-builder-"+r.RequestUUID, cloneCheck(commit)+"\ntest ! -e /workspace/flake.lock"); err != nil {
		return fmt.Errorf("native command changed the clone or wrote a lock file: %w: %s", err, out)
	}
	if _, err := b.DeleteBuilder(ctx, r); err != nil {
		return err
	}
	created = false
	return nil
}
