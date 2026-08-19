# P — development validations

Evidence to gather while implementing P.

> These validations do not block unrelated development. Each one blocks only
> the feature or support claim named under **Gate**. Product behavior remains
> authoritative in the corresponding design document.

## 1. Rootless runtime networking

**Validate:** Under the pinned rootless Podman version, allow public internet
egress while denying the host, private and link-local networks, metadata
endpoints, sibling runtimes, and container-engine administration. Cover IPv4,
IPv6, DNS, redirects, host aliases, and only the explicit P/Bifrost paths.

Default `pasta` connectivity is not proof of this policy; test the selected
firewall, namespace, or proxy mechanism directly. Run the same suite before
claiming Docker support.

**Gate:** the isolated outbound-network profile for each tested backend. Core
Git, registry, RPC, TUI, and network-disabled runtime work may proceed.

## 2. Isolated Nix realization

**Validate:** A restricted worker can evaluate an immutable commit, realize its
devShell, capture activation, and export only the required closure without
exposing the host Nix daemon, full store database, or ambient credentials.
Verify cache behavior and actionable failure reporting.

For a Nix-native repository that must invoke Nix again during interactive
development, also execute the complete
[Nix project workflow validation](nix-project-workflow-validation.md). It
separates devShell realization, writable runtime Nix, fleet builds, secrets,
remote builders, VM devices, and deployment authority instead of assuming
that a closure-only runtime covers them all.

**Gate:** the Nix environment builder. Runtime, Git, and control-plane work may
use the minimal substrate until this passes. The interactive Nix capability is
separately gated by the workflow evidence; it must not be implemented by
mounting the host Nix daemon as a shortcut.

## 3. Session RPC socket restart

**Validate:** A daemon restart does not leave a runtime bound to a stale Unix
socket inode. Test mounting a private per-session directory containing the
socket, reconnection, authentication, and preservation of the session UUID.

**Gate:** reliable runtime-to-daemon RPC and agent-hook status across daemon
restart. Git and interactive attachment do not depend on it.

## 4. Bifrost integration

**Validate:** Treat Bifrost as an independently configured persistent service.
Against a pinned release, verify:

- virtual-key creation, persistence, use, and revocation;
- mandatory virtual-key enforcement and model filtering;
- session access to inference without access to dashboard/admin routes;
- disabled prompt/response content logging when configured; and
- P and Bifrost restart behavior.

Projects without model access must remain ready when Bifrost is absent.

**Gate:** optional model access. All non-model session work may proceed.

## 5. Agent-hook mappings

**Validate:** Capture and version real Claude Code and Codex hook traces for the
events mapped to P's latest unattended condition. Cover permission/input,
activity, normal stop, failure, subagents, session termination, absent hooks,
and events emitted around attach/detach.

Confirm that latest-event replacement, clear-on-enter, and suppression while
attached match the supported agent versions. Do not add causal attention or
history semantics during this validation.

**Gate:** semantic agent status and notifications for each claimed agent/version.
Runtime lifecycle and attachment remain usable without an adapter.

## 6. Dependency and protocol pins

**Validate:** Record and exercise the exact versions used by each integration
suite: Go, Bubble Tea/Bubbles/Lip Gloss, Wish, Git, Nix, Podman, Docker,
Bifrost, and the default interactive host (`tmux` initially). Pin external
schemas and CLI output formats relied upon by parsers.

**Gate:** release support for the affected dependency or integration. Pins may
be added incrementally as each component enters implementation.

## 7. Runtime engine and grant conformance

**Validate:** Against the pinned rootless Podman release, exercise the complete
normalized runtime contract: stable labels, explicit workspace/home/data
storage, stop/start persistence, guarded removal, read-only/read-write and
non-executable/executable bind grants, private mount propagation, pause/resume,
and the isolated non-activating workspace helper. Confirm images cannot create
anonymous volumes, publish ports, add capabilities, replace P endpoints, or
inherit ambient host sockets and environment.

Run the same suite before enabling rootless Docker. Record every engine-specific
flag and inspect-field mapping behind the shared backend interface; a difference
must be normalized or reported as an unsupported capability, never ignored.

**Gate:** runtime creation and grant support for each claimed engine. Git,
registry, RPC, TUI, environment building, and a fake backend may proceed while
the first real engine is being validated.

## Recording results

For each validation, record the version, host/kernel context, commands or test
case, result, and resulting implementation constraint beside the code or test
that depends on it. A failed validation narrows or postpones that feature; it
does not stop unrelated milestones.
