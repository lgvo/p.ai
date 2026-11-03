# Using P

P gives each development stream its own Git branch, workspace, private home,
and terminal. You can leave work running, return to it later, stop it without
removing its files, or remove it after reviewing what would be lost.

This guide follows the implemented CLI and terminal-browser experience. It is a walkthrough,
not a replacement for the [API reference](control-api.md) or the lifecycle
contracts. `p tui` connects to the real daemon; the original prototype runs on
fixtures. The validation record distinguishes automated integration checks
from live TUI experience review. **Authenticated Codex acceptance remains pending
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

With a configured instance, run `sudo --preserve-env=P_SOCKET -u p p tui` for the standard service,
or `p tui` as the owner of your separately run daemon with `P_SOCKET` configured.
The layout and navigation follow the
[reviewed prototype decisions](../.prototype/tui-options/DECISIONS.md).

| Key | Action |
|---|---|
| Arrows or `j/k` | Select a session; waiting streams precede other running work, then remaining states. |
| PgUp/PgDn, Ctrl+B/F; Home/End, `gg`/`G` | Page or jump in the current list; Ctrl+U/D moves half a page. |
| `P`, `/` | Select an exact project; fuzzy-search project/branch/status/report context. |
| Enter | Start if needed and enter the real terminal. |
| `s`, `c`, `R` | Confirm Stop; create Project → Branch → Policy; rename a session branch. |
| `D`, `A`, `S` | Read complete session details; inspect unattended agent reports; browse project user services. |
| `O`, `p`, `b` | Inspect durable operations, captured policy/environment, or retained branches. |
| `d`, `X` | Review Discard or session Delete after detaching. |
| `?`, `q`/Escape | Show help; close the current interaction, then search, project scope, or browser. |

