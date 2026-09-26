# P — implementation plan for a testable MVP

> Proposed delivery plan, 2026-09-23. This document sequences implementation;
> it does not change product scope or define new contracts. [Project guidance](../PROJECT.md),
> the subject design documents, and [development validations](development-validations.md)
> remain authoritative. [Missing pieces](missing-pieces.md) remains the work tracker.

## Target

Deliver a personal-use build that lets the developer create two isolated
development sessions, run Codex, switch between them, see unattended attention,
retain work across detach and stop/start, and deliberately publish committed
work. Reach a smaller real terminal-and-Git checkpoint first, so feedback does
not wait for the entire MVP.

The repository currently contains design and a fixture-backed TUI prototype,
not a production daemon or runtime integration. Reuse the
[reviewed browser interactions](../.prototype/tui-options/DECISIONS.md), with
production behavior supplied by the daemon.

Assume one Linux host, one architecture, one storage driver, and a locally
provisioned confined Incus project for the first run. An ordinary SSH login to
that host can run the same local client. Exact versions and host configuration
must be recorded before support is claimed. The planning shell is Linux
x86_64 with Nix on PATH; Incus and Go were not found on PATH. This is not proof
that the intended test host lacks them.

## Delivery checkpoints

| Checkpoint | What the developer can actually test | Prerequisites |
|---|---|---|
| A: real session | Create a blank project, enter its bootstrap session, edit/commit/push, detach/reattach, stop/start, and resume after daemon restart | Steps 0–3 |
| B: personal development build | Work in two sessions from a real committed repository, use its Nix devShell and Codex, see attention in the browser, publish explicitly, and discard safely | Steps 4–5, plus checkpoint A |
| C: documented MVP | Exercise the complete lifecycle, policy, retained-branch, plugin, installation, and recovery surface | Step 6, plus checkpoint B |

A and B are explicitly incomplete delivery checkpoints, not a redefinition of
the documented MVP. Every exposed operation must already meet its applicable
authority and evidence gates. Unsupported operations should be unavailable
with a clear explanation.

## 0. Settle the minimum plugin contract and verify the host

The [technology design](technology-stack.md) explicitly blocks plugin linkage
until the public plugin architecture is reconciled. Resolve this before
building a backend around internal Go interfaces.

- Assign a detailed plugin-design owner through the project authority map.
  Specify packaging, discovery, compatibility, transport, process/isolation
  model, trusted activation/grants, lifecycle participation, failure handling,
  and install/update/removal behavior.
- Evaluate versioned local executable packages with typed process RPC for
  host-side capabilities and packaged systemd/runtime assets for session-side
  capabilities. This is a design candidate, not a selected architecture.
  Process separation alone is not capability enforcement: the design must
  explain how undeclared host access is prevented.
- Keep identity, policy, grants, confirmations, durable workflow intent, and
  recovery in P core. Define the minimum public contracts needed by Incus,
  tmux, Git, Nix, the file-log handler, and the Codex adapter. Ordinary setup
  selects their bundled implementations automatically.
- Specify a local authoring/validation/conformance workflow that does not
  rebuild core. Do not require a marketplace or a separate agent-authored
  plugin demonstration before MVP.
- Inventory the actual test host. Document machine-owner provisioning of the
  confined Incus project, storage, network resources, and allowed endpoint
  paths. P verifies that setup; it does not initialize or administer Incus.
- Run the confinement and base-image feasibility experiments early, including
  private Nix with the required sandbox posture and systemd/tmux supervision.
  Record any incompatible host setting before promising a runnable milestone.

**Exit:** a reconciled plugin design that can guide implementation, a pinned
host/image recipe, and evidence for confinement on the chosen host. Fake-backed
core work may proceed while runtime evidence is pending; real runtime support
may not bypass this gate.

## 1. Build the smallest durable control plane

- Scaffold the production Go module and `p` daemon/client entrypoints, separate
  from the disposable prototype. Establish boundaries for state, policy,
  operations, RPC, plugin contracts/activation, and capability implementations.
- Implement SQLite migrations and single-writer state, UUID/branch uniqueness,
  immutable policy snapshots, idempotency, and cross-authority operation intent.
  Keep Git, Incus, and Nix as their respective authorities.
- Implement local Unix NDJSON-RPC and `p api` as the first debugging client,
  with typed errors, cancellation, bounded diagnostics, and subscriptions.
- Implement trusted configuration, automatic bundled-plugin composition, and
  the redacted NDJSON event handler. Handler failure is diagnostic, not a
  lifecycle rollback.
