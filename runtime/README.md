# Base session image and tmux host

`image.nix` is the production base-image module. It installs the local Nix
daemon/client, Git, OpenSSH (`/usr/bin/ssh` for the source-git asset), tmux,
Codex `0.151.0`, Python, and the root-owned runtime kit. The interactive-host package is authored in
`plugins/bundled/tmux-host`; only its three declared assets should be staged
into a package directory for conformance and digest-pinned activation.

Trusted session assembly, before the first container start, installs the
selected asset plans with their modes and root ownership, and writes a regular,
root-owned, non-writable-by-p `/etc/p/session.json` (mode `0644` or tighter while
remaining readable by p). For the base-only selection it has this shape:

```json
{"schema":"p.runtime-session/v1","activation":"base","command":["/run/current-system/sw/bin/bash","-l"]}
```

The config is a closed structured argv. The first argument is an absolute,
clean executable path. The helper executes it directly inside the tmux pane.
For committed devShell activation, `p.runtime-session/v2` requires
`activation: "devshell"` and the accepted `material_sha256`. The kit verifies
the root-owned activation material before sourcing it once per Start, then
executes the foreground tmux host with the resulting environment.

Sessions with a selected agent asset use `p.runtime-session/v3`, which also
requires `agent_sha256` and permits either base or devShell activation. Assembly
and startup verify the installed adapter bytes against that digest. The
optional Codex adapter appears at `/usr/libexec/p/codex-adapter` through
`/etc/p/assets/p-codex-adapter`. Its explicit `init` command creates private
session-user configuration without overwriting existing user state; it does
not log in or trust hooks on the user's behalf. Authentication-free fixture
evidence and the pending manual gate are recorded in
[implementation progress](../docs/implementation-progress.md).

The image uses static local account lookup without nscd/nsncd so Incus can
enumerate files while the container is frozen. Hostnames use standard
`files dns` lookup; additional NSS modules are excluded by the
[runtime substrate contract](../docs/runtime-isolation.md#non-activating-workspace-access).

NixOS links `/etc/systemd/system` to an immutable generated store tree, so the
image provides fixed unit links from the plan's visible destinations to
`/etc/p/assets/p-session.target` and `/etc/p/assets/p-interactive.service`.
Trusted assembly writes verified bytes to those exact mutable targets and
checks that the fixed paths resolve to them, rather than trying to replace a
store-owned symlink. NixOS adds a stateless `Wants=p-session.target` dependency
to `multi-user.target`; before assembly the unit link is dangling, so the base
image alone does not start the host. Boot-time tmpfiles gives the attach
asset the fixed path `/usr/libexec/p/attach` through a root-owned link to
`/etc/p/assets/p-attach`. The source-Git asset similarly appears at
`/usr/libexec/p/git-ssh` through `/etc/p/assets/p-git-ssh`. Assembly writes
verified bytes to those mutable targets.

Before `p-interactive.service` starts, trusted assembly must establish the
paths specified by [runtime isolation](../docs/runtime-isolation.md#fixed-paths):

- Incus mounts the socket-only host directory read-only, private, and unshifted
  at `/opt/p/endpoints`. Its host owner is unmapped inside the container. A
  private `0700` host ancestor and placement into only the assigned instance
  protect the sockets. The directory has mode `0755` and contains only
  `session.sock` and `git.sock` with mode `0666`.
- `/etc/p/git` lives in the private instance root. Through the confined Incus
  file API, install the directory as guest uid/gid `0:0`, mode `0755`;
  `ssh_config` and `known_hosts` as `0:0`, mode `0644`; and `identity` as
  `1000:1000`, mode `0400`. The SSH config addresses `/run/p/git.sock` through
  the fixed stream helper (`ProxyCommand /usr/libexec/p/runtime-kit git-stream`)
  and uses these identity and known-host files.

The fixed user is `p` (uid/gid 1000). NixOS activation mounts `/run` as tmpfs,
so the root-only unit hook validates the exact staging mount at
`/opt/p/endpoints`, makes it private, and bind-clones the same read-only socket
directory to `/run/p` after activation. It makes the final bind private and
checks that both mounts expose the same socket objects. Unprivileged validation
checks the final socket types and modes and credential ownership before host
activation. Incus restores the staging mount on every start; the hook recreates
the public bind after `/run` tmpfs recreation. Credentials remain in the
private root. The root-only `init-workspace` hook validates trusted config,
assets, credentials, and endpoints. It initializes private scratch through a
mount namespace, with Git running as uid/gid `1000:1000`, then atomically
publishes the complete standalone clone at `/workspace`. Git verifies the
assigned branch against the captured object ID, or establishes unborn `main`
for an empty P Git repository. Retry reconstructs interrupted unpublished
scratch; a completed retained workspace is left intact, and unexpected
contents at `/workspace` are preserved and refused. The protected staging and
publication rules are owned by [runtime isolation](../docs/runtime-isolation.md#fixed-paths).
Trusted workspace v2 binds the captured commit to the accepted environment
selection: absent root flake, valid absent default, or selected devShell. The
older workspace v1 contract still refuses an unresolved root flake. After assembly and boot, the
stateless target dependency starts the host. If assembly happens after boot,
reload systemd and start `p-session.target`. Confined file installation, socket
connectivity, host/sibling denial, and retained-instance restart require VM proof.

Systemd tracks the foreground tmux server as `MainPID`. `ExecStartPre` checks
the config and paths. `ExecStartPost` creates the fixed session and checks its
socket before systemd marks the unit active. When the tmux session ends, the
server exits and `ExecStopPost` requests container poweroff. Attachment runs
only `/usr/libexec/p/attach` as p with an Incus PTY and `/workspace` as cwd.
