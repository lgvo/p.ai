# MVP implementation progress

## Current state — 2026-09-26

- Latest committed checkpoint: **VM44 passed** retained-branch Delete; commit
  `f079675` on `feature/cli-first-mvp`. VM43 retained Rename, VM42 both-absent
  record cleanup, VM40 principal rotation, VM39 bare-present ref repair,
  VM38 missing-image repair, and VM34 missing-runtime repair passed their
  selected gates. Detailed findings and logs remain in the evidence log.
- **8f1 in progress:** the partial existing-branch Create replacement patch is
  preserved. One Sol/high stream is finishing tests, API docs and a real
  public-path VM45 fixture before recovery review. No VM45 pass is claimed.
  New-branch, dirty-workspace and uncertain-effect replacements remain outside
  this first slice and require separate verified recovery/loss-review work.
- The 2026-09-25 model/approval-service 401 interruption is recorded below.
  The user reported the OpenAI outage resolved and authorized resumption on
  2026-09-26. No VM or prior agent was running at resumption.
- Latest full VM checkpoint is **through VM28**, before subsequent changes;
  a final full-suite delivery checkpoint remains required. Actual VM runs stay
  serial; VM44 powered down and removed its fresh disk.
- Remaining implementation includes broader replacement, abandonment/orphan
  handling, bulk project deletion, and installation/upgrade/backup/restore
  acceptance. VM37 proved negative public-egress isolation and synthetic
  probes only; real public Nix fetch/DNS/redirect evidence remains unverified.
- Codex event/persistence and Discard/Delete cleanup used fixtures and dummy
  credentials. Real authenticated Codex acceptance remains **pending user
  validation** using the procedure below; no login or host credentials are used.

