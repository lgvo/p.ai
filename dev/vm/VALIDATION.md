# VM infrastructure validation — 2026-09-23

This records a narrow lab result. It does not close the full
[P runtime/environment gates](../../docs/development-validations.md).

## Tested configuration

| Component | Observed value |
|---|---|
| Host | NixOS 26.11, x86_64, Linux 6.18.49 |
| Virtualization | QEMU 11.1.0; guest `systemd-detect-virt` returned `kvm` |
| VM | 2 vCPUs, 4096 MiB RAM, sparse 24 GiB root disk |
| Nixpkgs lock | `c043004d1c6985732bcc1cbc5a9c9aecbbb4e0f0` |
| Guest kernel | Linux 6.18.49 |
| Incus client/server | 7.4 / 7.4 |
| Storage | `dir`, pool `default` inside the VM |
| Account/project | `pdev`, UID 1000, group `incus`; project `user-1000` |
| Container limits | 1 CPU, 768 MiB RAM each; isolated unprivileged UID maps |
| Network | No container NIC; QEMU restricted user networking |
| Container Nix | 2.34.8, private daemon/store, sandbox configured on |
| Container Git / tmux / systemd | 2.55.0 / 3.7c / 261.2 |
| Fixture fingerprint | `ed0872cebd64253e1674a674086b917cb1840176e33ed68280cb2f2bf1b892bb` |

Exact provisioning and restriction values are in [machine.nix](machine.nix).
The table records the original run. Current tests use an 8 GiB VM and disable
Nix build sandboxing inside containers under the required Incus boundary;
[implementation progress](../../docs/implementation-progress.md) records their
separate validation results.
The VM does not mount the workstation's Nix store, checkout, or home; its
store is supplied through a generated disk image. Containers receive only
their own managed root disks in the smoke test.

## Automated result

Command from the repository root:

```sh
nix run path:./dev/vm#smoke
```

Exit status: **0**. Full console output for this run remains locally at
`.cache/p-vm/smoke-20260923T200138Z-21263.log`. The runner deleted its temporary
VM disk. Selected raw output follows; container IDs identify this run only:

```text
Client version: 7.4
Server version: 7.4
PASS: confined Incus, two private containers, Git, tmux, private Nix store, stop/start, removal
P_VM_SMOKE_PASS
VM smoke test passed; fresh VM disk removed.
```

The test exercised both fresh image import and repeated provisioning of the
same image, then [the smoke probes](smoke.sh). It rejected project creation,
server-listener configuration, privileged mode, nesting, raw LXC settings,
an `/etc` host mount, and a bridged NIC with authorization errors. It also
proved separate workspaces/store additions and file retention across container
stop/start, followed by removal without affecting the sibling container.

Nix flake evaluation passed. `writeShellApplication` also checked the shell
scripts during the build. These static checks are separate from the booted VM
result above.

The interactive launcher was also tested across a full VM shutdown/relaunch.
Provisioning returned `active`, a retained container started successfully, and
`test -f /workspace/reboot-probe` returned success (`VM_RESTART_PERSISTENCE_PASS`).
The temporary interactive test container was then removed and the VM shut down.

## Findings that affected the implementation

- Codex's workspace sandbox hid `/dev/kvm` and denied the Nix daemon socket.
  Both were available through approved execution outside that sandbox. No
  host configuration or global Codex setting was changed.
- `security.idmap.size` in the profile conflicts with the project's blocked
  low-level settings. Removing that unnecessary setting lets Incus supply its
  default size while retaining isolated UID maps and the restriction.
- This Incus version does not support `config show --format json`. The test
  reads expanded configuration through `incus list --format json` instead.
- Importing an already-present image fails. VM provisioning now resolves the
  split image's fingerprint, imports only when absent, and restores the lab
  alias. The automated test executes this step twice.

## Product plugin foundation — 2026-09-23

The separate `./dev/test-vm` runner built P with Go 1.26.7, ran all Go unit
tests, and booted a fresh VM under the same confined host configuration. The
first-party file-log package and CLI went through the public manifest,
digest, activation, grant, and broker path. The guest test is
[`01-plugins.sh`](../../tests/integration/steps/01-plugins.sh).

Exit status: **0**. Raw console output:
`.cache/p-vm/integration-20260923T212016Z-87915.log`.

