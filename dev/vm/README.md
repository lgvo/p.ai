# Disposable Incus development VM

Run the runtime experiments from this repository without installing Incus on
the workstation. Nix builds a NixOS VM and a container fixture from the locked
Nixpkgs revision; QEMU runs the VM; Incus runs the containers inside it.

This is an infrastructure lab, not a runnable P MVP. P's initial plugin CLI and
daemon/RPC foundation and Git/SSH substrate now run in the product suite;
session lifecycle, attachment leases, and connected TUI are still pending.
The container fixture is not the production
P base image and does not implement its persistent-host supervision contract.

## Requirements

- An `x86_64-linux` host with Nix and flakes enabled.
- Access to the Nix daemon and, preferably, readable/writable `/dev/kvm`.
- Capacity for an 8 GiB, two-vCPU VM, its sparse 24 GiB disk, and Nix build
  outputs. Initial dependencies are downloaded through Nix.

No host Incus, QEMU package installation, root invocation, or NixOS system
rebuild is needed. Nix supplies QEMU. The runner supports software emulation
when KVM is unavailable, but that path is slower and requires separate runtime
evidence before claiming it works within the smoke-test timeout.

When running through a coding-agent sandbox, the Nix daemon socket and
`/dev/kvm` may be hidden even though they exist on the host. Run the command in
an ordinary host terminal, or use the agent's approved execution outside its
sandbox. Changing global Codex permissions is not a prerequisite.

## Automated test

From the repository root:

```sh
nix run path:./dev/vm#smoke
```

This builds the fixtures, boots a fresh VM, runs the smoke test as its confined
`pdev` account, powers off, and removes the temporary VM disk. A nonzero exit
means failure, including boot/timeout failure or a missing success marker.
Console output is retained under `.cache/p-vm/smoke-*.log`.

The timeout defaults to 900 seconds after the Nix build. Override with
`P_VM_TIMEOUT=1800` if needed. `P_VM_LOG_DIR` changes the log directory.

The test checks:

- Only the account's project is visible; project creation, server configuration,
  privileged containers, nesting, raw LXC settings, arbitrary host mounts, and
  NIC attachment are rejected by Incus authorization.
- Two unprivileged containers boot systemd from the same locally built image.
- Both have no NIC, and each owns a private workspace and Nix store.
- The unprivileged session user can commit with Git, start tmux, and add a
  path to its own Nix store through its local daemon.
- A second container does not see the first one's workspace or added store
  path. Files/store additions survive stop/start; old tmux processes do not.
- Removing one container leaves the other operational.

This does not prove project Nix builds/devShell activation, public egress,
the complete endpoint/mount policy, P attachment leases, Git branch authority,
or P lifecycle recovery. Those remain in the
[development validation gates](../../docs/development-validations.md).

## Interactive lab

For the growing CLI product suite, run `./dev/test-vm` from the repository
root. It builds P with the same pinned Nixpkgs, runs all Go unit tests, and
boots a fresh VM containing the packaged CLI and explicit source fixtures.
The VM runs the infrastructure smoke test followed by
`tests/integration/steps/*.sh`. A checkout-wide lock rejects overlapping
product integration runs. Console logs are retained under
`.cache/p-vm/integration-*.log`; the disposable disk is removed on exit.
To debug specific steps, pass their exact filenames, repeating `--step` as
needed: `./dev/test-vm --step 12-attachment.sh --step 13-daemon-events.sh`.
Step filenames must start with a letter or digit, use only letters, digits,
dots, underscores, and hyphens, and end in `.sh`.
Selected steps run in repository order and still run the VM smoke test. Their
result is marked `P_PRODUCT_INTEGRATION_SELECTED_PASS` with the selected names;
only a full run produces `P_PRODUCT_INTEGRATION_PASS`.
`tests/integration/test-vm-selection.sh` checks selection and markers with mock
commands without starting a VM.
See [implementation progress](../../docs/implementation-progress.md) for
the exact features covered so far. The current product suite validates plugin
and durable daemon/RPC foundations plus actual Git/SSH authorization and
executable source-plugin behavior. It is not a complete session control plane.

```sh
nix run path:./dev/vm
```

The serial console logs in as `pdev`. Wait for `p-vm-prepare.service` to finish,
then use:

```sh
systemctl status p-vm-prepare.service
p-vm-smoke
incus launch p-lab-base my-session
incus exec my-session --user 1000 --group 100 -- bash
```

The container's writable workspace is `/workspace`. Exit the shell, then use
`incus stop my-session`, `incus start my-session`, or
`incus delete my-session` to explore raw Incus behavior. These are Incus
operations, not P lifecycle commands.

For VM-only administration, use `su -` with the disposable root password
`p-vm`. Run `poweroff` there to shut down cleanly. The VM has no SSH listener
or forwarded ports. QEMU's Ctrl+A, X exits immediately if the guest is stuck.

The interactive disk persists at `.cache/p-vm/disk.qcow2`; set
`P_VM_STATE_DIR` to use a different directory. To reset the lab, shut it down
and delete that disk. Resetting deletes every container and file in the lab.
Do not run two interactive VMs against the same disk.

## Boundaries and implementation

- The VM boots from a generated store image and has its own writable store.
  It shares no host checkout, home, credential directory, or host Nix store.
- QEMU guest networking is restricted, and the Incus project prohibits NICs.
  An addressless `incusbr0` exists for Incus user-service initialization; it
  is not attached to the test containers.
- VM root provisions the `dir` pool, `user-1000` project, image, and restricted
  profile. `pdev` belongs to `incus`, not `incus-admin`, and has no sudo grant.
  The project exists before first user contact so Incus does not create its
  broader default user profile.
- The only permitted host disk-source prefix is the VM's
  `/var/lib/p-vm/endpoints`. Containers receive only their managed root in this
  test; the full P endpoint contract remains future work.
- The smoke test uses unique instance names and removes only its own instances.
  It never deletes an interactive session.
- The checked-in lock initially matches the UI prototype's Nixpkgs pin. Each
  lab invocation uses this lock; update deliberately and rerun validation.

Configuration lives in [machine.nix](machine.nix), the container fixture in
[image.nix](image.nix), and behavioral probes in [smoke.sh](smoke.sh).
The [validation record](VALIDATION.md) lists the tested versions, results,
implementation findings, and remaining gates.

For development tools and static evaluation:

```sh
nix develop path:./dev/vm
nix flake check --no-build path:./dev/vm
```

Static evaluation does not boot the VM. Use the smoke command for runtime
evidence. Public references: [NixOS VM support](https://nixos.org/manual/nixos/stable/#sec-building-vm),
[Incus user confinement](https://linuxcontainers.org/incus/docs/main/howto/projects_confine/),
and [Incus project restrictions](https://linuxcontainers.org/incus/docs/main/reference/projects/).
