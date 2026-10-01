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
unexpected instance metadata, manually changed containers, and external image
deletion. Unfamiliar machinery must not be silently adopted or used as proof
that the expected runtime is absent.

**Gate:** the claimed Incus/storage-driver/architecture combination.

## 3. Runtime networking

**Validate:** Prove the `none` profile has no general network. For the optional
`public-egress` profile, allow required public DNS/HTTP(S)/Nix fetch traffic
while denying host, RFC1918/ULA, link-local, carrier-grade NAT, metadata,
multicast, gateway administration, sibling instances, Incus API, and undeclared
services. Cover IPv4, IPv6, DNS rebinding, redirects, literal addresses, and
host aliases.

For the dedicated public-egress VM selection, enable outbound public routing
with explicit outer-VM host/LAN/private/metadata/inbound denial rules; other
selections retain restricted networking. Validate the session-local and outer
loopback resolvers against real DNS-over-HTTPS upstreams with literal bootstrap
addresses and verified TLS names on TCP 443. Preserve existing pinned port-53
allowances at the VM boundary; prove from the resolver configuration and
bounded network evidence that hostname HTTPS and a fresh Nix fetch use DoH
without plaintext bootstrap or fallback. Require actual public-to-private hostname
resolution and HTTPS redirect denial evidence; synthetic fixtures are separate.

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
test the supported **Try again with changes** paths as a new creation that
supersedes and cleans the failed provisional creation before reusing the desired
branch name. For complex cases that refuse integrated replacement, validate the
documented cleanup followed by a separate new Create. Cleanup must preview
losses, require explicit confirmation, recheck identity/ownership, recover
durably, and preserve shared or unrelated resources. Uncertain cleanup must
explain the unresolved condition and leave uncertain resources intact.

For the narrow assembled failed-creation inspection path, use an actual failed
host startup with a stopped, committed local base-image workspace. Prove that
the non-activating loss helper preserves workspace/private files and the assigned
P ref, removes only its helper, and leaves the original session `creating`.
Exercise durable creator fencing across Retry, replay and daemon restart;
reject changed native identity, unsupported policy/layout and uncertain init.
Read-only inspection evidence does not establish confirmed cleanup acceptance.

For confirmed assembled cleanup, change workspace bytes after preview and prove
fresh quiescent comparison refuses source deletion, preserves private/dummy
credential files and releases only the settled precommit helper/guards. Fresh
inspection/review/confirmation must then remove only the exact owned source and
reviewed local authority, retaining the P branch, sibling and shared base image.
Crash after native deletion/local cleanup; unavailable or competing authority
must retain the removing identity and accepted intent until exact recovery.
Reject retired Create replay and prove a separate corrected Create receives a
new UUID. Label injected outage/crash evidence separately from real native work;
whole-runtime loss warnings do not claim private credential enumeration.

**Gate:** reliable status/control from sessions and local NixOS client support.

## 6. Lifecycle and authority recovery

**Validate:** Crash at every documented cross-authority commit point. Verify
create/rename/discard/delete converge without duplicate Incus instances or
silent Git ref loss. Verify Incus-owned start/stop uses Incus operation/state
without a duplicate P workflow. Test missing versus unreachable, supported
repair, identity conflicts, image cache misses, immutable-policy
current/outdated/invalid comparison, and cleanup failures. Branch/upstream
mismatches must show expected and actual values, block dependent actions, and
recheck on the next attempt after manual Git correction. No dedicated mismatch
repair action or automatic checkout/reset is required.

When Incus is unavailable, verify cleanup remains incomplete, session/project
identity and durable confirmed operation state survive restart, and existing
authorization restrictions remain intact. Restore Incus and resume that same
confirmed operation through Retry/reconciliation. Do not report deletion
success, forget uncertain machinery or create a replacement while existence
is uncertain. Explicit abandonment, abandonment tombstones and the associated
orphan-cleanup/forget workflow are outside MVP; manual investigation is supported.