```text
P_PLUGIN_FOUNDATION_PASS
P_PRODUCT_INTEGRATION_PASS
P_VM_SMOKE_PASS
Product VM integration passed; fresh VM disk removed.
```

The CLI accepted the valid package and private log path, appended valid NDJSON,
and bounded rotation. It rejected missing/extra grants, changed package bytes,
an incompatible API, symlink package/log paths, a hard-linked log, and a
nonprivate log directory. The preceding infrastructure smoke suite also passed.
Independent agents reviewed the implementation and reran unit tests and vet.

An earlier run passed all guest assertions but the host runner incorrectly
expected an unprefixed marker written through journald. The VM service now
writes its final product-success marker directly to the console only after
both suites succeed. That correction was reviewed and the complete VM run
repeated successfully. Runs were sequential, and each VM shut down before the
next started. No executable WASI runner or session lifecycle is validated by
this foundation result.

## Control foundation and executable plugins — 2026-09-23

The cumulative product suite also validates the daemon's private SQLite state,
Unix RPC, single-writer lock, and stable identity across graceful and forced
restart. Source-built WASI fixtures exercise filtered event delivery, typed
broker calls, digest pins, resource limits, and denied ambient authority. Fixed
asset plans are checked without claiming live session installation.

The latest sequential `./dev/test-vm` run exited **0**. Raw console:
`.cache/p-vm/integration-20260923T220854Z-158799.log`.

```text
P_PLUGIN_FOUNDATION_PASS
P_CONTROL_PLANE_PASS
P_PLUGIN_EXECUTION_ASSETS_PASS
P_PRODUCT_INTEGRATION_PASS
P_VM_SMOKE_PASS
```

The VM shut down and its temporary disk was removed. Review findings and the
corrected assertions from earlier runs are recorded in the
[implementation progress log](../../docs/implementation-progress.md).

## Git substrate — 2026-09-23

The serialized `./dev/test-vm` rerun exited **0** with
`P_GIT_SSH_SUBSTRATE_PASS` and both final success markers. Console:
`.cache/p-vm/integration-20260923T234147Z-253932.log`.

The test-only Git driver composes the production authority and plugin APIs.
Real SSH validates bootstrap and assigned-branch pushes, denied host writes,
ref/project confinement, guard persistence, revocation, hidden refs, native
pagination, and alternate-plugin behavior. This does not establish production
daemon lifecycle wiring. The VM shut down and its temporary disk was removed.

## Base runtime and configured daemon — 2026-09-24 UTC

The serial `./dev/test-vm` run exited **0** with `P_RUNTIME_HOST_PASS`,
`P_RUNTIME_INCUS_PASS`, `P_DAEMON_GIT_COMPOSITION_PASS`, all earlier suite
markers, and both final success markers. Console:
`.cache/p-vm/integration-20260924T011138Z-591574.log`.

The assembled production image passed exact socket-mount identity and
read-only isolation, scoped Git access, private workspace/home/Nix state,
tmux PTY detach, retained-state stop/start, clean/killed-host shutdown, and
failed-start recovery. The runtime WASI package exercised the actual confined
Incus user socket, metadata/device checks, and create/start/stop/delete. The
configured production daemon passed Git/RPC access, read-only host access,
identity continuity, and invalid-key/activation startup refusal. Assembly and
registry seeding remain test-only; these results do not claim public lifecycle
or attachment leases. The VM shut down and its temporary disk was removed.

## Committed source effects — 2026-09-24 UTC

The serial run `.cache/p-vm/integration-20260924T012335Z-627864.log` exited
**0**, adding `P_GIT_COMMITTED_SOURCE_PASS` to all earlier success markers.
The bundled executable source plugin and native Git validated committed branch
and ancestor selection, absent-ref creation, unchanged refs after denied
operations, symbolic-ref protection, and cross-project/hidden/noncommit
denials. An alternate plugin without these methods refused them without a
native fallback. The fixture invokes private backend methods; public session
creation remains unvalidated. The VM shut down and its temporary disk was
removed.

## Remaining product evidence

This fixture does not yet validate project Nix builds, default-devShell
activation, policy filesystem grants, public egress, performance
claims, attachment leases, origin workflows,
or lifecycle recovery. Only the tested x86_64/KVM/dir configuration has runtime
evidence here; software emulation and other host/storage combinations remain
unverified.