- Use fake implementations to test duplicate requests, assignment races,
  interrupted creation, plugin refusal/crash, and invalid activation/grants.
  Exercise the selected public contracts rather than adding a second linkage
  path for bundled implementations.

**Exit:** restart the daemon without losing identity or operation intent;
repeating a request cannot allocate a second session or widen its authority.

## 2. Make blank-project creation reach a real runtime

- Implement the Git plugin using real bare repositories and Git service
  processes, session-scoped SSH principals, the read-only host principal,
  branch guards, and unconditional fast-forward-only session writes.
- Implement explicit blank-project creation with exactly one unborn `main`
  bootstrap session. Its first push creates the assigned ref; later sessions
  require committed source.
- Build the pinned base image with the private Nix daemon/store, Git/SSH,
  shell, tmux, runtime helpers, and the specified systemd units. The first
  checkpoint uses the valid base-only path; it does not silently ignore a
  repository's failing devShell.
- Implement the confined Incus plugin, deterministic identity/labels, resource
  limits, narrow Git/RPC endpoints, private workspace/home/root, and `none`
  networking. No general network or external filesystem grants are needed
  for this checkpoint.
- Connect the journaled Create phases through workspace assembly and
  systemd readiness. Include blocked creation diagnostics, exact Retry,
  cancellation rules, and safe cleanup of verified provisional resources.
  Retry retains the original operation, UUID, source, and policy.

**Exit:** create a blank project through RPC, commit inside its real workspace,
and push only to its assigned P branch. Attempts to push another branch,
force-update recorded history, or write using the host key fail. Crash/retry
at each implemented Create boundary produces no duplicate runtime or ref loss.

## 3. Deliver checkpoint A: enter, leave, and resume real work

- Implement Start, Stop, reconciled session conditions, and the trusted attach
  helper with structured argv, pending tokens, confirmed connection-owned
  leases, PTY resize/input, and terminal restoration on exit/failure.
- Preserve tmux across detach, client loss, and session switching. Stop ends
  processes while retaining files; Start creates a fresh interactive host.
  Ordinary Stop must not terminate another client's attached terminal.
- Implement restart reconciliation and endpoint recreation. Daemon restart
  tears down temporary attachments; it must not kill the persistent host or
  fabricate attachment presence.
- Connect a small production browser to RPC: project/session list, selection,
  creation, progress/diagnostics, enter, detach, stop, and start. Preserve the
  reviewed layout and navigation where applicable. Validate real prefix-key
  behavior with tmux before retaining the simulated terminal controls.
- Show the four authoritative status facts. Before a validated agent adapter
  exists, an absent unattended report stays empty; terminal output is not a
  substitute signal. Include retained-branch fetching through the host key
  so committed work is accessible outside P.

**Developer acceptance run:**

1. Create a blank local project and enter its bootstrap `main` session.
2. Create a file, commit, and push to P. Fetch that branch into an ordinary
   checkout using the read-only host credential.
3. Leave an uncommitted edit and a long-running shell command. Detach and
   reattach: both the edit and process remain.
4. Detach, Stop, and Start: files and Git state remain; the old process does not.
5. Kill the client, then separately restart the daemon while attached. Reenter:
   the persistent host survives and attachment counts converge correctly.
6. Exercise a failed host startup and exact Retry; inspect the retained
   diagnostic and verify there is still only one assigned session/runtime.

**Gate:** applicable checks from validations 1–3, 5–7, and 10. Ship a build,
setup instructions, the acceptance transcript, and explicit limitations.
This checkpoint need not expose destructive session deletion yet.

## 4. Support real repositories and reproducible environments

- Add explicit SSH-origin project creation, contact-before-association,
  refresh, empty-origin bootstrap, and local-only behavior. Origin credentials
  stay on the host.
- Add both session creation paths: assign an existing unassigned branch, or
  create a new branch from committed source. Keep source selection out of the
  existing-branch flow. Implement superseding creation with changes for failed
  requests, preserving pre-existing refs and reviewing unexpected work.
- Implement the Nix plugin's restricted builder, immutable committed source,
  lock policy, activation compatibility gate, scrubbing, verified publication,
  project-scoped cache, private session stores, and explicit cache collection.
- Validate `public-egress` for builder/session use before enabling it. Probe
  IPv4/IPv6, host/private/link-local/metadata/sibling destinations, DNS rebinding,
  and redirects. A failed gate leaves that capability unavailable.
- Add explicit origin publication with a fresh observation, one destination,
  fast-forward enforcement, and truthful unknown-outcome handling.
- Add non-activating workspace inspection and safe Discard, including fresh
  loss previews, stale-confirmation rejection, credential revocation,
  forward recovery, and retained-branch continuation.