Create projects from a reachable SSH origin and as blank repositories. Verify
failed origin contact leaves no new association, an empty origin produces the
single unborn-`main` bootstrap session, first push creates `main`, and later
creation requires committed history. Test both creation paths: selecting an
existing unassigned branch requires no source and leaves its ref unchanged;
creating a new branch requires a source and creates only the named destination.
An already assigned branch must not acquire a second session. Cancel, fail,
retry, and replace each path, verifying that cleanup preserves pre-existing
refs. Exercise retained-branch assignment/list/source/fetch,
rename, fast-forward publication, and deletion after the loss preview.

For **Delete project and all P data**, confirm the aggregate preview enumerates
and terminates listed live attachments, the minimal deletion record survives daemon
restart, partial failures leave an idempotent ensure-absent retry with a smaller
remainder, and unavailable Incus keeps identity and cleanup incomplete until
the confirmed operation can resume. Verify
there is no rollback or hidden multi-phase recovery mode.

For the implemented quiescent aggregate boundary, also prove stale ref/workspace
refusal before retirement; atomic project/session/principal closure; partial
resource absence and blocked competing/renamed/unavailable identities across
restart; no repeat dispatch of an issued source DELETE; repository/registry-last
removal; and retired Create/origin/publication keys that cannot revive deleted
authority. VM52 is the base-session fixture for this batch. Production-path
cached-session/image deletion requires separate actual builder/cache evidence;
synthetic indexes or base-only sessions do not establish that gate. Running/live
attachment preparation and oversized/unsupported inventory refusals remain
explicit boundaries, not proof of automatic deletion in those states.

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

For the current CLI-first implementation, authenticated Codex acceptance is
reserved for the user's final manual test. Automated unit and VM checks use
event fixtures and dummy credential files for persistence, isolation, and
deletion. They must not log in, request credentials, or access host Codex or
OpenAI credentials. Fixture results do not satisfy the real versioned-trace
gate above. Record authenticated acceptance as pending user validation in
[implementation progress](implementation-progress.md) until the user completes
the session-local execution, hook/status, Stop/Start, and Discard/Delete checks.
Other MVP work proceeds without that authentication.

## Live terminal browser

### TUI change and validation workflow

Apply this workflow to changes in TUI layout, navigation, controls, feedback,
or terminal handoff, and when assessing the implemented experience. An LLM
must navigate and review the actual rendered product before its experience is
accepted or its workflow is encoded in the integration driver. Passing driver
checks alone does not satisfy this review.

