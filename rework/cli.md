# Task commands in the CLI

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).
It designs step 7 and builds on steps 2 to 6.

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
- makes its own idempotency keys, and with `--key` replays a request whose
  reply was lost without recomputing it;
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
`/var/lib/p/control.sock`. Each command connects with `api.Dial`, which sends
`system.hello`, and calls through the typed `api.Do`
([wire-types.md](wire-types.md#the-version-agreed-at-systemhello)), so the
request types' `Validate` catches bad arguments before anything is sent.

### Command set

The set comes from the API methods left after steps 1 to 6 and from what the
TUI lets users do today: create projects and sessions, attach, start, stop,
rename, discard, delete, read details and agent reports, run services, and
inspect and retry operations. "Asks" marks the commands whose API method has
a dry run; [Risky changes](#risky-changes) describes them.

| Command | API method | Behavior |
|---|---|---|
| `p status` | `system.hello`, `system.health`, `system.inspect` | Instance ID, build version, protocol, configured capabilities (`git`, `runtime`, `services`), control state, record counts |
| `p project list` | `project.list` | |
| `p project create PROJECT [--origin URL] [--attach]` | `project.create` | Waits. `--attach` enters the first session when creation makes one, as for a blank project; the TUI does the same. |
| `p project delete PROJECT` | `project.delete` | Asks, waits. P stops running sessions itself. |
| `p branch list PROJECT [--retained]` | `project.branches`; `project.retained_branches` with `--retained` | |
| `p branch rename PROJECT:BRANCH NEW` | `project.retained.rename` | Reads the current tip for `expected_old_tip`, as the TUI does for a session rename; waits |
| `p branch delete PROJECT:BRANCH` | `project.retained.delete` | Asks, waits |
| `p origin show PROJECT [--refresh]` | `origin.inspect`; `origin.refresh` with `--refresh` | Shows the status, `fresh`, `unknown` or `local-only`, with the observation time |
| `p origin refs PROJECT` | `origin.refresh`, then `origin.sources` | Lists refs only from a fresh observation, as the TUI does ([below](#origin-refs-must-be-fresh)) |
| `p origin set PROJECT URL` | `origin.change`, kind `set` | Reads the current URL for `expected_url` |
| `p origin remove PROJECT` | `origin.change`, kind `remove` | Same |
| `p origin publish PROJECT:BRANCH [--to NAME]` | `origin.publish` | Asks. Kind `session` when a session holds the branch, otherwise `retained`. `--to` defaults to the same branch name. |
| `p session list [--project PROJECT]` | `session.list` | The project filter is applied by the CLI |
| `p session show SESSION` | `session.inspect` | Conditions, attachments, the latest unattended agent report, policy, diagnostic |
| `p session create PROJECT:BRANCH [--from REF \| --from-origin REF] [--attach]` | `session.create` | Waits. Without `--from`, resumes the retained branch (`choice: existing`). `--from` takes a P branch or commit (`choice: new`). `--from-origin` refreshes the origin, takes the commit and URL only from a fresh observation, and prints which commit it chose. |
| `p session create … --replace SESSION` | `session.create.replace` | Asks, waits: replaces a blocked creation with this request |
| `p session start SESSION` | `session.start` | Waits for its operation, which completes when the session is ready ([waiting.md](waiting.md#start-and-stop-as-operations)). A ready session prints "already running" and exits 0 ([Start and stop](#start-and-stop)). |
| `p session stop SESSION` | `session.stop` | Waits for its operation. A stopped session prints "already stopped" and exits 0. |
| `p session rename SESSION NEW` | `session.rename` | Reads the tip, as the TUI does; waits. A running session is refused. |
| `p session discard SESSION` | `session.discard` | Asks, waits. P stops a running session itself. |
| `p session delete SESSION` | `session.delete` | Same |
| `p session repair SESSION` | `session.repair`; `session.repair.prepare` when the image is missing | Asks, waits. The daemon diagnoses which of the four repairs applies ([repairs.md](repairs.md)). |
| `p session cleanup SESSION` | `session.create.cleanup` | Asks, waits: ends a failed creation and keeps its branch |
| `p attach SESSION` | `session.start`, `operation.wait` when it returns an operation, then `session.attach` | `session.attach` no longer starts a session ([waiting.md](waiting.md#start-and-stop-as-operations)), so `p attach` calls start first, by the [same rule](#start-and-stop): a ready session attaches at once, otherwise it waits for the start; users see the same result. Also accepts `PROJECT:BRANCH`. |
| `p service list SESSION` | `session.services` | |
| `p service start\|stop\|restart SESSION NAME` | `session.service.action` | Returns once queued ([services-plugin.md](services-plugin.md)); `p service list` shows progress. Never repeated automatically ([Errors](#errors-and-automatic-repeats)). |
| `p service logs SESSION NAME` | `session.service.logs` | |
| `p operation list` | `operation.list` | Shows each operation's key |
| `p operation show ID` | `operation.inspect` | |
| `p operation wait ID` | `operation.wait` | Waits until the status is not `running` |
| `p operation retry ID` | `operation.retry` | Waits |

List commands follow `next` to the end. `p help` lists the commands, and
`p NOUN --help` describes one noun's commands.

Only `rpc.cancel`, which cancels a request on the caller's own connection,
has no command.

### Origin refs must be fresh

A refresh whose contact fails still succeeds: it records status `unknown`,
and `origin.sources` then returns the cached refs marked `stale`
(`OriginAPI.Refresh` and `addOriginMethods` in internal/control/origin_api.go).
A stale ref cannot authorize a source choice, so the TUI withholds stale refs
(internal/tui/workflows.go). The CLI does the same: `p origin refs` and
`--from-origin` use refs only when the refresh returned `fresh` and every
page of `origin.sources` says `fresh`. Otherwise they print no refs, say when
the origin was last reached, and exit 1. A local-only project has no refs and
exits 3.

### A sample session

The output is illustrative:

```text
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

## Running a change

### Idempotency keys

**Generated:** one key per run call of a method that takes one, `cli-`
followed by 32 hex digits from `crypto/rand`, the same scheme as the TUI's
`tui-` keys (`newKey` in internal/tui/model.go). A dry run carries no key. A
run after a new dry run carries a new tag, so it gets a new key. Start, stop
and `operation.retry` take none, by the maintainer's decision for start and
stop.

**Supplied:** `--key KEY` (1 to 128 bytes) lets a script repeat a command
whose reply it lost, without making the change twice. The repeat must not
recompute inputs from state the first request changed. After
`p origin set team/app NEW --key K`, the current URL is `NEW`, so a
recomputed `expected_url` differs from the recorded one and the daemon
refuses the key as reused (`CompletedOriginChange` in
internal/control/origin_store.go). After a branch rename the old branch has
no tip to read, and after a session deletion `team/app:work` resolves to
nothing. So with `--key`, the CLI looks for an operation recorded under that
key before it resolves any argument:

1. **Found:** it sends no change. If the operation's kind and target match
   this command, it reports the operation and waits for it as usual;
   otherwise it exits 2.
2. **Not found:** it resolves the arguments and sends the request with the
   key. If the first request is recorded between the lookup and the send,
   the daemon replays it or refuses the changed request as `key_reused`; the
   change happens at most once.

The lookup pages `operation.list`, whose summaries carry each key; a key
whose operation the journal no longer keeps, such as one of a deleted
project, is not found. `origin.change` and `origin.publish` record no
operation today (waiting.md, open question 3), so until they do, the CLI
refuses `--key` on `p origin set`, `remove` and `publish`. On a command that
asks, a `precondition_failed` under `--key` stops with exit 3 instead of
asking again, so one key is never sent with two tags.

**Shown:** always in `--json` output, inside the operation. In human output
only when the CLI sent a change and lost the connection before the reply, so
it cannot tell whether the daemon accepted it: it prints the key and the
same command with `--key`, which replays it as above. `p operation list`
shows every operation's key.

### Waiting

A command that starts an operation waits for it with
[`operation.wait`](waiting.md#operationwait), which returns as soon as the
status is not `running` (`completed`, `failed`, `superseded` or `blocked`),
or with the status still `running` when its timeout passes. The CLI asks for
a timeout of 20 seconds, as the TUI does, inside a budget of 25 seconds for
the whole call (connect, hello, wait, reply), so it stays under the server's
30-second deadline. A wait that returns `running` is not an error: the CLI
prints the operation's phase on standard error if it changed, and waits
again. So a long build shows its phase at most every 20 seconds.

`--no-wait` prints the operation ID as soon as the daemon accepts the change
and exits 0; `p operation wait ID` waits later.

Ctrl-C while waiting stops only the CLI. The operation runs on in the
daemon, and the CLI prints its ID and `p operation wait ID`.

Service actions have no operation, so there is nothing to wait for.

### Start and stop

By the maintainer's decision of 2026-11-02, a start or stop that would change
nothing records nothing. `session.start` and `session.stop` answer in one of
three ways, and `p session start`, `p session stop` and the start inside
`p attach` follow each:

| Reply | When | The CLI |
|---|---|---|
| `{"changed": false, "session": …}` | Start of a ready session, stop of a stopped one | Prints "already running" or "already stopped" and exits 0; `p attach` attaches at once |
| `{"changed": false, "operation": …}` | A start or stop of the session is unfinished | Waits for that operation, as for its own |
| `{"changed": true, "operation": …}` | The change was accepted | Waits for the operation |

So repeating either command is always safe, and neither takes a key.

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
| Change | `{"operation": …}`, the operation after waiting, or as accepted with `--no-wait`. Start and stop add `changed`; when it is `false` with no operation, they print `{"changed": false, "session": …}`. |
| Change that asks | `{"plan": …, "change_tag": …, "operation": …}`: the last dry run, and the operation if it ran |
| `--dry-run` | The dry run's reply as the API returns it |
| Any error | `{"error": …}`, the API's error object: `code`, `message` (the kind's name) and `data` ([errors.md](errors.md#the-error-object)) |

Color and bold appear only when standard error is a terminal.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done: the read succeeded, the operation completed, or with `--no-wait` the daemon accepted it; a start or stop found the session already as asked; `--dry-run` of an allowed change. |
| 1 | Failed: the daemon could not be reached; it answered `failed`, `not_found`, `not_configured` or `internal`; a `retry` did not succeed in time; a service action's outcome is unknown; the origin observation is stale; or the operation ended `failed` or `superseded`. |
| 2 | Usage: the arguments or flags are invalid, found by the CLI or answered `invalid` by the daemon; or a `--key` belongs to another command. |
| 3 | Not run, nothing changed: the plan is not allowed; the daemon answered `conflict`, `capacity` or `precondition_failed`; the user declined or cancelled; or no terminal could ask. |
| 4 | Not finished: the operation is `blocked`, or the CLI stopped waiting while it runs on. |

`p api` keeps its own rule: 0, or 1 on any error.

### Errors and automatic repeats

An error carries no text: `message` is the kind's name, and `data` holds a
`reason` code, and where they apply a `target`, a `field` or the `supported`
protocols ([errors.md](errors.md#the-error-object)). The CLI prints one
sentence per reason, naming `target` or `field` when present, and falls back
to its sentence for the kind when it meets a reason it does not know. Error
reasons and refusal codes share one namespace, so they share the CLI's one
sentence table ([Printing the plan](#printing-the-plan)).

On `retry`, the CLI sends the identical call again, with the same key and
tag, only when a second call cannot act twice:

| Reason | What the daemon did | The CLI |
|---|---|---|
| `session_held` | Refused before dispatch: an operation holds the session | Says so, waits for the named operation with `operation.wait`, then repeats. Any call. |
| `too_many_requests` | Refused at admission | Repeats after a short pause, for up to 10 seconds. Any call. |
| `deadline` | Gave up; the handler may already have acted | Repeats a read, a keyed change (the key replays it) or a start or stop (a repeat changes nothing, or returns the unfinished operation). Never a service action. |

A service action has no key and no record, and the runtime may have queued
it before the deadline: `Backend.Services` (internal/runtimeincus/services.go)
runs `systemctl` before it observes the runtime again. So after a `deadline`
or a lost connection, `p service start|stop|restart` sends nothing more: it
says the action may have been queued, points to `p service list`, and exits 1.

If the operation a `session_held` names ends `blocked`, the repeat answers
`conflict`, `operation_blocked`, and the CLI stops. A risky run repeated
after a wait may meet an expired or changed tag; that is
`precondition_failed`, handled as in [The flow](#the-flow).

## Risky changes

The commands that ask are the ones whose API method has a dry run: the eight
methods left after step 6 ([repairs.md](repairs.md#before-and-after)).
They are session discard, delete, repair, create replacement and cleanup;
project deletion; retained-branch deletion; and publication.

### The flow

```text
with --key: an operation under that key? report it and stop (Idempotency keys)
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
  --force or --key: stop, exit 3          asked: ask again
wait for the operation, unless --no-wait
```

### Printing the plan

The CLI reads only the envelope's `allowed`, `refusals`, `loss` and `gone` to
decide what to print and whether to warn ([plan-shape.md](plan-shape.md)).
The API carries no message text, so the CLI owns one sentence per refusal
code, error reason and kind, and loss kind. The sentences live in one table
that the TUI and the CLI share, so both say the same thing; whichever step
first needs it creates it.

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
   naming its `target` when it is not the plan's. For example,
   `attachment_active`: "A terminal is attached to session 4f1c…. Detach it
   (Ctrl+B, then d) and try again." The CLI does not print the loss of a
   refused plan, which can be incomplete
   ([plan-shape.md](plan-shape.md#a-plan-that-is-not-allowed)).
3. **Already gone:** one line per `gone` item, as information. It never
   starts the countdown.
4. **The loss warning** when `loss` is not empty: the large block from
   [state-hash-confirmation.md](state-hash-confirmation.md#--force-in-the-cli),
   one line per item, with or without `--force`. For example, `p_commits`:
   "2 commits on branch work will be removed from P's repository; P does not
   check whether the origin has them". An item with `certain: false` reads
   "possibly: …".

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
decides: a plan that loses nothing runs at once; a plan that loses work
prints the loss warning and counts down 10 seconds, and Ctrl-C during the
countdown cancels with nothing sent. A refusal after the countdown makes a
new dry run, prints the new plan and stops, so a forced run never acts on a
plan nobody saw. A plan that is not allowed is refused with or without
`--force`. The TUI offers no force.

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
  that decide from the plan. See [Open questions](#open-questions).

`--dry-run`, `--force` and `--if-match` exclude one another.

## `p api`, `p tui`, `p attach` and host commands

**`p api`** stays for protocol work: debugging, a raw view of any reply, and
the VM scenarios that test the API itself. It does nothing for the user: no
keys, no waiting, no prompts. Its output and exit codes stay. Other steps
change it: it sends `system.hello` itself and its params lose `"v":1`
([wire-types.md](wire-types.md#summary)); its deadline for `operation.wait`
is the requested timeout plus 5 seconds
([waiting.md](waiting.md#operationwait)); and its two-minute deadline for
`project.delete.confirm` goes, because project deletion returns an operation
at once.

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
`p plugins defaults` (nix/module.nix). Steps 8 and 9 own any change to them,
and step 2 changes `p version` to report the protocol string instead of
`control_api_version`. `p git-hook` and `p attach-helper` stay unlisted: P
runs them itself. Every command, host commands included, exits 2 on a usage
error.

## Depends on

The shapes come from [wire-types.md](wire-types.md), [errors.md](errors.md),
[waiting.md](waiting.md), [repairs.md](repairs.md),
[plan-shape.md](plan-shape.md),
[state-hash-confirmation.md](state-hash-confirmation.md) and
[services-plugin.md](services-plugin.md), cited where each applies. Still
open there:

- **waiting.md, question 3:** if `origin.change` and `origin.publish` record
  operations, they wait and accept `--key`; if not, they print their result
  and refuse `--key`.
- **errors.md, question 1:** if blocked and failed operations get a
  `reason`, the CLI prints its sentence before the diagnostic.
- **A `key` filter on `operation.list`** would make the replay lookup one
  call instead of paging; waiting.md does not have one yet.
- **Step 5:** the full list of refusal codes, and whether `origin.publish`
  still takes `expected_origin_url` and `source_oid` once the tag binds
  them; the CLI passes them as long as it does.

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
| On a repeat with `--key`, recompute the inputs and resend | The first request may have changed what the inputs are read from, so the repeat is refused or cannot resolve its arguments. |
| Show stale origin refs, marked stale | A stale ref cannot authorize a source choice, and the TUI withholds them. |
| Ask before Stop and Rename, as the TUI does | The TUI asks because one key press acts; a typed command is already deliberate. Neither change has a plan to show. |
| Type the session or project name to confirm a loss | The TUI confirms with `y`; the loss warning comes right before the prompt. |
| Unique UUID prefixes as session names | A prefix unique today can match another session tomorrow, also inside a script. `PROJECT:BRANCH` is the name the TUI shows. |
| Drop the positional socket argument from `p api`, `p tui` and `p attach` | Every VM scenario passes it, and `--socket` and `P_SOCKET` already cover the new commands. |
| `p host daemon`, `p host plugins` | Longer to type and changes the NixOS module, for a grouping a heading in `p help` gives. |
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
3. **A TUI question, separate from the CLI commands: should the TUI get a
   Repair action in step 7?** The TUI offers no repair today, and the user
   guide sends repairs to the CLI. With one `session.repair` method, the TUI
   needs one action on a session that shows the plan and asks, as it does
   for a removal. *Recommendation:* yes, with the same plan and the same
   sentence per refusal code as the CLI, under the
   [TUI change and validation workflow](../docs/development-validations.md#tui-change-and-validation-workflow).

## How it will be checked

CLI unit tests against a fake daemon on a Unix socket, as
cmd/p/main_test.go does today, cover what can lose work or act twice:

- **Confirmation:** a dry run carries no key, and the run repeats its inputs
  with the tag; nothing runs without `y`; a refused plan exits 3 with or
  without `--force` and prints no loss; `gone` alone starts no countdown;
  without a terminal and without `--force`, nothing is sent;
  `precondition_failed` after a prompt shows the new plan and asks again;
  `--if-match` sends no dry run; the `--force` tests from
  [state-hash-confirmation.md](state-hash-confirmation.md#how-it-will-be-checked),
  on a fake clock.
- **Replay,** each with the first reply lost: an origin replacement, a
  branch rename and a session deletion, repeated with the same `--key`, find
  the recorded operation and send no change; a key recorded for another kind
  or target exits 2 without sending.
- **Cancellation:** Ctrl-C at the prompt or during the countdown sends
  nothing; Ctrl-C while waiting exits 4 and leaves the operation running.
- **Waiting and repeats:** every `operation.wait` asks for 20 seconds within
  a 25-second call budget; a wait that returns `running` is repeated;
  `completed` exits 0, `blocked` 4, `failed` and `superseded` 1;
  `session_held` waits for the named operation, then repeats with the same
  key and tag; a queued service restart followed by a `deadline` issues
  exactly one action call.
- **Start and stop:** each of the three replies above: "already running" or
  "already stopped" exits 0 and waits for nothing; an unfinished operation
  returned with `changed: false` is waited for; `changed: true` waits for
  the new operation. `p attach` attaches at once to a ready session, and on
  a stopped one starts it, waits, then attaches.
- **Origin refs:** a successful refresh followed by a failed one, with
  cached refs present, prints no refs and exits 1.
- **Sentences:** every refusal code, error reason, error kind and loss kind
  in `internal/api` has a sentence, and an unknown reason falls back to its
  kind's.

VM scenario 57, the developer workflow, moves from `p api` to the task
commands and keeps its outcome checks. The other scenarios keep `p api`,
because they test the API.