**Exit:** launch two branches from the same committed environment; verify
private workspace/home/Nix changes, cache reuse, stop/start persistence,
explicit publication, and Discard followed by continuation under a new UUID.
Run the [Nix workflow validation](nix-project-workflow-validation.md) first on a
small committed-flake fixture and then this project's latest fetched
`origin/main`, as selected by the user for this implementation run. Record the
exact resolved commit in the [progress record](implementation-progress.md#real-repository-fixture).
A repository without a root flake exercises base-only selection; the separate
flake fixture must still prove devShell realization. Record unsupported inputs
without weakening isolation.

## 5. Deliver checkpoint B: concurrent Codex work

- Package the Codex adapter through the selected plugin contract. Pin a tested
  Codex version and validate actual hooks before claiming semantic status.
  Missing hooks remain unknown; do not infer activity from process or terminal
  contents.
- Let the developer authenticate inside each session's private home. Verify
  credentials survive Stop/Start, remain isolated, and disappear with Discard.
- Implement the latest-unattended reducer: replace on report, clear only on
  first confirmed attachment, suppress while attached, and begin empty after
  the final detach. Record real traces for attention, activity, completion,
  failure, missing hooks, and attach/detach races.
- Complete browser flows for origin projects, branch/source selection, policy
  review, retry/replacement, attention, publication, Discard, and retained
  branches. Keep business logic and confirmations in the daemon.
- Present the single MVP unattended signal. Do not ship fixture-backed agent
  inventories, conversation previews, service controls, or journals as real
  capabilities; those integrations remain outside this implementation plan.

**Developer acceptance run:** create two sessions on a real repository, run a
small Codex change in each, detach, observe a real unattended report, switch
back, verify clearing, test/build using the devShell, commit/push to P, and
explicitly publish one branch. Stop/restart one session, then safely discard
it and continue from its retained branch. Run this through ordinary work over
several days and record friction, failures, and launch/build/disk measurements.

**Gate:** all evidence for the exposed features, including validations 4 and
9. If hooks or public egress fail, checkpoint A remains useful, but checkpoint
B is not declared complete.

## 6. Close the documented MVP

- Complete transactional session rename, Delete, Repair, Abandon, orphan
  recognition, origin change/removal, and the full retained-branch operations.
- Complete aggregate project deletion with attachment termination, a minimal
  durable tombstone, idempotent ensure-absent retry, and unreachable-resource
  handling.
- Complete typed filesystem grants, policy diffs/current/outdated/invalid
  behavior, and guided recreation. Safety validation and immutable snapshots
  are required from the first exposed operation, not postponed to this step.
- Complete plugin install/update/removal and compatibility behavior, public
  capability documentation, and the authoring/test workflow. Prove all six
  bundled capabilities use those contracts without rebuilding core for plugin
  changes. A separate agent-authored plugin remains a later demonstration.
- Complete the authorized CLI-first lifecycle, NixOS/Incus packaging and
  installation, service definitions, dependency/license checks and bounded
  diagnostics. Production TUI, backup/restore and software upgrade/rollback
  are outside the current MVP delivery gates.
- Preserve validated integrated failed-creation replacement paths. Complex
  refusal requires a documented and validated cleanup-then-new-Create path;
  uncertain resources stay intact when safe cleanup cannot proceed.
- Map every MVP acceptance criterion to an automated test, recorded integration
  result, or explicit support gate. Run crash injection for each newly exposed
  mutation and the full tested-host conformance suite. Update the status and
  work tracker from evidence.

**Exit:** the documented MVP works on the recorded host configuration. Broaden
architecture/storage/version claims only after their validation passes.

## Working order and scope controls

Start with step 0 and then implement steps 1–3 as the first delivery batch.
After testing checkpoint A, implement steps 4–5 as the personal-use batch;
finish with step 6. Do not spend the first batch completing every API or
polishing fixture-only pages before connecting a real session.

Each implementation change should include the checks for the behavior it
exposes. Use deterministic tests for orchestration and real Git/Incus/Nix/tmux
tests for authority boundaries. Record exact versions, host configuration,
commands, raw results, and constraints beside the dependent tests. No support
claim follows from a fake backend or a successful UI screenshot.

Keep Bifrost, remote P transport/native non-Linux clients, alternative runtimes,
project-service orchestration, multi-agent inventory, conversation previews,
marketplaces, and generalized plugin breadth outside these delivery batches.
The first runnable build still requires real isolation, scoped Git authority,
truthful status, durable recovery, and the agreed plugin composition.
