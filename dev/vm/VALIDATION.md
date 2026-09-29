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

## Interactive P lab — 2026-09-28

The default `nix run path:./dev/vm` now launches the current product checkout.
The original infrastructure-only console remains under `#incus-lab`; the smoke
runner and fresh product validation selections remain separate.

After changing the lab to open a shell by default, a fresh native KVM boot in
`/tmp/p-lab-shell-check` reached the `pdev` prompt without launching the TUI.
`p-demo-api system.health` reported `ready`, and direct CLI access through
`p api "$P_SOCKET" session.list '{"v":1,"limit":8}'` returned an empty list.
Explicit `p-demo` opened the real TUI, `q` returned to the shell, and
`p-demo-poweroff` shut the guest down cleanly. The temporary disk was removed.
The Nix build, shell syntax check and `git diff --check` passed.

Native KVM console exploration used only new validation disks in
`/tmp/p-lab-check-offline` and `/tmp/p-lab-check-public`, with no credentials
imported or authenticated agent execution. Before the shell-default change,
it proved automatic real TUI entry,
JSON health and lifecycle access, actual project/session creation and tmux
attachment/detachment. The offline workspace's `lab-persistence` file and its
retained P Git commit survived shutdown, a changed VM build, and session Start.
The public session completed creation with `public-egress`, reached
`https://example.com` with certificate verification (`P_LAB_PUBLIC_HTTPS 200`),
and returned `ready` after a second boot. Repeated bridge/ACL provisioning and
bundled-plugin loading succeeded; activation remained mode `0600`, owned by UID 1000.

Exploration exposed two implementation gaps: the tmux host did not inherit the
fixed Git SSH wrapper, and activation paths into the previous VM's read-only
Nix store disappeared after rebuilding. The first is covered by an ordinary
terminal Git push in VM56; the second is handled by versioned bundled packages
copied into private persistent lab state. Customized activations are retained;
the customization probe initially used an invalid metadata mode and was refused
as designed, then loaded successfully after correcting it to `0600`.
The final preparation script pins `cmp` from diffutils instead of depending on
the service PATH.

Console records are `.cache/p-vm/lab-offline-20260928.log`,
`lab-public-20260928.log`, and `lab-public-restart-20260928.log`. The offline
record includes the expected refusal of invalid metadata and the corrected
successful health/selection observations; it is exploratory evidence, not an
asserting automated suite.

The selected VM55+56 invocation exited with code 0 with smoke, hardened-service and live-TUI
markers in `.cache/p-vm/integration-20260928T154712Z-67914.log`. It includes the
new native interactive Git push check. The package build ran the full Go suite
and 17 Python tests; all passed. The existing VM-selection unit checks and
`nix flake check --no-build path:./dev/vm` passed. Flake evaluation also caught
and corrected the infrastructure module's unconditional out-of-flake import of
the production service module.

The selected VM37 public-egress invocation also exited with code 0, with
`P_PUBLIC_EGRESS_PASS`, selected-suite and smoke markers in
`.cache/p-vm/integration-20260928T155835Z-75337.log`. This exercised the shared
ACL provisioning through real outer/session DoH, HTTPS, fresh Nix fetch and
the existing host/private/metadata/sibling/redirect/DNAT denial probes. All
validation VMs powered off before the next started; their disposable disks
were removed. The final interactive validation disks were also removed after
retaining the console records. No QEMU process remained.

### Committed repository seed — 2026-09-28

The launcher now bundles committed `HEAD` and its reachable history. First-boot
provisioning creates local-only `p-ai/main`, transfers the bundle through the
confined Incus file API, and pushes from the ordinary session user through P's
scoped Git endpoint. A persistent completion record prevents later launches
from overwriting VM work or recreating deliberately removed projects.

The final Nix runner build, public-mode service evaluation, Bash syntax,
ShellCheck and `git diff --check` passed. A bundle clone matched host commit
`b8317f46c6c6ed92008f0cea2fec67641983d57b` and all 45 reachable commits;
the uncommitted lab launcher was absent. Isolated loader checks used real Git
and proot over disposable directories, proving initial push, safe replay,
refusal of occupied workspaces, and preservation of later files and commits.
The first proot attempt was refused by sandbox ptrace restrictions; approved
host execution passed with `P_LAB_LOADER_PASS`.

The initial native seed check was deferred while the user's existing lab held
the checkout's VM lock. It was left running. After it shut down, the default
socket validation below completed native seed and reboot checks as well.

### Default host socket and ordinary lab commands — 2026-09-28

`p api`, `p tui`, and `p attach` now share endpoint selection: explicit socket,
then nonempty `P_SOCKET`, then `/var/lib/p/control.sock`. Tests exercise actual
Unix-socket requests for short commands and legacy arguments, snapshot flags,
default selection, and invalid override refusal. The initial focused run was
blocked by sandbox socket restrictions; approved host execution passed.
The final production package ran the full Go suite and all 17 Python tests;
all passed. The final lab build, public-mode endpoint evaluation, ShellCheck,
Bash syntax, flake evaluation and `git diff --check` also passed.

A fresh native KVM lab at `/tmp/p-default-socket-check` logged into a shell and
seeded the local-only project `p-ai/main` successfully. Ordinary `p api` calls
reported ready health, active project identity and `origin.status: local-only`.
`p tui` displayed the real session; `p attach UUID` entered its terminal without
a socket argument. The workspace contained source commit `b8317f4` and all 45
reachable commits, with only P as its Git remote. Uncommitted host launcher
files were absent. An ordinary session Git commit and push advanced `main` to
`6336721` through P's scoped endpoint.

After clean shutdown and a changed VM store build, the same UUID returned ready.
Its new commit (46 reachable commits) and uncommitted `lab-uncommitted-work`
file survived. Checked file/count comparisons emitted
`P_LAB_REBOOT_PRESERVATION_OK`. An explicit socket overrode an unavailable
`P_SOCKET`; clearing the variable selected the absent standard socket instead
of falling back to the lab, and a relative override was refused. Both guest
shutdowns and launcher exits succeeded. The temporary validation disk was
removed; the user's persistent lab disk was not changed by these checks.

Console records are `.cache/p-vm/lab-default-socket-20260928.log` and
`lab-default-socket-restart-20260928.log`. They are exploratory console evidence,
including expected negative probes and two pager-consumed command prefixes
that were subsequently reissued; the unit tests provide repeatable assertions.

### Independent review follow-up — 2026-09-28

An independent review of the complete uncommitted change set found two issues:
service-account command examples dropped `P_SOCKET` through sudo, and the
public lab launcher did not package `ip` and `awk`. The examples now preserve
only `P_SOCKET`; the launcher includes iproute2 and gawk. Direct-script
prerequisites are documented, with an explicit diagnostic for missing public
network tools.

The rebuilt default launcher passed a probe with the host PATH removed:
its packaged `ip` and `awk` successfully collected and parsed host addresses
and connected routes, emitting `P_LAB_PACKAGED_TOOLS_PASS`. Bash syntax,
ShellCheck, Nix formatting and `git diff --check` passed. The local sudo manual
confirms `--preserve-env=list` semantics; an attempted runtime preservation
probe could not execute because host sudo requires a password. No additional
VM boot or runtime sudo result is claimed for these focused fixes.