1. **Establish the intended experience.** Read the
   [reviewed prototype decisions](../.prototype/tui-options/DECISIONS.md) and
   the [implemented keys and limitations](user-guide.md#use-the-terminal-browser).
   Run the current prototype using its [launch instructions](../.prototype/tui-options/README.md#run-the-current-browser)
   and inspect the affected interaction. Use its selected browser as the
   comparison baseline; the older gallery captures are historical evidence.
   Identify the developer's task, expected sequence and visible outcome.
   Record prototype features outside implemented scope separately from
   mismatches in supported behavior; lifecycle and authority contracts still
   govern production behavior.
2. **Observe the current product before changing it.** For presentation and
   navigation, use `just tui-mock` to run the production `p tui` against its
   [mock socket](#mock-socket-tui-exploration-and-fast-integration). For backend
   behavior, use a real daemon and Incus sessions in the
   [P lab](../dev/vm/README.md#explore-p) or an equivalent disposable instance.
   The LLM chooses actions from the
   current screen, reads the resulting screen, and continues through the
   task. Capture the states and transitions that explain any mismatch with
   the prototype, including errors or unavailable actions. API observations
   may confirm effects, but navigation must use the visible product controls.
3. **Implement and review the changed experience.** Run relevant component
   tests, then have the LLM repeat the task on the changed product, inspecting
   screens between actions. Compare layout, spacing, hierarchy, selection,
   command placement, readable feedback and Back/cancel behavior with the
   prototype. For layout changes, compare at matched dimensions, including
   120×35 and 80×24; inspect 48×16 when changing compact presentation. Exercise
   affected loading/error/empty states and resizing. For terminal handoff,
   inspect entry, command execution, detach and repeated entry, including
   intermediate frames that could expose the previous console. Fix discovered
   mismatches and repeat the affected path before accepting the experience.
4. **Validate integration at the affected boundary.** After the LLM experience
   review passes, run `just tui-tests` for socket-backed navigation, action
   wiring, cancellation, and local terminal transport. Add or update its action
   coverage for the reviewed workflow. If production backend behavior or its
   boundary changes, also run the smallest relevant serial VM selection and
   update major workflow coverage where needed. Use actual backend effects
   to establish completion: creation and branch assignment, native terminal
   execution, Git updates, attachment teardown, service state/journals, and
   reviewed cleanup with native absence. Screen recognition may synchronize
   actions; keep detailed navigation invariants in focused component tests.
   A changed heading or key sequence in the driver cannot resolve a product
   experience mismatch. Existing driver checks may help reproduce a failure
   during investigation; they do not replace the LLM review.
5. **Record both kinds of evidence.** Record the tested revision/build,
   instance setup and fresh/persistent state, terminal dimensions, actions,
   representative before/after screens, findings and their resolution in
   [implementation progress](implementation-progress.md). Identify the LLM
   experience-review result separately from component and VM results. Store
   its screenshots and action records in the
   [interactive LLM review evidence](llm-interactive-review/tui/README.md)
   directory, separately from automated integration-test implementations.
   After any further product change, repeat the affected experience review
   and relevant integration checks. Preserve unaffected evidence with its scope.

Use terminal screenshots or faithfully rendered terminal frames that retain
cell positions, styling and cursor/selection state. Read the reconstructed
screen; raw ANSI output, text matches and post-run transcripts alone cannot
establish presentation or navigation quality. A PTY or capture helper can
provide input/output, but a complete scripted run followed by an LLM reading
its pass marker is not an interactive experience review. Include enough
transition evidence to assess loading and terminal handoff, beyond final
screens. Keep credentials out of captures.

The review passes when the affected developer task completes through visible
controls, its screens and transitions match the reviewed direction within
documented product scope, and discovered experience defects are resolved.
Major integration checks must also pass before claiming the implemented
workflow works end to end. If actual terminal access or an integration run is
unavailable, record the missing evidence and leave that acceptance pending;
do not substitute fixture snapshots or a scripted pass marker for the live
review. Interactive navigation of the production TUI against the mock socket
can establish presentation and navigation evidence. Record that scope; actual
Incus, Git, systemd, isolation, and durable-storage acceptance requires native
integration evidence.

### Mock socket TUI exploration and fast integration

`just tui-mock` builds the current production CLI and
[fixture server](../tests/integration/cmd/tui-mock/main.go), starts the socket
server in the background, and runs normal `p tui SOCKET` in the foreground.
The client, renderer, key decoder, attachment client, and helper use their
production paths. There is no TUI fixture flag or alternate renderer.

```sh
just tui-mock                       # portfolio: 24 projects, 120 sessions
just tui-mock --dataset small        # two projects, four sessions
just tui-mock --dataset empty        # empty-state and project creation review
just tui-mock --theme light          # explicit light palette
just tui-mock --theme dark           # explicit dark palette
just tui-tests                      # fast CI gate, no VM
```

The recipes supply pinned Go, tmux, Python, pyte, Pillow, and fonts through
`dev/tui-test-shell.nix`. Warm-cache builds and tests need no VM image or Incus
daemon. The launcher owns a private `/tmp/p-tui.*` directory and tmux socket;
normal quit, Ctrl-C, or termination cleans up its server and files. Every run
starts fresh. Enter reaches a local tmux shell through the production attachment
helper and a mock native HTTP/WebSocket endpoint. Ctrl-B then d detaches; tmux
survives detach, and workspace files survive mock Stop/Start within one run.
This shell runs as the local user, with a disposable home and workspace; it
does not provide the VM's isolation. Project services and their journals are
stateful simulations of three session-user units, not host systemd services.

The [PTY integration suite](../tests/integration/tui-mock-test.py) issues TUI
actions as keyboard input. Fixture state and request records provide separate
effect assertions; the tests do not call lifecycle mutations through the API.
One-shot delay/error injection exercises loading, refusals, and stale replies.
`mock.inspect` and `mock.configure` exist only on the disposable fixture socket
and are excluded from production capabilities.

| Production action | Fast integration coverage and oracle |
|---|---|
| Movement: j/k, arrows, pages, Ctrl-B/F/U/D, Home/End, gg/G | Production terminal decoder, selection/page changes, first/last retained branches |
| P project scope; / search | Scope, branch/report matching, empty matches, backspace, cancel and clearing search/scope |
| A reports; p policy; b branches; ? help; r refresh | Actual production inspection pages, complete paginated branch inventory, help return and socket inventory refresh |
| Enter session | Start, production attachment claim/confirmation, real tmux command output, resize, detach, re-entry, presence teardown and workspace retention |
| s Stop | Default No and explicit decline issue no Stop; confirmation changes fixture condition; subsequent Start retains files |
| c Create | Project creation, new branch from P source, captured external origin, retained-branch resume; assigned identity, source params and automatic attachment |
| R Rename | Literal q/g/G in forms, editing/cancel, reviewed rename preserving UUID and changing assigned ref |
| d Discard; X Delete | Stopped-session requirement, fresh loss inspection and bound preview, cancel with no removal; Discard retains ref, Delete removes it |
| O operations; Enter inspect; r retry | Inspect failed operation, retry request and completed observation |
| S services; s start/stop; r restart | Exact session/unit/action, updated state, inventory/action refusals, queued action bound to original unit, Back cancels queued work |
| Enter/J journal; h/l; /; n/N; f; movement | Journal data, horizontal pan, find/matches, follow toggle, scroll, clear find then Back |
| q/Esc/Ctrl-C; resize | Contextual Back and form cancellation, empty inventory, clean quit, browser at 120×35, 80×24 and 48×16, attached tmux pane size |

The suite checks every RPC method used by the current TUI action path, and flags
new method literals that have no entry in its coverage set. Focused Go tests
validate the fixture's closed request fields, bounded pagination, source
freshness, idempotency, cancellation, and preview/token binding. Existing model
tests retain finer navigation and asynchronous-state invariants. Failures save
rendered frames and fixture state under `.cache/tui-mock-failures/`.

This gate checks all current action families and selected failure paths. It
does not enumerate every failure combination or prove prototype visual parity.
An LLM must still navigate and inspect affected screens before accepting a UI
change, following the workflow above. Mock checks establish UI/socket wiring;
selected VM workflows establish actual backend effects. `just test` includes
both the fast gate and the full VM suite.

### Automated browser evidence

**Validate:** Model fixtures prove global waiting/running ordering, selection
identity, project scope and fuzzy search, layered Back, literal text inputs,
resize-aware frame and paging bounds, inert terminal controls, complete readable
confirmation/loss fields, default-No and captured destructive identity, and no
automatic entry after leaving progress. They are client/unit evidence.

The native VM56 selection drives the installed `p tui` through a real PTY and
daemon. Verify creation and confirmed attachment, command execution in the
native workspace, detach lease teardown, actual navigation and default-No Stop,
and native session-user service start/restart/stop and journal reads. Installed
inactive units must be discoverable. Deny infrastructure names and preserve
Incus identity/isolation; finish with confirmed cleanup and native absence.
VM17 separately covers the optional reviewed-origin URL binding and captured
creation replay. Run actual VM selections serially. Record passing and failed
evidence in implementation progress; prototype simulations do not satisfy this gate.

**Gate:** implemented browser and bounded user-service integration. Real Codex
authentication remains the separate manual gate above. Experience acceptance
also requires the LLM review in the workflow above; native VM evidence proves
the integration effects exercised by that selection.

### Three-service developer workflow

Use the [notes application](../examples/notes/README.md) as the shared source
for the developer walkthrough and native checks. Provision its pinned
PostgreSQL/Python/Psycopg runtime before acceptance. Verify real SQL, HTTP
requests and worker results in a confined session, with a private Unix socket
and no database TCP listener or published application port.

The CLI/API selection `57-developer-workflow.sh` covers blank bootstrap and
first push, new and retained branches, edit/fail/fix, attachment teardown,
three discoverable user services and journals, dependency failure/recovery,
private databases and schema, Stop/Start, daemon restart, rename and reviewed
cleanup. Independently verify Git tips, native processes, rows/jobs and
resource absence. Existing origin/publication gates retain their own scope;
they do not establish a sample-source origin walkthrough.

After those workflows pass, review the sample through adaptive live LLM
navigation under the procedure above. The major TUI selection
`58-notes-tui.sh` preserves the reviewed creation, native terminal execution,
three-service control/journals, SQL/job effects, source and database isolation,
Stop/Start, rename, retained-branch reassignment and reviewed cleanup effects.
It cannot replace the experience review. Keep detailed navigation invariants
in component tests and record its native result separately from the LLM review.

Test a full clean shutdown and relaunch on a dedicated persistent notes lab
disk separately from daemon restart. The supplied
[persistence check](../tests/integration/notes-lab-persistence.sh) prepares and
checks retained identity, pushed/local source, dirty/private files, installed
units, SQL rows and a pending job. Record the two distinct boot identities and
successful explicit service recovery. Preserve existing developer disks.

**Gate:** the documented provisioned three-service developer walkthrough.
A real committed devShell remains a separate environment-building milestone;
success with provisioned tools does not establish offline input resolution.

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
every CLI JSON/API field and protocol behavior parsed by P. MVP installation
support is NixOS with Incus only. Backup/restore and software upgrade/rollback
are outside delivery gates; normal Stop/Start and daemon-restart persistence
and operation-level crash recovery remain required. Future changes to supported
dependency ranges require the relevant conformance suites.
Bifrost and the SSH client transport receive their own pins when those
post-MVP capabilities are enabled.

**Gate:** release support for each affected integration.

The distribution selection (VM48) inspects installed CLI and runtime-kit ELF
headers for absence of an interpreter and linked shared libraries, verifies
installed dependency and pinned Go notices, and exercises the installed plugin
catalog through actual CLI/daemon and Incus session creation, Git and Stop/Start.
Its Codex adapter notification remains an authentication-free event fixture;
authenticated execution is a separate manual acceptance gate.

The NixOS service selection (VM55) must exercise the actual module-generated
service and private account-owned configuration, explicit bundled activation,
real isolated session creation and Git, live-host continuity through daemon
restart, stopped-state restart, Stop/Start persistence, and confirmed cleanup.
Focused module evaluation must reject root or administrative Incus accounts,
unavailable Incus, unsafe state directory names and public-egress settings.
The hardened service supports `network: none`; the separate owner-run daemon's
public-egress evidence must not be described as service-module acceptance.
Authentication-free dummy private files prove persistence and removal only;
authenticated Codex execution remains the user's manual gate.

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

## Cached-session bulk deletion

Selected `53-project-delete-cached.sh` exercises the public environment build,
native image import/index and aggregate preview before exact project deletion.
It requires removal of the owned image/index after runtime absence, preservation
of shared base/unrelated resources, and the same outage/restart/competing-identity
checks as VM52. Its offline fixture source does not establish external-repository
or public-fetch acceptance. Evidence and remaining gates belong in the progress
record.
