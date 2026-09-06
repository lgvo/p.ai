# P — development validations

Evidence to gather alongside implementation.

> A validation blocks only the capability/support claim named by its **Gate**.
> Product behavior remains authoritative in the corresponding design document.

## 1. Confined Incus authority

**Validate:** With the pinned Incus release, give P only the configured confined
user project/socket. Prove it can create and operate labeled builders and
session instances but cannot access other projects, the administrative API,
unapproved devices/paths, host namespaces, privileged/raw configuration, or
the Incus socket from inside an instance.

Also verify startup detects a missing/misconfigured confinement ceiling and
fails without falling back to administrative authority.

**Gate:** real Incus runtime/build support. State, Git, RPC, TUI, and fake
backend development may proceed.

## 2. Incus runtime and storage conformance

**Validate:** Exercise deterministic names/metadata, image launch, private root,
workspace/home/private `/nix` persistence, start/pause/resume/stop/remove,
operation inspection, events/gaps, structured exec/attach, resource limits,
fixed endpoint mounts, filesystem grants, and the non-activating workspace
helper.

Run on each claimed storage driver and architecture. Measure logical and
physical image/session sizes, but do not generalize copy-on-write or
deduplication behavior across drivers. Test interruption, duplicate detection,
builder/session orphans, and external image deletion.

**Gate:** the claimed Incus/storage-driver/architecture combination.

## 3. Runtime networking

**Validate:** Prove the `none` profile has no general network. For the optional
`public-egress` profile, allow required public DNS/HTTP(S)/Nix fetch traffic
while denying host, RFC1918/ULA, link-local, carrier-grade NAT, metadata,
multicast, gateway administration, sibling instances, Incus API, and undeclared
services. Cover IPv4, IPv6, DNS rebinding, redirects, literal addresses, and
host aliases.

Incus network/ACL defaults are not sufficient evidence; capture the actual
configured routing and packet-level test results.

**Gate:** `none` first, then the public-egress project capability. Narrow Unix
endpoint access may proceed independently.

## 4. Nix environment images

**Validate:** In a disposable restricted Incus builder, evaluate an immutable
commit, resolve the conventional default devShell without lock mutation,
realize it without host Nix state, capture activation, create the GC root,
verify/scrub it, and publish a coherent private Incus image. Pin the Nix
version and validate the experimental `nix print-dev-env --json` contract:
feature flags/argv, JSON schema and value types, quoting/unset/export behavior,
functions, hooks, failures, and activation equivalence against representative
`nix develop` fixtures. Reject unknown output rather than sourcing an
unvalidated fallback.

Verify the cache key includes P project scope, the materialized checkout and
unreachable temporary paths are absent, and any committed source-derived store
path retained by the closure cannot be reused by another P project.

Launch two sessions from one fingerprint and prove:

- each activates the expected environment;
- each local Nix daemon can update only its own private store/database;
- each has a private writable Nix database/store delta;
- new paths in one session do not affect the image or the other session;
- stop/start retains private paths while removal deletes them;
- removing the cached image does not break existing instances and becomes a
  cache miss for new creation;
- after both image and instance loss, repair exposes any environment change in
  the current committed branch and requires explicit recreation approval; and
- base-only behavior works when no default devShell exists.

Run the complete [Nix project workflow validation](nix-project-workflow-validation.md)
against the real homelab repository.

**Gate:** MVP environment building and Nix-capable sessions. Other control-plane
work may use a fixture image.

## 5. Session RPC restart and attachment

**Validate:** A daemon restart does not strand runtimes on a stale Unix socket
inode. Test the mounted per-session endpoint directory, socket recreation,
identity binding, reconnect, loss of pending tokens and confirmed live leases,
structured Incus attachment, multiple clients where supported, and the direct
local Unix client path. Prove that a failed channel establishment or
expired token never increments attachment presence or clears unattended
status, while successful confirmation performs both. Prove the trusted helper
retains the confirmed lease until channel teardown completes while the daemon
is reachable. If daemon restart removes the lease first, teardown must begin
immediately and that helper must establish no new lease/channel until it
finishes. Verify that no existing-channel registration is accepted.

Kill the ordinary client and helper with SIGKILL and sever local terminal
pipes. Every case must remove only the affected temporary attachment while
preserving the tmux server/session and persistent host. Switching between
sessions must behave the same way. SSH/network and remote-client-loss cases
gate the post-MVP SSH client transport.

Validate the base image's systemd contract: `p-session.target` starts
`p-interactive.service`; the service applies environment activation and
supervises the configured persistent host in its cgroup; the root-owned
`/usr/libexec/p/attach` connects to that host with fixed structured argv; and a
clean or failed host exit captures bounded journal diagnostics before stopping
the container. Ordinary Start after the stop must launch the host again.
Neither Start nor Attach depends on an attachment being retained.

During initial creation, inject activation and host-start failures. The
container must stop, the durable operation must identify the failed phase, and
diagnostics must remain available after the container has stopped. Exact Retry
must preserve its operation/session/source/policy identity, clean only verified
partial derived resources, and rebuild without creating a retry chain. Also
test **Try again with changes** as a new creation that supersedes and cleans the
failed provisional creation before reusing the desired branch name.

**Gate:** reliable status/control from sessions and local Linux client support.

## 6. Lifecycle and authority recovery

