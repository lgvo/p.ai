# Using P

P gives each development stream its own Git branch, workspace, private home,
and terminal. You can leave work running, return to it later, stop it without
removing its files, or remove it after reviewing what would be lost.

This guide follows the implemented CLI and terminal-browser experience. It is a walkthrough,
not a replacement for the [API reference](control-api.md) or the lifecycle
contracts. `p tui` connects to the real daemon; the original prototype runs on
fixtures. The preserved CLI-first automated validation covers all 55 VM steps
across serial checkpoints. **Authenticated Codex acceptance remains pending
your manual test.** See [current scope](mvp-status.md) and
[validation evidence](implementation-progress.md).

For a first run, follow [setup](#before-your-first-session),
[project creation](#create-your-first-project-and-commit), and
[another stream](#create-another-stream). For daily use, jump to
[resume/stop](#return-to-work-or-stop-it), [status](#read-the-session-status),
[publication](#publish-work-to-the-external-origin), or
[reviewed removal](#discard-or-delete-safely).

## The things you work with

A **project** is a repository on your P instance. It can be local-only or linked
to an external Git origin, such as your shared repository.

A **session** is one stream of work in that project. Its UUID stays the same
when you stop and start it or rename its assigned branch. Each session has a
separate workspace and private home; another session does not inherit its
credentials or uncommitted files.

An **attachment** connects your terminal to a session. Leaving that terminal
does not mean deleting the session. The **runtime** is the container that runs
its processes and holds its private files.

There are two steps to sharing source: push from the workspace to P, then
explicitly publish from P to the external origin. P does not automatically
publish every agent commit to your shared repository.

## Choose the action you need

| What you want | Action | What happens to your work |
|---|---|---|
| Start a separate stream | Create | Assign a branch and prepare a separate workspace and home. |
| Enter a stream | Attach | Connect to its terminal; a stopped session is started first. |
| Leave it running | Detach | Keep processes, terminal state, files, and identity. |
| Free running resources | Stop | End processes; keep workspace, private home, P branch, and identity. |
| Resume a stopped stream | Start, then Attach | Start new processes in the retained filesystem. |
| Share retained commits externally | Publish | Push the reviewed P commit to the reviewed external origin. |
| Remove the environment but keep pushed work | Discard | Remove the session and its private data; retain its existing P branch. |
| Remove the stream and its branch | Delete | Remove the session, private data, and assigned P branch. |

Stop/Start preserves files, including session-local credential files, but does
not suspend and resume processes. Discard/Delete removes those private files.
Neither action removes the external origin's branches or the contents of
externally granted directories.

## Use the terminal browser

With a configured instance, run `sudo -u p p tui /var/lib/p/control.sock` for
the standard service, or `p tui /absolute/path/to/control.sock` as the owner of
your separately run daemon. The layout and navigation follow the
[reviewed prototype decisions](../.prototype/tui-options/DECISIONS.md).

| Key | Action |
|---|---|
| Arrows or `j/k` | Select a session; waiting streams precede other running work, then remaining states. |
| PgUp/PgDn, Ctrl+B/F; Home/End, `gg`/`G` | Page or jump in the current list; Ctrl+U/D moves half a page. |
| `P`, `/` | Select an exact project; fuzzy-search project/branch/status/report context. |
| Enter | Start if needed and enter the real terminal. |
| `s`, `c`, `R` | Confirm Stop; create Project → Branch → Policy; rename a session branch. |
| `A`, `S` | Inspect real unattended agent reports; browse project user services. |
| `O`, `p`, `b` | Inspect durable operations, captured policy/environment, or retained branches. |
| `d`, `X` | Review Discard or session Delete after detaching and stopping. |
| `?`, `q`/Escape | Show help; close the current interaction, then search, project scope, or browser. |

The browser refreshes real state and retains selected identity while it exists.
It adapts its list/detail layout to terminal size; 48×16 is the minimum.
Search stays inside the list. In branch/project name fields, letters including
`q`, `g` and `G` are text; Escape/Ctrl-C cancels or goes back. Confirmation
defaults to **No**; Enter declines. Long confirmation and loss fields wrap and
can be scrolled before explicit `y` authorizes the action.

Leaving creation/startup progress does not cancel accepted daemon work and
prevents automatic terminal entry afterward. Use Operations to inspect or
Retry its durable intent. A connection failure marks cached inventory stale;
it does not turn old state into proof that cleanup succeeded.

Inside the actual terminal, tmux owns input. **Ctrl+B, then lowercase `d`** detaches and
returns to the picker. Detach before opening Agents/Services: the prototype's
in-terminal popup is not implemented. Agents shows the latest retained report,
not an active-instance inventory or conversation history. Bulk project deletion,
publication, plugin management and specialized repairs remain CLI/API workflows.

Policy, agent reports, operation diagnostics and Help also support `j/k`, paging,
and `gg/G` scrolling. Long fields wrap so they remain readable in small terminals.

### Project services

Services lists `p-project-*.service` units in the session user's systemd manager.
It excludes system and internal P units. `s` starts/stops the selected unit;
`r` restarts it; Enter opens its bounded recent journal. Use `/` for text find,
`n/N` for matches, `h/l` to pan, and `f` to follow the refreshed tail. Back clears
find first, then returns to Services, then the picker. This is a recent tail,
not an unbounded journal archive.

For example, inside a session built from the updated base image:

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/p-project-example.service <<'UNIT'
[Unit]
Description=Example project service
[Service]
WorkingDirectory=/workspace
ExecStart=/run/current-system/sw/bin/python3 -m http.server 8000 --bind 127.0.0.1
UNIT
export XDG_RUNTIME_DIR=/run/user/1000
export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus
systemctl --user daemon-reload
```

Detach and press `S` to select and start it. The base image enables the session
user manager; older images without it report services unavailable. An installed
unit that has not been loaded is shown as `unknown (not-loaded)` rather than
inventing a runtime state. A stopped or unreachable session cannot be controlled
through this page. Service ports remain inside the session; starting a unit does
not add host port publication or network grants.

If an inventory read is underway, one explicit service key can wait for it,
bound to the selected session, unit and action. Back cancels an unissued request;
a failed read or missing unit refuses it. Failed or uncertain actions are never
automatically retried. A stopped unit may be unloaded by systemd and shown as
`unknown (not-loaded)` until a new loaded observation is available.

The unit file and other private files survive Stop/Start. Processes end on Stop;
this example has no enablement rule, so start it again when needed. Project units
execute with ordinary session-user authority, including that session's captured
grants. Service requests are synchronous; after a timeout inspect the unit before
issuing another restart.

## Before your first session

The supported MVP installation is **NixOS with local Incus on x86_64-linux**.
The machine owner first installs P, provisions the confined Incus account and
base image, and configures trusted project policies. Follow the
[NixOS installation instructions](../README.md#nixos-installation) for that
operator work. Having the CLI binary alone is not a configured instance.

The hardened NixOS service currently supports `network: none`. Sessions that
need public access use a separately owner-run daemon with the documented
restricted public-egress configuration. A session cannot grant itself network
access or host directory mounts. Ask the instance owner to configure those
capabilities before planning an online agent task.

The following host commands use **Bash, jq, and p on PATH**. For the standard
NixOS service, define these shell helpers:

```bash
P_SOCKET=/var/lib/p/control.sock
api() { sudo -u p p api "$P_SOCKET" "$@"; }
attach() { sudo -u p p attach "$P_SOCKET" "$1"; }
```

`api` and `attach` here are convenience functions, not additional P commands.
If you run your own daemon, use its absolute socket path and its owning account:

```bash
P_SOCKET=/absolute/path/to/control.sock
api() { p api "$P_SOCKET" "$@"; }
attach() { p attach "$P_SOCKET" "$1"; }
```

Use one of these configurations, then check the instance:

```bash
p version
api system.health '{"v":1}'
api system.capabilities '{"v":1}'
api project.list '{"v":1,"limit":20}'
api session.list '{"v":1,"limit":8}'
```

The examples are separate tasks, not one script to run from top to bottom.
Choose your own project, branch, UUID, and unique action keys. Keep the same
key only when replaying the same intent.

Responses are JSON-RPC envelopes: successful values are under `result`; an
error is under `error` and the CLI exits unsuccessfully. `system.health` checks
the control state, not whether every container is ready. Capabilities describe
the methods enabled by this instance's configuration; the lifecycle capability
label `partial` is not a report that your session failed.

Lists are paginated. Follow `result.next` by supplying it as `after` with the
same method. Session pages have at most eight entries. A retained-branch page
can be empty with a nonempty continuation because assigned branches are filtered
out while traversing the repository.

## Create your first project and commit

Choose a project path allowed by the owner's configuration and not already in
use. This example starts an empty, local-only project:

```bash
PROJECT=demo
KEY=demo-create-1
CREATE=$(api project.create "$(jq -nc --arg project "$PROJECT" --arg key "$KEY" \
  '{v:1,key:$key,project:$project}')")
OPERATION=$(jq -er '.result.operation.id' <<< "$CREATE")
SESSION=$(jq -er '.result.operation.session_uuid' <<< "$CREATE")
api operation.inspect "$(jq -nc --arg id "$OPERATION" '{v:1,id:$id}')"
```

Creation continues in the daemon after the request returns. Repeat the last
inspection until the operation is `completed`. If it is `blocked` or `failed`,
read its `phase` and `diagnostic` before proceeding. Do not enter an incomplete
session or issue another Create to hide the failure.

The `key` identifies this exact intent. Keep the key and request if a response
is lost: replaying that exact request retrieves the same operation rather than
creating another session. Use a new key for a genuinely different action;
changing parameters under an existing key is refused.

Once creation completes, inspect the session and enter it:

```bash
api session.inspect "$(jq -nc --arg uuid "$SESSION" '{v:1,uuid:$uuid}')"
attach "$SESSION"
```

Expect `session_condition: "ready"`. You enter the workspace at `/workspace`.
The initial `main` branch is unborn: P does not invent an initial commit.
Inside the session, create your first commit and push it:

```bash
git status
git config user.name 'Your Name'
git config user.email 'you@example.com'
printf '# Demo\n' > README.md
git add README.md
git commit -m 'Start demo'
git push -u origin HEAD
```

**The workspace remote named `origin` points to P's Git server.** It is not the
project's optional external origin. This push retains the commit in P; external
publication is a separate action described below. Changes that you have only
committed locally have not yet been retained by P's repository.

With the bundled tmux configuration, detach by pressing **Ctrl+B, then lowercase `d`**.
Avoid exiting the last shell when you intend to leave it running: ending the
last tmux session also ends its interactive host.

## Create another stream

After the initial commit has been pushed to P, create a branch from that retained
source. Run this on the host after detaching:

```bash
CREATE=$(api session.create "$(jq -nc --arg project "$PROJECT" \
  '{v:1,key:"demo-feature-1",project:$project,branch:"feature",choice:"new",source:"refs/heads/main"}')")
OPERATION=$(jq -er '.result.operation.id' <<< "$CREATE")
FEATURE_SESSION=$(jq -er '.result.operation.session_uuid' <<< "$CREATE")
api operation.inspect "$(jq -nc --arg id "$OPERATION" '{v:1,id:$id}')"
```

Wait for completion as before, then `attach "$FEATURE_SESSION"`. This session
has its own files and home. Editing the first session's workspace does not edit
this one. A branch can be assigned to only one session at a time.

To resume a branch left behind by Discard, list retained branches and use
`choice: "existing"` without a source:

```bash
api project.retained_branches "$(jq -nc --arg project "$PROJECT" \
  '{v:1,project:$project,limit:8}')"
api session.create "$(jq -nc --arg project "$PROJECT" \
  '{v:1,key:"demo-resume-feature-1",project:$project,branch:"feature",choice:"existing"}')"
```

This produces a new UUID and a fresh private home. It recovers the retained
Git branch, not the discarded session's unpushed work or authentication.

### Start from an external repository

Include `url` in `project.create` to associate an SSH Git origin. P contacts
that origin before committing the project. A nonempty origin does not create a
session automatically: choose a source after observing it.

```bash
CREATE=$(api project.create '{"v":1,"key":"app-create-1","project":"app","url":"ssh://git.example.com/team/app.git"}')
OPERATION=$(jq -er '.result.operation.id' <<< "$CREATE")
api operation.inspect "$(jq -nc --arg id "$OPERATION" '{v:1,id:$id}')"
```

Replace the example URL before creating. Wait for the operation to complete
before discovering its sources:

```bash
api origin.refresh '{"v":1,"project":"app"}'
api origin.sources '{"v":1,"project":"app","limit":16}'
```

For a new session, supply
`choice: "new"`, an `origin_ref`, and the freshly observed `commit_oid` as
`expected_commit_oid`; omit `source`. Use the peeled `commit_oid` for a tag,
not its tag object ID. P captures that exact source and origin identity, so a
Retry does not silently pick up a later upstream commit. For example, after
reviewing the fresh `main` observation:

```bash
ORIGIN_COMMIT=replace-with-observed-main-commit_oid
CREATE=$(api session.create "$(jq -nc --arg commit "$ORIGIN_COMMIT" \
  '{v:1,key:"app-work-1",project:"app",branch:"work",choice:"new",origin_ref:"refs/heads/main",expected_commit_oid:$commit}')")
OPERATION=$(jq -er '.result.operation.id' <<< "$CREATE")
APP_SESSION=$(jq -er '.result.operation.session_uuid' <<< "$CREATE")
api operation.inspect "$(jq -nc --arg id "$OPERATION" '{v:1,id:$id}')"
```

Wait for completion and check readiness before `attach "$APP_SESSION"`. See the
[creation schemas](control-api.md#implemented-methods) for exact request fields.

## Return to work or stop it

Keep the session UUID from creation or find it with `session.list`. Reattach
to return to the persistent terminal:

```bash
attach "$SESSION"
```

To stop, detach all attachments first. On the host:

```bash
api session.stop "$(jq -nc --arg uuid "$SESSION" '{v:1,uuid:$uuid}')"
api session.inspect "$(jq -nc --arg uuid "$SESSION" '{v:1,uuid:$uuid}')"
```

Expect `session_condition: "stopped"`. A pending or confirmed attachment causes
Stop to return `busy`; it does not silently evict another terminal.

When ready to work again:

```bash
api session.start "$(jq -nc --arg uuid "$SESSION" '{v:1,uuid:$uuid}')"
api session.inspect "$(jq -nc --arg uuid "$SESSION" '{v:1,uuid:$uuid}')"
```

Start may initially report `starting`. Repeat inspection until `ready`, or
inspect the diagnostic if it returns to `stopped`. Attach after readiness.
Workspace files, private home files, and session-local Nix additions remain;
commands and terminal processes must be started again.

A daemon restart retains P's registry and local Git repositories. Your terminal
connection may end; reconnect by UUID. These retention guarantees cover normal
lifecycle operations, not disk loss or deliberate deletion. MVP includes no
backup/restore or upgrade/rollback subsystem.

## Read the session status

`session.inspect` and `session.list` expose four separate facts:

| Field | How to use it |
|---|---|
| `session_condition` | Lifecycle readiness: `creating`, `starting`, `ready`, `stopped`, `missing`, `unreachable`, `discarding`, or `deleting`. Read the diagnostic when progress stops. |
| `attached_count` | Number of confirmed terminal attachments; it is not a process count. |
| `latest_unattended_condition` | Latest accepted agent report while nobody was attached, or null. Signals include `running`, `attention`, `idle`, `failed`, and `unknown`. |
| `policy_condition` | Whether the session's captured policy is `current`, `outdated`, or `invalid` relative to trusted configuration. |

An unattended `attention` report is a reason to inspect the session. An `idle`
report is not proof that every agent or child process has finished. The field
records the latest valid signal, not a live reconstruction of agent behavior.
The first confirmed attachment clears it. Reports made while attached do not
repopulate it, and detaching does not restore an old report.
When non-null, this field is an object: its `condition` contains the signal,
alongside source and receipt information.

An `outdated` session keeps its captured policy; editing the host configuration
does not grant a running container new access. Recreate through the supported
lifecycle when you need new grants. An `invalid` policy can block Start rather
than silently altering the session. See [status semantics](session-observability.md).

## Publish work to the external origin

First commit and push your workspace changes to P. Then review the project's
external origin and the exact retained branch tip on the host:

```bash
api origin.inspect "$(jq -nc --arg project "$PROJECT" '{v:1,project:$project}')"
api project.branches "$(jq -nc --arg project "$PROJECT" '{v:1,project:$project,limit:8}')"
```

A local-only project needs an explicit origin association before publication;
use `origin.change` from the [origin API table](control-api.md). Setting or
replacing an origin compares the exact prior URL and contacts the new origin.
It does not retarget an already captured creation request.

Copy the reviewed origin URL and assigned branch's P commit OID into the
following variables. Use the UUID of the session being published:

```bash
ORIGIN_URL=ssh://git.example.com/team/app.git
SOURCE_OID=replace-with-reviewed-P-commit-OID
DESTINATION=refs/heads/feature
PUBLICATION=$(jq -nc --arg project "$PROJECT" --arg url "$ORIGIN_URL" \
  --arg session "$SESSION" --arg oid "$SOURCE_OID" --arg dest "$DESTINATION" \
  '{v:1,project:$project,expected_origin_url:$url,kind:"session",source:$session,source_oid:$oid,destination_ref:$dest}')
api origin.publication.preview "$PUBLICATION"
```

Review the returned origin, source, destination, and commit relationship.
Only proceed if they describe the action you intend:

```bash
api origin.publish "$(jq -c '. + {key:"demo-publish-1"}' <<< "$PUBLICATION")"
```

Publication checks the same reviewed origin identity and exact source again.
It creates an absent destination or advances it by an ordinary fast-forward;
it does not force-push. Results distinguish `created`, `advanced`, `satisfied`,
`refused`, and `outcome_unknown`. If the outcome is unknown, investigate and
reconcile the recorded publication; do not blindly submit a new key.

The preview's `workspace_status: "unknown"` matters: publication reviews P's
retained commit, not dirty files or unpushed commits in the workspace.

## Rename a stream

Use `session.rename` to rename the session's assigned branch while keeping its
UUID and private files. Supply the current P tip as `expected_old_tip`:

```bash
api session.rename "$(jq -nc --arg uuid "$SESSION" --arg tip "$SOURCE_OID" \
  '{v:1,key:"demo-rename-1",uuid:$uuid,new_branch:"feature-renamed",expected_old_tip:$tip}')"
```

Read a fresh tip before using this example; the publication variable may now
be stale. Inspect the returned operation until complete. The destination must
be absent, and the supported workspace must have the expected branch and
upstream. This action does not rename a branch on the external origin.

If P reports a branch/upstream mismatch, compare the expected and actual values
in its diagnostic. Correct Git manually after deciding what you want to keep,
then recheck. A Rename refused before making changes has a failed operation:
read a fresh P tip and submit the corrected Rename with a **new key**. Replaying
its old key only returns that same failure. This differs from resuming a blocked
operation that has already made effects. P does not automatically check out or
reset your workspace to make it match.

## Discard or Delete safely

Discard keeps the existing branch pushed to P. Delete removes that assigned
branch too. Both remove the session's runtime and private home, including
credentials, dirty files, and commits that exist only in its workspace.
Shared cached images and other sessions are preserved.

Detach, then Stop the session before this walkthrough. Choose the UUID carefully
and run a loss inspection:

```bash
ACTION=discard  # Set delete only when you also intend to remove the P branch.
LOSS=$(api workspace.loss.inspect "$(jq -nc --arg uuid "$SESSION" \
  '{v:1,key:"demo-loss-1",uuid:$uuid}')")
LOSS_OPERATION=$(jq -er '.result.operation.id' <<< "$LOSS")
api operation.inspect "$(jq -nc --arg id "$LOSS_OPERATION" '{v:1,id:$id}')"
```

Wait until inspection completes and review `operation.evidence.result`.
It accounts for supported Git worktrees, file changes, and local-only commits;
it also warns about private runtime data outside Git. An incomplete or unsupported
scan is a refusal, not a clean report.

Get the corresponding preview promptly; evidence and tokens have a two-minute
freshness window:

```bash
PREVIEW=$(api session.removal.preview "$(jq -nc --arg uuid "$SESSION" \
  --arg kind "$ACTION" --arg loss "$LOSS_OPERATION" \
  '{v:1,uuid:$uuid,kind:$kind,loss_operation_id:$loss}')")
jq '.result.preview' <<< "$PREVIEW"
```

**Pause here and review the loss, session identity, branch, and preserved
resources.** If the loss is not acceptable, keep the session and push or export
the work you need first. After a new inspection, use a new inspection key.

Only after deciding to authorize the displayed removal:

```bash
TOKEN=$(jq -er '.result.preview.confirmation_token' <<< "$PREVIEW")
REMOVE=$(api "session.$ACTION" "$(jq -nc --arg uuid "$SESSION" \
  --arg token "$TOKEN" --arg key "demo-$ACTION-1" \
  '{v:1,key:$key,uuid:$uuid,confirmation_token:$token}')")
REMOVE_OPERATION=$(jq -er '.result.operation.id' <<< "$REMOVE")
api operation.inspect "$(jq -nc --arg id "$REMOVE_OPERATION" '{v:1,id:$id}')"
```

Inspect until complete. Acceptance of a confirmation is not proof that cleanup
finished. Changed facts or expired tokens require a fresh inspection and preview;
P rechecks under quiescence before committing removal. An already accepted
operation retains its exact intent and key across restart.

If Incus positively proves the runtime missing, the preview can instead use
`acknowledge_missing_runtime: true`, explicitly acknowledging unknown runtime
loss. An unavailable Incus service does not prove absence and cannot authorize
this shortcut.

For an unassigned retained branch, use `project.retained.delete.preview` and
`project.retained.delete.confirm`; no runtime loss inspection is needed. Whole
project deletion uses an aggregate preview and confirmation covering every
session and branch. Its bounded MVP path requires stopped/detached established
sessions with fresh loss evidence; failed creations need supported cleanup first.
See the [project lifecycle](project-lifecycle.md) and [API schemas](control-api.md).

## When something does not finish

Read the diagnostic and inspect the durable operation before choosing another
action. A transport timeout does not prove the daemon abandoned an operation.

| What you see | What to do next |
|---|---|
| Creation is still running | Inspect the same operation. Do not create another stream with a new key just to retry. |
| A blocked operation with a corrected prerequisite | Use `operation.retry` with its ID. Retry keeps the captured request, source, identity, and policy. |
| `busy` | Read the message: attachments, guarded resources, branch assignment, or capacity may prevent the action. Remove the specific conflict before retrying. |
| Branch/upstream mismatch | Compare expected and actual values, correct Git manually, then recheck. |
| Incus unavailable or runtime unreachable | Restore or investigate the authority. P retains identity and incomplete cleanup; it must not claim deletion or create an uncertain duplicate. |
| Runtime missing | Use the supported identity-checked recovery or reviewed removal path. P does not adopt an unfamiliar replacement container. |
| Failed Create needs different inputs | Try the explicit replacement preview only for supported cases. Otherwise use confirmed failed-create cleanup, then a new Create. |
| Cleanup preview refuses | Read `unsafe_reasons`. Resolve the stated condition or investigate Incus manually; uncertain resources remain untouched. |

Retry example:

```bash
api operation.retry "$(jq -nc --arg id "$OPERATION" '{v:1,id:$id}')"
```

Replacement and repair are bounded capabilities, not universal recovery.
For example, if an assigned P ref is gone and its commits exist only in the
workspace, P does not promise to import those objects automatically. Preserve
the workspace and investigate the reported condition. A supported cleanup path
must still show loss, require confirmation, and verify ownership. Consult
[failure and recovery behavior](session-lifecycle.md); deleting state directories
or forgetting an uncertain container is not a supported repair procedure.

## What to expect from the development environment

The base environment supplies the common session tools. With the environment
capability configured, a committed default devShell can select a prepared Nix
environment. Without a committed default devShell, creation uses the base image;
an invalid declared default fails instead of silently falling back.

The currently validated environment builder is **offline**. It has no public
fetching, host Nix store, or host credentials. Do not assume an arbitrary flake
with remote inputs will build merely because it has a lock file. The selected
source must be supported by that builder. Session public-network Nix-fetch
evidence is separate from environment-builder support.

Editing a flake in a running workspace does not automatically rebuild that
session. Creation captures committed source; other sessions' dirty workspaces
do not become build inputs. Cached prepared images can speed up creation but
do not restore another session's files or credentials. Your writable Nix state
and private home remain session-local.

Incus supplies the required isolation boundary. Nix build sandboxing is disabled
inside P-managed containers; containers remain unprivileged with isolated user
mappings and nesting disabled. Network, filesystem, and resource grants still
apply. See [environment behavior](environment-building.md) and
[isolation](runtime-isolation.md) for operator detail.

## Use Codex inside a session

Codex integration is implemented, but real authenticated execution, hook behavior,
Stop/Start credential persistence, and authenticated cleanup are **pending your
manual acceptance**. Automated event tests use fixtures; credential tests use
dummy files. Those checks do not establish successful real Codex use.

For that manual test, use a disposable session with the selected adapter,
Codex `0.151.0`, and working trusted public egress. Inside the session:

```bash
export CODEX_HOME=/home/p/.codex
/usr/libexec/p/codex-adapter init
codex --version
```

Initialization prepares private configuration and refuses conflicts instead of
overwriting existing user configuration. Authenticate yourself inside the
session, and review/trust the installed hooks through Codex's `/hooks` interface.
Do not copy host credentials into it.

The adapter maps prompt/tool activity to `running`, permission requests to
`attention`, and the supported turn-complete notification to `idle`. This does
not make an idle child report proof that its parent stopped, or promise a hook
for every possible failure. Check unattended reports after detaching, then
reattach to inspect the actual work.

Follow the [exact manual acceptance procedure](implementation-progress.md#manual-codex-acceptance--pending-user-validation)
to verify real execution, hook/status reporting, Stop/Start persistence, separate
session authentication, and Discard/Delete cleanup. Keep this gate pending until
you have run it; successful automated fixtures do not close it.

## Further reference

- [Host API and trusted configuration](control-api.md): all method schemas,
  pagination, operation fields, and error meanings.
- [Project lifecycle](project-lifecycle.md): origin association, retained branches,
  and whole-project deletion.
- [Session lifecycle](session-lifecycle.md): detailed lifecycle and recovery rules.
- [NixOS installation](../README.md#nixos-installation): machine-owner setup.
- [Plugin contract](plugin-contract.md): optional extension and managed package
  installation. Package changes require trusted review and explicit selection;
  installing a package alone does not activate it.
- [VM lab and tests](../dev/vm/README.md): repository development and validation.
- [Current MVP scope](mvp-status.md) and [evidence record](implementation-progress.md):
  implemented support and remaining manual acceptance.
