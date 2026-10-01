# Interactive P lab and disposable validation VMs

Run the current P implementation from this checkout without installing P or
Incus on your workstation. The default lab boots NixOS with the production P
CLI, TUI, bundled plugins, runtime image, and a configured daemon. QEMU runs
the VM; Incus runs P sessions inside it. Both people and coding agents can
use its console to explore behavior and reproduce issues.

The separate automated runners boot fresh VMs, assert behavior, shut down,
and remove their disks. The infrastructure-only lab remains available for
investigating Incus directly.

## Requirements

- An `x86_64-linux` host with Nix and flakes enabled.
- Access to the Nix daemon and, preferably, readable/writable `/dev/kvm`.
- Capacity for one 12 GiB, two-vCPU VM, its sparse 24 GiB disk, and Nix build
  outputs. Initial dependencies are downloaded through Nix.

No host Incus, QEMU package installation, root invocation, or NixOS system
rebuild is needed. Nix supplies QEMU. The runner supports software emulation
when KVM is unavailable, but that path is slower and requires separate runtime
evidence before claiming it works within the smoke-test timeout.

When running through a coding-agent sandbox, the Nix daemon socket and
`/dev/kvm` may be hidden even though they exist on the host. Run the command in
an ordinary host terminal, or use the agent's approved execution outside its
sandbox. Changing global Codex permissions is not a prerequisite.

## Explore P

From the repository root, in an interactive terminal:

```sh
nix run path:./dev/vm
# Equivalent shortcut, including rebuilding P from the current checkout:
./dev/demo-vm
```

`nix run` supplies the launcher tools. The direct script needs Bash, Git,
Nix's `nix-build`, GNU coreutils, and `flock` on PATH; public mode also needs
`ip` (iproute2) and `awk`.

The serial console logs in as the confined `pdev` account and opens a shell.
The configured daemon starts in the background. Use `p api` directly or
run `p tui` to open the browser. The lab configures its instance automatically.
On first boot, the lab creates the local-only project `p-ai`, with a `main`
session containing this repository's committed `HEAD` and its reachable Git
history. A Git bundle carries only committed objects; uncommitted changes,
untracked files, workstation Git configuration, hooks, and remotes are excluded.
The workspace's `origin` is P's private Git server inside the VM. There is no
external origin and no synchronization back to the workstation.

Seeding runs in the background. Check `systemctl status p-lab-repository`;
`active (exited)` means the repository is ready. On failure, use
`su -` and `journalctl -u p-lab-repository` to inspect the diagnostic.
To enter the copied repository from the shell:

```sh
SESSION=$(jq -r .session_uuid /var/lib/p-demo/lab-repository.json)
p attach "$SESSION"
```

Or open the TUI and press Enter on `p-ai/main`.
To create another project there, press `c`, choose a name, leave the origin
empty, and accept the configured policy. Enter attaches to the resulting session.
Inside the session, make a Git commit and push it to P; use **Ctrl+B, then d**
to detach and return to the browser. Stop/Start, services, branch creation,
rename, and reviewed removal operate on real Incus instances and P state.
See [Using P](../../docs/user-guide.md) for the complete workflows.