**Validate:** Crash at every documented cross-authority commit point. Verify
create/rename/discard/delete converge without duplicate Incus instances or
silent Git ref loss. Verify Incus-owned start/stop uses Incus operation/state
without a duplicate P workflow. Test missing versus unreachable, repair,
abandonment, orphan recognition, image cache misses, immutable-policy
current/outdated/invalid comparison, and cleanup failures.

Create projects from a reachable SSH origin and as blank repositories. Verify
failed origin contact leaves no new association, an empty origin produces the
single unborn-`main` bootstrap session, first push creates `main`, and later
creation requires committed source. Exercise retained-branch list/source/fetch,
rename, fast-forward publication, and deletion after the loss preview.

For **Delete project and all P data**, confirm the aggregate preview enumerates
and terminates listed live attachments, the minimal tombstone survives daemon
restart, partial failures leave an idempotent ensure-absent retry with a smaller
remainder, and unreachable resources require explicit abandonment. Verify
there is no rollback or hidden multi-phase recovery mode.

**Gate:** each lifecycle mutation as it enters the implementation.

## 7. Git and origin

**Validate:** Against pinned Git/OpenSSH/Wish versions, test session/host
principal scope, unconditional fast-forward-only session updates, ref guards,
reserved/hidden namespaces, arbitrary object wants, rename races,
local-only bypass, host-SSH origin refresh, and explicit origin publication.

Simulate definite and unknown publication failures. A later explicit retry must
freshly fetch and safely satisfy an already-applied result without protected
tracking refs or a publication ledger.

**Gate:** the corresponding P Git/origin feature.

## 8. Post-MVP Bifrost

**Validate before enabling the post-MVP capability:** Treat a pinned Bifrost
release as an independently configured service. Verify virtual-key
ensure/persist/use/revoke,
model filtering,
disabled content logging where configured, and P/Bifrost restarts. Enable
administrative authentication without giving its credential to sessions and
prove every inference request requires a valid virtual key.

Using the real session key, positively probe approved inference and filtered
model discovery. Negatively probe dashboard, management, governance, logs,
MCP, skills, and every other route in the pinned-version inventory; require an
authorization rejection rather than treating a connection/server failure as
evidence. Also test absent, invalid, and revoked keys. Inventory new routes on
upgrade and fail model access closed for an unclassified route or unvalidated
effective configuration. Do not assume a default value of
`disable_auth_on_inference`; validate the resulting behavior. Projects without
model access must not depend on Bifrost.

After a model-enabled session is established, take Bifrost down and prove Start
and Attach still work without a gateway probe while inference fails clearly.
Initial creation must still fail closed when key provisioning or boundary
validation cannot complete.

**Gate:** optional OpenAI-compatible model access. Anthropic, MCP, Skills,
Agent Mode, and Code Mode each need later evidence before support is claimed.
This is a blocking spike for the post-MVP model capability only. Failure
leaves model access disabled for that pinned release; it cannot weaken the
boundary or block Git, RPC, lifecycle, runtime, TUI, or any project without a
model grant. An L7 proxy requires separate design.

## 9. Agent-hook mappings

**Validate:** For MVP, capture and version real Codex hook traces for
input/permission, activity, normal stop, failure, subagents, session end,
absent hooks, and attach/detach timing. Confirm latest replacement,
clear-on-confirmed-entry, and confirmed-attached suppression against each
claimed version. Validate that authentication created inside the session's
private home survives Stop and Start, is isolated from other sessions, and is
removed by Discard or Delete without P reading or copying host credentials.
Claude Code and other agent mappings require post-MVP evidence.

**Gate:** semantic status-adapter support for that agent/version.

## 10. Event handler

**Validate:** For every MVP reduced event kind, verify the typed versioned
envelope, redaction, ordering at the handler call boundary, and NDJSON file
encoding. A handler write failure must produce a bounded diagnostic without
rolling back the lifecycle action or changing authoritative state. Restart
must not imply replay, acknowledgement, or an outbox. Repository content must
not be able to configure handlers.

**Gate:** the MVP local event log and its extension interface.

## 11. Dependency and protocol pins

**Validate:** Record the exact Go, Bubble Tea ecosystem, Wish, Git, OpenSSH,
Incus, Nix, tmux, Codex adapter, and SQLite driver versions used by MVP. Pin
every CLI JSON/API field and protocol behavior parsed by P. Verify upgrades
through the relevant conformance suites before widening supported ranges.
Bifrost and the SSH client transport receive their own pins when those
post-MVP capabilities are enabled.

**Gate:** release support for each affected integration.

## 12. Performance and capacity

**Validate:** On representative `x86_64-linux` and `aarch64-linux` machines,
measure cold realization, substituted realization, cached-image hit, builder
publication, session launch/activation, private Nix growth, cache deletion, and
storage-driver physical use for small and real projects.

Results describe tested projects, inputs, caches, storage drivers, and hosts;
they do not become a universal devShell-to-image latency or size promise.

**Gate:** performance/capacity claims and default operational guidance, not
functional development.

## Recording results

For every validation, record the date, exact versions, host/kernel/Incus
project/storage/network configuration, commands/test cases, raw result, and
resulting implementation constraint beside the dependent test or code. A
failure narrows or postpones that support claim; it does not block unrelated
milestones.
