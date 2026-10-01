# P — TUI experience and developer workflow plan

> Authorized delivery plan, 2026-09-30; initial provisioned-sample delivery
> completed 2026-10-01. The real devShell extension remains separately pending.
> This document preserves the requested work sequence; passing results and
> exact evidence are recorded in [implementation progress](implementation-progress.md#final-delivery-checkpoint--2026-10-01).
> [Project guidance](../PROJECT.md), the subject contracts and the
> [TUI change and validation workflow](development-validations.md#tui-change-and-validation-workflow)
> remain authoritative.

## Outcome

Bring the supported production TUI closer to the reviewed prototype, and give
developers a simple working application with repeatable development journeys.
Prove those journeys through CLI/API integration first, then through the TUI.
For the initial experience baseline and every UI change, an LLM must actually
navigate rendered screens and review the experience before driver acceptance.

The two workstreams can develop in parallel. One subagent owns interface
alignment and live navigation; another owns the sample and CLI/API workflows.
The primary agent coordinates shared interfaces, evidence and serial native
runs. After implementation, use a reviewer with fresh context to assess the
changes and acceptance evidence. Parallel code work must not launch overlapping
VMs or mutate another agent's lab/session state.

## 1. Establish live observation and the experience baseline

Build the current prototype and installed product. Record the exact revisions,
runtime image, lab state and terminal dimensions. Use an isolated lab state
directory; preserve the developer's existing persistent disks. Check daemon
and repository-seed readiness separately before navigation. A reused lab can
contain older source and captured runtime images, so record those identities.

Provide a persistent terminal observation helper with start, observe, send,
resize and close operations. Reuse the query-response and scrolling handling
in [the existing PTY driver](../tests/integration/tui-drive.py), while allowing
the LLM to choose the next action after reading each screen. Preserve terminal
cell positions, colors, selection and cursor in rendered captures. Service
terminal queries continuously while the LLM inspects captures or waits for
native observations. Verify the observer against a real terminal so a capture
bug is not mistaken for a product defect.

The helper supplies terminal input/output; it must not execute the whole
scenario automatically. The LLM navigates the prototype and actual product at
matched sizes, starting with 120×35 and 80×24 and adding 48×16 for compact
layout. Capture meaningful transitions, including startup and attachment,
rather than only completed screens. Follow the authoritative review workflow
for loading, errors, resizing and evidence requirements.

Review project selection and search, blank-project creation, both branch
creation paths, policy/confirmation, progress/Back, entry/detach/re-entry,
Stop/Start, services/journals and reviewed removal. Include an empty inventory,
long names and enough real sessions or branches to exercise scrolling within
the configured capacity. Simulated stress data can supplement layout review;
they do not establish native workflow completion.

**Deliverable:** an initial LLM navigation record and a prioritized mismatch
list with before screens, expected prototype behavior and reproduction steps.
Separate supported-interface defects from prototype capabilities beyond the
[implemented scope](user-guide.md#use-the-terminal-browser), such as agent
instance inventories and in-terminal management popups.

## 2. Repair the supported interface

Code inspection suggests the following review priorities. These are candidates
for the initial live review, not confirmed live defects:

| Area | Candidate mismatch and intended review |
|---|---|
| Session rows | Whole-row truncation can hide runtime/attention after long project and branch names. Reserve space for status and compare green/amber/red cues with the prototype. |
| Selected details | Compare framing, hierarchy and readable identity/report details across wide, stacked and compact layouts. |
| Project selector | Compare aligned counts, bounded names, search placement and stable row geometry. |
| Creation | Complete existing-branch and new-branch paths; compare selector sizing, source/name/policy order, literal text input and Back/cancel. |
| Progress and notices | Keep command placement stable and diagnostics accessible during loading/failure; leaving progress must not later force entry. |
| Terminal handoff | Inspect intermediate frames and repeated attachment/detach, including the console exposure reported by the developer. |
| Services and removal | Check selection, actionable feedback, journal navigation, complete loss information and default-No behavior. |

Start with rendering and navigation in `internal/tui/view.go`, `model.go`
and `workflows.go`, with regressions in `navigation_test.go`; use the
prototype's selected browser and decision record as references. Preserve
daemon-owned lifecycle, captured
identity, confirmation and freshness checks. Improve the presentation of
unavailable actions without inventing capabilities.

Implement small coherent changes with focused component regressions. **Every
UI change must receive at least one adaptive live LLM navigation review of its
changed behavior.** Repeat the affected path after corrections or any further
UI edits; an earlier review cannot accept later unreviewed changes. First
establish the experience against the prototype, then preserve that experience
through subsequent reviews and integration checks.

**Exit:** supported interactions match the reviewed direction, observed
mismatches are resolved, component checks pass, and changed screens and
transitions have passing live LLM review evidence.

## 3. Add a small application with three real services

Create `examples/notes/`: a Python notes application backed by PostgreSQL,
plus a Python worker that processes queued notes and records their word counts.
The HTTP layer can use the standard library, with Psycopg for database access.
Use one source tree for the manual walkthrough and automated scenarios.

| Session-user unit | Responsibility | Observable result |
|---|---|---|
| `p-project-notes-db.service` | Run a private PostgreSQL cluster as the session user. | Real SQL reads/writes, retained rows, startup/shutdown diagnostics. |
| `p-project-notes-web.service` | Serve `GET /health`, `GET /notes` and `POST /notes` on `127.0.0.1:8000`. | Health includes database readiness; creating a note commits its row and queued job together. |
| `p-project-notes-worker.service` | Process pending jobs from the same database. | Notes transition from pending to processed with a stored word count and identifiable job logs. |

Each session owns its database under `$HOME/.local/state/p-notes/`, with a
data-directory override for tests. Use a private session-local Unix socket
directory for PostgreSQL and disable its TCP listener. This follows
[PostgreSQL's connection settings](https://www.postgresql.org/docs/current/runtime-config-connection.html).
Use local session-user database authentication so no external credentials are
needed. Keep runtime socket paths short and recreate them during startup.
Application HTTP probes run inside the session. No database, app or worker
endpoint is published on the host.

Include schema initialization/migrations, real SQL and HTTP tests, worker
tests, a small command-line HTTP client, and an idempotent user-service
installer. Initialize the cluster only when absent; repeated setup must
preserve existing rows, and uncertain/incompatible state must produce a clear
error. Reject an existing cluster with a mismatched PostgreSQL major version
without reinitializing it. Test graceful database shutdown/restart followed by
successful SQL access and retained rows/jobs. Keep database files, generated
jobs and runtime configuration outside
tracked source. The README must show installation, database startup/readiness,
schema setup, app/worker startup, tests and expected results.

The app and worker depend on the database unit, with explicit start ordering
and bounded readiness checks. Ordering alone does not establish usable SQL
connections; consult the [systemd dependency semantics](https://github.com/systemd/systemd/blob/main/man/systemd.unit.xml)
and verify the chosen unit behavior natively. Document whether database stop
or failure stops dependents, and explicitly start them again during recovery
where required. A stopped worker leaves durable pending jobs while the web
application continues to accept notes. Resume processing without duplicate
results after worker restart; do not claim exactly-once execution.

The installer resolves the actual PostgreSQL and Python executables, reloads
the user manager and leaves enablement explicit. All three units use the
existing `p-project-*.service` boundary. Keep diagnostic output in each unit's
journal and make controlled failure states reproducible for screen review.

### Provision the sample dependencies before workflow acceptance

The existing base image includes Python3, but not this database stack. Build a
dedicated pinned sample runtime image containing PostgreSQL, a Python environment
with Psycopg, and its required client-library closure through ordinary Nix image
provisioning. [Psycopg's installation guide](https://www.psycopg.org/psycopg3/docs/basic/install.html)
describes its libpq requirements. Use that configured image for the sample's
bootstrap and later sessions; record its fingerprint and tool versions. This
dependency preparation is now required for the initial three-service sample,
not an optional database test after a Python-only delivery.

The image contains tools, not an initialized cluster or another session's
application data. Private clusters, schema and jobs are created by the sample's
session setup. Use the provisioned Python-with-Psycopg executable in both units;
a Python import plus an actual SQL query must pass before accepting setup.

The builder and sessions receive dependencies through their private images,
without host store mounts, runtime downloads or credentials. Prove database
initialization, Python imports and all three units in a fresh confined session
before running developer journeys. Preserve the default production base image
unless a separately reviewed change calls for adding these tools generally.

Developers can copy the sample into a blank bootstrap workspace or import its
committed source through a test-owned SSH origin. Tests commit only the sample
subtree, preserving its tracked files and executable modes. A new session gets
source and provisioned tools, then initializes its own empty database; Git
branch creation does not copy another session's database or worker state.

Treat committed default-devShell selection as a separate extension. The
current [environment builder is offline](user-guide.md#what-to-expect-from-the-development-environment).
A conventional locked nixpkgs input alone does not make dependencies available.
Prove the real sample flake selecting Python/Psycopg and PostgreSQL through the
production adapter in a fresh confined builder, using explicitly provisioned
input sources and closures, without network or lock mutation. Listing store
paths alone does not establish offline input resolution. Verify actual tools
used by terminal commands and all three units, exact source capture, image
miss/hit and independent private roots. Respect the reserved helper slot and
release the bootstrap runtime when necessary for builder admission.

Synthetic devShell fixtures cannot satisfy that real application environment
milestone. If input resolution remains unsupported, record this extension as
pending; the provisioned three-service sample can still be accepted separately.
Public fetching for builders requires separately scoped design and validation.
An invalid declared default must fail visibly rather than silently use the base.

**Exit:** real database-backed requests and worker effects pass in a confined
sample session. All three installed units are discoverable and controllable,
their dependencies/readiness are verified, and tests cover persistence and
recovery. The README distinguishes provisioned sample tools from default
devShell realization and describes the tested setup accurately.

## 4. Establish CLI/API journeys first

Add native scenarios under `tests/integration/steps/`, using `p api`, real Git
and actual attachment/command execution. Run P-owner commands from the lab's
owner shell and workspace/application commands as the session user. Native
inspection may independently verify outcomes; it must not substitute for the
public operation being tested. Use fresh test-owned projects, unique action
keys, bounded operation waits and explicit expected outcomes.

All rows below are required for the provisioned sample delivery except **Real
devShell extension**, which is a separately gated follow-on. Run that row
through CLI/API and live TUI review once its dependency prerequisite passes.

| Developer journey | Required observable outcome | Later TUI coverage |
|---|---|---|
| Start blank | One unborn-main bootstrap; first sample commit/push establishes main; a second stream is refused before committed source exists. | Create project, enter, commit/push in its terminal. |
| Import existing work | Test-owned SSH origin is contacted; nonempty import requires explicit session/source selection. | Create with origin and complete branch/source selection. |
| Start another stream | Create a new feature branch and assign an existing retained branch in separate scenarios; verify exact committed source, one assignment per branch and exclusion of dirty/unpushed work. | Complete both creation paths, including Back and cancel. |
| Set up a new session | Install its three units from committed sample source, initialize its own empty database and apply its schema. No home, rows or queued jobs are copied from the source session. | Enter the new session, run setup, then inspect its three installed units. |
| Real devShell extension | After input-resolution provisioning passes, create from the exact committed flake/lock, verify actual Python/Psycopg/PostgreSQL tools, image miss/hit and private writable roots. | Inspect the selected environment and creation feedback, then use its tools in the terminal. |
| Edit, test and run | Change sample behavior, demonstrate a failing test, fix it, pass SQL/HTTP tests and verify stored notes and processed results. | Execute the same commands through the attached terminal. |
| Leave and return | Detach removes the temporary lease while tmux/database/web/worker continue; re-entry reaches the same processes, rows and queued jobs. | Observe real entry, detach and repeated re-entry transitions. |
| Manage three services | All installed units are discoverable; individual start/restart/stop has correct native PID/state, SQL/HTTP/job effects and unit-specific journal evidence. Exercise mixed active/stopped/failed states. | Navigate between units, retain selected identity across refresh, and open the matching journals. |
| Pause and resume the worker | With worker stopped, web accepts a note and stores a pending job; worker start processes it. An interrupted worker transaction recovers without stranded jobs or duplicate stored results. | Stop/start/restart the worker, inspect its logs and verify pending-to-processed results in the terminal. |
| Recover database availability | Stop/fail the database, observe actual dependent states and clear unavailability; recover through database readiness/schema checks, web start and worker start. Preserve accepted rows/jobs; refused writes must not report success. | Review service state and diagnostics, restart the selected units explicitly, and verify recovered app behavior. |
| Stop and resume | Identity, pushed commits, a local-only commit, dirty files, unit files, SQL rows and jobs survive; old processes end. Explicitly start database, web and worker again unless enablement was separately tested. | Stop confirmation, Start/Enter, verify stored data and resume pending jobs. |
| Work in two sessions | Distinct branches, private clusters, rows and job queues; both use port 8000 and the same local socket path independently. Apply a small schema/code change in A and verify B is unchanged. | Create/switch branch sessions; scope/search preserve selection; inspect each session's independent three-service inventory. |
| Restart the daemon | Running tmux/database/web/worker PIDs, runtime/workspace identity, Git refs and session principals remain unchanged; stopped sessions remain stopped. | Reconnect the browser and enter retained work; owner restart remains an external setup action. |
| Relaunch the persistent lab | Clean shutdown/relaunch with the same dedicated lab disk retains identities, source, dirty files, units, database and pending jobs. Explicit service startup restores SQL access and resumes queued work; processes do not survive VM shutdown. | Open the browser after readiness checks, start the services and return to retained work. |
| Retain and publish work | Verify pushed OIDs in P; publish explicitly to a test-owned SSH origin, with no force update or automatic external publication. | Commit/push in terminal; publication uses the documented owner API. |
| Rename a stream | Rename the assigned branch while preserving UUID, workspace and private data; verify P/local ref and upstream consistency. | Rename and verify the selected stream and visible identity. |
| Discard, resume and delete | Discard removes cluster/rows/jobs and retains pushed source/schema branch/OID. Existing-branch recreation has a new UUID and fresh database with no old notes/jobs. Delete removes its branch; project deletion removes the reviewed aggregate without affecting a sibling database/project. | Review Discard/session Delete; whole-project deletion remains an owner API flow. |
| Recover a failure | Invalid devShell or unavailable test origin yields bounded diagnostics; Retry preserves exact intent, corrected creation captures new source where required. | Inspect actual progress/error/operations and complete a supported recovery path. |

Run application HTTP probes inside each session. No host port publication,
LAN connectivity, host checkout/store mounting or real authentication is needed.
Use dummy private-home markers to establish file persistence and cleanup.
Verify default-No and cancellation cause no mutation. Detach/stop as required,
then obtain fresh loss evidence for destructive review and confirmation.
Create destructive previews near confirmation so expiry during a long LLM
review does not turn into an unexplained navigation failure.
Review destructive losses and verify actual registry/ref/runtime absence;
preserve sibling resources. Reuse existing origin and lifecycle fixtures where
they already prove the required contracts.

**Exit:** each required provisioned-sample journey has passing CLI/API evidence
and a developer-readable walkthrough of actions and expected results. Session
Stop/Start, daemon restart and persistent lab relaunch have separate evidence.
Existing VM gates remain
in force; the sample suite adds developer continuity across them.

## 5. Review the same journeys through the TUI, then automate integration

When the required provisioned-sample CLI/API journeys pass and interface
alignment is ready, the TUI owner runs the same journeys by choosing actions
from rendered screens.
This is a new live LLM review using the real sample, even if earlier layout
changes already received their required reviews. Label unavoidable API steps
explicitly; do not claim that the TUI exposes those operations. Fix and review
any further UI changes before encoding their navigation in the driver.

Then update TUI integration coverage for the major outcomes in the matrix:
completed project/branch creation, native SQL/HTTP/worker execution, Git OIDs,
attachment teardown, all three service states/journals, database/job persistence
and cleanup. Retain screen recognition needed for synchronization. Put detailed
Back, selection,
filtering, literal input and stale-response invariants in component tests;
remove redundant driver navigation probes only after those tests cover them.
Reuse observer protocol regressions when extracting shared capture code.

**Exit:** required provisioned-sample journeys have separately identified
CLI/API results, live LLM TUI experience reviews and major TUI integration
results. The driver
preserves the reviewed workflow without becoming its experience specification.

## Delivery, evidence and completion

Sequence the work as baseline observation → interface corrections alongside
sample/CLI work → sample journeys through live LLM navigation → driver coverage
→ final review and documentation. VM and lab operations stay serial under the
existing checkout lock. An active developer lab is preserved; inability to run
a required selection leaves that evidence pending.

Keep the authoritative procedure in development validations. Add the sample
walkthrough to the user guide and link it from the lab guide. Record actual
build/commit identities, setup, actions, screen captures, comparison findings,
test names/logs and unresolved limitations in implementation progress. Evidence
must distinguish code-inspection candidates, live observations and automated
effects. This plan's matrix becomes the index to those acceptance records.

Run focused component and VM checks for each changed boundary; once the final
implementation and relevant checks pass, run `just test` for the delivery
checkpoint. Record fresh-context review findings and their resolution. The
initial delivery is complete when the reviewed interface and required
provisioned-sample journeys meet their exits, every UI change is covered by
live LLM navigation, and documented results are reproducible from both a fresh
setup and the tested persistent lab state.
Record the real devShell milestone separately if its dependency prerequisite
remains pending; provisioned-image success cannot close that milestone.