The browser refreshes real state and retains selected identity while it exists.
It adapts its list/detail layout to terminal size; 48×16 is the minimum.
The palette follows the terminal background. Use `p tui --theme light` or
`p tui --theme dark` to select it explicitly; `--theme auto` is the default
and uses dark colors until the terminal answers its background query. A
noninteractive `--snapshot` uses the dark fallback unless a theme is specified.
Wide browsers show aligned project, branch and runtime columns with selected
session details beside them. Medium browsers keep a stable list above details;
compact browsers prioritize the selected stream and its range. The overview
shows the latest retained report and selected-session project service observations.
Loading, unavailable, stopped and empty observations are distinct. `D` opens
scrollable identity, policy/environment, report provenance, service states and
diagnostics at every supported size; `A`, `S` and `p` opened there return to
Details with Back. Page movement uses the capacity of the visible layout.
`p` and Details show the image the session was created from: its environment
(the commit's default devShell, or `base`), source commit, image state, system,
and base and image fingerprints. Image `built` means this creation built the
commit's image; `reused` means it reused the image of an earlier session from
the same commit; `pending` and `building` appear while creation prepares it.

Search stays inside the list. In branch/project name fields, letters including
`q`, `g` and `G` are text; Escape/Ctrl-C cancels or goes back. On the final
creation policy review, Enter creates the project or creates, boots and enters
the session. Back returns to the previous editor or branch choices. Stop and
Rename confirmations default to **No**; Enter declines; `s` on a stopped
session says it is already stopped. Long policy, source,
confirmation and loss fields wrap and can be scrolled; explicit `y` authorizes
Stop, Rename and removal. Discard and Delete show the daemon's plan before
asking: what the removal loses, and, for a session whose runtime is missing,
that its contents are already gone. A plan that is not allowed shows why
instead and offers nothing to authorize. If the plan changed or the review
expired before `y`, nothing is removed and the current plan is shown to review
again. `r` on Retained branches reads them again.

Leaving creation/startup progress does not cancel accepted daemon work and
prevents automatic terminal entry afterward. A read that fails while the page
follows an operation is sent again on the next refresh, and entry still
follows. Use Operations to inspect an operation; `r` retries a blocked one with
its durable intent. A failed refresh marks cached inventory stale;
it does not turn old state into proof that cleanup succeeded.

Inside the actual terminal, tmux owns input. **Ctrl+B, then lowercase `d`** detaches and
returns to the picker. Detach before opening Agents/Services: the prototype's
in-terminal popup is not implemented. Agents shows the latest retained report,
not an active-instance inventory or conversation history. Bulk project deletion,
publication and plugin management remain CLI/API workflows.

Policy, agent reports, operation diagnostics and Help also support `j/k`, paging,
and `gg/G` scrolling. Long fields wrap so they remain readable in small terminals.

### Project services

For a complete application with a database, HTTP server and background worker,
use the seeded `notes/main` project in `just lab-public`
([notes walkthrough](../examples/notes/README.md)).
Its walkthrough includes installation, edit/test/run, service failure and
recovery, new branch sessions and private database state.

Services lists `p-project-*.service` units in the session user's systemd manager.
It excludes system and internal P units. `s` starts/stops the selected unit;
`r` restarts it; Enter opens its bounded recent journal. Use `/` for text find,
`n/N` for matches, `h/l` to pan, and `f` to follow the refreshed tail. Back clears
find first, then returns to the selected service, then the picker. Help also
returns to the selected service. This is a recent tail,
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
systemctl --user daemon-reload
```

Detach and press `S` to select and start it. The updated base image supplies
the session-user manager endpoint in the interactive shell. Older images
without a usable manager report services unavailable. An installed unit that
systemd has not loaded is shown as `installed`; P reports it as `stopped`, and
`s` starts it. A stopped or unreachable session cannot be controlled
through this page. Service ports remain inside the session; starting a unit does
not add host port publication or network grants.

If an inventory read is underway, one explicit service key can wait for it,
bound to the selected session, unit and action. Back cancels an unissued request;
a failed read or missing unit refuses it. Failed or uncertain actions are never
automatically retried. A stopped unit may be unloaded by systemd and shown as
`installed` until a new loaded observation is available.

The unit file and other private files survive Stop/Start. Processes end on Stop;
this example has no enablement rule, so start it again when needed. Project units
execute with ordinary session-user authority, including that session's captured
grants. A start, stop or restart returns as soon as systemd has queued it; the
page then shows the unit `starting` or `stopping` and later its result.

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

The following host commands use **Bash and p on PATH**; the walkthroughs that
still send raw API requests also use jq. Every client command, such as
`p status`, `p tui` and `p attach demo:main`, uses the configured instance.
The standard socket is `/var/lib/p/control.sock`; export `P_SOCKET` to
override it, or pass `--socket PATH`. Existing explicit socket arguments of
`p tui`, `p attach` and `p api` remain supported. See
[socket selection](control-api.md#host-framing-and-request-envelope).

For the standard NixOS service, this guide uses these shell helpers to run
commands as the owning account:

```bash
p() { sudo --preserve-env=P_SOCKET -u p p "$@"; }
api() { p api "$@"; }
```

The `p` function runs the `p` binary through sudo; it does not call itself.
Preserving `P_SOCKET` carries the configured socket or your exported override
through sudo's environment reset. `api` is a convenience function, not an
additional P command. If you run your own daemon, use its absolute socket path
and its owning account, with no `p` function:

```bash
export P_SOCKET=/absolute/path/to/control.sock
api() { p api "$@"; }
```

Use one of these configurations, then check the instance:

```bash
p version
p status
p project list
p session list
```

`p status` shows the instance ID, the build, the agreed protocol, the
capabilities this instance's configuration enables and the control state:
`git` for project lists, origins and retained branches; `runtime` for creating
projects and sessions and the session lifecycle; and `services` for project
services. A command whose capability is missing fails with the daemon's
`not_configured` answer. The control state says that P's state answered, not
that every container is ready.

`p help` lists every command, and `p session --help` describes the session
commands with their flags. Arguments name things as you see them: a project
path such as `demo`, `PROJECT:BRANCH` such as `demo:main` for a P branch or
for the session that holds it, a session UUID, or an operation ID. Results
go to standard output; progress and errors go to standard error. `--json`
prints the API's own types instead. The exit status says what happened: 0
done, 1 failed, 2 invalid arguments, 3 not run because the current state does
not allow it, and 4 not finished, because the operation is blocked or runs on
without the command.

The examples are separate tasks, not one script to run from top to bottom.
Choose your own project, branch and session names.

The sections on removal and publication still send raw requests with `p api`.
Its responses are JSON-RPC envelopes: successful values are under `result`;
an error is under `error` and the CLI exits unsuccessfully. Lists are
paginated: follow `result.next` by supplying it as `after` with the same
method. Choose a unique action key for each raw change, and keep the same key
only when replaying the same intent.

## Create your first project and commit

Choose a project path allowed by the owner's configuration and not already in
use. This example starts an empty, local-only project:

```bash
PROJECT=demo
p project create "$PROJECT"
```

A blank project gets its first session, on the unborn branch `main`. The
command waits for the creation and prints the project and the session's UUID;
while creation runs, it prints each new phase on standard error. If creation
ends `blocked` or `failed`, the command says what the reason means, prints the
diagnostic and exits nonzero; for a blocked operation it also prints the
`p operation retry` command. Do not enter an incomplete session or issue
another Create to hide the failure. Ctrl-C while it waits stops only the
command: creation runs on in the daemon, and the command prints the
`p operation wait` command that waits for it again.

Each change makes its own idempotency key, which identifies this exact intent.
If the connection fails before the daemon answers, the command prints itself
with `--key KEY` added: run that to repeat it. With the key, the command first
looks for the operation the key recorded and reports it instead of sending the
change again, so the change happens at most once. Running the command again
without `--key` is a new request. Reusing a key for a different change is
refused with exit status 2.

Once creation completes, read the session and enter it. Later sections use its
UUID, kept here as `SESSION`:

```bash
p session show "$PROJECT:main"
SESSION=$(p session show "$PROJECT:main" --json | jq -er .session.uuid)
p attach "$PROJECT:main"
```

`p project create "$PROJECT" --attach` does both at once.
Expect the condition `ready`. You enter the workspace at `/workspace`.
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
p session create "$PROJECT:feature" --from main
p attach "$PROJECT:feature"
```

`--from` takes a P branch, such as `main`, or a full commit ID; the command
waits for the creation as before. This session has its own files and home.
Editing the first session's workspace does not edit this one. A branch can be
assigned to only one session at a time; `PROJECT:BRANCH` names the session
that holds it.

To resume a branch left behind by Discard, list the retained branches and
create a session on one without `--from`:

```bash
p branch list "$PROJECT" --retained
p session create "$PROJECT:feature"
```

This produces a new UUID and a fresh private home. It recovers the retained
Git branch, not the discarded session's unpushed work or authentication.
`p branch rename PROJECT:BRANCH NEW` renames a retained branch.

### Start from an external repository

Pass `--origin` to associate an SSH Git origin. P contacts that origin before
committing the project. A nonempty origin does not create a session
automatically: choose a source after observing it.

```bash
p project create app --origin ssh://git.example.com/team/app.git
p origin refs app
```

Replace the example URL before creating. `p origin refs` observes the origin
again and lists its branches and tags with their commits. It lists refs only
from that fresh observation: when the origin cannot be reached, it lists none,
says when the origin was last reached and exits 1. `p origin show app` shows
the origin's URL and the status of its last observation, and `p origin set`
and `p origin remove` change it.

Create a session from an origin branch, or from a tag given as its full ref
such as `refs/tags/v1`:

```bash
p session create app:work --from-origin main
p attach app:work
```

`--from-origin` observes the origin again, takes the commit and the origin
URL only from that fresh observation, and prints the commit it chose. For a
tag it takes the peeled commit, not the tag object. P captures that exact
source and origin identity, so a Retry does not silently pick up a later
upstream commit. See the [creation schemas](control-api.md#implemented-methods)
for what the daemon records.

## Return to work or stop it

Name the session by its branch, or by the UUID from creation; `p session list`
lists every session. Reattach to return to the persistent terminal:

```bash
p attach "$PROJECT:main"
```

To stop, detach all attachments first. On the host:

```bash
p session stop "$PROJECT:main"
```

Stop waits until the session is stopped and keeps its files. A session already
stopped reads "already stopped" and exits 0; a stop already under way is
waited for. A pending or confirmed attachment makes Stop answer `conflict`
with reason `attachment_active`: the command says to detach every terminal
first and exits 3. It does not silently evict another terminal.

When ready to work again:

```bash
p session start "$PROJECT:main"
```

Start waits until the session is ready. A ready session reads "already
running" and exits 0; a start already under way is waited for. A failed start
prints the reason and the diagnostic, which `p session show` also shows. While
a start or stop runs, the session reads `starting` or `stopping`. `p attach`
starts a stopped session itself, waits for it, then enters it.
Workspace files, private home files, and session-local Nix additions remain;
commands and terminal processes must be started again.

A daemon restart retains P's registry and local Git repositories. Your terminal
connection may end; reconnect with `p attach`. These retention guarantees cover normal
lifecycle operations, not disk loss or deliberate deletion. MVP includes no
backup/restore or upgrade/rollback subsystem.

## Read the session status

`p session show SESSION` and `p session list` show four separate facts. With
`--json` they print the `session.inspect` and `session.list` fields below:

| Field | How to use it |
|---|---|
| `session_condition` | Lifecycle readiness: `creating`, `starting`, `ready`, `stopping`, `stopped`, `missing`, `unreachable`, `discarding`, or `deleting`. Read the diagnostic when progress stops. |
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
api origin.inspect "$(jq -nc --arg project "$PROJECT" '{project:$project}')"
api project.branches "$(jq -nc --arg project "$PROJECT" '{project:$project,limit:8}')"
```

A local-only project needs an explicit origin association before publication;
use `origin.change` from the [origin API table](control-api.md). It answers
an operation; wait for it with `operation.wait` and read the result with
`origin.inspect`. Setting or replacing an origin compares the exact prior URL
and contacts the new origin. It does not retarget an already captured creation
request.

Copy the reviewed origin URL and assigned branch's P commit OID into the
following variables. Use the UUID of the session being published:

```bash
ORIGIN_URL=ssh://git.example.com/team/app.git
SOURCE_OID=replace-with-reviewed-P-commit-OID
DESTINATION=refs/heads/feature
PUBLICATION=$(jq -nc --arg project "$PROJECT" --arg url "$ORIGIN_URL" \
  --arg session "$SESSION" --arg oid "$SOURCE_OID" --arg dest "$DESTINATION" \
  '{project:$project,expected_origin_url:$url,kind:"session",source:$session,source_oid:$oid,destination_ref:$dest}')
PLAN=$(api origin.publish "$PUBLICATION")
jq '.result.plan' <<< "$PLAN"
```

This first call is a dry run: it contacts the origin and returns the
publication's plan without publishing. Review the origin, source, destination,
and commit relationship in its `details`. A destination with commits your
source lacks makes the plan not allowed, with the refusal
`destination_divergent` and no change tag. Only proceed
if the plan describes the action you intend. Send the same request again with
an idempotency key and the plan's change tag:

```bash
TAG=$(jq -er '.result.change_tag' <<< "$PLAN")
PUBLISH=$(api origin.publish "$(jq -c --arg tag "$TAG" '. + {key:"demo-publish-1",if_match:$tag}' <<< "$PUBLICATION")")
api operation.wait "$(jq -nc --arg id "$(jq -er '.result.operation.id' <<< "$PUBLISH")" '{id:$id,timeout:20}')"
```

The change tag expires two minutes after the dry run. Publication plans again,
with the same reviewed origin identity and exact source, and runs only when
nothing changed. It creates an absent destination or advances it by an
ordinary fast-forward; it does not force-push. The operation's
`evidence.publication.status` distinguishes `created`, `advanced`,
`satisfied`, `refused`, and `outcome_unknown`. `refused` means the origin's
server rejected the push: the operation completes without publishing. A destination that diverged after the run
was accepted ends the operation `failed` with reason `changed`, without a
push. An unknown outcome ends the operation `failed` with reason
`outcome_unknown`: investigate and reconcile the recorded publication; do not
blindly submit a new key.

The plan's `workspace_status: "unknown"` matters: publication reviews P's
retained commit, not dirty files or unpushed commits in the workspace.

## Rename a stream

`p session rename` renames the session's assigned branch while keeping its
UUID and private files. Detach and Stop the session first: Rename answers
`conflict` with reason `session_not_stopped` for a session that is not
stopped, and the command exits 3.

```bash
p session stop "$PROJECT:feature"
p session rename "$PROJECT:feature" feature-renamed
```

The command reads the branch's current P tip and sends it as the tip it
expects, so a branch that moved in the meantime refuses the rename. It waits
for the operation. The destination must be absent, and the supported
workspace must have the expected branch and upstream. This action does not
rename a branch on the external origin.

If P reports a branch/upstream mismatch (operation reason
`workspace_mismatch`), compare the expected and actual values in its
diagnostic. Correct Git manually after deciding what you want to keep,
then recheck. A Rename refused before making changes has a failed operation:
run the corrected command again, which reads a fresh tip and makes a new key.
Repeating it with its old `--key` only reports that same failure. This differs
from resuming a blocked operation that has already made effects. P does not
automatically check out or reset your workspace to make it match.

## Discard or Delete safely

Discard keeps the existing branch pushed to P. Delete removes that assigned
branch too. Both remove the session's runtime and private home, including
credentials, dirty files, and commits that exist only in its workspace.
P does not look inside the runtime to list them; the plan states that
everything in it is lost. Other sessions are preserved. The image of the
session's source commit stays while another session started from that commit
exists; P deletes it after the last one is gone.

Detach every terminal first. A running session does not need Stop: P stops it
as part of the removal. Choose the UUID carefully and make a dry run, which
returns the removal's plan without removing anything:

```bash
ACTION=discard  # Set delete only when you also intend to remove the P branch.
REQUEST=$(jq -nc --arg uuid "$SESSION" '{uuid:$uuid}')
PLAN=$(api "session.$ACTION" "$REQUEST")
jq '.result.plan' <<< "$PLAN"
```

**Pause here and review the session identity, branch, runtime, and what the
plan says is lost.** `loss` lists what the removal destroys: a `runtime_data`
item means the runtime and everything in it will be deleted, and Delete's
`p_commits` item counts the commits that leave P's repository, which
`details.branch_loss` lists in full. P does not check whether the external
origin has those commits. If that is not acceptable, keep the session and
push or copy out the work you need first, then make a new dry run. When the assigned branch is already
gone, the plan has `assigned_ref_missing: true` and no tip; Discard then keeps
no branch and ends like Delete.

When `allowed` is `false`, the plan lists `refusals` instead and has no change
tag: each `code` names what stops the removal, such as `attachment_active` for
an attached terminal. Resolve it and make a new dry run.

Only after deciding to authorize the displayed removal, send the same request
with an idempotency key and the plan's change tag:

```bash
TAG=$(jq -er '.result.change_tag' <<< "$PLAN")
REMOVE=$(api "session.$ACTION" "$(jq -c --arg tag "$TAG" --arg key "demo-$ACTION-1" \
  '. + {key:$key,if_match:$tag}' <<< "$REQUEST")")
REMOVE_OPERATION=$(jq -er '.result.operation.id' <<< "$REMOVE")
api operation.wait "$(jq -nc --arg id "$REMOVE_OPERATION" '{id:$id,timeout:20}')"
```

Wait until complete. An accepted run is not proof that cleanup finished. The
change tag expires two minutes after the dry run. An expired tag, or facts
that changed since the dry run, answer `precondition_failed` and record
nothing: make a new dry run and review it again. P rechecks the branch tip and
the runtime identity once more before committing removal. An already accepted
operation retains its exact intent and key across restart.

If Incus positively proves the runtime missing, the plan says so:
`details.runtime.condition` is `missing`, and `gone` lists the runtime's
contents as already lost rather than as loss the removal causes. An
unavailable Incus service does not prove absence: the dry run answers
`failed`.

For an unassigned retained branch, use `project.retained.delete` the same way;
no runtime is involved. Whole project deletion, `project.delete`, makes one
plan covering every session and branch. It stops running sessions itself,
refuses while a terminal is attached, and lists a missing runtime's contents
in `gone`; failed creations need supported cleanup first.
See the [project lifecycle](project-lifecycle.md) and
[API schemas](control-api.md#risky-changes).

## When something does not finish

Read the diagnostic and inspect the durable operation before choosing another
action: `p operation list` lists the operations with their keys, and
`p operation show ID` shows one with its reason and diagnostic. A transport
timeout does not prove the daemon abandoned an operation. An error answers
with a kind that names your next step and a reason code; the
[control API](control-api.md#errors) lists them, and the commands and the TUI
say a sentence for each. `--json` prints the error object, with the reason
under `error.data.reason`. The daemon's log has the cause.

| What you see | What to do next |
|---|---|
| Creation is still running | Wait for the same operation with `p operation wait ID`. Do not create another stream with a new key just to retry. |
| A blocked operation with a corrected prerequisite | Run `p operation retry ID`. Retry keeps the captured request, source, identity, and policy. |
| `retry` | Something holds the session for a moment, such as another request or a running operation named in `error.data.target`. The commands wait for a named operation, or pause briefly, and send the same request again themselves; with `p api`, send it again once the holder finishes. |
| `conflict` | The state forbids the action until something changes it: an attached terminal, a session that must be stopped or started, a blocked operation, an assigned or reserved branch. `error.data.reason` names it; change that state, then try again. |
| `capacity` | Every session slot is taken. Discard or Delete a session; Stop keeps its slot. |
| `precondition_failed` | What you reviewed or expected changed, or the change tag expired. Make a new dry run, review its plan, and run that. |
| `precondition_required` | A run sent `key` without `if_match`. Send the dry run's change tag in `if_match`. |
| A plan with `allowed: false` | Read its `refusals`: each `code` names what stops the change, and `target` names the session or operation it is about when that is not the plan's own target. Resolve it, or investigate Incus manually, then make a new dry run. Uncertain resources remain untouched. |
| Reason `workspace_mismatch` | Compare the expected and actual values in the diagnostic, correct Git manually, then run Rename again without `--key`. |
| Reason `image_build_failed` | The image of the session's source commit did not build; the diagnostic says why. Retry builds the same commit again, so for a fault in the commit, such as an invalid devShell, clean up the failed creation and create a session from a fixed commit. For another cause, such as a failed fetch, fix it and Retry. |
| Reason `image_builder_changed` | The host's image builder configuration changed after the creation began. Restore it and Retry, or clean up the failed creation and create the session again. |
| Reason `internal` | P failed in a way it does not classify, or its work panicked; the diagnostic has the detail, and the daemon's log a panic's stack. A blocked operation keeps its guards; Retry it once the cause is understood. |
| Incus unavailable or runtime unreachable | Restore or investigate the authority. P retains identity and incomplete cleanup; it must not claim deletion or create an uncertain duplicate. |
| Runtime missing | Discard the session: its plan lists the runtime's contents under `gone`. Then create a new session on the retained branch. P does not adopt an unfamiliar replacement container. |
| Failed Create needs different inputs | Try the explicit replacement, `session.create.replace`, only for supported cases. Otherwise use confirmed failed-create cleanup, `session.create.cleanup`, then a new Create. |

Retry example, with the operation's ID:

```bash
p operation retry 9b0e2f4c-…
```

Replacement and cleanup are bounded capabilities, not universal recovery, and
P has no session repair. When a session's runtime is missing, or its Git
principal or key is missing, revoked or changed, discard the session and create
a new one on the retained branch. If its assigned P ref is gone, Discard or
Delete the session; with no branch both end the same way. P does not import
commits that exist only in its workspace, so rescue them by hand first. A
supported cleanup path must still show loss, require confirmation, and verify
ownership. Consult
[inconsistent sessions](session-lifecycle.md#inconsistent-sessions); deleting
state directories or forgetting an uncertain container is not a supported
recovery procedure.

## What to expect from the development environment

The base environment supplies the common session tools. With environment
building configured, each session starts from an image of its source commit:
the base, the commit's default devShell realized when it has one, and the code
already checked out in `/workspace`; P then puts that checkout on the
session's branch. Without a committed default devShell, the image holds the
base packages only; an invalid declared default fails instead of silently
falling back. Submodules are not fetched. Without environment building,
sessions start from the base image and clone their branch at first start, and
a commit with a root `flake.nix` is refused.

What commit images mean in practice:

- A second session from the same commit reuses that commit's image: no build
  and no clone.
- A session from a new commit builds a new image, even when only code changed:
  a full build, and a full image on disk while sessions from that commit
  exist.
- Discard followed by a new session on the kept branch normally builds again:
  if the discarded session pushed work, the branch is at a new commit; if not,
  the commit's image is deleted once its last session is gone, unless another
  session from that commit still exists.
- P deletes an image once no session started from its commit remains, and
  deletes all of a project's images with the project. There is no manual image
  cleanup.
- Your code lives in images too. Deleting a branch does not remove its code
  from images built from its commits, so a secret committed by mistake stays
  in an image until that image is deleted.

The image builder reaches the **public internet only**, and only when
the host configures public egress: it fetches the committed flake inputs and
substitutes from the public Nix binary cache (`cache.nixos.org`), with no route
to the host or your LAN, no host Nix store, and no host credentials. Without
public egress, as under the hardened NixOS service, the builder is offline and
only a devShell that needs nothing from the network builds. Private inputs and
authenticated caches are not supported. P's committed VM checks build offline
fixtures only; building from remote inputs has been checked by hand once.

Editing a flake in a running workspace does not automatically rebuild that
session. An image is built only from a commit in P: no session's uncommitted
work becomes a build input, and an image never holds another session's files
or credentials. Your writable Nix state and private home remain session-local.

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
