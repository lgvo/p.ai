# Task commands in the CLI

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).
It designs step 7 and builds on steps 4 to 6.

## Summary

**Today** the CLI has three client commands: `p tui`, `p attach` and `p api`.
Anything the TUI does not offer goes through `p api` with hand-written JSON:
publication, project deletion, retained-branch deletion, origin changes and
the repairs. The user picks idempotency keys, polls `operation.inspect`, and
for a deletion runs a loss inspection, previews, copies a confirmation token
into the confirm call and polls again
([user guide](../docs/user-guide.md#discard-or-delete-safely)).

**Proposed:** one task command for each thing a user does, named by noun and
verb: `p session delete`, `p project create`, `p origin publish`. Each
command:

- takes the names users see: project paths, `PROJECT:BRANCH`, session UUIDs;
- makes its own idempotency keys;
- waits for the operation it starts, unless given `--no-wait`;
- prints for people by default, and the API's own types with `--json`;
- for a change whose API method has a dry run, previews it, prints the plan
  with the refusals and the loss in the CLI's own sentences, asks, and runs
  it with the change tag; `--force` works exactly as
  [state-hash-confirmation.md](state-hash-confirmation.md#--force-in-the-cli)
  defines.

**What users notice:** normal work needs no JSON and no `p api`. `p tui` and
`p attach` stay as they are; `p attach` also accepts `PROJECT:BRANCH`. `p api`
stays for protocol work. The host commands (`p daemon`, `p plugins`) keep
their arguments and output and are listed under their own heading.

Three questions wait on the maintainer; see [Open questions](#open-questions).

## Before and after

| Task | Today | After |
|---|---|---|
| Create a project | `p api project.create '{"v":1,"key":"…","project":"team/app"}'`, then poll `operation.inspect` | `p project create team/app` |
| Create a session | `p api session.create` with `choice`, `source` or `origin_ref` and `expected_commit_oid` copied from `origin.sources`, then poll | `p session create team/app:work --from main` |
| Start | `p api session.start`, then poll `session.inspect` until `ready` | `p session start team/app:work` |
| Rename | `p api session.rename` with the tip copied from `project.branches` | `p session rename team/app:work feature` |
| Delete a session | Loss inspection, poll, preview, copy the token, delete, poll: six calls | `p session delete team/app:work` |
| Publish | `p api origin.inspect`, `project.branches`, `origin.publication.preview`, then `origin.publish` with the URL and OID copied | `p origin publish team/app:work` |
| Delete a project | Stop and detach every session, one loss inspection per session, preview, confirm, poll | `p project delete team/app` |
| Repair | Choose among four repair families, prepare an image if needed, preview, confirm, poll | `p session repair team/app:work` |
| Restart a service | `p api session.service.action '{"v":1,"uuid":"…","unit":"…","action":"restart"}'` | `p service restart team/app:work p-project-db.service` |
| Retry a blocked operation | `p api operation.retry '{"v":1,"id":"…"}'`, then poll | `p operation retry 7d3e…` |

Deleting a session, before (abridged from the user guide):

```bash
LOSS=$(api workspace.loss.inspect '{"v":1,"key":"demo-loss-1","uuid":"4f1c…"}')
api operation.inspect '{"v":1,"id":"…"}'      # repeat until completed
PREVIEW=$(api session.removal.preview '{"v":1,"uuid":"4f1c…","kind":"delete","loss_operation_id":"…"}')
jq '.result.preview' <<< "$PREVIEW"           # read the loss report
TOKEN=$(jq -er '.result.preview.confirmation_token' <<< "$PREVIEW")
api session.delete "{\"v\":1,\"key\":\"demo-delete-1\",\"uuid\":\"4f1c…\",\"confirmation_token\":\"$TOKEN\"}"
api operation.inspect '{"v":1,"id":"…"}'      # repeat until completed
```

After. The output is illustrative, and the operation's phases depend on
step 4:

```text
$ p session list --project team/app
SESSION                                PROJECT:BRANCH   CONDITION  ATTACHED
4f1c2a9e-0b7d-4c55-9e2a-31f0c8d4a6b1   team/app:work    ready      0
b5f6c1c2-1111-2222-3333-444455556666   team/app:docs    stopped    0

$ p origin publish team/app:work
Publish team/app:work (9f2c4e1) to ssh://git.example.com/team/app.git
  refs/heads/work: absent; the push creates it
Publish? [y/N] y
Published: created refs/heads/work at 9f2c4e1.

$ p session delete team/app:work
Delete session 4f1c2a9e-… (team/app:work); the branch work is removed too.

!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
!!  THIS WILL LOSE WORK
!!
!!  - the container of session 4f1c… and everything in it: uncommitted
!!    changes, files outside Git, commits not pushed to P; P does not
!!    look inside
!!  - 2 commits on branch work will be removed from P's repository;
!!    P does not check whether the origin has them
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

Delete session 4f1c…? [y/N] y
Deleted session 4f1c… and branch work.
```

## The commands

### Names and arguments

Commands are `p NOUN VERB ARGUMENTS [FLAGS]`. Flags may come before or after
the arguments, as in `p session delete 4f1c… --force`; Go's `flag` package
stops at the first argument, so each command parses its own flags. No new
dependency is needed.

| Argument | Form | Example |
|---|---|---|
| `PROJECT` | The project path | `team/app` |
| `PROJECT:BRANCH` | A P branch of a project. The first `:` ends the path, because project paths and Git branch names cannot contain `:` ([plan-shape.md](plan-shape.md#targets)). | `team/app:work` |
| `SESSION` | A session UUID, or `PROJECT:BRANCH` for the session that holds that branch. A branch is held by at most one session. The CLI resolves it with `session.list`; no match is an error. | `4f1c2a9e-…` or `team/app:work` |
| `ID` | An operation UUID | `7d3e…` |
| `NAME` | A service name as `p service list` prints it; opaque to P ([services-plugin.md](services-plugin.md#guarantees)) | `p-project-db.service` |

Every client command accepts `--socket PATH` and `--json`. Socket selection
is today's: `--socket`, then a nonempty `P_SOCKET`, then
`/var/lib/p/control.sock`.

### Command set

The set comes from the API methods left after steps 1 to 6 and from what the
TUI lets users do today: create projects and sessions, attach, start, stop,
rename, discard, delete, read details and agent reports, run services, and
inspect and retry operations. "Asks" marks the commands whose API method has
a dry run; [Risky changes](#risky-changes) describes them.

| Command | API method | Behavior |
|---|---|---|
| `p status` | `system.hello`, `system.health`, `system.capabilities`, `system.inspect` | Instance ID, build version, control state, whether lifecycle methods are available, record counts |
| `p project list` | `project.list` | |
| `p project create PROJECT [--origin URL] [--attach]` | `project.create` | Waits. `--attach` enters the first session when creation makes one, as for a blank project; the TUI does the same. |
| `p project delete PROJECT` | `project.delete` | Asks, waits. P stops running sessions itself. |
| `p branch list PROJECT [--retained]` | `project.branches`; `project.retained_branches` with `--retained` | |
| `p branch rename PROJECT:BRANCH NEW` | `project.retained.rename` | Reads the current tip for `expected_old_tip`, as the TUI does for a session rename; waits |
| `p branch delete PROJECT:BRANCH` | `project.retained.delete` | Asks, waits |
| `p origin show PROJECT [--refresh]` | `origin.inspect`; `origin.refresh` with `--refresh` | |
| `p origin refs PROJECT` | `origin.refresh`, then `origin.sources` | Refreshes first, as the TUI does |
| `p origin set PROJECT URL` | `origin.change`, kind `set` | Reads the current URL for `expected_url` |
| `p origin remove PROJECT` | `origin.change`, kind `remove` | Same |
| `p origin publish PROJECT:BRANCH [--to NAME]` | `origin.publish` | Asks. Kind `session` when a session holds the branch, otherwise `retained`. `--to` defaults to the same branch name. |
| `p session list [--project PROJECT]` | `session.list` | The project filter is applied by the CLI |
| `p session show SESSION` | `session.inspect` | Conditions, attachments, the latest unattended agent report, policy, diagnostic |
| `p session create PROJECT:BRANCH [--from REF \| --from-origin REF] [--attach]` | `session.create` | Waits. Without `--from`, resumes the retained branch (`choice: existing`). `--from` takes a P branch or commit (`choice: new`). `--from-origin` refreshes the origin and passes the observed commit and URL, and prints which commit it chose. |
| `p session create … --replace SESSION` | `session.create.replace` | Asks, waits: replaces a blocked creation with this request |
| `p session start SESSION` | `session.start` | Waits until ready |
| `p session stop SESSION` | `session.stop` | Waits |
| `p session rename SESSION NEW` | `session.rename` | Reads the tip, as the TUI does; waits. A running session is refused. |
| `p session discard SESSION` | `session.discard` | Asks, waits. P stops a running session itself. |
| `p session delete SESSION` | `session.delete` | Same |
| `p session repair SESSION` | `session.repair`; `session.repair.prepare` when the image is missing | Asks, waits. The daemon diagnoses which of the four repairs applies ([repairs.md](repairs.md)). |
| `p session cleanup SESSION` | `session.create.cleanup` | Asks, waits: ends a failed creation and keeps its branch |
| `p attach SESSION` | `session.attach` | Unchanged, plus the `PROJECT:BRANCH` form |
| `p service list SESSION` | `session.services` | |
| `p service start\|stop\|restart SESSION NAME` | `session.service.action` | Returns once queued ([services-plugin.md](services-plugin.md)); `p service list` shows progress |
| `p service logs SESSION NAME` | `session.service.logs` | |
| `p operation list` | `operation.list` | Shows each operation's key |
| `p operation show ID` | `operation.inspect` | |
| `p operation wait ID` | `operation.wait` | |
| `p operation retry ID` | `operation.retry` | Waits |

List commands follow `next` to the end. `p help` lists the commands, and
`p NOUN --help` describes one noun's commands.

Only `rpc.cancel` has no command: it cancels a request on the caller's own
connection. The methods steps 1 to 6 remove get none: `workspace.inspect`,
`workspace.loss.inspect`, `environment.cache.*`, the preview and confirm
pairs, and the four separate repair methods.

## Running a change

### Idempotency keys

- **Generated:** one key per run call, `cli-` followed by 32 hex digits from
  `crypto/rand`, the same scheme as the TUI's `tui-` keys
  (`newKey` in internal/tui/model.go). A dry run carries no key. A run after a
  new dry run carries a new tag, so it gets a new key.
- **Supplied:** `--key KEY` (1 to 128 bytes) makes a script's retry safe:
  repeating the same command with the same key returns the same operation
  instead of making a second change. On a command that asks, `--key` needs
  `--if-match`, because the daemon refuses a used key with a different tag.
- **Shown:** always in `--json` output, inside the operation. In human output
  only when the CLI sent a change and lost the connection before the reply,
  so it cannot tell whether the daemon accepted it: it prints the key and
  the command that replays it. `p operation list` shows every operation's key.

### Waiting

A command that starts an operation waits for it by calling `operation.wait`
in a loop, and prints each new phase on standard error. It stops waiting
when the operation is `completed`, `blocked`, `failed`, `unknown` or
`superseded`. `--no-wait` prints the operation ID as soon as the daemon
accepts the change and exits 0; `p operation wait ID` waits later.

Ctrl-C while waiting stops only the CLI. The operation runs on in the
daemon, and the CLI prints its ID and `p operation wait ID`.

Service actions are the exception: they return once queued and have no
operation, so there is nothing to wait for.

### Output

- **Standard output** carries the result: a list, a record, or the final
  line ("Deleted session 4f1c…").
- **Standard error** carries everything addressed to the person: progress,
  the plan, the loss warning, the prompt, the countdown and errors. The plan
  and the warning reach the terminal even when standard output goes to a
  file or `--json` is set.
- **`--json`** prints one JSON value on standard output, using the
  `internal/api` types from step 2:

| Command | `--json` prints |
|---|---|
| Read | The method's result; for lists, every page's items in one array, without `next` |
| Change | `{"operation": …}`, the operation after waiting, or as accepted with `--no-wait` |
| Change that asks | `{"plan": …, "change_tag": …, "operation": …}`: the last dry run, and the operation if it ran |
| `--dry-run` | The dry run's reply as the API returns it |
| Any error | `{"error": …}`, the API's error object |

Color and bold appear only when standard error is a terminal.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done: the read succeeded, the operation completed, or with `--no-wait` the daemon accepted it. `--dry-run` of an allowed change. |
| 1 | Failed: the daemon returned an error or could not be reached, or the operation ended `failed`, `unknown` or `superseded`. |
| 2 | Usage: the arguments or flags are invalid; nothing was sent. |
| 3 | Not run, nothing changed: the plan is not allowed, the user declined or cancelled, no terminal could ask, or the state changed after the preview. |
| 4 | Not finished: the operation is `blocked`, or the CLI stopped waiting while it runs on. |

`p api` keeps its own rule: 0, or 1 on any error.

### Errors

The CLI prints one sentence per error kind, naming the next step, followed
by the daemon's message if the errors step keeps one. On the kind that means
"retry", the CLI repeats the identical call, with the same key and tag, for
up to 10 seconds before it gives up. The identical key makes the repeat safe.
VM scenario 57 retries `busy` reads by hand today.

## Risky changes

The commands that ask are the ones whose API method has a dry run: the eight
methods left after step 6 ([repairs.md](repairs.md#before-and-after)).
They are session discard, delete, repair, create replacement and cleanup;
project deletion; retained-branch deletion; and publication.

### The flow

```text
resolve the arguments (PROJECT:BRANCH to a UUID; current tips and URLs)
dry run: the method without key or if_match
print the plan
not allowed?        exit 3, with or without --force
--dry-run?          exit 0
--force?            no loss: run now; loss: warning, 10-second countdown, then run
otherwise:          ask "[y/N]"; anything but y: exit 3
run: the method with key, if_match = change_tag, and the dry run's inputs
precondition_failed?
  say whether the tag expired or the state changed
  new dry run, print the new plan
  --force: stop, exit 3          asked: ask again
wait for the operation, unless --no-wait
```

### Printing the plan

The CLI reads only the envelope's `allowed`, `refusals`, `loss` and `gone` to
decide what to print and whether to warn ([plan-shape.md](plan-shape.md)).
The API carries no message text, so the CLI owns one sentence per refusal
code and per loss kind. The sentences live in one table that the TUI and the
CLI share, so both say the same thing; whichever step first needs it
creates it.

1. **A heading** from the command's arguments and a few facts of its own
   `details` type:

| Command | Heading facts |
|---|---|
| `p session discard`, `delete` | UUID and `PROJECT:BRANCH`; for delete, that the branch goes too, and for discard, that it stays |
| `p session repair` | Which repair applies (the details' `kind`); for a missing runtime, the source commit of the image and the tip it checks out |
| `p session create --replace`, `cleanup` | The blocked creation's UUID and branch; for replacement, the new request; for cleanup, what it keeps: the branch and shared images |
| `p project delete` | The project, its sessions and branches, and what deletion keeps: granted host directories, shared images, other projects, and logs already delivered to event handlers, whose retention is the handler's own |
| `p branch delete` | `PROJECT:BRANCH` and its tip |
| `p origin publish` | The origin URL, the source commit, the destination and what the push does: create, fast-forward or nothing |

2. **Refusals**, when the plan is not allowed: one sentence per item,
   naming its `target` when it is not the plan's. The CLI does not print the
   loss of a refused plan, which can be incomplete
   ([plan-shape.md](plan-shape.md#a-plan-that-is-not-allowed)).
3. **Already gone:** one line per `gone` item, as information. It never
   starts the countdown.
4. **The loss warning** when `loss` is not empty: the large block from
   [state-hash-confirmation.md](state-hash-confirmation.md#--force-in-the-cli),
   one line per item, with or without `--force`. An item with
   `certain: false` reads "possibly: …".

Illustrative sentences. Step 5 settles the full list of refusal codes, and
the loss lines follow [plan-shape.md](plan-shape.md#kinds):

| Code or kind | Sentence |
|---|---|
| `attachment_active` | A terminal is attached to session 4f1c…. Detach it (Ctrl+B, then d) and try again. |
| `session_running` | Session 4f1c… is running. Stop it with `p session stop` and try again. |
| `destination_divergent` | The origin's `main` has commits that P's branch lacks; P publishes only by fast-forward. |
| `recorded_image_missing` | The session's image is gone; it must be rebuilt from the session's source commit first. |
| `session_not_established` (repair) | Session 4f1c… is still being created. Retry it with `p operation retry`, replace it with `p session create --replace`, or end it with `p session cleanup`. |
| `runtime_data` (loss) | the container of session 4f1c… and everything in it: uncommitted changes, files outside Git, commits not pushed to P; P does not look inside |
| `runtime_data` (gone) | The runtime of session 4f1c… is already missing; whatever it held is gone. |
| `p_commits` | 2 commits on branch work will be removed from P's repository; P does not check whether the origin has them |
| `unreachable_objects` | possibly: Git objects in P's repository that no branch reaches; P does not count them |

A code the table lacks prints as the code itself. That cannot happen in a
release: the CLI and the daemon are the same `p` binary, and a unit test
checks that every code has a sentence.

### The prompt

`[y/N]`, defaulting to no, as the TUI's confirmations do. Only `y` or `yes`
runs. Ctrl-C at the prompt sends nothing and exits 3. A tag lasts two
minutes, so a slow answer can expire it; the run is then refused with
`precondition_failed`, and the CLI shows the new plan and asks again.

### `--force`

Exactly as [state-hash-confirmation.md](state-hash-confirmation.md#--force-in-the-cli)
decides:

| | Plan loses nothing | Plan loses work |
|---|---|---|
| Without `--force` | Print the plan, ask, then run. | Print the plan with the loss warning, ask, then run. |
| With `--force` | Print the plan and run at once. | Print the plan with the loss warning, count down 10 seconds, then run. Ctrl-C during the countdown cancels, and nothing is sent. |

A refusal after the countdown makes a new dry run, prints the new plan and
stops, so a forced run never acts on a plan nobody saw. A plan that is not
allowed is refused with or without `--force`. The TUI offers no force.

### Without a terminal

The CLI can ask only when standard input and standard error are both
terminals. Otherwise:

- Without `--force`, it prints the plan and exits 3: "Not run: no terminal
  to ask. Run again with `--force`." Nothing is sent.
- With `--force`, it behaves as on a terminal. The countdown still runs,
  printed as one line ("Deleting in 10 seconds; send SIGINT to cancel."),
  and SIGINT or SIGTERM cancels it.

### `--dry-run` and `--if-match`

- `--dry-run` prints the plan, warning included, and sends nothing else. With
  `--json` it prints the reply, including `change_tag` when the change is
  allowed.
- `--if-match TAG` runs the plan that tag names: no dry run, no prompt and no
  countdown, because the caller saw that plan from `--dry-run`, warning
  included, and the daemon runs it only if nothing changed; otherwise the CLI
  prints the refusal and exits 3. It is the API's own two calls, for scripts
  that decide from the plan. With `--key`, the same command replays the same
  operation. See [Open questions](#open-questions).

`--dry-run`, `--force` and `--if-match` exclude one another.

## `p api`, `p tui`, `p attach` and host commands

**`p api`** stays for protocol work: debugging, a raw view of any reply, and
the VM scenarios that test the API itself. It does nothing for the user: no
keys, no waiting, no prompts. Its output and exit codes stay. Two things
change with other steps: its default params follow the wire-types step's
version handling (today `{"v":1}`), and its two-minute deadline for
`project.delete.confirm` goes once project deletion returns an operation
(step 4).

**`p tui`** and **`p attach`** keep their forms, including the positional
socket argument (`p attach [SOCKET] SESSION`). `p attach` also accepts
`PROJECT:BRANCH`.

**Host commands** act on files and the state directory directly, not
through the control socket; offline package management even requires the
daemon stopped. `p help` lists them under their own heading:

```text
Host commands (run on the P host as the instance owner):
  p daemon HOST_JSON
  p plugins install | update | remove | defaults | conformance | list |
            activate | plan-assets | emit | run-event …
  p version
```

Their arguments and JSON output stay; the NixOS module calls `p daemon` and
`p plugins defaults` (nix/module.nix). Step 8 and step 9 own any change to
them. `p git-hook` and `p attach-helper` stay unlisted: P runs them itself.
Every command, host commands included, exits 2 on a usage error.

## Depends on

The documents for steps 2, 3 and 4 are being written alongside this one, and
[repairs.md](repairs.md) (step 6) is proposed. For steps 2 to 4 this design
follows their rows in the [plan](REWORK-PLAN.md#before-and-after); each item
below is an assumption to check against the document when it settles.

| Document | What this design assumes |
|---|---|
| Wire types (step 2) | The CLI decodes every reply into the shared `internal/api` types, and `--json` prints them. How the version is agreed at `system.hello` decides how each connection starts and what `p api` sends by default. |
| Errors (step 3) | Exit codes and sentences follow the eight kinds. The "retry" kind is safe to repeat with the same key. Whether errors keep a `message` is not settled. |
| Waiting (step 4) | `operation.wait` returns at `completed`, `blocked` and the failed states. It returns or reports progress often enough to print phases and stay within the server's request deadline. Start and stop return operations. Not verified: whether `origin.change` and `origin.publish` return operations; if not, those commands print their result and do not wait. |
| [repairs.md](repairs.md) (step 6) | `session.repair` returns the plan of the repair that applies, named by `details.kind`; no repair plan has `loss`, so `--force` never counts down for one. `session.repair.prepare`, `session.create.replace` and `session.create.cleanup` stay separate, hence `--replace` and `p session cleanup`. repairs.md leaves the preparation (open question 2 below) and a TUI Repair action (open question 3) to this step. |
| [plan-shape.md](plan-shape.md) | The envelope; step 5's final list of refusal codes for the sentence table. |
| [state-hash-confirmation.md](state-hash-confirmation.md) | The two calls, `precondition_failed` with "expired" or "changed", `--force`. Not verified: whether `origin.publish` still takes `expected_origin_url` and `source_oid` once the tag binds them; the CLI reads and passes them as long as it does. |
| [services-plugin.md](services-plugin.md) | `name`, `state`, `detail`, `session.service.logs`; actions return once queued. |
| [commit-images.md](commit-images.md), [runtime-driver.md](runtime-driver.md) | No image or cache commands. Host configuration changes do not touch the client commands. |

No conflict with a settled document was found.

## Departures from current docs

These change when step 7 lands:

- [User guide](../docs/user-guide.md): every walkthrough uses the task
  commands instead of `api` helpers and `jq`. The TUI section's "Bulk project
  deletion, publication, plugin management and specialized repairs remain
  CLI/API workflows" now names the commands.
- [Control API](../docs/control-api.md#host-framing-and-request-envelope):
  the paragraph on `p api` gains the task commands and loses the two-minute
  deadline.
- [README](../README.md#nixos-installation): the first-run checks use
  `p status` and `p project list`.
- `usage()` in cmd/p/main.go becomes `p help`.

## Options set aside

| Option | Why not |
|---|---|
| Keep `p api` as the only client command | The reason for this step: hand-written JSON, copied tokens and polling for every risky change. |
| A generic `p call METHOD --field value` built from the wire types | Spares the JSON but not the flow: the user still makes the dry run, copies the tag and polls, and nothing prints the plan. |
| Ask before Stop and Rename, as the TUI does | The TUI asks because one key press acts; a typed command is already deliberate. Neither change has a plan to show. |
| A flag that skips the countdown, or `--yes` beside `--force` | The maintainer wants a forced loss impossible to miss; one flag with one meaning keeps it so. |
| The countdown only on a terminal | [state-hash-confirmation.md](state-hash-confirmation.md#--force-in-the-cli) makes no exception; the warning lands in the script's log, and a signal can still cancel. |
| Type the session or project name to confirm a loss | The TUI confirms with `y`; the loss warning comes right before the prompt. |
| Unique UUID prefixes as session names | A prefix unique today can match another session tomorrow, also inside a script. `PROJECT:BRANCH` is the name the TUI shows. |
| Drop the positional socket argument from `p api`, `p tui` and `p attach` | Every VM scenario passes it, and `--socket` and `P_SOCKET` already cover the new commands. |
| `p host daemon`, `p host plugins` | Longer to type and changes the NixOS module, for a grouping a heading in `p help` gives. |
| Short service names (`db` for `p-project-db.service`) | Service names are opaque to P; the CLI cannot know a driver's naming convention. |
| Wait for a service to settle after an action | Service actions return once queued, by the maintainer's decision; `p service list` shows the progress. |
| `p session publish` and `p branch publish` | Two commands for one task. The user names the branch, and the CLI works out whether a session holds it. |
| A CLI framework such as cobra | A new dependency for a command set the standard `flag` package handles. |

## Open questions

1. **May scripts run a reviewed plan by its tag (`--if-match`)?** It skips
   the prompt and the countdown at run time. *Recommendation:* yes. The
   `--dry-run` call already printed the plan and the loss warning, and the
   tag makes the daemon run that plan or nothing. Without it, a script that
   decides from the plan has to use `p api`, and `--force` runs whatever the
   plan has become by then.
2. **Should `p session repair` build a missing image itself?** The image is
   rebuilt by a separate call, `session.repair.prepare`, which
   [repairs.md](repairs.md#sessionrepairprepare-stays) keeps; until it has
   run, the plan refuses with `recorded_image_missing`, and repairs.md leaves
   the CLI's behavior to this step. *Recommendation:* yes. When that refusal
   is the only one, the CLI says the image will be rebuilt, runs the
   preparation and waits, then makes a new dry run and asks as usual.
   Preparation destroys nothing, and the user asked for a repair. `--dry-run`
   never builds. The alternative is a refusal sentence that tells the user to
   run a second command.
3. **Should the TUI get a Repair action in step 7, alongside
   `p session repair`?** The TUI offers no repair today, and the user guide
   sends repairs to the CLI. With one `session.repair` method, the TUI needs
   one action on a session that shows the plan and asks, as it does for a
   removal. *Recommendation:* yes, with the same plan and the same sentence
   per refusal code as the CLI, under the
   [TUI change and validation workflow](../docs/development-validations.md#tui-change-and-validation-workflow).

## How it will be checked

- **CLI unit tests** against a fake daemon on a Unix socket, as
  cmd/p/main_test.go does today:
  - each command sends its method with the expected params; `PROJECT:BRANCH`
    resolves to the session holding the branch, and no match exits 1;
  - a dry run carries no key; the run repeats the dry run's inputs with a
    fresh `cli-` key and the tag; `--key` is sent as given;
  - nothing runs without `y`; Ctrl-C at the prompt sends nothing;
  - the `--force` tests from
    [state-hash-confirmation.md](state-hash-confirmation.md#how-it-will-be-checked),
    on a fake clock;
  - a refused plan exits 3 with or without `--force` and prints no loss;
    `gone` alone starts no countdown;
  - without a terminal and without `--force`, nothing is sent and the exit
    code is 3;
  - `precondition_failed` after a prompt shows the new plan and asks again;
  - `--if-match` sends no dry run; `--dry-run` sends nothing else;
  - waiting: `completed` exits 0, `blocked` 4, `failed` 1, Ctrl-C 4 without
    cancelling the operation, `--no-wait` 0 at acceptance; a "retry" error is
    repeated with the same key and tag;
  - every `--json` shape in the [Output](#output) table.
- **A sentence test:** every refusal code, loss kind and error kind defined
  in `internal/api` has a sentence.
- **VM scenario 57**, the developer workflow, moves from `p api` to the task
  commands and keeps its outcome checks. The other scenarios keep `p api`,
  because they test the API.
- **The user guide's** walkthroughs are rewritten with the task commands,
  and each command they show is one the unit tests cover.