Execution record for the CLI-first implementation requested on 2026-09-23.
This is a non-normative tracker. The [implementation plan](implementation-plan.md)
and [project authority map](../PROJECT.md#authority-map) identify the governing
contracts. The production TUI is deferred by the current user instruction;
structured CLI/RPC results must expose the information it will need.

## Delivery procedure

The user's 2026-09-25 procedure keeps one implementation stream. The existing
implementer finishes its current batch when available; the coordinator handles
later ordinary implementation directly unless delegation avoids substantial
work. Reuse one Sol/high reviewer after each completed patch and focused tests
for authorization, isolation, destructive behavior, or durable recovery.
Astra escalation requires the user's explicit authorization. A reported model
usage limit must not be bypassed by retrying or switching agents or models.

Actual VM/integration runs remain serial. Focused tests and the smallest
relevant VM selections establish changed behavior; valid prior evidence is
reused. Full-suite validation runs at the final delivery checkpoint. After two
unsuccessful corrections of the same failure, obtain new diagnostic evidence
before another attempt; if unavailable, record the blocker and continue
independent work. Fixture-only
results do not establish production support. Outstanding patches, findings,
and unrelated working-tree changes are preserved.

## Steps and acceptance evidence

| Step | Deliverable | Unit/review focus | VM acceptance | State |
|---|---|---|---|---|
| 1 | Public plugin contract, Go/CLI scaffold, package validation and activation | Compatibility, grants, package confinement, first-party composition | Run packaged CLI; valid and invalid plugin packages; file-log boundary | Passed for package/activation/file-log foundation |
| 2 | SQLite control state, Unix NDJSON-RPC, trusted configuration, operation identities | Migrations, uniqueness, idempotency, bounded framing and errors | Daemon restart, duplicate/conflicting requests, private socket | Passed for state/RPC foundation |
| 3a | Executable plugin sandbox, typed broker and session asset plans | Resource bounds, absence of ambient authority, compatibility, digest pins | Independently authored module and packaged assets; denied undeclared effects | Passed for executable event handlers and asset plans |
| 3b | Bare Git repositories, session/host SSH principals and ref guards | Path/ref validation, assignment authorization, revocation | Actual clone/push; reject cross-branch, non-fast-forward and host writes | Passed for Git substrate and executable source plugin |
| 4a | Production base image and bundled systemd/tmux host assets | Trusted structured assembly, fixed attachment, readiness and lifetime | Private runtime, attach/detach, host exit and failed startup | Passed for assembled base runtime |
| 4b | Confined Incus runtime plugin and core broker | Authority ceiling, metadata ownership, bounded operations | Real user-socket create/inspect/start/stop and denial probes | Passed for executable runtime plugin |
| 4c1 | Production daemon Git composition and read-only project queries | Trusted activation, persistent keys, startup/cancellation, bounded RPC | Real configured daemon SSH and restart; foundation-mode compatibility | Passed for configured daemon Git |
| 4c2 | Blank bootstrap and committed-source session lifecycle | Immutable requests, assembly, retry/reconciliation | Two real sessions, Git workspaces, stop/start, failed creation/retry | Passed for base-image blank/committed creation and recovery |
| 5 | Trusted attachment helper, leases, session RPC and observability | Token races, connection ownership, unattended reducer, status projection | PTY attach/detach and client/daemon loss; persistent host survives | Status RPC, attachment, and daemon events passed VM |
| 6 | SSH origins, source selection, publication and retained branches | Contact-before-association, fast-forward publication, unknown results | Local SSH origin fixture; fetch/publish/retained branch workflows | Origin transport/association/creation and public publication/retained queries passed VM |
| 7 | Committed Nix devShell builds, activation and project-scoped image cache | Source/lock identity, activation validation, cache keys and cleanup | Restricted builder; two private stores; cache loss and stop/start | Offline creation/cache/activation/retry and explicit collection/recovery passed selected VM gates; public fetch remains gated on step 9 |
| 8 | Rename, destructive previews, discard/delete, repair/abandon and project deletion | Stale confirmations, guards, quiescence, crash recovery, tombstones | Real workspace/ref loss checks and restart at mutation boundaries | 8a1/8a2 loss, 8b1 previews, 8b2a Discard, 8b2b Delete, and 8c Rename passed VM28–33; repair/project deletion pending |
| 9 | Immutable project policy, filesystem grants and public-egress configuration | Normalization, drift, path identity, fail-closed capability gates | Negative mount/network probes, unchanged old policy, explicit recreation | 9a/9b VM35/36 passed; 9c negative isolation and synthetic probes VM37 passed, real public traffic pending |
| 10 | Versioned Codex adapter and session-local authentication workflow | Strict semantic mapping, absent/unsupported hooks, isolation | Authentication-free event fixtures and dummy credential storage checks; real authenticated acceptance by user | VM27 adapter/event/persistence and VM31/32 dummy Discard/Delete cleanup passed; authenticated acceptance pending user validation |
| 11 | Installation, upgrade, backup/restore and complete CLI acceptance | Compatibility, dependencies/licenses, API documentation | Clean VM install and full MVP acceptance matrix | Pending |

Steps may be split further when review or evidence reveals a distinct boundary.
No unrun check or fixture-only result establishes production support. Missing
external prerequisites stay explicit and do not silently reduce the MVP scope.

Step 4c2 has three verification boundaries, each with its own implementation,
review, and serial VM gate:

- **4c2a — committed source and branch effects:** extend the executable Git
  contract with bounded committed-source observation and guarded branch
  creation. Validate actual object/ref behavior, changed expected tips, and
  plugin authority denials before lifecycle orchestration uses these effects.
- **4c2b — trusted runtime assembly:** install verified assets and scoped
  credentials through closed Incus operations, initialize the private workspace,
  and observe systemd readiness. Validate assembly, retained state, and refusal
  of unexpected existing files in the VM without claiming public creation.
- **4c2c — public creation and recovery:** connect blank-project bootstrap,
  existing/new-branch sessions, immutable creation intent, exact retry, and
  start/stop inspection to the host RPC. Validate these through CLI requests,
  including interrupted creation and daemon restart.

Step 5 has three implementation boundaries. **5a** provides UUID-bound session
identity/capability queries, strict status reports, the durable unattended
reducer, and host status projections. **5b** adds the trusted PTY helper and
connection-owned attachment tokens/leases, connects their transitions to the
reducer, and enforces attachment-aware Stop. Each receives independent review
and serial VM validation; reducer unit tests alone cannot establish attachment
presence or transport behavior. **5c** wires trusted event-handler selection
and execution to committed reduced status/lifecycle/policy/presence changes;
the tested file-log plugin and a reducer callback alone do not establish that
daemon integration.

Step 6 will also use separate verification boundaries. **6a** adds the bounded
host OpenSSH origin runner and typed source-plugin methods for contact, fresh
ref observation, and fetching selected committed objects. **6b1** integrates
contact-before-association, replacement/removal, explicit refresh, and bounded
origin-source views for existing active projects. **6b2** adds origin-backed
project creation and captured origin sources to session creation. **6c** adds explicit
create-or-fast-forward publication and retained-branch queries. Retained-branch
rename/deletion and their loss confirmation share step 8's destructive-operation
gate. Each boundary requires independent review and a serial local-SSH-origin
VM fixture; network access alone is not evidence of correct origin semantics.

Step 7 begins with **7a**, a strict versioned parser and activation adapter for
the pinned Nix `print-dev-env --json` interface. Its unit coverage precedes a
VM compatibility check against `nix develop`; production flake creation remains
disabled until that check and the restricted builder/image pipeline pass.
**7b** adds immutable committed-source resolution and realization through the
environment plugin in a restricted builder. **7c** integrates verified image
publication, project-scoped cache identity, lifecycle selection, and explicit
cache collection. The adapter can be implemented independently while earlier
origin and attachment gates run; their actual VM runs remain sequential.

Step 7c is split into three reviewable boundaries:

- **7c1 — native image publication:** root the captured environment, verify and
  smoke-test activation, collect temporary store paths, scrub the stopped
  builder, and publish/verify a private image. The VM gate creates two private
  instances from the image and checks activation, retained closure, cleanup,
  and independent writable state. This alone does not enable public creation.
- **7c2 — cache and session composition:** persist project-scoped verified
  image identity, treat external image loss as a miss, bind environment
  selection to durable creation intent, and activate inside ordinary session
  startup. Validate cache reuse, source revision semantics, exact retry,
  stop/start, and two public sessions through CLI/RPC.
- **7c3 — explicit collection:** expose bounded loss previews and confirmed
  exact-owned image/index removal, including stale identity and missing image
  cases. Existing instances must continue independently. Whole-project
  deletion reuses this authority in step 8; no session action implicitly
  collects cached images.

Public fetch/substituter access remains gated on step 9's destination-isolation
evidence. Offline fixture success cannot establish general flake support.

For step 10, the user's 2026-09-24 instruction reserves authenticated Codex
acceptance for a final manual test. Automated work must not attempt Codex login,
request credentials, or copy/reuse host Codex or OpenAI credentials. Unit and
VM tests use event fixtures and dummy files to check credential-storage
persistence, isolation, and deletion. These are fixture evidence, not real
authenticated Codex integration evidence, and missing authentication does not
block other MVP implementation. Delivery must include a short CLI-first
procedure for the user to authenticate inside sessions and verify real Codex
execution, hook/status reporting, Stop/Start persistence, and Discard/Delete
cleanup. **Authenticated acceptance: pending user validation; not passed.**

**10a** follows the offline environment/cache checkpoint without waiting for
authentication: selected adapter installation, pinned-version event mapping,
private configuration setup, and fixture-backed status/Stop/Start/isolation
checks. A small Python standard-library asset keeps agent-specific logic in
the plugin and within the existing asset bound; Python and Codex are pinned
guest dependencies. Dummy credential deletion checks will compose with step
8's public destructive lifecycle. Networked manual execution also depends on
step 9's public-egress policy. Those later gates remain pending rather than
being replaced by direct fixture cleanup.

Step 8 will proceed through these bounded implementation/review/VM gates in
the same implementation stream:

- **8a — non-activating workspace access:** implement the closed isolated
  workspace inspection/mutation boundary from runtime isolation. Prove that
  stopped/frozen inspection neither activates the session nor runs repository
  hooks, helpers, monitors, or configuration. Bound worktree/ref/status/loss
  results, reject path escapes, and preserve quiescence across interruption.
- **8b — confirmed Discard/Delete:** bind previews to current workspace,
  runtime, and Git facts; reject stale confirmations and attachments; persist
  guards and the removal commit point; recover exact runtime, credential, and
  assignment cleanup. Retain the branch for Discard and delete only the
  confirmed ref for Delete. Compose the Codex dummy credential deletion and
  unaffected-sibling checks with these public operations.
- **8c — Rename:** preserve UUID, local-ahead work, credentials, and processes
  while guarded server/workspace refs and assignment converge through restart.
- **8d — Repair/Abandon:** expose only explicit targeted repair plans;
  distinguish missing from unreachable machinery; preserve orphan recognition
  and cleanup tombstones without automatic adoption or deletion.
- **8e — project deletion:** aggregate the reviewed loss, attachments, sessions,
  retained refs, and project images; persist ensure-absent recovery and report
  the remaining cleanup after partial failure.

Each gate requires focused tests and retained-reviewer agreement before its
serial VM selection. These are pending steps, not support claims. No step may
replace the required isolation or recovery checks with direct fixture cleanup.

Preparation checked the pinned Nixpkgs Codex package (`0.151.0`) and current
[official hook documentation](https://learn.chatgpt.com/docs/hooks). The later
adapter must verify that pinned release's actual event shape; current docs
alone do not establish release compatibility or live status evidence. Manual
acceptance must include reviewing/trusting the installed hook definition via
`/hooks`. The documented
[headless authentication flow](https://learn.chatgpt.com/docs/auth#login-on-headless-devices)
allows the user to run `codex login --device-auth` inside a session, subject to
account support. File-based credential storage can remain inside that
session's private home. No login or credential access was performed during this
documentation check; no upstream credential-copy fallback is authorized here.

Read-only preparation also fetched the hash-pinned `0.151.0` source through
Nix (source only, no Codex execution), at
`/nix/store/zx4az7iz6ningrh8k7fagra96wpansm4-source`. Its
`codex-rs/hooks/schema/generated/` contains versioned command input/output
schemas suitable for fixture design. Inspection found no generic agent/API
failure event in `HookEventName` (`protocol/src/protocol.rs`), and
`core/src/session/turn.rs` can continue a turn after Stop handlers run. The
adapter must not infer unsupported failure or definitive completion semantics
from those events. These source findings are not authenticated trace evidence.
The same release retains a `notify` callback with `type:agent-turn-complete`
(`hooks/src/legacy_notify.rs`). Its call site in `core/src/session/turn.rs`
runs after Stop continuation handling on the normal completion path. This is
a candidate completion signal for the pinned adapter, subject to fixture and
manual validation; it is asynchronous and is not a generic failure signal.
The pinned release enables the stable `hooks` feature by default
(`features/src/lib.rs`) and discovers `hooks.json` beside the user configuration
(`hooks/src/engine/discovery.rs`), while retaining individual hook trust. This
source inspection does not run Codex or bypass that trust. Adapter packaging
must also respect the existing one-MiB session-asset limit and keep agent-specific
mapping in the selected plugin rather than the P core status reducer.

### Manual Codex acceptance — pending user validation

The pinned adapter and authentication-free checks are available. Run the full
procedure after steps 8–9 provide public cleanup and public egress.
Use two disposable sessions, A and B, with separate private homes. The final
delivery will supply their creation and confirmed cleanup commands; those
methods are not all implemented yet.

1. Enter A with `p attach "$P_SOCKET" "$SESSION_A"`. Inside the session, run:

   ```sh
   export CODEX_HOME=/home/p/.codex
   /usr/libexec/p/codex-adapter init
   codex --version  # Expected: codex-cli 0.151.0
   codex login --device-auth
   codex login status
   codex
   ```

   Initialization creates private hook and file-credential-storage configuration
   for an empty session home. It preserves credentials and refuses conflicting
   user configuration; inspect such a conflict yourself rather than overwriting
   it. In Codex, review and trust the installed hooks through `/hooks`. Do not
   import host configuration or credentials. These commands are for your later
   manual test; automated tests never run login or authenticated execution.
2. Ask Codex to make a small disposable workspace change and verify its result.
   Exercise activity, permission/input, and normal completion. Detach while it
   runs, then use `p api "$P_SOCKET" session.inspect
   "{\"v\":1,\"uuid\":\"$SESSION_A\"}"` to check unattended status. Reattach
   and verify that confirmed entry clears the unattended value and attached
   reports do not repopulate it. Reports identify the available Codex session
   scope or concrete thread; a child's completion does not mean its parent has
   stopped. Record only redacted
   event/status observations, never credentials, prompts, or transcript content.
3. Detach, call `session.stop` and then `session.start` with the same UUID
   parameters, and poll `session.inspect` until ready. Reattach and verify
   `codex login status` and a new real Codex task without another login.
4. Enter B and confirm it has no authentication from A. Do not copy A's
   credential file into B. Any login in B is a separate manual login.
5. Review and confirm Discard for a disposable authenticated session, then
   Delete for another. Verify each operation's cleanup evidence and that its
   old private home/runtime cannot be reopened. A replacement session must
   require a new login. Check that the other session remains intact.

Record the pinned version and each outcome here. Authentication-free fixtures
cannot mark any real execution or authenticated persistence check as passed.

## Real repository fixture

The user selected this repository's latest remote `main`, replacing the earlier
homelab-repository validation target for this implementation run. On 2026-09-23,
fetching `https://github.com/lgvo/p.ai.git` branch `main` resolved to
`0bc8f4b972ae82609a96cd2931e9bc742ce68ba4`. The configured SSH origin could not
authenticate in the execution environment; the public HTTPS read succeeded
without changing remote configuration or the working tree. This commit has no
root `flake.nix`, so it exercises base-only environment selection. Separate
committed-flake fixtures must exercise default-devShell realization and failure
cases. The test input is the fetched commit, not the uncommitted implementation.

## Evidence log

- Starting point: the infrastructure lab passes its existing confinement,
  private-root/Nix-store, Git/tmux and stop/start probes. See
  [the lab validation](../dev/vm/VALIDATION.md). It does not yet execute P.
- Step 1: fresh implementation and independent review agents agreed after
  fixes for digest/read consistency, invocation revalidation, bounded reads
  and directory scanning, log-path confinement, hard links, and typed event
  values. The reviewer independently passed `go test ./...` and `go vet ./...`
  using the pinned development shell. Executable WASI invocation, daemon
  wiring, installation, and automatic composition are still pending work.
- Step 1 VM: `./dev/test-vm` exited 0 on 2026-09-23. Raw console:
  `.cache/p-vm/integration-20260923T212016Z-87915.log`. The packaged CLI passed
  conformance/discovery, content-pinned activation, append/rotation, and denials
  for invalid grants/API/digest, symlink/hard-link log paths and nonprivate log
  directories. The baseline Incus suite also passed. An earlier run passed the
  guest assertions but exposed a runner success-marker mismatch; the corrected
  runner was reviewed and rerun successfully. All runs were sequential and
  their VMs shut down before the next run.
- Step 2: fresh implementation/review agents agreed after fixes to connection
  shutdown, strict JSON/ID handling, cancellation, operation transitions,
  exact-number policy normalization, and idempotency results surviving removal
  of live rows. Independent unit tests, vet, and the control race test passed.
  The Nix build's unmapped root ownership required a narrow unit-fixture seam;
  exported production entrypoints retain strict ancestry checks. The pinned
  package then passed all unit tests.
- Step 2 VM: `./dev/test-vm` exited 0 on 2026-09-23. Raw console:
  `.cache/p-vm/integration-20260923T214237Z-114344.log`. Plugin tests remained
  green. The real daemon passed private state/socket checks, system queries,
  malformed/version/ID rejection, second-writer refusal, graceful restart, and
  SIGKILL/stale-socket recovery with unchanged instance identity. This proves
  the control foundation, not Git/Incus session lifecycle.
- Step 3a: fresh implementation/review agents agreed on a WASI event-handler
  ABI, closed broker calls, digest-bound execution, bounded resources, and
  fixed session asset plans. Review fixes covered package-size bounds,
  diagnostic redaction, exact unsupported-operation isolation probes, portable
  memory-limit assertions, and refusal of a module without exported memory.
  Independent unit tests, vet, and static integration-script checks passed.
  The script runs source-built benign and adversarial modules; live session
  asset installation remains step 4 work.
- Step 3a VM: `./dev/test-vm` exited 0 on 2026-09-23. Raw console:
  `.cache/p-vm/integration-20260923T220854Z-158799.log`. The source-built
  independent filter skipped and appended the expected reduced events;
  adversarial modules were denied filesystem/environment/process/network
  authority, unknown and malformed broker calls, invalid memory pointers,
  false results, excessive output/memory, and unbounded execution. Digest/grant
  checks and fixed asset-plan destinations passed. Earlier foundation suites
  remained green. One prior run exposed a CLI diagnostic expectation mismatch;
  the reviewed correction passed this sequential rerun. Both VMs shut down.
- Step 3b design review: a fresh GPT-6 Astra/high agent resolved an extension
  boundary the initial Sol proposal did not meet. A manifest selecting a
  compiled-in Git backend was rejected as insufficient replacement evidence.
  The executable source-Git contract now separates module-owned operation
  sequencing from core-owned authority and native Git transport. Bundled and
  alternate modules compile with the pinned Go toolchain; their behavioral
  replacement and Git authorization still require review and VM validation.
  Bootstrap push authority is being made single-use at the validated
  pre-receive boundary. Step 8 must expose repair for a consumed grant whose
  ref-write outcome is unknown; ordinary retries must not silently renew it.
- Step 3b code review: the fresh Sol reviewer and implementer agreed after
  fixes for authority-lock cancellation, repository path/initialization checks,
  strict broker scopes and limits, stream byte limits, hook callback shutdown,
  and single-use bootstrap grants. The reviewer independently passed
  `go test -race ./...` and `go vet ./...` with the pinned environment. A prior
  isolated Nix package snapshot passed all Go tests. The final Git VM driver
  and test script are still being added; no Git transport integration result
  is claimed yet.
- Step 3b first VM run exited 1 after earlier suites and positive Git pushes
  passed, including the alternate's small push and the bundled provider's
  larger push. Console:
  `.cache/p-vm/integration-20260923T225853Z-226130.log`. The remaining failure
  had no assertion diagnostic; the script now reports failure lines. Root also
  found that ref-page counters measured in-memory broker calls rather than
  native Git queries. A second fresh Astra/high agent is correcting that
  evidence and implementation gap before independent review and a sequential
  rerun. The failed VM shut down; no concurrent validation is running.
- Step 3b pagination correction: fresh Astra implementation and independent
  Sol review agreed on one bounded native heads-only Git observation per
  broker call. Tests cover the hidden sibling namespace, 8+4 pagination,
  actual bundled/alternate query counts, fresh observations, cancellation,
  and explicit head/output ceilings. Focused unit tests, race checks, vet,
  and integration-script static checks passed. The coordinator started one
  serialized VM rerun; its result remains pending.
- Step 3b VM rerun: `./dev/test-vm` exited 0 on 2026-09-23. Console:
  `.cache/p-vm/integration-20260923T234147Z-253932.log`. All earlier suites and
  `P_GIT_SSH_SUBSTRATE_PASS` passed, followed by both final success markers.
  Real SSH exercised bootstrap dry-run/first push, assigned fast-forward
  updates, read-only host access, cross-project/ref denials, persistent guards,
  revocation, hidden refs, native pagination, and alternate-plugin stream
  limits. The VM shut down and its temporary disk was removed. Git is composed
  through a test-only driver here; production daemon/lifecycle wiring remains
  step 4c work.
- Steps 4a/4b are independent implementation slices. The base image and
  tmux assets are separate from the confined Incus broker. Endpoint assembly
  is unresolved: Incus rejects shifted disk mounts when the allowed-path
  ceiling is set, so a mixed-ownership credential mount cannot be assumed.
  The implementation must resolve this without administrative authority or
  relaxing the project restriction. No runtime product VM result is claimed.
- Step 4 endpoint correction: a fresh Astra/high agent confirmed the pinned
  Incus restriction and stopped-root file API. The selected design keeps only
  sockets in the unshifted read-only `/run/p` mount, protected by an unmounted
  private host ancestor, and installs credentials in the private instance root
  at `/etc/p/git`. Runtime checks, the source-Git helper, and subject-owned
  path documentation now reflect that split. Focused tests pass; actual guest
  ownership, connectivity, isolation, and restart behavior still need VM proof.
  Fresh Sol reviewers are checking both runtime slices. Initial findings cover
  persistent journald storage and effective Incus configuration inherited from
  profiles. The production image's pinned NixOS evaluation passed; this is not
  runtime evidence.
- Step 4a image assembly review found NixOS's immutable generated unit tree
  incompatible with direct unit copying. Fixed links now lead to root-owned
  verified assets under `/etc/p/assets`; a generated multi-user dependency
  starts the selected target. The actual Nix unit build verified both links
  and the dependency. The packaged CLI then passed all Go tests in the pinned
  Nix builder. Independent review approved the image, fixed Git stream helper,
  and VM fixture.
- Step 4a VM runs exited 1, sequentially, with all earlier suites passing:
  `.cache/p-vm/integration-20260924T001706Z-348912.log`,
  `.cache/p-vm/integration-20260924T002206Z-377758.log`, and
  `.cache/p-vm/integration-20260924T002514Z-401606.log`.
  The latter two added stopped-container diagnostics. The user journal reveals
  the exact pre-start failure: the endpoint mount retains propagation flags
  rejected by the runtime, despite Incus's configured `propagation=private`.
  The implementer is fixing propagation inside the container while retaining
  the strict check. Every failed VM shut down; no runtime success is claimed.
- Step 4b independent review fixes cover effective inherited Incus devices
  and configuration, trusted path ownership, exact declared disk ceilings,
  and explicit unshifted mounts. Unit/race/vet checks passed. The reviewed
  native integration fixture revealed a Nix-store executable ancestry issue
  before execution; that narrow fix is pending re-review. Step 4c1's first
  review found SSH startup readiness, durable server-key identity, and Git
  preflight cancellation issues to fix before its configured-daemon VM gate.
- Step 4a's next sequential VM run also exited 1:
  `.cache/p-vm/integration-20260924T003245Z-432016.log`. The root preparation
  helper failed its endpoint-directory precondition before changing mount
  propagation. A fresh Astra/high agent separated syscall errors from observed
  type/mode/ownership and added bounded mount diagnostics without relaxing
  checks. Independent Sol review, focused race tests, vet, and shell checks
  passed. The coordinator is running the next serialized diagnostic retry.
- Step 4b's executable ancestry correction and integration fixture passed
  independent review. Step 4c1's durable server-key pin, pre-RPC SSH setup, and
  context-aware Git preflight passed independent review and focused race/vet
  checks. Actual configured-daemon integration evidence remains pending.
- Step 4a diagnostic retry exited 1 with earlier suites green:
  `.cache/p-vm/integration-20260924T004226Z-475505.log`. Host metadata confirms
  the intended `0700` private ancestor, `0755` source, and `0666` sockets.
  In the guest, `/run/p` is recorded as a read-only mount but `lstat /run/p`
  returns `ENOENT`. The Astra agent is investigating mount visibility/order;
  permission checks remain unchanged. This VM shut down before further work.
- Step 4a's missing path is explained by the pinned NixOS activation script:
  it mounts `/run` tmpfs over the directory containing the preattached Incus
  mount. The correction uses a fixed internal staging target outside `/run`
  and trusted startup binding to the unchanged public `/run/p` path. Separate
  Astra runtime-kit and Sol adapter/fixture implementations are in progress;
  both require independent review and VM evidence.
- Step 4c1's new configured-daemon fixture passed fresh independent Sol review,
  Bash syntax checking, and pinned ShellCheck. It covers RPC pagination, real
  host/session Git access, graceful/crash restart with stable server/client
  identities, and startup refusals. No configured-daemon VM pass is claimed.
- Step 4a staging correction passed independent combined review, focused race
  tests, vet, shell checks, and Nix parsing. The sequential VM run
  `.cache/p-vm/integration-20260924T005456Z-520863.log` proved both assembled
  runtimes reached readiness, socket mount identity/read-only isolation, Git
  clone/push, PTY detach, private state, and retained-state stop/start. It then
  exited 1 after clean tmux exit: the test observed Incus `Stopped` but the
  immediate start returned `already running`. A fresh Sol agent is resolving
  this transition race; the full runtime gate remains incomplete. The VM shut
  down, and the Incus-plugin/configured-daemon suites were not reached.
- The reviewed exact-error Start retry advanced the next serial VM run through
  clean exit, killed-host recovery, bad-command recovery, and read-only-mount
  startup refusal. Run `.cache/p-vm/integration-20260924T010145Z-548231.log`
  then exited 1 when the test attempted to restore the device while Incus still
  held its stop operation. The fixture needs completion synchronization before
  subsequent stopped-instance mutations. No force-stop workaround is used;
  the VM shut down normally, and later suites remain unrun.
- Steps 4a, 4b, and 4c1 passed the complete serial VM run:
  `.cache/p-vm/integration-20260924T011138Z-591574.log`, exit **0**. All earlier
  markers passed, followed by `P_RUNTIME_HOST_PASS`, `P_RUNTIME_INCUS_PASS`,
  `P_DAEMON_GIT_COMPOSITION_PASS`, and both final success markers. This proves
  the assembled base runtime, confined executable Incus plugin, and configured
  daemon Git surface. Runtime assembly still uses a test driver; public session
  lifecycle and attachment leases remain pending. The VM shut down and its
  temporary disk was removed before further work.
- Step 4c2a was implemented in an isolated copy and independently reviewed.
  Review fixed symbolic-ref dereferencing in native branch creation and added
  regression cases; implementer and reviewer agree. Focused race tests and vet
  passed. The approved source-Git files are now merged into the working tree;
  their new committed-source effects still require VM validation.
- Step 4c2a's integration fixture passed independent review after adding
  unchanged-ref assertions to every denied observation. The full serial VM run
  `.cache/p-vm/integration-20260924T012335Z-627864.log` exited **0**, including
  `P_GIT_COMMITTED_SOURCE_PASS` and all earlier/final markers. Actual bundled
  WASI and native Git proved captured-source selection, atomic branch creation,
  existing/symbolic-ref protection, namespace/object/project denials, and
  alternate-plugin refusal without native fallback. The VM shut down and its
  temporary disk was removed. Trusted assembly is being implemented separately;
  public lifecycle remains pending.
- Step 4c2b's isolated implementation is under fresh independent Sol/high
  review. It adds trusted stopped-instance assembly, initial workspace setup,
  and systemd observation. Review is addressing resource bounds, source
  identity binding, and preservation of established work on Start. The older
  manual runtime fixture also needs the new workspace configuration. No
  assembly VM pass is claimed. Step 4c2c's isolated lifecycle implementation
  is in progress; neither slice has been merged into the validated tree.
- Step 4c2b passed independent implementation review after fixing bounded Git
  execution, retained workspace preservation, project/source identity binding,
  and native file metadata checks. The pinned Incus 7.4 source showed that a
  content GET can return 404 for a dangling symlink; assembly now checks HEAD
  metadata first and refuses that case before writing. Unit, targeted race,
  vet, and shell syntax checks passed. The reviewed assembly files and adapted
  step-05 fixture are merged. A separate fresh agent is preparing the step-09
  VM fixture; no assembly VM result is claimed yet.
- The merged step-4c2b production package passed `nix-build
  dev/integration.nix -A pPackage --no-out-link`, including `go test ./...`
  inside the Nix builder. Build log: `/tmp/p-assembly-package-build.log`.
  This is package/unit evidence; the new assembly VM gate remains pending.
- Runtime-image evaluation found an obsolete journald option introduced by
  assembly work. The setting now uses the pinned NixOS
  `services.journald.settings.Journal.Storage` option. The corrected image
  built successfully; log: `/tmp/p-assembly-image-build.log`. This catches
  image configuration independently of the Go checks and is not a VM pass.
- Step 4c2c is under fresh independent Sol/high review in its isolated copy.
  Review is addressing startup/shutdown concurrency, interrupted creation,
  immutable selections, endpoint shutdown, and policy/RPC size bounds. The
  public lifecycle files remain unmerged until implementation and review
  agree; their VM acceptance gate remains pending.
- Step 4c2b's fixture implementer and independent reviewer agreed after
  replacing a fixed startup delay with a bounded test-only gate, verifying
  refusal postconditions, and correcting required Git pagination inputs.
  Bash syntax, ShellCheck, and Nix parsing passed. The coordinator merged
  the fixture and is starting its serialized VM gate with all earlier suites.
- Step 4c2b's first VM gate exited **1** after steps 01–08 passed. Console:
  `.cache/p-vm/integration-20260924T020127Z-710627.log`. Step 09's first native
  assembly refused its `/etc` directory precondition before installing files.
  The check currently hides the underlying file-API error or metadata, so
  investigation must resolve that observation before any safety-check change.
  The VM shut down and its disk was removed; no integration run remains active.
- Review also exposed a step-4c2c exact-retry gap after interrupted initial
  workspace creation. A fresh Astra/high agent is implementing a bounded
  recovery extension that must preserve unexpected and established work.
  This dependency needs independent review and its own VM evidence before
  public creation/recovery can pass.
- The assembly failure was traced to the pinned squashfs image having no
  `/etc` before activation; the older manual fixture had created it during
  upload. Fresh Sol implementation and independent review agreed on creating
  that absent directory only beneath the verified image root, with postchecks
  and unchanged refusal of unsafe existing paths. Targeted independent tests,
  implementer package race tests, and vet passed. The two-file fix is merged;
  the coordinator is starting one serialized VM rerun.
- The rerun `.cache/p-vm/integration-20260924T021336Z-777812.log` exited **1**
  after steps 01–08 and native assembly/idempotence/refusal checks passed. The
  fixture then called `cmp`, absent from its declared Nix runtime inputs.
  `diffutils` is now explicit in the test package. No runtime behavior change
  was required for this failure; the VM shut down before another run.
- Run `.cache/p-vm/integration-20260924T021655Z-806556.log` exited **1** after
  the first native-assembled workspace booted, pushed its initial commit, and
  preserved its state through stop/start. The second container was inspected
  before NixOS created `/usr/libexec/p/systemctl`; the fixture treated this
  transient read error as fatal. Its bounded readiness poll needs to retry
  observation errors without claiming readiness. The VM has shut down.
- The fixture's bounded observation retry passed independent review and is
  merged. One serial assembly VM rerun is in progress. Separately, the Astra
  transactional workspace fix passed Sol review after removing the supervisor
  lock descriptor from the unprivileged population helper. A regression test
  proves that helper cannot release the root lock; the implementer agrees.
  Those seven production/documentation files are merged after the running VM
  captured its immutable build snapshot. Their VM proof therefore remains
  pending, including a dedicated interrupted-initialization fixture.
- Assembly run `.cache/p-vm/integration-20260924T022736Z-838778.log`
  exited **1** after steps 01–08 passed and both assembled runtimes booted and
  pushed their branches. The final dangling-symlink refusal occurred correctly,
  but its test expected older diagnostic wording. The assertion is being
  aligned with the reviewed `differs from trusted assembly` diagnostic; the
  separate link-preservation postconditions remain required. The VM shut down
  and its disk was removed.
- Trusted assembly passed the complete serial VM run
  `.cache/p-vm/integration-20260924T023119Z-865627.log`, exit **0**, including
  `P_RUNTIME_ASSEMBLY_PASS` and all earlier/final markers. This build also uses
  the reviewed transactional initializer, proving its private mount namespace
  and uid-1000 Git execution work under the actual confined container. Blank
  and committed workspaces, branch pushes, retained state, assembly reuse, and
  unsafe-file refusals passed. Dedicated crash interruption and public lifecycle
  validation remain pending. The VM shut down and its disk was removed.
- Public lifecycle implementation and its fixture are merged after independent
  review and implementer agreement. A coordinator check found that native
  exact-ref inspection treated Git's missing-ref exit 128 as an unexpected
  failure. The implementer corrected absence detection and symbolic-ref
  rejection; a fresh reviewer passed real-Git absence/present/symbolic/error
  cases and the branch-replay test. The full Git-service unit suite also
  passed. The next serial VM gate will run the separately reviewed workspace
  interruption test before public lifecycle acceptance; neither new gate is
  claimed passed yet.
- After merging the reviewed lifecycle and transactional workspace code,
  `go test ./...` passed with local socket access. Log:
  `/tmp/p-lifecycle-merged-unit.log`. The step-5a status/RPC implementation is
  beginning in an isolated copy while the remaining step-4 fixtures are
  reviewed; its VM gate will follow step 4.
- The next `./dev/test-vm` attempt stopped during package unit tests, before
  starting a VM. Two new daemon tests call the strict production store opener;
  the Nix builder's mapped ownership of `/` triggers their ancestry check.
  Host unit tests passed, but this build-context gap must be fixed without
  relaxing production path checks or skipping tests. A fresh Sol agent is
  correcting the fixture setup. Build log: `/tmp/p-vm-public-lifecycle.log`.
- The Nix portability fix passed independent review and package checks. Private
  decision helpers preserve production key and Start checks; unit fixtures no
  longer need the production store opener in the mapped build namespace.
  Store principal lookup coverage remains in the control package. No ownership
  check or test was disabled. Final package:
  `/nix/store/vdsci1nllyxz15lsrhrrisrrwwq9lkf0-p-0.1.0-dev`; log:
  `/tmp/p-lifecycle-nix-build.log`. The three reviewed files are in the working
  tree, and serial VM validation resumes.
- Run `.cache/p-vm/integration-20260924T024404Z-971025.log` exited **1**.
  All earlier suites, trusted assembly, and the new
  `P_WORKSPACE_RETRY_PASS` gate passed. The latter proves actual supervisor
  SIGKILL leaves unpublished scratch, preserves/refuses unexpected destination
  work, and retries the same inputs successfully without duplicates. Public
  lifecycle then timed out before its first container existed. Inspection
  suggests inherited `umask 077` turns a newly requested `0755` endpoint
  directory into `0700`, which the daemon rejects. A fresh agent is adding a
  regression test, correcting only newly created directory setup, and improving
  the fixture's operation diagnostics. The VM shut down and its disk was removed.
- The endpoint `umask` fix passed fresh independent review and implementer
  agreement. A subprocess regression now runs the actual `Ensure` path with
  `umask 077` and verifies both sockets. Mode correction is confined to a newly
  created directory through a no-follow descriptor; existing wrong-mode or
  symlink paths are preserved and refused. Full daemon unit/race checks and
  ShellCheck passed. The three reviewed files are merged and one serial VM
  rerun is active.
- Step 5a's isolated status/RPC implementation passed independent review,
  control/daemon race tests, and vet. Review corrected malformed-frame
  throttling, UTF-8/string bounds, idle connection limits, first-attachment
  clearing, and byte-aware list pagination. API documentation and the reviewed
  endpoint dependency are being integrated before merge; its guest RPC fixture
  is in preparation. Actual attachment transport and handler wiring remain 5b
  and 5c.
- Public lifecycle passed the complete serial VM run
  `.cache/p-vm/integration-20260924T025834Z-1028822.log`, exit **0**.
  `P_DAEMON_LIFECYCLE_PASS`, `P_WORKSPACE_RETRY_PASS`, all earlier gates, and
  both final markers passed. Actual CLI requests proved blank creation and
  first push, captured-source branch/session creation, exact blocked-operation
  retry, stable identity/keys/endpoints through graceful and killed-daemon
  restart, retained Stop/Start state, missing-key refusal, and failed-host Start
  recovery. The VM shut down and its disk was removed. Root-flake environment
  creation, attachment leases, origins, and later MVP steps remain pending.
- Step 5b's initial Sol implementer identified an unresolved trusted-helper
  channel-confirmation boundary and stopped before changing the contract.
  Following the user's escalation rule, a fresh Astra/high agent is resolving
  and implementing helper-owned PTY/lease behavior in an isolated copy.
- Step 5a's reviewed code, API reference, and VM fixture are merged. The static
  probe executes inside each real guest and connects to its mounted private
  socket. Review strengthened response/notification assertions, pagination,
  malformed-report postconditions, and a paced semantic-rate probe with a fresh
  rate window. Bash syntax, ShellCheck, Nix parsing, and the static probe build
  passed; one serial VM gate is active. Step 5c's trusted handler wiring is
  beginning separately while Astra implements attachment.
- Status run `.cache/p-vm/integration-20260924T030702Z-1064509.log`
  exited **1** after all previous gates, including public lifecycle, passed.
  The new guest test passed identity/capability, isolation, and invalid-report
  checks before the oversized-frame probe encountered a Unix connection reset.
  Its client discards received bytes when `io.ReadAll` also returns an error;
  the fixture is being corrected to accept only an actual validated parse-error
  response, never an arbitrary transport failure. Remaining rate/restart/status
  checks were not reached. The VM shut down and its disk was removed.
- The status probe correction passed a fresh independent review, actual Unix
  socket race tests, vet, and shell checks; the implementer agrees. Its narrow
  negative-test mode requires a complete bounded parse-error response followed
  by EOF or the expected reset, and rejects trailing bytes, an open peer, and
  arbitrary transport errors. The three fixture files are merged, and a serial
  VM rerun is active. Status production framing is unchanged.
- Step 5c passed initial unit/race/vet checks and is under fresh Sol review.
  Astra's step-5b implementation has native control-channel confirmation and
  helper-owned carrier/lease teardown tests; a fresh Sol reviewer is checking
  that implementation independently. Neither attachment nor daemon handler
  wiring has a VM pass yet.
- Step 5a passed serial run
  `.cache/p-vm/integration-20260924T031949Z-1152255.log`, exit **0**.
  `P_SESSION_STATUS_PASS`, every earlier product gate, and both final markers
  passed. Real guest sockets proved identity/capability isolation, strict report
  validation and rate bounds, durable unattended projections, pagination, and
  graceful/killed-daemon restart and Stop/Start preservation. The VM shut down
  and its disk was removed. Attachment presence remains a separate step-5b gate.
- Step 5c's independent review found report-event enqueue ordering could differ
  from committed receive order, the declarative handler did not honor the
  advertised deadline, optional event setup added startup runtime inspection,
  and callback-error diagnostics bypassed throttling. Findings returned to the
  implementer; this step has not passed review or VM validation.
- Step 5b's Astra implementation and independent Sol reviewer agree after
  fixes for channel loss during confirmation and expiring Incus operation
  records. The helper opens a native completion observer before the exec
  channel, confirms only after native establishment, and retains a reachable
  lease through teardown. Full unit tests, vet, and focused race tests passed.
  The 27 reviewed production/dependency/API files are merged; a fresh agent is
  implementing the public-CLI PTY/loss VM fixture. VM attachment evidence is
  still pending.
- Step 5c's ordering, startup, diagnostics, and timeout fixes passed unit/race
  tests and are back with its independent reviewer. Step 6a's isolated origin
  substrate implementation has started while step-5 fixtures are prepared;
  actual integration runs remain exclusively coordinated and sequential.
- The merged attachment production package passed the pinned Nix build and
  its full unit suite: `/nix/store/awxqb95bbb491l0lbp9xyd2bmiapifib-p-0.1.0-dev`.
  Build evidence is `/tmp/p-step05b-nix-build.log`. This validates packaging
  and unit behavior; the real terminal/channel VM gate remains pending.
- Step 5c's independent reviewer and implementer agree on the revised code and
  its composition with 5b. Both attachment callback sites use post-commit
  reducer callbacks; no database or plugin work occurs under that reducer
  lock. Optional event setup avoids runtime inspection during recovery. The
  daemon retires delivery on deadline, with at most one unresolved native
  call, and documents the uncertain final file effect. Reviewed production
  changes are merged. Separate fresh agents are preparing attachment and
  daemon-event VM fixtures; neither gate has run yet.
- Combined attachment/event race tests and vet passed. The first Nix build
  exposed two event unit tests with unnecessary host-ancestry assumptions.
  A fresh agent removed unrelated SQLite setup from the cached-context test
  and separated activation selection from the still-mandatory production
  ownership check. Independent review agreed. The corrected full Nix package
  passed: `/nix/store/7n7my5mbf6afq6p5pagdhbajakiqi28k-p-0.1.0-dev`, log
  `/tmp/p-step05-composed-nix-fixed.log`.
- The step-5b fixture passed fresh review and author agreement. Review fixed
  its API page limit, distinguished command output from terminal echo, added
  tmux process start time to the host-survival checks, and bounded helper and
  client shutdown assertions. The four fixture/build files are merged and
  the first serial attachment VM run is active. Step 5c's separate daemon-event
  fixture is under fresh review; step 6a's origin substrate is also under its
  independent code review.
- Step 5c's daemon-event fixture passed independent review after the author
  added immutable policy-hash assertions, exact NDJSON physical-line checks,
  and bounded waits for absent events. The fixture is merged for the next
  serial run, after the active attachment VM has stopped. It has no VM result
  yet. Step 6a review requested failed-fetch observation invalidation, bounded
  subprocess pipe teardown, native Git runner tests, and alternate-plugin
  origin ABI coverage; those findings are back with its implementer.
- First attachment run
  `.cache/p-vm/integration-20260924T034521Z-1265835.log` exited **1**.
  Every earlier gate through `P_SESSION_STATUS_PASS` passed. Step 12 stopped
  before attachment establishment because its tmux `display-message` probe
  returned a server PID with no pane PID. The fixture author is correcting the
  host-identity query without relaxing the identity assertion. The VM shut
  down and its temporary disk was removed. This run establishes no attachment
  result; step 13 was not part of this build.
- The tmux probe fix passed author/reviewer agreement and focused checks.
  It now queries panes explicitly and refuses missing or multiple pane PIDs.
  A serial VM retry is active and includes the reviewed step-13 event fixture.
- Step 6a passed re-review after all four findings were resolved. Native tests
  now exercise real Git with a fixed SSH stand-in, including hostile config,
  object-cache integration, moved-ref refusal, and observation invalidation;
  the alternate WASI package proves the new ABI separately. Subprocess pipe
  waiting is bounded. The reviewed production files are merged after the
  active step-5 VM build captured its source. A fresh agent is preparing the
  actual OpenSSH VM fixture; public origin lifecycle/publication remain 6b/6c.
- The merged origin substrate passed the pinned full Nix package check:
  `/nix/store/yv3wvr8bxzym841kvss6h55lp47mcsai-p-0.1.0-dev`, log
  `/tmp/p-step06a-nix-build.log`. Its actual SSH VM gate is still pending.
  Step 6b1 association/refresh and step 7a activation-adapter implementation
  are proceeding in isolated copies, with separate review and VM gates.
- Attachment retry
  `.cache/p-vm/integration-20260924T035530Z-1304836.log` exited **1**.
  All earlier gates passed, and the corrected host-identity probe succeeded.
  The first public `session.attach` request then returned `unavailable` for a
  ready session, before any attachment was established. A fresh Sol agent is
  tracing the native authority/asset checks and adding a regression. The VM
  shut down and its disk was removed; step 13 was not reached.
- Step 7a review found missing Nix-version cache identity, case-insensitive
  JSON field acceptance, missing sourced-Bash behavior tests, and altered
  export state for `XDG_DATA_DIRS`. Material-to-capture integrity also remains
  an assembly boundary. These must be addressed before adapter compatibility
  validation or production environment activation.
- The step-6a OpenSSH fixture passed author/reviewer agreement. It uses valid
  Git advertisements to test the output ceiling and verifies rejected
  authentication/trust attempts reach no upload-pack command. It is merged
  for the next serial VM run; SSH-agent authentication and public origin
  lifecycle/publication are outside this fixture's evidence.
- Step 6b1's independent review found case-variant RPC parameters accepted by
  the shared decoder and invalid UTF-8 ref bytes changed by JSON storage.
  Both findings returned to the implementer. Its association/replay/refresh
  code remains isolated and has no VM result.
- Pinned-source inspection explains the attachment refusal: Incus HTTP file
  metadata strips the sticky bit from NixOS's `/nix/store` mode. The proposed
  fix reads only that directory's full metadata through the same confined
  instance's native SFTP endpoint. Independent review is strengthening
  protocol parsing, header bounds, and cancellation before merge and VM retry.
- Step 7a passed independent re-review after the remaining material null/type
  cases were fixed. The four staged adapter/documentation files are merged.
  The adapter has sourced known-fixture Bash tests, but no repository Nix code
  has been evaluated on the host. A separate disposable-guest compatibility
  comparison is still required; production flake activation remains disabled.

- The attachment metadata correction passed independent reviewer and author
  agreement. It preserves the sticky-bit requirement using fixed-path native
  SFTP metadata, with bounded headers/packets, cancellation, and strict parsing.
  The five reviewed files are merged; the next serial VM run includes
  attachment, daemon events, and the origin SSH substrate fixture.

- Step 6b1 passed re-review and author agreement. Exact-case typed RPC fields
  and valid UTF-8 origin refs are now enforced with regressions. The 14 reviewed
  files are merged after the active VM build captured its source; that run
  cannot establish public origin association evidence. A separate CLI fixture
  will cover association, failed replacement, refresh, pagination, and replay.

- The merged step-6b1 package passed the pinned Nix build and full Go unit
  suite: `/nix/store/7jf5fbh3lykncjzgw26qvwjdxhkjlqw2-p-0.1.0-dev`; log
  `/tmp/p-step06b1-nix-build.log`. Public origin VM evidence remains pending.

- Serial VM `.cache/p-vm/integration-20260924T042024Z-1380179.log` exited
  **1** after all prior gates passed. Fixed native asset verification issued
  the first pending attachment token. Step 12 then misclassified the expected
  `session.stop` busy RPC error because the CLI correctly exits 1 for errors.
  This fixture decoding defect must be fixed before attachment validation can
  continue; events/origin were not reached. The VM stopped and disk was removed.

- Step 6b1's public-origin VM fixture passed independent review and author
  agreement, including exact structured CLI errors and unchanged observations
  through failures. The single step-16 script is merged for the next serial
  run; its runtime evidence is pending behind the step-12 fixture correction.

- Step 7a's confined-guest Nix compatibility fixture passed independent review
  and final author agreement. Its eight files are merged. It compares actual
  `nix develop` child-process exports with activation, checks shell-local state
  separately, and requires pure evaluation and sandboxing. Actual pinned Nix
  compatibility remains unproven until the serial VM reaches step 15.

- Step 12's CLI error handling correction passed independent review and author
  agreement. Subprocess tests now accept exit 1 only with the expected valid
  JSON-RPC error envelope; Stop checks both `busy` and code `-32003`. The two
  fixture files are merged. The next serial VM also includes reviewed steps
  13–16; no gate is marked passed before its actual guest result.

- Step 6b2 review found a durable-source blocker: origin fetch records a
  captured commit before asynchronous branch creation, while receive-pack can
  trigger automatic maintenance. A blocked creation could lose its unreferenced
  commit and fail exact Retry. The patch remains isolated and returned to its
  implementer; the full affected unit/race suite otherwise passed.
- Step 6c is split into **6c1**, the closed native/source-plugin publication
  substrate, and **6c2**, public preview/publication and retained-branch queries.
  The substrate is being implemented independently; neither boundary is yet
  supported or VM-validated.

- The next serial run stopped during packaging, before any VM boot.
  `/tmp/p-vm-attachment-cli-errors.log` reports that the new static Nix
  activation fixture binary retained a prohibited Go-toolchain reference.
  Packaging must be corrected without relaxing dependency checks; this run
  establishes no new integration result.

- The Nix fixture packaging fix passed author/reviewer agreement and a scoped
  build. Its static override now preserves `buildGoModule`'s environment and
  `-trimpath`; prohibited references remain rejected. Verified fixture output:
  `/nix/store/fa8ph0aq88pki4qfm4kv3agqqsihjabx-p-0.1.0-dev`. The one-file build
  correction is merged for a serial VM retry.

- Step 6b2's retention correction passed independent re-review and author
  agreement. P disables implicit native maintenance on Git commands that can
  prune, including receive-pack; real Git tests preserve the dangling captured
  commit through ref movement, and store tests cover blocked/restarted replay.
  The 13 reviewed files are merged after the active VM build captured source.
  Origin-backed project/session creation still needs its separate VM fixture.

- Serial VM `.cache/p-vm/integration-20260924T043935Z-1472434.log` exited
  **1** after all prior gates passed. Step 12 progressed through token/Stop
  checks and a confirmed attachment that cleared unattended status, then
  timed out waiting for helper/native teardown after closing its private
  carrier. A fresh agent is tracing this production/fixture boundary. The VM
  stopped and disk was removed; steps 13–16 were not reached.
- The merged origin-creation/retention code passed the full pinned Nix package
  check: `/nix/store/ykfc2zz8dh0af9brqsmksa2d31k3mcq5-p-0.1.0-dev`, log
  `/tmp/p-step06b2-nix-build.log`. Its public creation VM fixture is in preparation.

- Step 6c1's publication substrate passed independent review and author
  agreement. Review distinguished pre-start local failures from uncertain
  remote outcomes and corrected raced up-to-date classification. Full affected
  unit/race tests and vet passed under scoped execution. Eight files are merged,
  preserving the step-6b2 maintenance protections. Real SSH publication and
  public preview/publication RPC remain separate unpassed gates.

- The helper timeout was proven to be a fixture descriptor leak: its socket
  pair lacked close-on-exec, so the child inherited the client endpoint. A
  subprocess regression fails before and passes after `SOCK_CLOEXEC`. The
  production client already sets it. Review also corrected the resize test
  for tmux's status row while checking exact client and pane dimensions. The
  two fixture files passed unit/race checks and author/reviewer agreement and
  are merged.
- Step 6c1's full Nix package build exposed a regression-test assumption: the
  simulated pre-start failure instead started a process under Nix, returning
  `outcome_unknown`. The reviewer is correcting the test/diagnosing the
  environment before another VM run; the failure is not waived.

- The step-6b2 public creation fixture passed independent review and author
  agreement and is merged as step 17. It checks exact P-only remotes, distinct
  session/host-origin keys, fresh source capture, empty bootstrap, and exact
  Retry after origin replacement and loss of SSH access. Its blocked runtime
  retry occurs after branch assignment; pre-CAS object-retention evidence stays
  in the dedicated native Git/unit regressions, not this VM claim.

- Step 6c1's Nix failure was traced to test-only `rm` lookup in the intentionally
  restricted Git PATH. The regression now uses the resolved executable and
  verifies the intended pre-start failure. Reviewer and author agree; its
  composed isolated full package build passed at
  `/nix/store/gf6z4kafyxyhf4jp0kmxz9xwfaba49ai-p-0.1.0-dev`. The corrected test
  is merged, preserving original maintenance protections, for the VM retry.

- Step 7b is split into **7b1**, bounded immutable committed-tree capture for
  the future builder, and **7b2**, restricted Incus resolution/realization.
  Source capture can be tested independently of the pending Nix compatibility
  gate. No production flake activation or image publication is enabled by it.

- Serial VM `.cache/p-vm/integration-20260924T045542Z-1596174.log` exited
  **1**. Earlier gates passed; step 12 now passed carrier teardown, real
  terminal I/O/resize, multiple attachments, and client loss, then failed to
  find the helper process for its SIGKILL test. The fixture reads only the
  main thread's `/proc/.../children`, which misses children forked from another
  Go OS thread. After repeated fixture issues, a fresh GPT-6 Astra/high agent
  is repairing process discovery and auditing the remaining loss tests. The
  VM stopped/discarded its disk; later gates were not reached.

- Step 6c1's real-OpenSSH publication fixture passed independent review and
  author agreement. The matching hostile URL-rewrite probe was corrected;
  accepted-but-lost response checks verify exactly one receive and no automatic
  repeat. Its three fixture files are merged as step 18, retaining upload-only
  behavior for existing fixture-server mode. Actual VM evidence remains pending.


- The targeted VM harness passed independent review and its mocked selector,
  shared-lock, and exact success-marker tests. It supports repeated `--step`
  arguments while retaining the same serial lock as a full run. Selected runs
  emit a separate marker and never establish full-suite acceptance.
- The fresh Astra attachment-fixture audit passed independent Sol review and
  pinned Go race verification. It finds helpers across all Go OS threads,
  verifies process identity before pidfd termination, and keeps the PTY master
  nonblocking across resize so Close can interrupt readers. Three fixture files
  are merged; production attachment behavior was not changed by this repair.
- Step 7b1 immutable committed-tree capture passed independent review and author
  agreement. Review corrected composed relative-symlink escapes and a test's
  hardcoded Go path. Three files are merged; selected-WASI units passed with
  the pinned compiler on PATH. Its separate VM fixture is in preparation.
- The first selected attachment launch did not start because automatic
  permission review timed out. The permitted single retry started successfully;
  actual VM evidence remains pending in `/tmp/p-vm-attachment-astra.log`.

- Selected serial VM `.cache/p-vm/integration-20260924T052036Z-1683471.log`
  exited **0**. Step 12 emitted `P_ATTACHMENT_PASS`, with selected-suite and
  infrastructure markers accepted. Real terminal bytes/resize, multiple
  attachments, client/helper SIGKILL, PTY closure, expiry, graceful/abrupt daemon
  restart, and Stop/Start passed. The VM stopped and its disk was removed.
  This closes step 5b's gate; it does not claim a full-suite pass.
- The next serial VM selects steps 13–18 to validate daemon events, origin
  substrate/association/creation/publication, and pinned Nix activation.

- Serial selected VM `.cache/p-vm/integration-20260924T052345Z-1723255.log`
  exited **1** at step 13: `attachment ended before confirmation`. The event
  fixture's explicit exit did not print its captured attachment diagnostics;
  no event gate is claimed and steps 14–18 were not reached. VM shutdown/disk
  cleanup completed. A separate serial run now selects 14–18 while this event
  fixture boundary is investigated.

- Serial VM `.cache/p-vm/integration-20260924T052550Z-1727605.log` emitted
  `P_ORIGIN_SUBSTRATE_PASS` for step 14, establishing its real OpenSSH/WASI
  observation/fetch and denial gate. The overall run exited **1** immediately
  after entering step 15, without its expected failure diagnostic. Steps 16–18
  were not reached. Nix compatibility remains unproven; the fixture needs
  diagnostic/early-launch investigation. Shutdown/disk cleanup completed, and
  the next serial VM selects the remaining origin steps 16–18.

- Serial VM `.cache/p-vm/integration-20260924T052724Z-1730393.log` emitted
  `P_ORIGIN_LIFECYCLE_PASS` for step 16. Step 17 then failed at line 335:
  captured-origin session creation blocked in `source-ready` with
  `commit is not reachable from an ordinary P head`. The overall run exited
  **1**, and step 18 was not reached. This requires fixing the production
  captured-origin branch path; the unit-only creation claim is insufficient.
- Step 19's source snapshot fixture passed independent review and author
  agreement, including a new hidden-only existing commit denial. Its three
  files are merged. The next serial VM selects publication substrate (18) and
  source snapshot (19); event, Nix, and origin-creation failures stay open.

- Serial selected VM `.cache/p-vm/integration-20260924T052927Z-1735652.log`
  exited **0**. Steps 18 and 19 emitted `P_ORIGIN_PUBLICATION_PASS` and
  `P_COMMITTED_SNAPSHOT_PASS`; selected-suite and infrastructure markers passed.
  This validates real OpenSSH publication relations/races/uncertain outcomes
  through selected WASI, plus exact committed snapshot materialization and
  authority/cleanup denials. VM shutdown/disk cleanup completed. Public
  publication RPC and Nix realization remain separate pending gates.
- Step 7b2a native restricted builder substrate is implemented in an isolated
  tree, with focused/full runtime units and vet passing; independent review
  and real Incus transfer/cleanup validation are still pending.

- The step-13 failure was reproduced with an unsized `script` PTY reporting
  `0 0`. The client now substitutes valid dimensions, including resize; the
  fixture sets an explicit terminal size. Independent review replaced raw
  terminal/guest-log diagnostics with fixed classifications and byte counts.
  Author and reviewer agree on the final three-file patch, which is merged.
  Attachment package checks and controlled failure diagnostics passed; the
  next serial VM retries step 13.

- Serial selected VM `.cache/p-vm/integration-20260924T053727Z-1791240.log`
  exited **0**, with `P_DAEMON_EVENTS_PASS`, selected-suite and infrastructure
  markers. The real configured daemon/event handler, session RPC, attachment
  transitions, policy comparison, restart/no replay, and handler-failure
  isolation passed. VM shutdown/disk cleanup completed.
- Outstanding agent results were collected after the workflow revision.
  The origin-creation reviewer requires a real SQLite-backed selected-WASI
  regression; the public publication reviewer found missing origin-identity
  binding between preview and action. The Nix diagnostic patch is preserved
  at `/tmp/p-step15-fix.duKy8t`; its underlying guest failure remains unknown.
  Origin-creation implementation, publication review, and Nix diagnostic agents
  reported model usage limits. No retry, new-agent workaround, or model switch
  will be used. Coordinator work can continue, with unfinished independent
  reviews explicitly pending.

- The captured-origin production patch had no remaining code finding from
  its fresh reviewer; the blocking test gap is now addressed locally. The
  regression opens/reopens real SQLite state and the production Git backend,
  invokes `CreateCapturedOriginBranch` through selected WASI, rejects mismatched
  operation/project/branch/OID/evidence/phase/status/session authority, and
  preserves the captured commit after origin movement/loss. It passed unskipped
  with normal host ownership; the sandbox's foreign-owned `/tmp` explicitly
  skips this production-entrypoint test. Focused daemon dispatch/CAS tests also
  passed. Six production/test files are merged; serial step 17 is running.
- Nix diagnostic VM `.cache/p-vm/integration-20260924T094448Z-1838655.log`
  exited **1** at `capture-default`. Pure evaluation rejects the fixture's
  absolute Bash store path. Incus launch and guest setup succeeded. The saved
  console diagnostic patch is merged; the next correction must declare the
  input without weakening pure evaluation or the build sandbox.

- Origin-creation VM `.cache/p-vm/integration-20260924T094906Z-1877851.log`
  passed captured-source creation/retry but exited **1** at fixture line 404:
  empty-origin contact had not yet assigned its bootstrap UUID. The fixture now
  waits for that asynchronous assignment, with a bounded failure path.
- Serial VM `.cache/p-vm/integration-20260924T095245Z-1919258.log` exited **0**
  with `P_ORIGIN_CREATION_PASS` and selected/infrastructure markers. This closes
  step 6b2's origin-backed project/session creation and exact Retry gate. The
  VM stopped and its disk was removed.
- Publication identity corrections are preserved in `/tmp/p-step6c2-aFDbdu`:
  required expected origin URL, durable key binding, pre-contact conflict on
  replacement, and shared Git authority exclusion during publication. Focused
  publication tests and race checks pass, including restart, pre-start retry,
  completed replay, and changed-origin denials. The interrupted independent
  review remains pending; the public API patch is not yet merged.
- Local review of existing builder patch `/tmp/p-step7b2a-fJlCoB` corrected
  logical project identity validation, source-parent traversal, and immutable
  source ownership. Guest source is root-owned and readable/executable by the
  build user without permitting that user to chmod it writable. Focused builder
  tests pass. The patch remains isolated, with independent review and real
  Incus verification pending; it has not been extended into Nix realization.
- Nix fixture correction uses an explicit dependency string context for the
  pinned guest Bash store identity, as described in the [Nix manual](https://nix.dev/manual/nix/2.34/language/string-context.html).
  Pure evaluation, offline operation, and sandboxing remain enabled. Syntax
  and ShellCheck pass; the next serial step-15 VM checks actual behavior.

- Serial VM `.cache/p-vm/integration-20260924T095632Z-1957329.log` exited **1**
  at `capture-default`. The explicit Bash dependency corrected pure evaluation;
  actual derivation realization then failed with Nix's required-kernel-namespaces
  error and the kernel message `VFS: Mount too revealing`. No activation
  compatibility or production Nix build pass is claimed. VM shutdown/disk
  cleanup completed; no integration run remains active.
- Inspection of the pinned Nix 2.34.8 source identifies
  `mountAndPidNamespacesSupported()` in
  `src/libutil/linux/linux-namespaces.cc`: it creates fresh mount and PID
  namespaces, optionally a user namespace, and tries to mount procfs. Its
  comment explicitly describes rejection when the existing `/proc` is covered
  by other mounts. The realized source is
  `/nix/store/2ijv0g6069dsh55z3bdr5ln2iv69mw7r-source`.
  Incus 7.4's `internal/server/instance/drivers/driver_lxc.go` installs extra
  procfs/sysfs mounts to address this kernel restriction only when nesting is
  enabled. Nesting is forbidden by P's current container baseline. This
  identifies a confinement compatibility blocker, not another activation
  parser or fixture quoting issue. Sandboxing, the no-nesting baseline, and all
  assertions remain unchanged. Another unchanged VM run would add no evidence.
- Corrected but unmerged publication and builder batches are additionally
  preserved under `.cache/p-vm/pending-review-20260924/` as `publication.patch`
  and `builder.patch`, with base/proposed SHA-256 values in `manifest.json`.
  Both pass `git apply --check` against the current working tree. The isolated
  source trees remain available at the paths recorded above. These artifacts
  preserve review work; they do not constitute review approval or VM evidence.
  Independent security reviews remain blocked by the reported model usage
  limit, with no retry or model substitution. The builder has not been extended
  beyond its existing substrate. Full MVP delivery and full-suite acceptance
  remain incomplete.

- The user reports weekly usage available. A single resumption of the existing
  Sol publication reviewer succeeded; no model substitution was needed. Review
  approves the corrected origin binding, authority lock order, exact source
  authority, cross-journal key constraints, and durable uncertainty handling.
  The 13-file publication patch is applied. New step 20 exercises production
  daemon RPC with a real session, selected WASI, and real SSH publication;
  syntax and ShellCheck pass. Retained refs are explicitly seeded fixture
  preconditions, not evidence of destructive-lifecycle retention. Actual step
  20 VM evidence remains pending. A fresh Sol/high reviewer is checking the
  existing builder batch before any realization extension.
- The existing publication reviewer also approved step 20's public API fixture.
  Focused merged control/daemon tests passed. Serial selected VM
  `.cache/p-vm/integration-20260924T113143Z-1997479.log` exited **0**, emitting
  `P_PUBLIC_PUBLICATION_PASS` and selected-suite/infrastructure markers. Real
  session and retained-source publication, exact P tip versus unpushed workspace
  work, divergence refusal, restart replay, accepted-but-lost result handling,
  changed-origin pre-contact denial, and retained paging passed. VM shutdown
  and disk cleanup completed. This closes the public publication API gate;
  retained-branch creation by destructive lifecycle operations is still step 8.
- Fresh builder review found three implementation blockers in the existing
  isolated batch: Incus file POST does not chmod existing directories, its
  symlink GET resolves rather than preserves relative/dangling targets, and
  root size metadata does not prove quota enforcement on the current `dir`
  pool. The memory file fake missed the first two API behaviors. One Sol/high
  implementation stream is correcting these findings, with the same reviewer
  retained for follow-up. No builder realization extension or VM pass is claimed.
- Nix confinement follow-up: the pinned Incus AppArmor template's nesting
  branch grants general mounts, pivot-root, and broader tracing/signals while
  omitting several non-nesting `/proc/sys` and `/sys` write denials. Therefore
  simply turning on `security.nesting` is not an equivalent-isolation fixture
  repair. The [upstream maintainer explanation](https://discuss.linuxcontainers.org/t/what-is-the-purpose-of-dev-lxc-proc/25700)
  confirms why its extra hidden procfs/sysfs mounts satisfy the kernel check.
  The current baseline remains unchanged; a compatible restricted configuration
  still needs design and isolation evidence before adoption. This blocker does
  not prevent continuing the other CLI MVP boundaries.
- The user explicitly selected Incus as P's required isolation boundary and
  disabled the additional Nix build sandbox inside managed containers. This
  supersedes the earlier sandbox-enabled requirement; unprivileged instances,
  isolated mappings, disabled nesting, and filesystem/network/resource
  restrictions remain required. The runtime authority document, environment
  documentation, production/lab container configurations, and Nix fixture now
  agree. Step 15 checks the effective isolation settings and `sandbox=false`
  before actual evaluation/build/capture. Syntax, ShellCheck, and Nix parse
  checks pass; compatibility still awaits the selected VM result.
- The corrected builder batch is selectively merged after the same reviewer
  approved SFTP sealing/readlink, trusted btrfs pool selection, and safe source
  ancestor handling. The real fixture includes both existing-target and
  dangling relative symlinks. Focused merged builder/SFTP tests passed outside
  the agent socket sandbox. Step 21 uses a separate 16 GiB btrfs pool and must
  observe EDQUOT at its 8 GiB root limit; the existing `dir` session pool stays
  intact. No builder VM result is claimed yet.
- Host inspection found about 54 GiB available of 60 GiB and no active VM.
  With the user's authorization, the single test VM now has 8 GiB RAM for the
  4 GiB builder plus host services. The checkout-wide integration lock still
  spans build, execution, and shutdown. Next runs are selected steps 15 and 21,
  serially; no overlapping VMs or full-suite claim.
- Serial selected VM `.cache/p-vm/integration-20260924T115811Z-2054756.log`
  exited **0** with `P_NIX_ACTIVATION_PASS` and selected/infrastructure markers.
  The approved Incus-only boundary passes the pinned Nix 2.34.8 compatibility
  gate: real offline builds, default/structured JSON capture, activation
  equivalence with `nix develop`, hooks/quoting/arrays, and invalid schema,
  unsupported version, failed derivation, and missing-lock rejection. Effective
  container restrictions and `sandbox=false` were checked in the guest. The
  original namespace blocker is resolved. VM shutdown/disk cleanup completed.
  Production environment realization/cache lifecycle still needs implementation.
- Serial selected builder VM
  `.cache/p-vm/integration-20260924T120003Z-2101616.log` exited **1** at the guest
  `fallocate` prerequisite, after builder creation/source transfer/start.
  It did not establish quota enforcement or the builder gate. The same
  implementation context is investigating guest readiness versus executable
  availability; EDQUOT remains required. VM shutdown/disk cleanup completed.
- The unused environment-key helper incorrectly required an absolute host path
  for project scope. It now accepts the bounded logical P identity (`team/app`)
  used by control/source/builder code, rejects unsafe forms, and retains
  distinct digests across projects. Focused tests pass and the same environment
  batch reviewer approved it; this is not new environment lifecycle support.
- The builder fixture now waits for the guest Nix daemon socket and allocation
  executable after Incus reports Running. Serial selected VM
  `.cache/p-vm/integration-20260924T120654Z-2147733.log` exited **0** with
  `P_BUILDER_SUBSTRATE_PASS` and selected/infrastructure markers. Real source
  transfer preserved executable modes and relative/dangling symlinks; the build
  user could not modify sealed source, the 8 GiB btrfs quota rejected a 9 GiB
  allocation with EDQUOT, and changed identity blocked deletion. VM shutdown
  and disk cleanup completed. This closes the native builder substrate gate,
  not environment realization or public session integration. The same
  implementation stream is proceeding with closed native Nix
  resolution/realization/capture and focused tests before another reviewed VM
  gate; image publication/cache/lifecycle remain separate work.
- The next native Nix batch adds fixed guest commands for pure, offline
  resolution, realization, and structured activation capture. Focused tests
  exposed a buffer-method output-limit bypass, now fixed, and review required
  process-group cancellation plus a bounded pipe wait before cleanup. The
  backend tests now cover fixed arguments/environment, identity refusal, and
  verified builder stop after interrupted execution. Their scoped run outside
  the agent socket sandbox passed after correcting the test endpoint and
  sample capture JSON. Fresh Sol/high review and VM step 22 are still pending;
  no public environment or image-cache integration is claimed.
- Fresh review approved the closed native code and VM22 fixture after the
  cancellation test proved an actual detached child and the missing-lock case
  used a committed dependency. The package build passed the full Go suite.
  Serial selected VM `.cache/p-vm/integration-20260924T123533Z-2200493.log`
  exited **1** at the valid shell's derivation/system proof, after the absence
  and expected-rejection cases. The implementation stream is comparing the
  strict parser against pinned Nix's actual derivation JSON; no native Nix VM
  pass is claimed. VM shutdown and disk cleanup completed.
- Pinned Nix source and a read-only existing-store derivation inspection
  confirmed that version 4 uses the store basename as its derivation-map key.
  The narrow parser correction retains exact identity, version, system, and
  output checks; focused regression tests and the same reviewer approved it.
  Serial selected VM `.cache/p-vm/integration-20260924T123952Z-2246475.log`
  exited **0**, emitting `P_BUILDER_NATIVE_NIX_PASS` and selected/infrastructure
  markers. Real offline default-shell build/capture, no-flake/no-default
  selection, invalid-default rejection, committed dependency without a lock,
  no lock writes, and changed-builder identity refusal passed. VM shutdown and
  disk cleanup completed. This closes the offline native gate only; executable
  environment-plugin composition, public fetching, image/cache lifecycle, and
  public session integration remain pending.
- Full checkpoint VM through step 22,
  `.cache/p-vm/integration-20260924T124138Z-2291496.log`, exited **0** with
  `P_PRODUCT_INTEGRATION_PASS` and `P_VM_SMOKE_PASS`. All included product
  fixtures passed together, finishing at about 452 seconds within the existing
  aggregate timeout. The run used one 8 GiB VM; shutdown and disk cleanup
  completed before any further VM run. This immutable test snapshot excludes
  the executable environment-plugin batch being implemented next. That batch
  will receive its own review and selected VM evidence before image/cache and
  public lifecycle work.
- Preparation for image publication checked pinned Incus 7.4 source:
  `publish --format` selects an image format, not JSON, and `query` rejects the
  `--project` flag used by the existing command wrapper. The later publication
  adapter must use an explicitly project-bound API request or the pinned CLI's
  verified fingerprint result; it must not assume a JSON publish command exists.
  No image publication implementation or validation is claimed yet.
- Executable environment-plugin composition is implemented for separate
  scoped `environment.resolve` and `environment.realize` stages. Native
  selection/material stay pending until the content-pinned WASI command
  returns valid `ready` after exactly one authorized broker effect. A fresh
  Sol/high review found a long-held pipeline mutex; brief state locks now keep
  queries and cancelled conflicting calls responsive. Actual WASM adversarial
  tests cover wrong scope/kind/fields, repeated or skipped calls, invalid
  memory, malformed/refused output after effects, and absent ambient authority.
  Affected package tests and the pipeline race test passed; the reviewer
  approved the batch. Selected VM23 is pending. Public environment/session and
  image-cache integration remain separate work.
- Serial selected VM `.cache/p-vm/integration-20260924T125738Z-2310669.log`
  exited **0** with `P_BUILDER_ENV_WASI_PASS` and selected/infrastructure
  markers. The content-pinned selected environment module performed base-only
  resolution and a real offline devShell resolve/realize/capture through the
  restricted builder; changed identity was refused. The package build passed
  the full Go suite. VM shutdown and disk cleanup completed. This closes
  executable environment composition, while private image publication,
  project cache, public fetching, and session lifecycle integration remain
  pending. The next native image gate must root the actual captured environment,
  install and verify root-owned activation files, smoke-test inside the guest,
  scrub builder-only data, stop, publish privately, and verify the image before
  exposing a handle. Cache and public lifecycle integration follow separately.
- A source-compatibility follow-up remains before general lifecycle enablement:
  the current hash-locked path reference does not supply the captured Git
  revision to a flake's `self.rev`. Pinned Nix `libfetchers/path.cc` explicitly
  supports `rev` metadata on exported path inputs. Binding that field to the
  core-captured commit and validating a revision-dependent fixture can preserve
  this common committed-source behavior without transferring `.git` or allowing
  impure evaluation. This is not covered by the current offline fixture gate.
- Image scrub preparation found that pinned `github.com/pkg/sftp` v1.13.11
  `RemoveAll`, used by Incus forced file deletion, begins with a following
  `Stat`. Recursive removal therefore requires a stopped builder and checked
  nonsymlink directory ancestors/root; it cannot be used blindly on a writable
  path. The implementation stream is adding those checks and a retained-target
  symlink probe to the image gate. No new VM has started. Available host memory
  remains about 54 GiB, so the single-VM 8 GiB allocation is retained.
- Native image assembly/publication code now compiles and existing focused
  packages pass; adversarial unit coverage and VM24 preparation are in progress.
  Creating its fresh Sol/high reviewer was rejected by the agent tool with
  `agent thread limit reached`. The coordinator asked whether the completed
  environment reviewer may be reused with retained context. No retry, model
  switch, or review bypass was attempted; the review and selected VM gates are
  still pending.
- The user explicitly authorized reuse of the existing Sol/high environment
  reviewer with retained context. That reviewer is now checking the image
  batch. Coordinator inspection also found GC was ordered before quiescing
  hook descendants; the implementation stream is correcting the sequence
  before review agreement and VM24. The remaining VM run stays serial.
- Image review added stopped pre-GC removal of writable scratch paths and
  per-user Nix roots/profiles, followed by final stopped scrub after maintenance.
  Interrupted start/stop now attempts a fresh-context verified stop. A lost
  publication response yields no accepted handle and explicitly reports an
  unresolved outcome; a read-only inventory can identify an exact labeled
  orphan but cannot prove absence while an Incus operation may remain active.
  Public cache/retry integration must reconcile such outcomes before another
  publication. Focused image/pipeline tests pass; VM24 is still pending final
  fixture review. Its checks distinguish published-image cleanup from later
  activation effects and will exercise private Nix filesystem/database state.
- The reused reviewer approved the native image batch and VM24 fixture. The
  package build passed the full Go suite, including the final defensive image
  metadata-map copy. Serial selected VM
  `.cache/p-vm/integration-20260924T132730Z-2369435.log` exited **1** after a
  builder restart: the Nix daemon socket refused a connection. The image stage
  reported a verified stopped builder and returned no handle. VM shutdown and
  disk cleanup completed. The same implementation/review contexts are fixing
  actual daemon readiness after restart; no image VM pass is claimed.
- The reviewed readiness correction polls pinned `nix store info --json`,
  which performs a daemon connection, after both image-stage restarts. Its
  stale-socket and wrong-version regressions pass. Serial VM24 retry
  `.cache/p-vm/integration-20260924T133331Z-2418334.log` exited **1** at the
  `/tmp` scrub metadata check; the VM shut down and disk cleanup completed.
  Pinned Incus source confirms HTTP file metadata omits special permission
  bits. The existing SFTP full-mode mechanism used for `/nix/store` will be
  reused for fixed scrub directories, preserving the required sticky bit
  rather than weakening the assertion. No image VM pass is claimed.
- The reviewed full-mode SFTP fix passed memory and actual wire regressions.
  Serial VM24 `.cache/p-vm/integration-20260924T134056Z-2467065.log` exited **1**
  after successful collection/scrub and Incus publication, at image metadata
  verification. The fingerprint remained unaccepted; VM shutdown and disk
  cleanup completed. Pinned Incus `lxc.Export` merges requested labels with
  the original base metadata rather than replacing it. The same stream is
  correcting verification to compare the complete expected metadata from the
  exact pinned base plus P's closed label set. Arbitrary extra properties or
  mismatched ownership remain rejection cases; the image VM gate is not passed.
- Exact inherited-metadata verification and its tampering regressions passed
  review. VM24 `.cache/p-vm/integration-20260924T134721Z-2516717.log` exited
  **1** after accepting the published image, when creating the first independent
  root: the host Incus extractor denied NixOS gzip's hidden wrapped executable.
  VM shutdown and disk cleanup completed. The next correction explicitly uses
  Incus's supported uncompressed archive, binds that choice to image-format
  identity, and preserves AppArmor and container restrictions. This has a
  storage cost and does not establish the image gate until the private-instance
  checks pass. No concurrent VM or authenticated Codex validation ran.
- The closed uncompressed publisher, compression label, and format-key binding
  passed review and focused tests. VM24
  `.cache/p-vm/integration-20260924T135316Z-2566199.log` exited **1** at the final
  private-root restart probe: its fixture checked socket presence before
  issuing Nix RPC, and received connection refused. Publication, metadata,
  pre-activation cleanup, activation, and A/B private-store checks had passed.
  The coordinator had identified this fixture race during the run; the same
  stream prepared and reviewed a bounded UID 1000 live-daemon readiness check,
  which was not in that immutable snapshot. VM shutdown/disk cleanup completed;
  the next selected run includes that correction. Store persistence is not
  claimed until the post-restart RPC succeeds.
- Serial VM24 `.cache/p-vm/integration-20260924T135624Z-2614118.log` exited
  **0** with `P_BUILDER_PRIVATE_IMAGE_PASS` and selected/infrastructure markers.
  It verified publication metadata, private uncompressed image identity,
  rooted activation closure, removal of the hook-created extra store object
  from filesystem and database, nested-symlink target survival, activation in
  two independent roots, private Nix filesystem/database writes, and persistence
  after one root's Stop/Start. The package build passed all Go checks. The
  single 8 GiB VM shut down and its disk was removed. This closes 7c1 native
  image publication; direct fixture-created instances do not establish public
  session creation or cache support. The next coherent batch is project cache
  and public environment/session composition, including unknown publication
  reconciliation and captured Git revision semantics.
- Full serial checkpoint through VM step 24
  `.cache/p-vm/integration-20260924T135915Z-2659003.log` exited **0** with
  `P_PRODUCT_INTEGRATION_PASS` and `P_VM_SMOKE_PASS` after about 503 guest
  seconds. It includes the executable environment plugin and native private
  image publication in addition to the earlier lifecycle, attachment, events,
  origin, and Nix gates. The single 8 GiB VM shut down and its fresh disk was
  removed; the runner was drained. This immutable snapshot predates the active
  7c2 cache/public-session changes and does not validate them. No authenticated
  Codex execution was attempted or claimed.
- The 7c2 cache/recovery foundation now has a schema-7 project/key index,
  bounded metadata, exact-generation stale-index removal, fresh Incus image
  verification, and conservative prior-publication reconciliation. Hash-locked
  path-flake references now carry the captured commit as `self.rev`; a commit
  change alone remains outside cache-key identity. Focused control/runtime
  tests passed. The authorized retained Sol/high reviewer is checking this
  coherent subgate while the same implementation stream wires public session
  creation and activation. Current changes have no VM evidence yet.
- The retained reviewer approved that foundation after inspecting the pinned
  Incus operation response and Nix path-fetcher `rev` support. A real schema
  6→7 reopen regression was added and focused tests passed. Public composition
  must still prove durable publication-attempt intent before the effect, exact
  project/key-to-claim binding, and refusal to republish an unresolved attempt.
  Reviewer agreement on the foundation does not close those call-site gates.
- Coordinator inspection found the publication marker preceded image
  preparation, which could strand a known preparation failure as an unknown
  publication. The implementer moved the durable callback to immediately
  before the actual Incus publish command, after preparation and verification;
  regression coverage is in progress. Startup design inspection also rejected
  a second activation-readiness file. The retained reviewer confirmed the
  documented activation/hook → foreground host order: source as UID 1000,
  exec tmux, and use bounded systemd/host readiness. Non-exported shell state
  is available to activation and its hook; only exported state crosses exec.
  Public startup and recovery VM validation remain pending.
- The full checkpoint through VM24 used about 503 seconds of the previous
  600-second aggregate test-service allowance. Before adding public environment
  creation/recovery gates, that aggregate allowance was raised to 1100 seconds,
  below the runner's 1200-second deadline. Individual operation/test deadlines,
  assertions, isolation, and serial execution are unchanged. No extra VM was
  launched solely for this harness budget change.
- The retained Sol/high reviewer approved the 7c2 public core and independently
  passed six focused Go packages. Closed findings include missing-cache-image
  recovery after ambiguous instance creation, Create-only partial-endpoint
  repair through the selected WASI module, visible/retryable exact builder
  cleanup after publication, and aligned bounded host readiness. Trusted
  workspace v2 now binds accepted environment selection to the captured OID,
  distinguishes absent flake from valid absent default, and retains v1's
  rejection of unresolved root flakes. Stopped assembly verifies root-owned
  material bytes before writing session state. This is code/unit approval;
  VM25 and public session acceptance remain pending.
- Builder smoke now uses a disposable writable copy of the captured tree, so
  hooks can read committed files and write local scratch without changing the
  sealed evaluation source or published image. Focused coverage and retained
  reviewer inspection passed, including refusal of a workspace containing only
  a whitespace-named file. Post-smoke source/derivation/key checks and stopped
  scrub remain required. Review also found that a requested Nix system could
  differ from the base/host architecture before absent-default fallback; a
  bounded observation of the actual Incus host and exact base architecture now
  precedes selection. The retained reviewer approved that fix against pinned
  Incus source and independently passed focused runtime tests. Seven affected
  Go packages passed in the implementation stream, and the VM25 fixture passed
  syntax/static review. The selected public-environment VM run is in progress;
  no integration pass is claimed yet.
- Selected VM25 exited **1**:
  `.cache/p-vm/integration-20260924T150654Z-2713955.log`. Public A/B/C creation,
  cache miss/hit, activation and captured revision, private Nix state,
  Stop/Start, and external image loss/rebuild passed their preceding assertions.
  Later invalid/absent-default requests both failed at builder creation with a
  generic Incus error; this does not prove invalid-default rejection. The
  fixture must require that specific rejection, and the underlying Incus
  failure needs diagnosis before rerun. The single VM powered down at about
  169 guest seconds and its runner was drained. VM25 remains unpassed.
- Inspection identified a fixture capacity error: the VM project permits four
  containers, and bootstrap plus A/B/C occupied all four before the next
  builder. The correction will release the completed disposable C instance
  after its assertions, preserving the limit and all isolation checks. The
  invalid-default assertion will also require the intended Nix diagnostic.
- Corrected selected VM25 passed with runner exit **0**:
  `.cache/p-vm/integration-20260924T151306Z-2762763.log`, with
  `P_PUBLIC_ENVIRONMENT_PASS`, the selected-suite marker, and `P_VM_SMOKE_PASS`.
  Public CLI/daemon creation exercised captured Git and the selected WASI Nix
  plugin, cache miss/hit, tracked-file reads and writable hook scratch,
  `self.rev`, exported pane environment and one hook execution per Start,
  private Nix store/database state, Stop/Start persistence, and external image
  loss/rebuild without damaging existing roots. Both invalid-default requests
  now require the specific Nix rejection; Retry retained the captured commit
  after main advanced. A valid absent default selected the base and initialized
  its tracked root flake. This is real offline public-lifecycle integration
  using committed fixture repositories, not general networked Nix or Codex
  evidence. The 8 GiB VM powered down at about 188 seconds, its disk was removed,
  and the runner was drained. No VM is left running.
- The same implementation stream is now implementing 7c3 explicit cache
  preview/collection and durable exact-image cleanup. A coherent retained
  reviewer pass, focused tests, selected VM gate, and then full-suite delivery
  checkpoint remain required. No automatic cache collection is introduced.
- The 7c3 schema-8/native foundation has focused passing tests for migration,
  accepted-use timestamps, exact-generation cleanup, project-scoped exclusion,
  other-project independence, and stale/expired pre-insert refusal. Review
  closed the competing-collection guard and moved expiry checking inside the
  acceptance transaction. Destructive image verification uses the persisted
  accepted properties and current P ownership labels, so removal of the old
  base image does not strand cleanup; foreign, public, or changed images remain
  refusals. The native test also covers a lost delete result after observed
  image absence. This is unit and incremental review evidence only. Daemon/RPC
  adversarial coverage, final review, and VM26 remain pending.
- Collection review confirmed a concurrent-publication gap: creation workers
  were serialized only by operation ID, while the cache index upsert could
  overwrite another fingerprint for the same project/key. The displaced
  P-owned image would then be absent from collection inventory. The agreed
  correction prevents duplicate publication with an in-memory key critical
  section, checks unresolved durable publication attempts after restart, and
  refuses silent generation replacement in SQLite. Verified external image
  loss still uses exact stale-index removal before rebuilding. Focused
  concurrency/recovery tests and reviewer approval are required before VM26.
- The next fixture must exercise two overlapping same-key builders, each still
  limited to 4 GiB. Under the user's existing memory authorization, the single
  VM allowance was raised from 8 to 12 GiB to leave room for both builders,
  session containers, and guest services. The host reported about 54 GiB
  available before this change. The serial harness lock, four-container limit,
  and all per-container isolation/resource restrictions remain unchanged. No
  VM was launched solely to check this configuration change.
- The retained Sol/high reviewer approved the completed 7c3 code and VM26
  fixture. Both implementer and reviewer passed all tests in
  `./internal/control`, `./internal/daemon`, and `./internal/runtimeincus`
  using the pinned offline Go environment. The new per-key lock, unresolved
  publication check, and cache CAS close the displaced-image race. VM26 reuses
  the public lifecycle fixture with separate paths, a test-only immutable
  Incus wrapper for a post-delete daemon crash and an overlap barrier, and
  exact P-instance/project/key image counting. Stale preview assertions require
  the expected conflict error and no inserted operation. Bash syntax,
  ShellCheck, Nix parse, and whitespace checks pass. The selected VM26 run is
  now in progress; no VM pass is claimed yet.
- First selected VM26 exited **1**:
  `.cache/p-vm/integration-20260924T154628Z-2817702.log`. The startup-order
  fixture did not observe its transient hook-in-progress marker before its
  deadline, while the diagnostic showed bootstrap and A creation both
  completed/established. Collection assertions were not reached. Replace this
  timing-sensitive observation with a bounded, explicit fixture hook gate,
  retaining the assertion that tmux is absent while activation is held. The
  single 12 GiB VM powered down at about 336 seconds; runner and disk cleanup
  completed. No VM remains running and VM26 is still unpassed.
- The startup probe now uses a 45-second release-file gate only for the first
  `env-a` hook in a real Git workspace. Builder smoke, other sessions, and A's
  next Start do not wait. The fixture bounds each marker read, retains a short
  error diagnostic, checks tmux is absent while activation is held, and then
  releases it. Coordinator inspection, Bash syntax, ShellCheck, and whitespace
  checks passed. This fixture-only correction is undergoing a selected serial
  VM26 rerun; the production isolation and readiness assertions are unchanged.
- Corrected selected VM26 passed with runner exit **0**:
  `.cache/p-vm/integration-20260924T155650Z-2866720.log`, with
  `P_ENVIRONMENT_CACHE_COLLECTION_PASS`, the selected-suite marker, and
  `P_VM_SMOKE_PASS`. Through public CLI/daemon operations it rejected a stale
  preview without inserting an operation, deleted the exact reviewed image,
  recovered after a forced daemon crash between actual image deletion and
  index cleanup, and kept independent existing roots usable. Two same-key
  builders were observed overlapping before publication; their sessions used
  one exact P-owned image with one miss and one hit. External image removal
  then permitted confirmed index-only collection. The bounded startup gate also
  proved tmux remained absent until activation was released. This is real
  offline Incus/CLI integration using fixture repositories, not Codex evidence.
  The single 12 GiB VM powered down at about 193 seconds; the runner was drained
  and its disk removed. A full-suite checkpoint through step 26 is now running
  serially. The authentication-free Codex adapter is the next implementation
  batch; destructive session cleanup and public egress remain separate work.
- The full checkpoint through step 26 exited **1** at step 22:
  `.cache/p-vm/integration-20260924T160105Z-2912087.log`. Steps 1–21 passed,
  including public origin creation/publication. The older native Nix fixture
  expected base fallback for `aarch64-linux` on the x86_64 VM; the reviewed
  host-architecture check correctly refused it before fallback. The fixture now
  requires that specific refusal for both native and selected-WASI paths.
  Actual-host absent-default coverage remains intact. This changes no
  production checks or isolation. The single 12 GiB VM powered down at about
  455 seconds and its runner was drained. A focused rerun remains pending;
  the full checkpoint is not passed.
- The 10a implementation now pins an optional trusted agent asset in creation
  evidence and a `p.runtime-session/v3` startup digest, preserving v1/v2
  compatibility. The pinned Python adapter uses a fixed timed session socket
  and emits no prompt/tool/answer/transcript content. Review found that Codex
  child threads inherit completion callbacks and that review subagents can
  share unmarked hook scope. Reports therefore use shared `codex/session/…`
  sources for unmarked hooks and concrete `codex/thread/…` sources when supplied
  by the pinned protocol; they never infer aggregate main-agent idleness.
  Explicit private configuration initialization preserves existing auth and
  refuses custom config rather than overwriting it. The implementer reports
  five focused Go packages passing; implementer and retained reviewer passed
  the initial four-test Python fixture suite. Tests use a fake version binary,
  local socket, and dummy credential file. Final regression/review and VM27
  gates are still pending; no authenticated Codex validation occurred.
- The retained Sol/high reviewer approved the completed 10a batch. Focused Go
  tests passed for `control`, `daemon`, `runtimeincus`, `plugin`, and
  `runtimekit`; the Python suite now has five passing authentication-free
  tests, independently repeated by the reviewer. Nix parsing, Bash syntax,
  ShellCheck, and whitespace checks passed. VM27 uses the selected asset in
  two private sessions, verifies its installed digest, emits event fixtures
  through the fixed socket into public status, and checks dummy credential
  isolation and Stop/Start persistence. Duplicate/unsupported input must leave
  the receive sequence unchanged. A single 12 GiB VM run selecting steps
  22/23/24/27 is now building/running serially; its result is pending.
- That first selection stopped before any VM booted: Nix evaluation found
  infinite recursion because the new version assertion referenced `pkgs` at
  module top level. The coordinator moved the same exact-version requirement
  into NixOS `assertions`, preserving the pin. The failed runner was drained;
  `/tmp/p-vm-codex-nix-evaluation-failed.log` contains the evaluation error. Evaluation and the
  selected VM gate must be rerun after this packaging correction.
- The corrected build passed the complete packaged Go suite and all five
  Python fixture tests. Selected VM steps 22 and 23 then passed with the
  architecture-refusal correction. Step 24 failed before publication because
  its old GC fixture seeded a temporary file in `/workspace`, which the newer
  reviewed smoke path requires to be empty before copying captured source.
  Log: `.cache/p-vm/integration-20260924T163155Z-2934545.log`; runner exit **1**,
  poweroff at about 70 seconds, runner drained. VM27 was not reached. The
  coordinator moved only this disposable input into `/home/p`; the test still
  requires the unrooted store object to disappear and all publication/scrub
  assertions remain intact. Production workspace-emptiness checks are unchanged.
  The next serial selection needs only steps 24 and 27.
- Selected step 24 passed after the fixture correction. VM27 then verified
  selected creation, installed asset/config digests, and the actual guest
  Codex/Python versions, but stopped at its isolation probe because pinned
  Incus `config show` does not accept `--format`. This is a fixture CLI error,
  not a passed Codex gate. Log:
  `.cache/p-vm/integration-20260924T163450Z-2983011.log`, runner exit **1**;
  shutdown and runner cleanup completed. The coordinator changed the read-only
  probe to the supported `list --format json`, requiring exactly the intended
  instance and checking its expanded config/devices. Bash syntax, ShellCheck,
  and whitespace checks pass. Only VM27 needs the next selected rerun.
- Before rerunning VM27, pinned Codex source inspection found that even
  `--version` creates its `CODEX_HOME` for command aliases before parsing the
  version flag. The adapter's initial version probe could therefore create
  `.codex` with an incompatible mode, and the fixture's later `mkdir` would
  collide with it. Implementation and review are correcting the probe to avoid
  session user state; the manual procedure initializes private config before
  a user runs Codex. This finding comes from source inspection, not an
  authenticated test. No VM is running during this correction.
- The retained reviewer approved the fresh-home correction. The version probe
  verifies root-owned nonwritable `/`, `/var`, and mode-0555 `/var/empty`, then
  uses `HOME=CODEX_HOME=/var/empty`, `cwd=/`, and a fixed PATH. No session
  configuration is consulted by that probe. All six Python tests pass,
  independently repeated by review, including closed environment and unsafe
  path cases. VM27 now requires fresh A/B homes, private initialization, and no
  alias scratch from the probe before running direct guest version checks.
  Syntax/static checks passed; the single-step VM27 rerun is in progress.
- Corrected selected VM27 passed with runner exit **0**:
  `.cache/p-vm/integration-20260924T164538Z-3029714.log`, with
  `P_CODEX_ADAPTER_PASS`, the selected-suite marker, and `P_VM_SMOKE_PASS`.
  Public creation installed the reviewed asset with its pinned runtime digest
  into A and B. Actual guest Codex/Python version checks, fresh private config
  initialization without version-probe home mutation, fixture event delivery
  into public status, child/session source attribution, malformed/unsupported
  input refusal, and content redaction passed. Dummy credential/config files
  survived Stop/Start; B did not inherit A's dummy credential and A remained
  unchanged. This is authentication-free adapter/runtime fixture evidence,
  **not real authenticated Codex execution or native hook-trace evidence**.
  Public Discard/Delete dummy cleanup awaits step 8; authenticated acceptance
  remains **pending user validation; not passed**. The single 12 GiB VM powered
  down at about 46 seconds; its runner was drained and temporary disk removed.
  A full checkpoint through step 27 is next, before new lifecycle source edits.
- The full checkpoint through step 27 is running from the reviewed immutable
  snapshot: `.cache/p-vm/integration-20260924T164729Z-3074952.log`. Only after
  that snapshot booted was the existing implementation stream released for
  **8a1**, the read-only workspace inspection foundation. Pinned Incus SFTP
  supports stopped/frozen storage without session activation. The proposed
  helper uses the pinned base, no NIC/endpoints/P credentials, fixed inert
  startup state, and sanitized-copy Git analysis. Durable quiescence/helper
  recovery, bounded no-follow export, and explicit refusal of unsupported
  layouts remain review/VM gates. This does not implement Discard/Delete or
  claim that dummy credential deletion has passed.
  Initial explicit refusal of linked worktrees or other complex layouts is a
  foundation boundary, not removal of step 8's requirement to account for every
  known runtime-owned worktree and identify external ones. That lifecycle gate
  remains pending until the required loss analysis is implemented and tested.
- The full checkpoint through step 27 passed with runner exit **0**:
  `.cache/p-vm/integration-20260924T164729Z-3074952.log`. Every selected script
  through 27, including 09b, emitted its pass marker; the run emitted
  `P_PRODUCT_INTEGRATION_PASS` and `P_VM_SMOKE_PASS`. Cache collection/recovery
  and Codex adapter fixtures passed together with the earlier lifecycle,
  attachment, origin, and environment tests. This validates the reviewed
  immutable snapshot before the ongoing 8a1 edits, not the current unfinished
  workspace-inspection implementation. Codex evidence remains authentication-
  free fixture evidence; authenticated acceptance is **pending user validation;
  not passed**. The single 12 GiB VM powered down at about 826 seconds, the
  runner was drained, and its fresh disk was removed. No VM remains running.
- Incremental 8a1 review found that the pinned Incus file server joins a
  running/frozen container's mount namespace. A root-only expanded device list
  therefore does not exclude guest-created mounts beneath `/workspace`.
  The implementation must verify the source mount view after quiescence and
  refuse nested mounts or unavailable/malformed evidence before copying data.
  This is a source-review finding; the correction, adversarial coverage, and
  selected VM28 remain pending. No workspace-inspection pass is claimed.
- Further 8a1 review requires two corrections before VM validation: preserve
  supported Git configuration semantics in the sanitized copy (or refuse them)
  so status/upstream results are accurate; and persist freeze intent before
  Incus pause, retaining the session guard when a timed-out transition may
  still complete. One `Running` observation must not release an uncertain
  pause guard. Real Git parity and delayed-pause regressions are pending.
- Coordinator inspection of pinned Incus `instance_state.go` confirmed that
  state requests can wait before `OperationCreate` without checking request
  cancellation. Repeated empty operation inventories plus a still-running
  source cannot prove an earlier pause request will never take effect. Review
  and implementation agreed to distinguish definite pre-command refusal from
  possibly sent mutations: only the former can release intent on absence;
  the latter retains its guard and permits bounded retry inspection for a
  positive exact helper/transition result. This is the required recovery rule,
  not a passed implementation or VM result.
- The retained Sol/high reviewer approved the coherent 8a1 read-only batch and
  independently passed the complete `control`, `daemon`, and `runtimeincus`
  test packages. Focused implementation tests, all-package compilation, Bash
  syntax, ShellCheck, and whitespace checks passed. Corrections cover Git
  status/upstream parity, delayed native effects, active/pending attachment
  exclusion, recovered exec operations, post-freeze mount checks, and verified
  helper limits (one CPU, 768 MiB, 256 processes). No disk quota is claimed for
  the VM's `dir` pool. The initial scan is bounded and is not complete
  destructive loss analysis. VM28 observes the actual inert helper and tests
  running/stopped sources, hostile inputs, exact cleanup, and daemon crash
  after pause. Its bounded fixture recovery gate is 45 seconds; production
  bounds are unchanged. The selected serial VM28 run is starting; no pass is
  claimed yet. A canceled request with no positive outcome evidence remains
  guarded and blocked, rather than being retried as a new native mutation.
- First selected VM28 failed with runner exit **1**:
  `.cache/p-vm/integration-20260924T174029Z-3092137.log`. The initial running
  inspection became blocked at `init-issued` after about 48 seconds, with
  `context deadline exceeded`. The source scan and recovery assertions were
  not reached; this is not a passed workspace gate. The exact helper stage
  needs diagnosis before a correction or rerun. The complete packaged Go suite
  and six authentication-free Codex Python tests passed during the build.
  The single 12 GiB VM powered down at about 82 seconds and its runner was
  drained. No VM remains running.
- VM28 diagnosis located the timeout in the helper's 45-second boot predicate
  loop, whose deadline branch discarded the last predicate error. Built NixOS
  artifacts also show a concrete layout mismatch: `system-units/p-session.target`
  links through the immutable `p-host-unit-links` store output before reaching
  `/etc/p/assets/p-session.target`; the verifier incorrectly required one
  direct hop. The correction must validate the bounded owned store chain to
  the exact inert asset and preserve the last readiness error. It does not
  relax activation, ownership, or quiescence assertions. Focused regression,
  retained review, and selected VM28 rerun are pending.
- The retained reviewer approved the narrow VM28 correction and independently
  passed focused runtime tests. The verifier checks the pinned two-hop unit
  layout, real `/nix` and sticky `/nix/store` ancestors, mode-0555 store-item
  directories, and the exact inert terminal asset. Negative regressions reject
  wrong/direct targets, loops, writable directories, and symlinked ancestors.
  The 45-second readiness deadline now retains its last predicate error.
  The selected serial VM28 rerun is starting; its result is pending.
- Second selected VM28 failed with runner exit **1**:
  `.cache/p-vm/integration-20260924T174949Z-3141799.log`. The helper passed boot
  verification with the corrected unit-link chain. The first source scan then
  refused `frozen proc ancestor unavailable`; the operation finished `failed`
  at `cleaned`, after exact helper cleanup and restoration of the running
  source. The diagnostic still needs the exact proc path/metadata to explain
  the mismatch; no owner assertion may be loosened without evidence. The
  single VM powered down at about 39 seconds, and the runner was drained.
  VM28 remains unpassed; no VM is running.
- The diagnostic-only third VM28 run exited **1**:
  `.cache/p-vm/integration-20260924T175622Z-3190626.log`. It identified `/proc`
  itself as a directory with UID/GID `65534:65534`, mode `0555`; cleanup and
  source restoration again completed. The single VM powered down at about
  39 seconds and its runner was drained. Linux 6.18's
  [proc root definition](https://raw.githubusercontent.com/torvalds/linux/v6.18/fs/proc/root.c)
  uses mode 0555 and zero-initialized kernel owner IDs; its
  [user namespace conversion](https://raw.githubusercontent.com/torvalds/linux/v6.18/kernel/user_namespace.c)
  returns overflow IDs for unmapped owners. Together with the measured
  metadata and pinned Incus user-namespace behavior, this supports a narrowly
  reviewed `/proc`-only mapping exception: real directory, exact mode 0555,
  owner pair `0:0` or `65534:65534`. `/proc/1`, mountinfo, effective procfs,
  overmount, and nested-workspace checks remain strict. No `/proc/self`
  substitution is authorized by this evidence. Focused regression/review and
  another selected VM28 run are required before a pass is claimed.
- Before that rerun, pinned Incus source inspection found that HTTP file GET
  supplies `stat.Size()` to `Content-Length`/`ServeContent`. Procfs mountinfo
  reports size zero despite containing dynamic data, so the HTTP reader cannot
  establish the required mount evidence. The same correction batch will use a
  fixed-path bounded SFTP read through EOF, retaining all metadata, namespace,
  size, and mount checks. Transport regressions and retained review are
  required; this avoids rerunning a VM for an already identified incompatibility.
- The retained Sol/high reviewer approved both narrow corrections and
  independently passed the focused runtime tests. The fixed-path SFTP reader
  checks read-only OPEN, sequential IDs and offsets, bounded chunks/content,
  explicit EOF, and successful CLOSE; malformed and oversized replies refuse
  inspection. The implementer also passed the complete runtimeincus, daemon,
  and control packages. The fourth selected VM28 run is now running serially;
  its result remains pending. Codex authenticated acceptance remains pending
  user validation; this run uses no authentication.
- Fourth selected VM28 exited **1**:
  `.cache/p-vm/integration-20260924T180821Z-3241407.log`. The corrected proc
  metadata and SFTP mountinfo checks passed; the source export then timed out
  waiting for HTTP directory listing headers at `/workspace`. The operation
  finished `failed` at `cleaned`, with exact helper cleanup and source
  restoration. The single VM powered down at about 59 seconds and its runner
  was drained; no VM remains running. The existing implementer/reviewer are
  diagnosing the HTTP/SFTP interaction before another run. VM28 remains
  unpassed, and no authentication was attempted.
- Pinned SFTP source narrows the directory timeout: its
  [READDIR implementation](https://raw.githubusercontent.com/pkg/sftp/v1.13.11/server.go)
  formats each entry's long name using
  [OS user/group lookup](https://raw.githubusercontent.com/pkg/sftp/v1.13.11/ls_formatting.go).
  The NixOS substrate enables nsncd by default. A lookup waiting on that frozen
  guest service is therefore a concrete hypothesis; direct SFTP READDIR would
  retain the same dependency. The team is reviewing static account lookup in
  the P base image before any further VM run. This cause is not yet confirmed
  by a passing integration test.
- The retained reviewer approved a base-image-only NSS correction: disable
  guest nscd/nsncd, exclude additional NSS modules, use static `files` account
  lookup and `files dns` hostname lookup. The fixed root/p/Nix build accounts
  remain local. VM28 must check generated configuration, fixed identity
  lookups, and absent name-service sockets before retrying frozen export.
  No host name-service, runtime service masking, or Incus isolation change is
  included. Implementation and serial validation remain pending.
- The retained reviewer approved the implemented NSS image change and VM28's
  pre-freeze assertions. Nix syntax, Bash syntax, and ShellCheck passed
  independently. The fifth selected serial VM28 run is starting with the
  original HTTP directory read, so its outcome will test the prior failure
  path directly. Built-image behavior is still pending validation.
- Fifth selected VM28 **passed**, runner exit **0**:
  `.cache/p-vm/integration-20260924T181641Z-3291837.log`, with
  `P_WORKSPACE_INSPECTION_PASS` at about 72 seconds and selected/smoke pass
  markers. Generated NSS configuration, fixed identity lookup, absence of
  nscd, and the previously timing-out frozen HTTP directory read passed.
  Running/stopped inspection, hostile-input refusal, exact helper cleanup,
  inert helper limits/isolation, durable Start/Attach guards, and recovery
  after the fixture killed the daemon following a real pause all passed.
  The single 12 GiB VM powered down at about 75 seconds, its runner was drained,
  and the fresh disk was removed. This validates the bounded 8a1 foundation,
  not complete loss analysis, destructive operations, or authenticated Codex.
  A full checkpoint through step 28 is next because the base-image NSS change
  also affects existing session/Nix paths.
- The full checkpoint through 28 is running from the reviewed immutable
  snapshot at `.cache/p-vm/integration-20260924T181900Z-3338514.log`. Meanwhile,
  the single implementation stream proceeds with **8a2**: bounded inventory
  of all Git-known worktrees, runtime/external classification, ignored-file
  summaries, local ref/commit retention evidence, and a complete canonical
  fingerprint bound to freshly observed P refs. The retained reviewer checks
  the batch. No destructive calls or container-ceiling increase are included;
  unsupported/incomplete evidence remains unavailable. This new work is not
  part of the running checkpoint and has not passed a VM gate.
- 8a2's supported-policy boundary is explicit: current trusted configuration
  rejects filesystem grants and native inspection permits only the root and
  fixed P endpoint device. An empty external-worktree list therefore requires
  exact zero-grant/device proof; an out-of-policy Git-known path is unavailable.
  External-grant classification remains part of step 9's composition gate.
  The coordinator drafted VM29 for pushed/unpublished commits, linked runtime
  worktrees, dirty/untracked/ignored files, changed-content fingerprints,
  stopped inspection, and refusal to copy a protected home. Its credential
  sentinel is a dummy file; fixture teardown is not public cleanup evidence.
  Bash syntax and ShellCheck pass; product implementation, batch review, and
  VM29 validation remain pending.
- Full serial checkpoint through **28 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260924T181900Z-3338514.log`.
  Cache collection passed at about 801 seconds, authentication-free Codex
  fixtures at 826 seconds, workspace inspection at 878 seconds, and the full
  `P_PRODUCT_INTEGRATION_PASS` marker at 880 seconds with `P_VM_SMOKE_PASS`.
  The one 12 GiB VM powered down at about 882 seconds; the runner was drained
  and its fresh disk removed. This is the reviewed 8a1/NSS snapshot, before
  ongoing 8a2 changes and VM29; it does not validate that unfinished work.
  Authenticated Codex acceptance remains **pending user validation; not passed**.
  No VM is running.
- 8a2 review requires enumerating every bounded stored local commit object,
  including detached/reflog-only/dangling commits, rather than relying on
  `rev-list --all`. VM29 now seeds an unreferenced commit and requires it in
  the loss report. A Git-generated linked-worktree unit fixture must preserve
  ordinary inert administration files/reflogs; a reader-trace regression must
  prove that a forged private-home root is refused before reading that home.
  These checks are pending implementation/review, not passed evidence.
- The retained reviewer approved the coherent 8a2 batch for VM29. Both
  implementer and reviewer passed the complete `control`, `daemon`,
  `runtimeincus`, and `gitservice` packages. Evidence includes real-Git
  unborn/detached worktrees, linked metadata/reflogs, every bounded stored
  commit object, exact P object absence versus query failure, schema 9→10
  migration and durable guards, protected-home reader traces, and replacement
  refusal during thaw/recovery. The fingerprint binds the confined Incus
  project/name and server-issued instance UUID/generation; cleanup also checks
  that identity before resuming or releasing the guard. VM29 now starts
  serially, with an additional empty-bootstrap case. No pass is claimed yet.
- First VM29 exited **1**:
  `.cache/p-vm/integration-20260924T190149Z-3362030.log`. Public unborn loss
  inspection completed and restored the ready source. Fixture setup then
  attempted a push without P's fixed `GIT_SSH` helper, causing default SSH to
  try DNS for host `p`. The coordinator corrected that fixture environment to
  match existing lifecycle/environment tests; product code and assertions are
  unchanged. The VM powered down at about 42 seconds and its runner was drained.
  Linked/retention/fingerprint cases were not reached, so VM29 remains unpassed.
- Second selected VM29 **passed**, runner exit **0**:
  `.cache/p-vm/integration-20260924T190554Z-3411084.log`, with
  `P_WORKSPACE_LOSS_PASS`, selected-suite pass, and `P_VM_SMOKE_PASS`.
  This used the public CLI/daemon against real Incus and Git: unborn bootstrap,
  linked worktree status/ignored sizes, retained versus unpublished and dangling
  commits, same-size changed-content fingerprints, stopped inspection, and
  protected-home refusal with an unchanged dummy credential all passed.
  The single 12 GiB VM powered down at about 87 seconds; its runner was drained
  and the fresh disk removed. No VM is running. This validates bounded 8a2
  under the current no-external-grants policy. It does not establish public
  Discard/Delete, credential cleanup through those operations, or authenticated
  Codex acceptance, which remains **pending user validation; not passed**.
- The same implementation stream and retained reviewer are preparing **8b**
  in two verifiable batches: creation admission plus fresh destructive previews,
  then confirmed removal and durable forward recovery. Admission must preserve
  one inspection-helper slot under the existing native container ceiling.
  Count all actual containers and unresolved creation reservations, associating
  an exactly owned builder with its pending session so concurrent builders are
  not counted twice. Foreign or ambiguous occupancy still counts; later
  external occupancy can make inspection unavailable. No ceiling increase or
  foreign-instance cleanup is authorized. Removal must bind the server-issued
  Incus UUID/generation, reject replacements and stale confirmation, and add
  Delete's separate retained-branch/origin loss evidence. These are pending
  implementation/review/VM gates. Automated credential cleanup uses dummy files
  only; authenticated Codex acceptance remains **pending user validation**.
- The capacity subgate passed retained-reviewer source review and focused
  `control`, `daemon`, and `runtimeincus` tests covering admission, exact builder
  association, and workspace behavior. Review fixed an origin-lock reentrancy
  by persisting the builder tree OID before init, and fixed established-runtime
  association to include its exact endpoint path. Both rowless bootstrap
  intents and sessions without a creation-operation row count conservatively.
  VM30's capacity/preview fixture is being prepared; no new VM has run.
- Fixture audit found that VM25/26 currently free completed test roots with
  direct Incus deletion while retaining their session rows. New admission must
  continue counting those unresolved sessions. Before the next full checkpoint,
  compose public Discard into fixture cleanup after the existing assertions.
  Release the bootstrap before C so A/B remain available for the image-loss
  checks. In the collection case, release B/C and then A after their checks,
  leaving D plus the two concurrent E/F builders beneath the helper reservation.
  Retain the final D/E/F private-root assertions. The non-collection case can
  recreate main for subsequent source edits and release checked roots before
  further admission. The retained reviewer agreed with this sequence. Keep
  every prior cache/concurrency assertion and the native four-container limit.
  This dependency does not invalidate the recorded earlier full checkpoint;
  that checkpoint predates the admission change. Public removal remains pending.
- Selected serial **VM30 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T124724Z-3503304.log`.
  `P_REMOVAL_PREVIEW_PASS` at about 99 seconds, selected suite pass at about
  102 seconds, and `P_VM_SMOKE_PASS` all appeared. The runner drained, the
  single VM powered down at about 104 seconds, the fresh disk was removed, and
  the integration lock is free. The public CLI and real Incus/Git verified
  three-session helper headroom, fourth-admission refusal, unrelated fourth
  container refusal before freeze, unborn/committed Discard and Delete previews,
  retained versus uniquely lost commits, wrong-session/stale-state refusal,
  stopped-state reinspection, explicit missing-runtime acknowledgement, and
  unchanged dummy credential and sibling session. This is read-only preview
  evidence; the fixture's direct native teardown is **not** public cleanup.
  Authenticated Codex acceptance remains **pending user validation**.
- **Next batch 8b2a — confirmed Discard, acceptance before edits:** a keyed
  public confirmation requires an unexpired, exact Discard token and no pending
  attachment. It guards the assigned P ref, quiesces the exact native
  UUID/generation, recomputes the complete workspace loss while execution
  cannot change it, and rejects stale token/facts before any irreversible
  point, restoring only that exact paused source. After a durable removal
  commit point, it disables the session's P Git/session RPC authority, removes
  only the verified owned runtime, removes P-owned endpoint/local secret copies,
  verifies the guarded P branch is retained at its expected tip, releases the
  assignment/guard, and completes forward after daemon restart or uncertain
  native outcome. An exact-name replacement must never be resumed or removed.
  Tests must cover stale token, attachments, branch changes, replacement,
  crash boundaries, idempotent retry, and dummy credential loss with an
  unaffected sibling. The retained reviewer approves this batch after focused
  tests; a selected serial VM validates public Discard before Delete starts.
- Native-delete recovery finding during 8b2a: pinned Incus 7.4 issues DELETE
  by instance name, and a request can be delayed before the server records an
  operation. A timed-out request may therefore race a future same-name
  replacement. The implementation must persist `delete-issued` before sending
  the request and preserve the guard/name tombstone on an ambiguous outcome;
  neither an empty operation list nor momentary native absence permits blind
  retry or guard release. A positively observed same-turn exact deletion can
  advance to `runtime-absent` and clean forward. Crash after deletion but before
  that durable phase may require targeted repair in 8d. This is a safety
  limitation to validate and report, not a claim that automatic recovery has
  passed. Pinned Incus source at
  `/nix/store/l8khfwrfkwvqigs8mbbyywjjjnn3vr58-source/cmd/incusd/instance_delete.go:52-105`
  waits for server readiness before loading by project/name, then registers a
  name-targeted delete operation without a request-cancellation or
  UUID/generation conditional in that path. Reviewer assessment remains
  pending.
- VM31's auth-free fixture is drafted in
  `tests/integration/steps/31-discard-lifecycle.sh`; Bash syntax and pinned
  ShellCheck pass. It selects the Codex adapter, initializes two separate
  private homes without login, writes dummy credential files, and plans to
  assert stale same-size workspace change rejection, exact public Discard,
  old runtime/endpoint/session absence, retained main tip, idempotent key
  replay, and an unaffected sibling. The fixture is **not** production evidence
  until product code, focused tests, review, and a serial selected VM31 pass.
- 8b2a first focused package run passed, but retained review found two
  precommit gaps before VM31: a stale request at `guarded` must release its
  Git guard even if an unrelated Stop or disappearance changed the source
  before any native effect; and the final commit must recompare the complete
  P ref set bound by the preview/workspace fingerprint, not only the assigned
  tip. Follow-up review also required token expiry inside the SQLite admission
  transaction and found a self-deadlock when a ref-verification callback tried
  to reenter the single-connection Store from `CommitDiscard`. The implementer
  is fixing these specific findings with focused regressions. Review and VM31
  remain pending.
- Selected serial **VM31 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T131804Z-3564160.log`.
  `P_DISCARD_LIFECYCLE_PASS` at about 79 seconds, selected-suite pass at about
  81 seconds, and `P_VM_SMOKE_PASS` appeared. The one VM powered down at about
  82 seconds; its runner drained, fresh disk was removed, and the integration
  lock is free. Public CLI rejection of changed same-size workspace content
  preserved the source, endpoint, key, dummy credential, and Git authority; a
  subsequent push proved guard release. Fresh confirmation removed the exact
  runtime, endpoint, host session key, and session row, retained main at its
  confirmed tip, and preserved the sibling runtime, key, and dummy credential.
  The selected Codex adapter initialized both private homes without login.
  This is auth-free public Discard evidence, not authenticated Codex acceptance
  or Delete. The reviewer approved 8b2a after four focused Go package suites
  and closure of the rollback, full-P-ref, transactional-TTL, and SQLite
  self-deadlock findings. Ambiguous native stop/delete remains guarded for
  targeted repair, never blind retry.
- **Next batch 8b2b — confirmed Delete, acceptance before edits:** keyed
  confirmation accepts only a fresh Delete token bound to the exact reviewed
  branch-loss and origin identity. It reuses Discard's guarded quiescence,
  exact runtime and local-secret removal, then atomically deletes only the
  assigned P branch with an expected-old-value check after runtime absence.
  Other P refs, the origin, external mounts, and image cache remain. Durable
  recovery completes forward from `runtime-absent`; a branch mismatch stays
  guarded for targeted repair. Tests cover unborn branch, retained sibling
  refs, local-only and unknown-origin evidence, stale P tips, wrong-action or
  reused tokens, post-removal crash and branch-CAS conflict, dummy credential
  cleanup, and unaffected siblings. Focused tests and retained review precede
  the smallest serial VM selection against real Incus/Git. Existing environment
  fixtures are adapted only after public Delete also passes its gate.
- VM32's auth-free fixture is implemented in
  `tests/integration/steps/32-delete-lifecycle.sh`; Bash syntax and pinned
  ShellCheck pass. With the selected Codex adapter and separate dummy private
  files, it checks Delete's unique P commit loss versus a retained sibling,
  changed-tip stale rejection, wrong-action token refusal, fresh exact ref
  deletion after runtime cleanup, old key/endpoint absence, idempotent replay,
  and unaffected sibling branch/runtime/credential. The retained reviewer
  approved the implementation after five affected Go suites and VM32 static
  checks; its unknown-origin binding and uncertain WASM broker retry regressions
  passed. Selected serial **VM32 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T134113Z-3633124.log`.
  `P_DELETE_LIFECYCLE_PASS` at about 69 seconds, selected-suite pass at about
  71 seconds, and `P_VM_SMOKE_PASS` appeared. The sole VM powered down at about
  73 seconds; the runner removed its fresh disk and released the integration
  lock. This is real Incus/Git Delete evidence with dummy credentials, not
  authenticated Codex execution.
- 8b2b review found an upgrade recovery gap: schema-11 in-flight Discard
  evidence lacks the new action field. The implementer patched and tested
  normalization only for legacy Discard at `validated` and `secrets-absent`;
  Delete still requires explicit Delete action evidence. The retained reviewer
  independently passed the five affected Go suites and VM32 static checks.
  The focused unknown-origin review-digest/revalidation regression and hostile
  WASM double-attempt test passed before selected VM32 approval.

- **Next batch 8c — Rename, acceptance before edits:** a public keyed Rename
  changes the assigned P and workspace branch while preserving session UUID,
  runtime identity, local-ahead commits, workspace files, credentials, process
  state, plugin bindings, and origin refs. It rejects invalid or occupied names,
  stale expected tips, missing/unreachable runtime or workspace, and concurrent
  lifecycle actions. Durable phases reserve and guard both refs, quiesce the
  runtime, atomically create the new P ref at the expected old tip, rename the
  workspace branch without resetting work, update assignment/principal policy,
  delete the old P ref with an expected-old check, and release guards/resume.
  Recovery before new-ref creation may roll back; from that commit point it
  completes forward or stays guarded for explicit repair on ambiguity. Focused
  tests cover conflicts, local-ahead work, each durable crash boundary,
  authority denial, and idempotent replay. Retained review follows focused
  tests; the smallest serial VM selection proves real Git/Incus rename and
  persistence after a completed-operation daemon restart. Focused real SQLite
  replay tests cover in-progress phase recovery; VM33 does not claim a
  deterministic mid-phase daemon crash. No fixture-only result will be called
  production evidence.
  Auth-free VM33 fixture is implemented in
  `tests/integration/steps/33-rename-lifecycle.sh`; Bash syntax and pinned
  ShellCheck pass. It checks the real selected Git/Incus path,
  local-ahead and untracked files, dummy credential, live PID, session key and
  endpoint, sibling isolation, idempotent replay, and daemon restart. Its public
  operation assertions were reviewed against the completed patch. The retained
  reviewer closed a durable backup-write attempt-marker issue and a definite
  pre-pause refusal rollback issue; it independently passed the four affected
  Go suites. Selected serial **VM33 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T141633Z-3704851.log`.
  `P_RENAME_LIFECYCLE_PASS` appeared at about 40 seconds, selected-suite pass
  at about 43 seconds, and `P_VM_SMOKE_PASS` appeared. The sole VM powered down
  at about 44 seconds; the runner removed its fresh disk. VM33 demonstrates a
  completed Rename and persistence after a subsequent daemon restart. Focused
  real SQLite/native-effect tests, not VM33, cover in-progress phase replay.

- **Next batch — VM25/26 capacity-fixture adaptation, acceptance before edits:**
  the existing public Nix environment/cache fixtures use confirmed public
  Discard to release completed fixture-owned sessions before subsequent
  admissions. They retain the four-container Incus limit and every existing
  cache, private-root, stale-preview, retry, and concurrent-publication
  assertion. The noncollection path recreates the retained main assignment
  through the public API before editing its source, then releases no-longer-
  needed completed roots so blocked invalid-default intent and later
  absent-default creation fit the same limit. The collection path preserves
  the live roots required for image-loss and cache-retention probes. Bash
  syntax/ShellCheck precede one serial selected VM25/26 run; a fixture-only
  correction does not add production Nix support.
  First selected combined attempt used
  `.cache/p-vm/integration-20260925T142118Z-3754062.log` and failed in VM25
  after public Discard/recreate had completed: the recreated private main
  workspace lacked the fixture's Git author identity, so its next commit
  returned 128. The VM powered down, fresh disk was removed, and lock was
  released. The fixture now sets that identity in the recreated session;
  VM25 will be rerun alone before VM26. No production assertion was weakened.
  Serial VM25 rerun **passed**, runner exit 0:
  `.cache/p-vm/integration-20260925T142639Z-3800180.log`;
  `P_PUBLIC_ENVIRONMENT_PASS` at about 243 seconds, selected pass about 246,
  smoke pass, powerdown about 248, fresh disk removed and lock free.
  The next serial VM26 attempt
  `.cache/p-vm/integration-20260925T143124Z-3846117.log` failed at the
  unchanged `related_count>=2` cache-preview assertion after completed public
  cleanup. Source inspection identified distinct image identities: A/B use the
  first image, C/D the rebuilt image. Discarding C before D's preview left
  only one related session. The fixture now retains C through exact-image
  collection, verifies its private root survives collection, and Discards C
  before concurrent E/F creation. No cache assertion was weakened; VM26 rerun
  **passed**, runner exit 0:
  `.cache/p-vm/integration-20260925T143718Z-3847929.log`.
  `P_ENVIRONMENT_CACHE_COLLECTION_PASS` appeared at about 268 seconds,
  selected pass at about 273, smoke pass, and powerdown at about 274 seconds.
  The fresh disk was removed and lock released. These VM25/26 reruns prove the
  existing offline Nix/cache behavior under current admission limits; they do
  not establish public-egress Nix fetching or authenticated Codex execution.

- **Next batch 8d1 — explicit missing-runtime repair, acceptance before edits:**
  when Incus authoritatively reports the assigned runtime absent and the P
  branch remains at a known committed tip, a host-only read-only preview
  identifies the UUID, branch/tip, recorded image and its presence, credential
  and policy identity, and unrecoverable runtime-local files/processes. A
  keyed confirmed repair rechecks those exact facts under lifecycle/ref
  authority and recreates one runtime for the same UUID and branch using the
  recorded image when present; it neither resets the P branch nor changes
  source or policy silently. Unreachable Incus, changed tip/image/assignment,
  active attachment or competing operation, and missing-image policy drift
  block confirmation with a fresh-plan requirement. Durable intent precedes
  any native create; uncertain native outcomes remain blocked and cannot make
  duplicate runtimes. Focused SQLite/native tests cover stale confirmation,
  idempotent replay, exact-generation reconciliation, and sibling isolation;
  retained safety review precedes the smallest serial real-Incus/Git VM gate.
  Other repair shapes, abandonment, and changed-request superseding Create
  remain separate gates; 8d1 alone will not be reported as complete repair.
  The auth-free VM34 fixture is implemented in
  `tests/integration/steps/34-missing-runtime-repair.sh`; Bash syntax and
  pinned ShellCheck pass. It injects a fixture-only exact runtime loss, then
  exercises the public preview/confirmation, same UUID and retained branch,
  preserved host session key, lost dummy runtime credential/local file,
  unaffected sibling, replay, and post-completion restart. The retained
  reviewer closed three findings before the VM: truthful recorded-image/source
  provenance against the current checkout tip, read-only registered private-key
  verification, and fresh committed-tip object proof. It independently passed
  five affected Go suites and rechecked the narrow final daemon/runtimekit
  follow-up. Selected serial **VM34 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T151241Z-3919531.log`.
  `P_MISSING_RUNTIME_REPAIR_PASS` appeared at about 47 seconds, selected pass
  at about 50, smoke pass, and powerdown at about 52 seconds. The fresh disk
  was removed and lock released. This proves public missing-runtime repair on
  real Incus/Git with dummy credentials; focused tests, not VM34, cover
  uncertain-init and in-progress phase replay. Other repair shapes remain.

- **Next batch 9a — exact-project trusted policy and immutable comparison,
  acceptance before edits:** trusted host configuration may supply policies
  keyed by complete P project path. An explicit map has no wildcard or
  fallback: creation for an unconfigured path is refused. To preserve existing
  local installations and VM fixtures, the prior single trusted
  `project_policy` remains a compatibility mode only when no map is supplied;
  simultaneous global and keyed fields are rejected. This batch accepts only
  the existing `network:"none"`, empty filesystem mounts, and validated
  interactive command; typed mounts and public egress remain separate 9b/9c
  gates. Creation captures the effective normalized policy and digest once.
  After a trusted config change/restart, an older session reports `outdated`
  while retaining its runtime snapshot; missing/unsafe current authority
  reports `invalid` and blocks Start. New sessions use the current exact
  project policy. No repository content can select grants and no live Incus
  policy update occurs. Focused config/SQLite/daemon tests cover exact key,
  cross-project isolation, drift, invalid policy, and compatibility mode;
  retained isolation review precedes one serial real-VM selection. 9a alone
  does not establish filesystem access or public network safety.
  VM35's fixture is implemented in
  `tests/integration/steps/35-project-policy.sh`; Bash syntax and pinned
  ShellCheck pass. It uses two exact project mappings, checks an unconfigured
  third path is refused, changes only A's trusted command after daemon
  restart, verifies old A `outdated`/B `current` and a new A snapshot
  `current`, then removes A's mapping to require `invalid` and Start refusal.
  The fixture also verifies old A's root-owned `/etc/p/session.json` bytes
  remain unchanged, new A receives the changed command, and B with omitted
  mounts stays current across restarts. The retained reviewer closed canonical
  nil/null/empty mount hashing, strict stored-snapshot decoding/integrity, and
  effective-policy event comparison findings; it independently passed full
  control and daemon Go suites. Selected serial **VM35 passed**, runner exit
  **0**: `.cache/p-vm/integration-20260925T153315Z-3977324.log`.
  `P_PROJECT_POLICY_PASS` appeared at about 48 seconds, selected pass at about
  52, smoke pass, powerdown about 54, fresh disk removed and lock released.
  9a proves exact-project policy selection and immutable drift on real
  sessions; no filesystem or public network grant is enabled by this gate.

- **Next batch 9b — typed project filesystem grants, acceptance before edits:**
  trusted exact-project policy can name bounded grants with a portable unique
  name, canonical absolute file/directory source, explicit read-only or
  read-write access, and explicit executable permission. The target is fixed
  at `/mnt/p/<name>`; requests and repository content cannot choose a source
  or target. Validation rejects symlinks, dangling/overlapping/changing paths,
  root/home/control/Incus/Nix/credential trees, and sources outside the
  confined project's preauthorized disk-source ceiling. Creation captures the
  source identity and effective grant snapshot; Start revalidates source
  identity and ceiling and reports `invalid` instead of silently remounting a
  changed source. Incus receives only exact private, non-propagating disk
  devices with the reviewed access/exec flags. Discard/Delete never delete
  external source contents. Focused config/native tests cover RO, explicit RW,
  noexec, path substitution, overlapping/broad paths, and cross-session or
  undeclared mount denial; retained isolation/destructive review precedes one
  serial VM gate. VM fixture may extend the confined project's *disk-source
  allowlist only* with one dedicated disposable grant root; it must not enable
  nesting, privileged containers, extra network access, or a broad host path.
  9b does not establish public egress.
  VM36's provisional auth-free fixture is drafted in
  `tests/integration/steps/36-filesystem-grants.sh`; Bash syntax and pinned
  ShellCheck pass. It checks real RO/noexec directory and file grants plus an
  explicit RW/exec directory grant,
  sibling absence, external contents after public Discard, and an inode-swapped
  source blocking Start. The grant root is disposable fixture-owned data under
  a dedicated narrow confined-project ceiling. This has not run; native
  options, production patch, and retained review remain pending.
  The VM project preauthorizes only its existing endpoint root plus this
  disposable grant root. All 16 earlier daemon fixtures now declare that same
  exact pair so their confinement checks remain meaningful in selected runs
  and the eventual full suite; Bash syntax passes for all step scripts.
  Review found effective mount-flag remount, bounded mountinfo EOF, writable
  ancestor, first-create grant wiring, and sibling endpoint-ceiling issues;
  the implementer addressed these and the retained reviewer approved five
  independently passing affected Go suites. First selected VM36 attempt
  (`/tmp/p-vm36-run.log`) stopped during the Nix package check before VM boot:
  control source capture reported unsafe parent `/`, and runtime-incus source
  validation reported a changed parent in their test fixtures. The implementer
  is diagnosing the Nix build-path assumptions; this is not VM integration
  evidence.
  The sandbox-specific test-fixture correction passed the full pinned Nix
  package check and retained review without relaxing production validation.
  Selected VM36 then booted but blocked initial `project.create` at
  `principals-ready`: native Incus reported `grant device postcondition
  failed` with all three grants captured. Log
  `.cache/p-vm/integration-20260925T161545Z-4108047.log` (runner exit 1,
  VM powered down and disk removed). This is a runtime failure, not a pass;
  native postcondition diagnostics are pending before correction.
  Because the normal bounded Incus command wrapper suppresses stderr, a
  failure-only VM36 probe now tries the exact disposable RO grant device and
  prints bounded CLI output and instance-device state before cleanup; its Bash
  syntax and pinned ShellCheck pass. This is diagnostic evidence, not a
  weakened assertion or production fix.
  Diagnostic serial VM36 also failed before guest startup
  (`.cache/p-vm/integration-20260925T161912Z-4141054.log`, runner exit 1,
  VM powered down/disk removed), but showed Incus accepted an exact disposable
  probe and `p-grant-data` already existed with the intended source, target,
  RO/private/unshifted/nonrecursive/raw mount options. That established the
  directory device but did not diagnose the next, file-device failure; an
  exact file probe was required before changing behavior.
  The exact file-source diagnostic resolved that ambiguity: serial VM36
  `.cache/p-vm/integration-20260925T162342Z-11303.log` showed local and expanded
  Incus state containing only the first directory grant. The exact file probe
  failed with Incus 7.4's `The recursive option is only supported for additional
  bind-mounted paths`. P was passing `recursive=false` for a file device; the
  implementer is making that option type-specific while preserving exact
  postcondition and other flags. Runner exit 1; VM powered down/disk removed.
  The reviewed type-specific fix passed five Go suites and the pinned Nix
  package check. Next serial VM36 reached a ready guest with all three exact
  devices, then the first `cat` failed because the disposable fixture's
  `umask 077` made its readme host-owned mode 0600 under an unshifted mount.
  Log `.cache/p-vm/integration-20260925T162713Z-63548.log` (runner exit 1,
  VM powered down/disk removed). The fixture now makes that readme 0644 and
  removes its temporary failure-only probe; Bash syntax and ShellCheck pass.
  Product source is unchanged. The effective read/write/exec and cleanup
  assertions still need the serial VM36 rerun.
  Selected serial **VM36 passed**, runner exit **0**:
  `.cache/p-vm/integration-20260925T162931Z-96361.log`.
  `P_FILESYSTEM_GRANTS_PASS` appeared at about 67 seconds, selected pass at
  about 70, smoke pass, powerdown about 72, fresh disk removed and lock
  released. The fixture used real Incus grants and guest commands to verify
  RO/noexec directory and file mounts, explicit RW/exec directory, no sibling
  grant, external data after public Discard, and Start refusal after a source
  inode swap. It does not establish public egress or authenticated Codex use.

- **Next batch 9c — validated public egress, acceptance before edits:**
  trusted exact-project policy may select `public-egress` only when P verifies
  a dedicated, preconfigured Incus network plus its host routing, DNS, and
  packet filters. `none` continues to attach no NIC. The public profile permits
  outbound public DNS and HTTP(S)/Nix fetch and the existing narrow Unix
  endpoints, while denying host/gateway, LAN/RFC1918, carrier-grade NAT,
  link-local/metadata, multicast, ULA, sibling runtimes, Incus API, and
  undeclared service destinations across IPv4 and IPv6. Test literals, DNS
  rebinding, redirects, and IPv4-mapped IPv6; fail closed when the configured
  network or filtering proof is absent or drifts. Capture the selected policy
  immutably and block Start when current trusted authority is invalid. Focused
  policy/native tests precede retained isolation review; a single serial VM
  selection must prove actual packet behavior and one public Nix fetch without
  weakening `none`, container confinement, or nested-build restrictions.
  If the VM cannot reach a real public destination, record only its negative
  isolation evidence and leave the public-fetch gate pending.
  Pinned Incus review found bridge ACLs do not filter same-bridge siblings and
  default bridge DHCP/DNS service rules precede ACLs. For this batch, the
  existing runtime-isolation contract is applied strictly: no DHCP or
  gateway-DNS exception; require static guest addressing, disabled bridge
  DHCP/DNS/IPv6, exact managed NIC and network ACLs, and packet-level denial.
  The machine owner and trusted host configuration provision the confined
  project/network; untrusted guest code has no Incus API authority. If a safe
  collision-free static-address substrate or public-fetch proof is unavailable,
  keep the relevant gate pending and continue independent MVP work.
  The 9c implementer is adding explicit trusted substrate selection, durable
  static IPv4 reservations, native ACL/NIC checks, a guest route/DNS setup,
  and a read-only live nftables proof. Root drafted the conditional VM37
  substrate in `dev/vm/machine.nix`: managed networking only for selected
  VM37 or full-suite runs, a dedicated no-DHCP/no-DNS/IPv6 bridge, exact ACL,
  and root-owned INPUT/FORWARD drops with narrow read-only sudo access. Nix
  syntax parses; no 9c focused, Nix build, or VM result is claimed yet.
  A fixture-only host-config adapter now gives the 17 earlier daemon VM steps
  the exact trusted public substrate selector when run in the final managed
  full-suite VM; their project policies remain `none`, and selected earlier
  VM runs still use the original NIC-blocked ceiling. All step Bash syntax,
  helper ShellCheck, and Nix parses pass. VM37's packet fixture is drafted but
  still needs host-public-IP, DNAT, other denied-range, and drift probes before
  its full 9c acceptance claim.
  First serial VM37 booted, provisioned the managed bridge/ACL and root-owned
  host listeners, but the daemon refused its proof executable before creation:
  `untrusted path ancestor /run/wrappers/bin`.
  Log `.cache/p-vm/integration-20260925T171830Z-162866.log` (runner exit 1,
  VM powered down/disk removed). This is a real NixOS wrapper-path shape
  conflict with the strict ownership check; no packet or public-fetch gate
  passed. Path diagnostics and a reviewed safe correction are pending.
  The retained reviewer approved a pinned NixOS sudo-wrapper symlink check
  that preserves the stable host path and verifies the root-owned resolved
  executable. Second serial VM37 passed that check but refused daemon startup
  on `public egress bridge config differs from trusted closed network`:
  `.cache/p-vm/integration-20260925T172526Z-215225.log` (runner exit 1,
  VM powered down/disk removed). A failure-only read of the confined Incus
  network's actual config is now in VM37; Bash syntax/ShellCheck pass. No
  packet or public-fetch result exists yet, and product assertions remain
  unchanged pending exact diagnostic evidence.
  Diagnostic serial VM37 confirmed the confined caller sees `config: {}` for
  the managed bridge: `.cache/p-vm/integration-20260925T172801Z-264410.log`
  (runner exit 1, VM powered down/disk removed). Pinned Incus source populates
  managed-network config only for callers with network-edit entitlement; that
  entitlement will not be granted. The proposed correction is a root-owned,
  fixed-argv, read-only network-config proof helper with a narrow sudo rule,
  while P continues using its confined Incus socket for all mutations. This
  remains under retained isolation review; no 9c packet gate has passed.
  The root-owned VM proof helper is drafted as a zero-argument Nix-store
  executable with an exact read-only Unix-socket GET for this bridge only,
  cleared environment/stdin, timeout and 64 KiB response bound, strict JSON
  duplicate/trailing rejection, and exact closed-network validation before it
  emits six sanitized fields. The VM sudo rule names only that helper with
  empty arguments. Nix syntax parses; product parser alignment, review, and
  the next serial VM run remain pending.
  Retained helper review closed a transfer-bound/tempfile gap in the VM
  implementation: it now writes beneath root-owned `/run`, enforces curl's
  64 KiB limit during transfer and rechecks size before strict JSON parsing.
  Product review additionally requires the confined bridge read to contain an
  explicit empty config object, not a missing/null field. Focused correction
  and reviewer recheck precede another VM run.
  The reviewed helper correction passed bridge proof in serial VM37, then
  daemon startup refused the ACL view as missing or having unexpected
  ingress/config: `.cache/p-vm/integration-20260925T174322Z-317419.log`
  (runner exit 1, VM powered down/disk removed). A failure-only confined
  `network acl show` diagnostic is added; exact ACL representation must be
  identified before changing the closed assertion. No packet gate passed.
  Diagnostic serial VM37 showed the root-provisioned ACL has the expected six
  egress rules, empty ingress, and empty config, but the confined caller's
  exact ACL read fails `User does not have permission for project "default"`:
  `.cache/p-vm/integration-20260925T174551Z-366787.log` (runner exit 1,
  VM powered down/disk removed). Incus does not grant this read to the
  confined user project; P must use a second fixed read-only observation via
  the same narrow root helper boundary or leave public egress disabled. No
  edit entitlement or general admin socket will be granted.
  The VM proof helper is now drafted to read exactly the named bridge and ACL
  through two fixed read-only admin-socket GETs, each time/size bounded,
  validate their complete closed configuration, and emit only a sanitized
  combined object. The zero-argument sudo rule is unchanged; Nix syntax
  parses. Product combined-parser tests and retained review are pending before
  another serial VM run.
  The retained reviewer approved the combined proof and ordered ACL check;
  serial VM37 then passed host preflight and attached a public NIC but blocked
  at guest assembly: `p-runtime-kit: public guest address inventory
  unavailable` in `p-interactive.service`.
  Log `.cache/p-vm/integration-20260925T175635Z-419985.log` (runner exit 1,
  VM powered down/disk removed). A bounded guest `ip -j address`/route read is
  added to the failure path to distinguish extra link-local/preexisting
  addresses from parser mismatch before any production correction. No packet
  or fetch gate has passed.
  The next serial diagnostic VM37 repeated that guest failure, but the
  lifecycle had already stopped the instance by the time the fixture tried
  `inc exec ip`: `.cache/p-vm/integration-20260925T175852Z-469475.log`
  (runner exit 1, VM powered down/disk removed). This did not yield the
  needed address evidence. Per the two-correction rule, the next attempt must
  collect the guest's bounded address inventory before service failure/stop;
  no validator relaxation or repeated blind VM run is authorized.
  The pre-stop diagnostic did collect that inventory in the next serial VM37:
  `.cache/p-vm/integration-20260925T180226Z-517559.log` recorded eth0 with
  `10.233.0.10/24` and `fe80::1266:6aff:feeb:29c/64`. Runner exited 1,
  powered down, and removed the fresh disk. The implementer prepared a
  fail-closed guest correction that disables IPv6 on eth0 before link-up and
  re-attests that state; focused runtimekit tests and retained isolation review
  passed. Serial VM37 `.cache/p-vm/integration-20260925T180635Z-568580.log`
  advanced past the address check, then blocked at `public guest resolver is
  not root-owned and bounded`. Runner exited 1, powered down, and removed the
  disk. The exact resolver path/ownership/mode is under diagnosis; no packet
  or fetch gate has passed.
  A reviewed metadata-only diagnostic then ran in serial VM37:
  `.cache/p-vm/integration-20260925T181127Z-619985.log`. The guest's
  `/etc/resolv.conf` is a root-owned symlink to `/etc/static/resolv.conf`;
  its regular target is UID/GID 153, mode 0644, 920 bytes. The original
  root-owner gate correctly refused it. Runner exited 1, powered down, and
  removed the disk. The implementer is determining a trusted NixOS/idmap
  correction; no packet or fetch gate has passed.
  The reviewed root-owned pinned-resolver correction advanced serial VM37 to
  the packet fixture: `.cache/p-vm/integration-20260925T181709Z-672843.log`.
  The fixture then stopped at line 206 because its sanitized test PATH lacked
  `python3`; this was a fixture command-path error, not a product denial.
  The three host-side calls now use `/run/current-system/sw/bin/python3`;
  Bash syntax and ShellCheck pass. VM runner exited 1, powered down, and
  removed the disk. Packet/fetch evidence remains pending.
  Serial VM37 `.cache/p-vm/integration-20260925T181954Z-722307.log` then
  exited 0: real Incus smoke passed, the guest public NIC/static slot/resolver
  and host/gateway/sibling/private/IPv6 denials passed, and the fixture emitted
  `P_PUBLIC_EGRESS_NEGATIVE_PASS`. It emitted
  `P_PUBLIC_NIX_FETCH_UNVERIFIED` because this test host could not establish
  the external fetch gate. Fixture DNAT and name/redirect coverage are being
  reconciled with 9c acceptance; this is partial isolation evidence, not
  production public-Nix-fetch evidence.
  A retained-reviewed controlled DNAT fixture then ran in serial VM37:
  `.cache/p-vm/integration-20260925T182721Z-769531.log`. The prerouting
  translation counter increased 0→4, but its forward `ct status dnat`
  counter stayed 0→0; the packet was denied, yet the intended post-DNAT
  forward path was not proved. Runner exited 1, powered down, and removed the
  disk. Additional read-only counters for any forward/input packets in the
  disposable probe table are being added to distinguish route/hook behavior
  from conntrack matching. The strict DNAT assertion remains unchanged; no
  controlled DNAT pass is claimed.
  Diagnostic serial VM37 `.cache/p-vm/integration-20260925T183202Z-816178.log`
  confirmed prerouting 0→4 while both any-forward and DNAT-forward stayed
  0→0; any-input stayed 15→15. Pinned Incus rules have an earlier forward
  priority -200 ACL, so rewriting to a private sibling is blocked before P's
  priority-0 DNAT rule. Runner exited 1 and drained. The fixture is being
  corrected to rewrite to a separately routed disposable public-address
  namespace/listener; it must prove the target live, forward path reached,
  and the production DNAT drop counter increased. No DNAT pass is claimed.
  After retained review, serial VM37
  `.cache/p-vm/integration-20260925T183947Z-863804.log` exited 0 and
  powered down/removed its fresh disk. Its disposable public-address
  namespace target was positively live, then the denied guest DNAT attempt
  increased prerouting, priority -1 forward, and the production priority-0
  `ct status dnat counter drop` counts; it emitted
  `P_PUBLIC_DNAT_NEGATIVE_PASS`. A synthetic hostname answer to forbidden
  gateway/sibling addresses emitted
  `P_PUBLIC_SYNTHETIC_RESOLUTION_NEGATIVE_PASS`. The selected product test and
  smoke passed. The VM emitted `P_PUBLIC_NIX_FETCH_UNVERIFIED`; actual public
  DNS, HTTP(S), real rebinding/redirect and positive Nix fetch remain pending
  and are not established by the synthetic probe.

- **Next batch 8d1 — missing recorded image during missing-runtime repair,
  acceptance before edits:** when the runtime is absent and its recorded image
  cache entry is missing, preview resolves the current committed assigned P
  branch under the same project/branch lock, reports the new environment
  identity and whether it differs from the recorded image/source commit, plus
  irrecoverable runtime-local loss. Confirmation binds the observed branch tip,
  policy, selection, image provenance and intended same-UUID recreation. It
  must refuse stale tip/authority or an ambiguous existing runtime; no
  workspace reset, second runtime, silent image substitution on Start, or
  credential widening. Focused tests precede retained recovery review; the
  smallest selected VM repair fixture must remove the cached image and runtime
  and prove the confirmed same-UUID repair or record a specific blocker.
  Architecture check: a truthful new environment key requires running Nix in
  a restricted builder; the current repair preview is expressly read-only.
  The implementation will preserve that contract by introducing an explicit
  durable prepare operation to reserve/reconcile the builder before a later
  read-only preview and confirmation. A guessed key or untracked preview-time
  builder is not acceptable. This is a design/implementation decision within
  the existing lifecycle and environment authorities, not validation evidence.
  Partial implementation checkpoint: keyed `session.repair.prepare` Store
  admission persists exact branch tip, policy, credential and builder identity
  under the ref guard, accounts for helper capacity, and retains an uncertain
  builder-init marker across restart. Completion requires exact builder
  absence and leaves the accepted runtime image unchanged. A read-only preview
  can consume an exact completed preparation identity. Focused
  `go test ./internal/control ./internal/daemon -run '^TestRepair' -count=1`
  passed. Confirmation, durable image publication/override and cleanup
  recovery are still incomplete; this checkpoint has no VM acceptance.
  Second partial checkpoint: confirmed repair now binds a completed
  preparation digest in SQLite, either accepts a verified same-key cached
  image or durably resolves/realizes/publishes a replacement before exact
  runtime init, and stores the completed per-session image for subsequent
  Start/inspection/status. Schema 16 excludes concurrent cache collection
  while repair/preparation owns the environment. Focused repair/migration tests
  passed; recovery hardening, full affected-package tests, retained review,
  and selected VM evidence remain pending.
  Completed 8d1 source review found and fixed two durable-recovery issues:
  accepted-image selection now follows SQLite insertion order rather than
  wall-clock timestamps, and repair publication shares the creation per-key
  lock plus a durable pending-publication reservation across restart. Focused
  real SQLite regressions cover reverse-clock ordering and same-key exclusion.
  The retained reviewer approved the repaired source; the five affected Go
  package suites passed with scoped Unix-socket permission. A selected VM38
  fixture for the derived-image cache-miss path is being prepared; this source
  approval is not VM acceptance.
  First serial VM38 `.cache/p-vm/integration-20260925T192729Z-941535.log`
  reached target-session creation, then the fixture's second guest commit
  failed because that new worktree had no Git author identity. Runner exited
  1, powered down, and removed the fresh disk. The fixture now sets its
  own local author name/email in the target worktree; Bash syntax and pinned
  ShellCheck pass. This was a fixture setup error, not repair evidence.
  Second serial VM38 `.cache/p-vm/integration-20260925T193015Z-990647.log`
  reached completed `session.repair.prepare`, then its idempotency assertion
  piped JSON to a helper that required a positional argument and exited with
  an unbound `$1`. The same helper misuse appeared in the later confirm
  idempotency assertion. Both fixture assertions now parse the piped operation
  ID directly with jq; Bash syntax and pinned ShellCheck pass. Runner exited
  1, powered down, and removed the disk. This is fixture evidence only.
  Third serial VM38 `.cache/p-vm/integration-20260925T193318Z-1036699.log`
  exited 0: `P_MISSING_IMAGE_REPAIR_PASS`, selected product test pass, and
  smoke pass. It removed only the target's derived image/runtime, left the
  base and sibling intact, required explicit durable prepare/read-only preview
  and confirmation, rebuilt/activated the new committed environment, retained
  UUID/branch/key, lost dummy runtime-local Codex data, and preserved accepted
  image provenance after daemon restart. Fresh VM disk was removed and the
  integration lock released. This does not prove stale-token or mid-effect
  crash recovery; focused SQLite tests cover those code paths.

- **Next batch 8d2 — missing assigned P ref with intact local branch,
  acceptance before edits:** inspection must show the exact missing assigned
  ref, the one matched runtime's local branch tip and workspace status, and
  any reason restoration is unsafe. A short-lived explicit confirmation binds
  session UUID/project/branch, runtime generation, local tip and current
  authority. Under the Git/ref guard, restoration may create only the absent
  assigned ref at that inspected tip; it must never reset the workspace,
  force-update an existing ref, mint a new principal, or adopt a different
  runtime. Stale/ref-race/ambiguous-runtime cases fail closed. Focused tests,
  retained recovery review, and a selected serial VM with externally removed
  P ref plus intact local commit establish the supported shape. This batch
  does not claim other repair shapes or project deletion.
  The bare-present first slice now has durable preview/confirmation and an
  absent-ref zero-old Git CAS. Retained review found and fixed exact runtime
  generation checks across quiesced export, definitive stale sibling-ref
  rollback before the effect marker, and one-shot broker create on uncertain
  results. Focused SQLite/real-Git/adversarial WASM tests and five affected Go
  suites passed; the reviewer approved this **narrow slice** for a selected
  VM39. A local-only tip whose commit object is absent from P bare remains
  explicitly blocked as `p_object_missing`; the later scope decision below
  excludes automatic transfer from MVP.
  First serial VM39 `.cache/p-vm/integration-20260925T200056Z-1097683.log`
  completed workspace-loss inspection and returned a ref-repair preview, but
  the fixture's compound preview assertion failed at line 221. Runner exited
  1, powered down, and removed the fresh disk. The fixture now emits bounded
  preview metadata (never its confirmation token) on failure, so the next
  serial run can identify the exact mismatch before changing product behavior
  or weakening the assertion. Bash syntax and pinned ShellCheck pass.
  Diagnostic serial VM39 `.cache/p-vm/integration-20260925T200325Z-1147056.log`
  still failed the preview assertion, but the bounded preview now shows an
  early refusal: `assigned_ref_status=unknown`, empty local tip/runtime
  identity, and ineligible. The fixture now also reports `unsafe_reasons`
  and bounded stored loss-operation identity/result metadata on failure to
  distinguish a rejected snapshot from a worktree mismatch. No assertion was
  relaxed; runner exited 1 and drained.
  Third serial diagnostic VM39
  `.cache/p-vm/integration-20260925T200605Z-1193648.log` identified the
  exact mismatch: the completed `workspace.loss.inspect` operation ends in
  phase `inspected`, while ref-repair preview required phase `completed`.
  The loss result's schema, fingerprint, worktree count, native generation and
  image identity were present; preview reported `loss_snapshot_unavailable`.
  A narrow shared predicate now requires the actual terminal `inspected`
  phase plus exact kind/status/session/project in preview, confirm and replay;
  a focused phase regression and `TestRefRepair` pass. Retained recovery review
  precedes another serial VM. The diagnostic VM exited 1 and drained.
  Retained review found the same stale phase assumption in Store admission.
  That SQL now requires `inspected`; a real SQLite regression seeds the actual
  terminal phase, rejects a wrong `completed` phase, and then admits the
  guarded operation. Independent focused control/daemon ref-repair tests and
  retained review passed. The next serial VM39 will test the corrected public
  path; no VM pass is claimed yet.
  Serial VM39 `.cache/p-vm/integration-20260925T201037Z-1241198.log`
  exited 0, emitted `P_MISSING_REF_REPAIR_BARE_PRESENT_PASS`, selected product
  pass and smoke pass, then powered down/removed its fresh disk. It deleted
  only the assigned P bare ref while a sibling retained the commit, required
  a completed loss snapshot, rejected stale sibling-ref confirmation, and
  restored the exact tip with the runtime UUID/generation, dirty/ignored
  workspace, dummy Codex file, session key and sibling intact across restart.
  This validates only the bare-present object shape. A local-only tip absent
  from P bare remains `p_object_missing`; automatic transfer is excluded from
  MVP by the scope decision below.

- **8d2 scope decision — local-only ref objects outside MVP:** the user
  narrowed the repair promise after VM39. Supported MVP ref repair covers the
  bare-present commit object and keeps its reviewed create-only CAS. When the
  commit exists only in the runtime, preview reports `p_object_missing`, no
  confirmation token is issued, and runtime/local data stay untouched. There
  is no automatic object transfer in MVP. The earlier 8d2b transfer proposal
  was interrupted before edits, was not implemented or tested, and is no
  longer a delivery gate. The
  authoritative lifecycle contract and MVP snapshot now state this boundary;
  VM39 remains evidence only for the supported bare-present case.

- **Next independent batch 8d3 — missing or revoked session Git principal,
  acceptance before edits:** inspection must distinguish a missing key file,
  a revoked registration, and an identity mismatch without creating a key.
  A named explicit repair previews the old fingerprint, exact runtime
  generation and session assignment, and whether the runtime can be updated.
  Confirmation disables old authority first, creates one new UUID-scoped
  principal, installs only that credential into the exact stopped runtime,
  and durably records recovery phases before any native effect. Existing
  project/branch, Git refs, workspace, image and other sessions remain
  unchanged; stale runtime/assignment or uncertain installation blocks rather
  than widening authority. Focused SQLite/key/native regressions, retained
  recovery review and the smallest serial VM with a dummy key fault are
  required for support.
  Partial 8d3 checkpoint: durable Store admission/ref guard, atomic old
  principal revocation and single new registration, restartable phases, and
  exact stopped-runtime credential path/one-shot native POST are implemented.
  Focused real SQLite, host-key rotation, and adversarial native-file tests
  passed across control, daemon and runtimeincus. This was a partial
  implementation checkpoint; later review and VM evidence follow below.
  Retained review found and closed a historical-bootstrap authority gap:
  missing assigned P refs now block principal repair even if the durable
  project.create row remains after main was committed. The VM fixture tests
  committed-then-deleted main refusal, then restores that exact ref. Review
  also required a live Git probe proving that the retired key is denied.
  Post-fix five affected Go suites, focused independent review tests, Bash
  syntax, pinned ShellCheck, and diff checks passed.
  Serial VM40 `.cache/p-vm/integration-20260925T212611Z-1311421.log`
  exited 0 with `P_PRINCIPAL_REPAIR_PASS`, selected product pass, and smoke
  pass. It exercised a dummy missing host key, kept the exact stopped Incus
  runtime/guest credential and sibling/workspace/Codex dummy data, accepted
  one replacement key at the live P Git endpoint, denied the saved old key,
  and passed replay and daemon restart checks. The fresh disk was removed.
  This is fixture-backed principal-repair evidence; it does not validate real
  Codex authentication.

- **Next independent batch 8d4 — stale runtime locator, acceptance before
  edits:** a read-only preview must identify the recorded locator and exactly
  one Incus runtime carrying the same P session UUID/project assignment; no
  candidate, multiple candidates, conflicting labels, unknown native state,
  or a concurrent lifecycle operation must refuse confirmation. A short-lived
  confirmation binds the inspected native identity and recorded assignment;
  reinspection and Store CAS precede any relink. Relinking changes only the
  stale locator in the control record, never creates/adopts a runtime, changes
  Git authority or refs, or touches workspace data. A stale token must fail.
  Focused Store/native tests, retained recovery/authority review, and one
  serial selected VM with a controlled locator fault are required before
  support is claimed.
  Pre-edit model inspection found this acceptance inapplicable: the sessions
  table has no locator field, the Incus name is deterministically `p-<UUID>`,
  and the Incus project comes from trusted configuration. An external rename
  can make lookup fail, but there is no stored locator to relink or CAS.
  Implementing relink would require a new durable/native locator model solely
  for that fault. No 8d4 code, tests, fixture, or VM run was made. The
  lifecycle contract and MVP snapshot now explicitly exclude adoption of a
  renamed Incus instance; P reports the missing expected runtime. This is a
  scope boundary, not validation evidence.

- **Next independent batch 8d5 — unrecoverable session record,
  acceptance before edits:** a read-only preview may offer registry removal
  only when Incus authoritatively confirms the expected runtime absent and
  Git confirms the exact assigned P ref absent. It must show both losses,
  the session assignment and principal, and reject native unreachability,
  in-flight operations, competing identity, or reappearance. Explicit keyed
  confirmation binds those facts and disables the session principal before
  removing the row and local key through durable recovery phases. It must not
  delete any other P/origin ref, runtime, image, sibling, or external mount.
  Ref/runtime reappearance or uncertain native evidence blocks; no blind
  cleanup or broad adoption. Focused real SQLite and native tests, retained
  destructive/recovery review, and one serial selected VM using a controlled
  both-absent fault are required before support is claimed.
  Retained destructive/recovery review approved the guarded source and VM42
  fixture after direct SQLite negatives for canonical runtime name and active
  principal at final row removal. Post-hardening five affected Go suites,
  independent focused tests, Bash syntax, pinned ShellCheck, and diff checks
  passed. A first `./dev/test-vm --step 42-record-repair.sh` invocation named
  a nonexistent fixture and exited 2 before building or starting any VM.
  The correct serial VM42 run
  `.cache/p-vm/integration-20260925T214622Z-1378109.log` exited 0 with
  `P_UNRECOVERABLE_RECORD_PASS`, selected product pass, and smoke pass. It
  removed only a disposable target runtime and its assigned P ref as the
  controlled fault, refused stale confirmation, then completed UUID-scoped
  principal/key/endpoint and record cleanup while sibling/ref/image remained.
  The product repair itself issued no native or Git deletion. The fresh disk
  was removed. This is fixture-backed recovery evidence.

- **Next independent batch 8e1 — retained branch rename, acceptance before
  edits:** a retained source branch must be a real unassigned P ref at its
  inspected commit, with an absent validated destination. Public keyed rename
  reserves both names and pins that tip; a changed tip, new assignment, or
  destination appearance refuses without touching refs. The durable operation
  creates the destination at the expected tip, then deletes only the exact
  source with guarded recovery for uncertain effects. It never changes an
  origin ref or any session/runtime/credential, and preserves sibling refs.
  Focused real-bare/SQLite tests, retained authorization/recovery review, and
  one serial local-SSH-origin VM step are required before support is claimed.
  Four affected Go suites, independent focused Store/real-Git tests, Bash
  syntax, pinned ShellCheck, diff check and retained authorization/recovery
  review passed. Serial VM43
  `.cache/p-vm/integration-20260925T220033Z-1442976.log` exited 0 with
  `P_RETAINED_RENAME_PASS`, selected product pass and smoke pass. It used a
  local SSH origin, refused stale/assigned/occupied source or destination
  cases, renamed only the exact retained P ref, preserved sibling refs,
  session runtime/key/dirty workspace and separate origin refs, and checked
  keyed replay after restart. The fresh disk was removed. This is local-origin
  fixture evidence, not external-origin availability evidence.

- **Next independent batch 8e2 — retained branch deletion,
  acceptance before edits:** read-only Git-only loss preview must name an
  unassigned retained P ref and exact tip, commits losing the last P ref,
  fresh local-SSH-origin preservation evidence or explicit unknown, and any
  refusal. It must not require a runtime loss inspection. Confirmation is
  short-lived, keyed and bound to the complete reviewed loss facts; changed
  tip, assignment, project, or origin evidence makes it stale. A durable guard
  precedes an expected-old-tip deletion of only that P ref; uncertain effects
  reconcile the exact ref and never delete an origin or sibling. Focused
  real-bare/SQLite tests, retained destructive/recovery review, and one serial
  local-origin VM step are required before support is claimed.
  Retained review approved source and VM44 after four affected Go suites,
  independent focused real-bare/Store tests, Bash syntax, pinned ShellCheck,
  and diff checks passed. First serial VM44
  `.cache/p-vm/integration-20260925T221555Z-1503699.log` reached the
  read-only retained-delete preview but failed the fixture's detailed JSON
  assertion at line 294 before any confirmation or deletion. It emitted
  `P_RETAINED_DELETE_FAIL`, exited 1, and removed its fresh VM disk. The log
  does not yet identify which preview field differed; bounded non-secret
  diagnostics are being added without relaxing the assertion. No VM pass is
  claimed for 8e2.
  Diagnostic serial VM44
  `.cache/p-vm/integration-20260925T221828Z-1553426.log` again exited 1
  before confirmation, now at preview assertion line 313. Its bounded output
  identified the precise difference: exact tip, last-P-ref loss and sibling
  refs matched; the local SSH origin observation was explicitly `unknown`
  with `origin_object_or_comparison_unavailable` and unresolved
  `refs/heads/divergent`, because that advertised object's comparison was
  unavailable. The contract permits this reported unknown. The fixture will
  assert that exact reason and unresolved ref while retaining all other
  strict checks; no product behavior is changed. The fresh disk was removed.
  Third serial VM44 `.cache/p-vm/integration-20260925T222034Z-1599926.log`
  passed the corrected preview assertion and reached the stale-origin
  confirmation probe, but failed its expected RPC error-envelope assertion
  in the shared `expect_error` helper (line 104). The actual envelope was not
  logged, so a bounded method/kind/code/message diagnostic is being added
  before another correction. It exited 1 and removed the fresh disk. No
  deletion pass is claimed.
  Fourth serial VM44 `.cache/p-vm/integration-20260925T222255Z-1646611.log`
  reported the actual stale-origin envelope: `unavailable/-32004` with
  `lifecycle authority is unavailable`, while the reviewed ref remained
  intact. The changed loss/origin digest is detected, but its private
  `errDeleteReviewChanged` is not classified as `ErrConflict` for public RPC,
  so the caller receives the wrong stale-confirmation result. A narrow error
  classification fix and focused public regression are in progress; the
  fixture assertion remains unchanged. This VM exited 1 and removed its disk.
  The focused fix classified changed full loss/origin review as public
  `ErrConflict` while retaining its private sentinel. A daemon regression
  changed the origin observation digest and verified the public
  `busy/-32003` envelope; daemon/control suites, VM fixture static checks,
  and direct recheck of that finding passed. Serial VM44
  `.cache/p-vm/integration-20260925T222635Z-1694151.log` then exited 0
  with `P_RETAINED_DELETE_PASS`, selected product pass and smoke pass. It
  proved assigned-branch refusal, explicit origin unknown, stale origin/tip
  refusals, exact retained P ref deletion, preserved sibling/session/runtime/
  dummy key/dirty workspace/origin refs, and keyed replay after restart. The
  fresh disk was removed. This is local-SSH-origin fixture evidence; it does
  not establish external-origin availability.

- **Next independent batch 8f1 — Try again with changes for a safely
  replaceable failed Create, acceptance before edits:** public preview must
  identify one blocked creation, its immutable old request/UUID/branch/source/
  policy, verified provisional refs/runtime/key/image resources, and the new
  requested source/policy/branch choice. A changed resource, local workspace
  data, uncertain native effect, or foreign assignment is ineligible until a
  later integrated loss-review path exists. Explicit keyed replacement must
  durably supersede the old request, clean only verified provisional resources
  while preserving pre-existing refs, then admit one new Create with new UUID,
  operation ID and key. A crash or repeated key must not leave two active
  assignments/principals/runtimes or restart the old request. Focused Store/
  native tests, retained destructive/recovery review, and one serial VM with a
  failed-create fixture are required before this safe subset is supported.
  Pre-edit architecture check narrowed the first slice: `source-ready` for a
  new branch may have already issued a zero-old Git CAS without a durable
  issued marker, and later phases may own builder/native/credential effects.
  This batch therefore admits only a blocked existing-branch Create still in
  `source-ready` after positive no-effect proofs for ref, native runtime,
  key, and builder. New-branch and later/uncertain phases remain blocked for a
  separate reconciliation design. VM45 must produce a real blocked
  existing-branch Create; a synthetic Store state is insufficient evidence.
  The delegated implementation stream then added a partial RPC/Store handoff
  for this narrow slice and direct SQLite negatives, but its model call ended
  with a 401 service authentication error before VM45 fixture, final tests,
  or review. No VM45 run or support claim exists. The partial patch is
  preserved on the feature branch working tree; coordination continues in
  the main thread without retrying or switching agents around that error.
  An unprivileged full Go test attempt for control/daemon/runtimeincus could
  not complete: socket tests failed at `connect`/`setsockopt: operation not
  permitted` under the workspace sandbox. This is environment denial, not a
  product pass or source regression. The focused SQLite replacement test did
  pass. A separate requested Git progress commit was not executed because
  automatic approval review itself returned 401 Unauthorized; no bypass was
  attempted. VM45 remains unrun.
  Resumption on 2026-09-26: the user attributed the interruption to the
  OpenAI outage and instructed work to continue. No prior agent or VM remained
  active. The existing patch is reused; one Sol/high implementation stream is
  finishing the missing tests/docs/VM45 fixture. Root is retaining the full
  historical evidence and committing progress updates independently from the
  still-unvalidated source batch.