The seeded P repository exceeds the current
[512-entry workspace-loss inspection limit](../../docs/control-api.md#workspace-loss-inspection).
Its reviewed Discard/Delete is therefore refused with a bounded diagnostic;
the source is preserved. Use the small notes project below to exercise complete
reviewed removal. A clean lab reset uses the disk-reset procedure below.

From the guest shell:

```sh
p api system.health            # JSON API, as the daemon's owning account
p api session.list '{"v":1,"limit":8}'
p tui                          # open the TUI
p tui --snapshot               # render a live frame without interaction
p-demo-poweroff                # clean shutdown, retaining all demo state
```

The lab sets `P_SOCKET=/var/lib/p-demo/control.sock`; commands read it without
requiring a socket argument. You can override it for another instance. The
standard installation defaults to `/var/lib/p/control.sock`. For the user
guide's examples, define `api() { p api "$@"; }` and
`attach() { p attach "$1"; }` in the guest shell.
The older `p-demo` and `p-demo-api` helpers remain compatible aliases;
`p-demo` waits for daemon readiness before opening the browser.
On first boot, initialization can take a while; check `p api system.health`
for `control_state: ready` before issuing lifecycle commands. The repository
seed has its own readiness check above. Quit the TUI
to return to the shell.
If the serial console reports the wrong dimensions, set them with
`stty rows 30 cols 100` before reopening P.
The interactive launchers preserve guest newline/cursor movements through
QEMU's serial console and restore your original terminal modes on exit.

The default policy is `network:none`. To explore public DNS/HTTP(S), online
Nix builds, and agent commands, launch the public mode instead:

```sh
nix run path:./dev/vm -- --public
# Or: ./dev/demo-vm --public
```

Public mode uses the existing public-egress bridge/ACL and resolver contract.
It denies host, LAN, private, metadata, sibling, and inbound access. Its owner-run
daemon uses the same confined Incus principal with two exact read-only sudo
proofs; the default offline lab uses the hardened NixOS P service. Public mode
captures the workstation's IPv4 addresses and directly connected LAN routes at
launch. Run it again after changing networks to refresh those deny rules.
Public-egress permits DNS and HTTP(S); it does not grant arbitrary outbound ports.

The offline disk is `.cache/p-vm/demo/disk.qcow2`; public mode uses
`.cache/p-vm/demo-public/disk.qcow2`. Both persist between runs. Set
`P_DEMO_STATE_DIR` (or the existing `P_VM_STATE_DIR`) to an alternate directory.
To reset the public lab, shut it down with `p-demo-poweroff`, wait for QEMU to
exit, then run:

```sh
just lab-reset             # public lab by default
just lab-reset --offline   # select the separate offline lab
# Select an alternate lab using the same override as its launcher:
P_DEMO_STATE_DIR=/path/to/lab just lab-reset
```

The reset command requires Python 3, previews the exact disk path and loss,
and requires typing `yes`; an empty answer or EOF preserves the disk. It
refuses an active checkout VM/integration run or an active VM in the selected
state directory. It deletes only `disk.qcow2`, including every guest project,
session, Git commit, workspace edit, private file, database, credential, and
guest Nix store stored there. Host Nix build outputs, logs, other files in the
state directory, and other lab disks are retained. State paths containing
symlinks or `..`, symlinked locks/disks, and disks with multiple hard links
are refused; select the actual dedicated state directory.

Bundled plugin packages are copied into
private persistent state. An untouched bundled selection follows the current
checkout on launch; customized activations are preserved. Existing sessions
retain their captured runtime image; use a fresh disk to explore a completely
fresh build after runtime changes. A disk lock prevents overlapping use, and
the launcher shares the checkout's integration lock with `dev/test-vm`.
The old infrastructure lab's disk is separate and remains untouched.
Repository seeding runs once per disk, including existing lab disks that have
not yet been seeded. Later launches preserve VM commits and uncommitted work;
they do not replace the copied repository with newer host commits. Deliberately
removed projects/sessions are not recreated once seeding completed. Use a fresh
`P_DEMO_STATE_DIR` for a new copy of the current committed source.

An unfinished session creation also pins its base image. If the lab is rebuilt
with a different runtime image before creation finishes, its operation can
remain blocked by the base-image mismatch. Retry requires a matching
configuration; ordinary Delete/Discard can remain unavailable while creation
is unsettled and its builder is present. See the
[recorded lab diagnosis](../../docs/implementation-progress.md) for the observed
case. Resetting the disk removes the entire selected lab, including unrelated
sessions and private work; it does not recover that individual operation.

The guest shares no workstation checkout, home, credentials, or Nix store.
No credentials are imported. Any real agent authentication is an explicit
manual action inside your guest session. The console's disposable root password
is `p-vm`: use `su -` for administration or `journalctl -u p` diagnostics.
QEMU's **Ctrl+A, X** exits immediately if stuck; prefer `p-demo-poweroff`.

### Exploring from Codex or another agent

Use a terminal tool with a PTY: start `./dev/demo-vm` with `tty: true`, retain its
session identifier, and send input through that same session. The lab opens a
shell; wait for its prompt between commands and check daemon health before
lifecycle commands; wait for `p-lab-repository` to finish before inspecting the
seeded project. JSON responses from `p api` and live
`p tui --snapshot` frames are easy to inspect. Finish with
`p-demo-poweroff`, then wait for QEMU to exit before starting another VM.
An agent sandbox may need approved host execution for the Nix daemon and KVM.

For a clean experiment, choose a fresh `P_DEMO_STATE_DIR`. This lab can produce
runtime evidence, but exploratory commands have no automatic assertions; use
`just vm-tests --step STEP.sh` for repeatable acceptance checks.

## Automated Incus backend tests

From the repository root:

```sh
just vm-incus-tests
```

This calls `nix run path:./dev/vm#smoke`, builds the fixtures, boots a fresh VM,
runs the Incus infrastructure checks as its confined `pdev` account, powers
off, and removes the temporary VM disk. A nonzero exit
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

## Automated P validation

### Three-service development sample

`just lab-public` (or `./dev/demo-vm --public`) consolidates the P repository
and notes sample in `.cache/p-vm/demo-public/disk.qcow2`. `just lab-notes` and
`--notes` are compatibility aliases for this same public lab. It supplies
PostgreSQL, Python/Psycopg and `/etc/p-notes-example` in new session runtimes.
On first provisioning it creates `notes/main`, commits and pushes the small
sample source, and installs, enables and starts its three session-user services.
The database starts empty. Open `p tui` and enter either `p-ai/main` or
`notes/main`; in notes run `python3 examples/notes/client.py health` or
`python3 examples/notes/client.py add 'my first note'`.
Check `systemctl status p-lab-notes` for sample readiness and
`journalctl -u p-lab-notes` as root for errors. Seeding runs once per disk and
does not replace later edits or recreate a deliberately removed sample.
Follow the [notes walkthrough](../../examples/notes/README.md) for services and
independent branch sessions. Newly created branch sessions need their own
service installation and enablement; private SQL data is not copied by Git.
The offline default lab continues to use its ordinary runtime and disk.

The outer notes-lab owner also has a disposable localhost SSH origin fixture:
`bash /etc/p-notes-origin-fixture.sh setup` seeds the exact sample source and
prints the import URL/OID. The [sample walkthrough](../../examples/notes/README.md)
explains its explicit publication and scoped cleanup. It refuses an existing
owner SSH configuration; it never uses real credentials.
The notes lab exposes the guest owner's `.ssh` directory read-only to its
daemon and hides the rest of the home with `ProtectHome=tmpfs`. Fixture keys
and repositories live in its private `/var/lib/p-demo/notes-origin-review`
directory. This optional lab configuration preserves the default hardened
service module.

Shut down with `p-demo-poweroff` to retain the consolidated lab. To reset it,
first shut down and wait for QEMU to exit, then run `just lab-reset` and review
the disk path and loss before typing `yes`.
An explicit `P_DEMO_STATE_DIR` overrides this path. Older `demo-notes` and
`demo-notes-public` disks are preserved; select one explicitly with
`P_DEMO_STATE_DIR` to continue its state. Existing sessions keep their captured
runtime image. Use a fresh disk for a new sample and both current seed projects.

`just notes-tests` uses a temporary real PostgreSQL cluster in the pinned
sample shell. `just vm-tests --step 57-developer-workflow.sh` exercises the
sample through the installed owner API and confined session tools. Live TUI
experience review and persistent lab relaunch require their separate evidence.

To check the consolidated lab on a fresh **test-owned** disk, launch with an
alternate `P_DEMO_STATE_DIR`, wait for `p-lab-repository` and `p-lab-notes` to
finish, then run `bash /etc/p-lab-public-check.sh prepare` as `pdev`. This
asserts both projects and three active services, writes a note through HTTP,
checks the worker's SQL result, and leaves private/source markers. Shut down,
relaunch the same directory with `just lab-notes` or `just lab-public`, wait
for the seed units, then run `bash /etc/p-lab-public-check.sh check`. The check
starts the notes session if stopped and asserts retained identities, commit,
source edits, private files and SQL data without duplicate projects.

For the automated relaunch check in a dedicated test-owned notes lab, run
`bash /etc/p-notes-persistence-check.sh prepare` as `pdev`, shut down with
`p-demo-poweroff`, relaunch the same disk, wait for daemon readiness, then run
`bash /etc/p-notes-persistence-check.sh check`. It creates and retains its own
`notes-persistence` project for inspection; use a fresh alternate
`P_DEMO_STATE_DIR` for repeated independent checks.

### Browser and integration checks

For TUI changes, follow the
[TUI change and validation workflow](../../docs/development-validations.md#tui-change-and-validation-workflow).
Review actual navigation and rendered screens against the prototype with an
LLM before using the driver to validate major integration effects. `just test`
runs automated checks; it does not perform that experience review.

For the growing CLI product suite, run `just vm-tests` from the repository
root. It calls `./dev/test-vm`, builds P with the same pinned Nixpkgs, runs all
Go unit tests, and boots serial fresh VMs containing the packaged CLI and
explicit source fixtures.
Full validation partitions the steps into restricted tests01–36, dedicated
public-egress test37, then restricted tests38 onward. Each VM runs the
infrastructure smoke test followed by its selected product steps. Only test37
has public routing and its outer denial protections; other guests keep restricted
networking. A checkout-wide lock covers all groups and rejects overlapping
product integration runs. Console logs are retained under
`.cache/p-vm/integration-*.log`; the disposable disk is removed on exit.
To debug specific steps, pass their exact filenames, repeating `--step` as
needed: `just vm-tests --step 12-attachment.sh --step 13-daemon-events.sh`.
Step filenames must start with a letter or digit, use only letters, digits,
dots, underscores, and hyphens, and end in `.sh`.
Selected steps run in repository order and still run the VM smoke test. Their
result is marked `P_PRODUCT_INTEGRATION_SELECTED_PASS` with the selected names;
only a full invocation produces `P_PRODUCT_INTEGRATION_PASS`, after every
group succeeds and removes its disk. Selections containing test37 are split
the same way. Multi-step runs use a bounded 10800s guest-runner budget and
single steps keep 1200s; per-test operation deadlines remain unchanged.

`just test` evaluates the root flake, runs `just unit-tests` (Go, Python, and
mocked VM runner checks), builds the packaged CLI with its checks, and runs
the full VM suite. It also runs `just tui-tests` for terminal protocol and
observer transport regressions. The Incus infrastructure checks run as part of
that VM suite.

`tests/integration/test-vm-selection.sh` checks selection and markers with mock
commands in an isolated fixture checkout without starting a VM or sharing a
running lab's lock. It runs as part of `just unit-tests`.
See [implementation progress](../../docs/implementation-progress.md) for
the exact supported CLI scope and acceptance evidence. The suite exercises
plugin management, durable lifecycle/RPC, Git/SSH, isolated environments,
networking and reviewed cleanup. Authenticated Codex execution remains the
user's manual gate; automated checks use event fixtures and dummy files.

## Infrastructure-only lab

This mode is for exploring Incus authorization, private storage, container boot,
and stop/start behavior without P. It retains the original smaller fixture image
and does not run P's daemon or persistent interactive-host contract.

```sh
nix run path:./dev/vm#incus-lab
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

The infrastructure-only disk persists at `.cache/p-vm/disk.qcow2`; set
`P_VM_STATE_DIR` to use a different directory. To reset the lab, shut it down
and delete that disk. Resetting deletes every container and file in the lab.
Do not run two interactive VMs against the same disk.

## Boundaries and implementation

- The VM boots from a generated store image and has its own writable store.
  It shares no host checkout, home, credential directory, or host Nix store.
- Offline guests use restricted QEMU networking and prohibit container NICs.
  Public mode enables only the managed public-egress bridge with its denial
  policy. The addressless `incusbr0` serves Incus user-service initialization.
- VM root provisions the `dir` pool, `user-1000` project, image, and restricted
  profile. `pdev` belongs to `incus`, not `incus-admin`. P lab shutdown and public
  network proofs have exact sudo grants; the infrastructure-only account does not.
  The project exists before first user contact so Incus does not create its
  broader default user profile.
- Permitted disk sources are under the VM's `/var/lib/p-vm/endpoints` and
  `/var/lib/p-vm/grants`. The infrastructure smoke fixture receives only its
  managed root; P sessions in the product lab use managed endpoint mounts.
- The smoke test uses unique instance names and removes only its own instances.
  It never deletes an interactive session.
- The checked-in lock initially matches the UI prototype's Nixpkgs pin. Each
  lab invocation uses this lock; update deliberately and rerun validation.

Shared configuration lives in [machine.nix](machine.nix), P console helpers in
[demo.nix](demo.nix), the product runner in [../demo.nix](../demo.nix), the
container fixture in [image.nix](image.nix), and behavioral probes in [smoke.sh](smoke.sh).
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
