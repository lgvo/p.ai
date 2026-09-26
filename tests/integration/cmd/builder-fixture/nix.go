package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/environmentnix"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
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

const nativeUnlockedFlake = `{ inputs.dep.url = "path:/opt/p/build/source/dep"; outputs = { self, dep }: { packages = dep.packages or {}; }; }
`

func runNixFixture(args []string, environment *plugin.Active, image bool) error {
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
	store, err := control.OpenStore(state)
	if err != nil {
		return err
	}
	defer store.Close()
	active, err := plugin.LoadActivation(args[0])
	if err != nil {
		return err
	}
	git, err := gitservice.New(store, state, active)
	if err != nil {
		return err
	}
	if err := git.InitBare(ctx, project); err != nil {
		return err
	}
	repo, err := git.RepositoryPath(project)
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("incus")
	if err != nil {
		return err
	}
	b, err := runtimeincus.New(runtimeincus.Config{Binary: binary, UserSocket: "/var/lib/incus/unix.socket.user", Project: "user-1000", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"}, BuilderStoragePool: "builders", PInstanceID: "a0000000-0000-4000-8000-000000000024"})
	if err != nil {
		return err
	}
	base := strings.TrimSpace(args[1])
	cases := []struct {
		name, flake, want string
	}{
		{"no-flake", "", "base"},
		{"no-default", `{ outputs = _: { packages.x86_64-linux.default = 42; }; }`, "base"},
		{"present-invalid", nativeInvalidFlake, "error"},
		{"missing-lock-input", nativeUnlockedFlake, "lock-error"},
		{"valid-offline", nativeValidFlake, "shell"},
	}
	if environment != nil {
		cases = []struct{ name, flake, want string }{{"no-flake", "", "base"}, {"valid-offline", nativeValidFlake, "shell"}}
	}
	if image {
		cases = []struct{ name, flake, want string }{{"valid-offline", nativeImageFlake, "shell"}}
	}
	for _, tc := range cases {
		if err := seedNativeCommit(ctx, repo, tc.flake, tc.name); err != nil {
			return fmt.Errorf("%s seed: %w", tc.name, err)
		}
		if err := runNixCase(ctx, git, binary, state, b, base, tc.want, environment, image); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
	}
	if image {
		fmt.Println("P_BUILDER_PRIVATE_IMAGE_PASS")
	} else if environment == nil {
		fmt.Println("P_BUILDER_NATIVE_NIX_PASS")
	} else {
		fmt.Println("P_BUILDER_ENV_WASI_PASS")
	}
	return nil
}

func seedNativeCommit(ctx context.Context, repo, flake, name string) error {
	readme, err := gitCommand(ctx, repo, []byte("fixture: "+name+"\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	entries := "100644 blob " + readme + "\tREADME\x00"
	if flake != "" {
		hash, err := gitCommand(ctx, repo, []byte(flake), "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		entries += "100644 blob " + hash + "\tflake.nix\x00"
	}
	if name == "valid-offline" {
		setup, err := gitCommand(ctx, repo, []byte("export NATIVE_VALUE=ok\n"), "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		setupTree, err := gitCommand(ctx, repo, []byte("100644 blob "+setup+"\tsetup\x00"), "mktree", "-z")
		if err != nil {
			return err
		}
		entries += "040000 tree " + setupTree + "\tstdenv\x00"
	}
	if name == "missing-lock-input" {
		depFlake, err := gitCommand(ctx, repo, []byte(`{ outputs = _: { packages = {}; }; }`), "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		depTree, err := gitCommand(ctx, repo, []byte("100644 blob "+depFlake+"\tflake.nix\x00"), "mktree", "-z")
		if err != nil {
			return err
		}
		entries += "040000 tree " + depTree + "\tdep\x00"
	}
	tree, err := gitCommand(ctx, repo, []byte(entries), "mktree", "-z")
	if err != nil {
		return err
	}
	commit, err := gitCommand(ctx, repo, []byte("native "+name+"\n"), "commit-tree", tree)
	if err != nil {
		return err
	}
	_, err = gitCommand(ctx, repo, nil, "update-ref", "refs/heads/main", commit)
	return err
}

func runNixCase(ctx context.Context, git *gitservice.Backend, binary, state string, b *runtimeincus.Backend, base, want string, environment *plugin.Active, image bool) error {
	snapshot, err := git.CaptureCommittedSource(ctx, project, plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/main"})
	if err != nil {
		return err
	}
	defer snapshot.Close()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	uuid := fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(id[:4]), hex.EncodeToString(id[4:6]), hex.EncodeToString(id[6:8]), hex.EncodeToString(id[8:10]), hex.EncodeToString(id[10:]))
	r := runtimeincus.Builder{RequestUUID: uuid, ProjectPath: project, CommitOID: snapshot.CommitOID(), TreeOID: snapshot.TreeOID(), BaseImageFingerprint: base, ContractVersion: "1"}
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
	if err := b.TransferBuilderSource(ctx, r, snapshot); err != nil {
		return err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return err
	}
	var selected runtimeincus.BuilderNixSelection
	var pipeline *environmentnix.Pipeline
	if environment == nil {
		selected, err = b.ResolveBuilderNix(ctx, r, "x86_64-linux")
	} else {
		pipeline, err = environmentnix.New(*environment, b, r, "x86_64-linux")
		if err == nil {
			selected, err = pipeline.Resolve(ctx)
		}
	}
	if want == "error" || want == "lock-error" {
		if err == nil {
			return fmt.Errorf("invalid present/default input selected %+v", selected)
		}
		if want == "error" && !strings.Contains(err.Error(), "present default devShell invalid") {
			return fmt.Errorf("present-invalid case failed before typed default validation: %w", err)
		}
		if want == "lock-error" && !strings.Contains(err.Error(), "cannot update unlocked flake input") {
			return fmt.Errorf("missing lock failed for another reason: %w", err)
		}
	} else if err != nil {
		return err
	} else if want == "base" {
		if !selected.BaseOnly || selected.DerivationPath != "" || selected.KeyDigest != "" {
			return fmt.Errorf("expected base-only, got %+v", selected)
		}
	} else {
		if selected.BaseOnly || selected.DerivationPath == "" || selected.KeyDigest == "" || selected.Builder.CommitOID != snapshot.CommitOID() || selected.Builder.TreeOID != snapshot.TreeOID() {
			return fmt.Errorf("invalid shell selection %+v", selected)
		}
		var other runtimeincus.BuilderNixSelection
		if environment == nil {
			other, err = b.ResolveBuilderNix(ctx, r, "aarch64-linux")
		} else {
			otherPipeline, createErr := environmentnix.New(*environment, b, r, "aarch64-linux")
			if createErr != nil {
				return createErr
			}
			other, err = otherPipeline.Resolve(ctx)
		}
		// A missing default may fall back only for this host's actual system.
		// A foreign system must be rejected before evaluating that fallback.
		if err == nil || !strings.Contains(err.Error(), "requested builder system differs from Incus host architecture") {
			return fmt.Errorf("foreign system did not receive the architecture refusal: %+v %v", other, err)
		}
		var result runtimeincus.BuilderNixResult
		if environment == nil {
			result, err = b.RealizeBuilderNix(ctx, r, selected)
		} else {
			result, err = pipeline.Realize(ctx)
		}
		if err != nil || result.MaterialDigest == "" || !strings.Contains(result.Material.Script, "NATIVE_VALUE='ok'") || !strings.Contains(result.Material.Script, "NATIVE_REV='"+snapshot.CommitOID()+"'") {
			return fmt.Errorf("offline build/capture: %+v %w", result, err)
		}
		if image {
			if pipeline == nil {
				return errors.New("image path requires accepted WASI pipeline")
			}
			if err := runPublishedImageCase(ctx, binary, state, b, r, pipeline, result); err != nil {
				return err
			}
			created = false
			return nil
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
		var foreignErr error
		if environment == nil {
			_, foreignErr = b.ResolveBuilderNix(ctx, r, "x86_64-linux")
		} else {
			foreignPipeline, createErr := environmentnix.New(*environment, b, r, "x86_64-linux")
			if createErr != nil {
				return createErr
			}
			_, foreignErr = foreignPipeline.Resolve(ctx)
		}
		if foreignErr == nil {
			return errors.New("foreign builder identity reached native Nix")
		}
		if _, err := incus(ctx, binary, state, "config", "set", name, "user.p.builder_request_uuid="+r.RequestUUID); err != nil {
			return err
		}
		tampered = false
	}
	instanceName := "p-builder-" + r.RequestUUID
	out, err := incus(ctx, binary, state, "exec", instanceName, "--", "/bin/sh", "-c", "test ! -e /opt/p/build/source/flake.lock")
	if err != nil {
		return fmt.Errorf("native command wrote a lock file: %w: %s", err, out)
	}
	if _, err := b.DeleteBuilder(ctx, r); err != nil {
		return err
	}
	created = false
	return nil
}
