# Confirming risky changes with change tags

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

Some changes can lose work or replace something: discarding or deleting a
session, deleting a project or a retained branch, publishing to an origin,
and the session repairs. P shows each of
these as a preview first and runs it only when the user confirms.

**Today** the daemon remembers each preview. It keeps the preview in memory
under a random token for two minutes, and confirm looks the token up. The
daemon has ten preview stores, each with its own copy of this code, and the
API has a preview method and a confirm method for each change. Publication is
the exception: it has no token, and `origin.publish` sends the whole request
again and checks it afresh.

**Proposed:** P confirms risky changes the way HTTP makes conditional requests
with `ETag` and `If-Match`.

- The daemon names each planned change by its **change hash**: a hash of the
  state before the change and the state after it.
- Each change has one method, called twice. The first call is a dry run: it
  returns the plan and a **change tag**, an expiry time followed by a hash of
  that expiry and the change hash.
- The second call sends the change tag back in `if_match`, with an idempotency
  `key`. The daemon refuses an expired tag; otherwise it reads the state
  again, computes the change hash and the tag again, and runs the change only
  if the tags match.
- The daemon stores nothing between the two calls.

The end result stays the same: the user sees what will happen, and it happens
only if the state is still what they saw and the preview is less than two
minutes old.

### The HTTP pattern this follows

| HTTP | P |
|---|---|
| `GET` returns a resource and its `ETag` | The first call, a dry run, returns a plan and its `change_tag` |
| `DELETE` with `If-Match: <etag>` runs only if the resource is unchanged | The second call, with `if_match: <change_tag>`, runs only if the state is unchanged |
| `412 Precondition Failed` | `precondition_failed`: the tag expired or the state changed |
| `428 Precondition Required` | `precondition_required`: the call had a `key` but no `if_match` |
| Strong ETags compare exactly | Every change tag is strong; there is no weak `W/` form |
| `If-Match: *` matches any version | Not supported: a risky change needs the exact tag the user saw |
| `Expires` limits how long a response counts | The tag's first part limits how long a preview counts |

## Before and after

### The flow

Before: preview stores the plan, and confirm looks it up.

```text
session.removal.preview {uuid, kind: "delete", loss_operation_id}
  read the state and plan the change
  token = 16 random bytes
  keep {plan, expiry = now + 2 minutes} in daemon memory under the token
  return the plan, the token and its expiry

session.delete {uuid, key, token}
  key used before? return that operation
  lock the session
  look the token up; refuse if missing, expired, or for another session
  read the state again and plan again; refuse if the plan differs
  write the operation and forget the token
```

After: the tag is computed from the plan, so there is nothing to look up.

```text
first call, a dry run:
session.delete {uuid}
  read the state and plan the change
  not allowed? return the plan with its refusals, and no change tag
  change_hash = SHA-256 of (action, target, inputs, state, plan)
  change_tag  = expiry + "-" + SHA-256 of (expiry, change_hash)
  return the plan and the change tag               (nothing is stored)

second call, the run:
session.delete {uuid, key, if_match: change_tag}
  key used before? return that operation
  expiry passed? refuse: precondition_failed, expired
  lock the session
  read the state again, plan again, compute change_hash and the tag again
  tag differs? refuse: precondition_failed, changed
  write the operation with its change_hash; inside the transaction, check the
    change hash and the expiry again
```

A change tag comes only from a dry run of a change that is allowed. A dry run
of a change that is not allowed still returns its plan, with the refusals that
say why, but no tag. Every error, including both precondition refusals,
carries no tag and no plan: it says why the call was refused, and the client
makes a new dry run to see the current plan.

Which call is which depends on two fields. Running a change always needs an
idempotency `key`, so a call that has a `key` but forgot its tag fails loudly
instead of quietly returning a dry run:

| The call has | Result |
|---|---|
| neither `key` nor `if_match` | Dry run: returns the plan, and a change tag only if the change is allowed |
| `key` and `if_match` | Runs the change, if the tag still matches |
| `key` but no `if_match` | Refused: `precondition_required` |
| `if_match` but no `key` | Refused: invalid params |

### The API

Today each change has two methods, a preview and a confirm: 23 methods in all.
After this decision each change has one method, called twice: 11 methods in
all. Step 6 then merges the four repairs of an established session into
`session.repair`, which diagnoses the session and returns the plan of the
repair that applies ([repairs.md](repairs.md)): 8 methods after step 6.

| Change | Before | After: one method, called twice |
|---|---|---|
| Discard a session | `session.removal.preview` with `kind: "discard"`, then `session.discard` | `session.discard` |
| Delete a session | `session.removal.preview` with `kind: "delete"`, then `session.delete` | `session.delete` |
| Repair a missing runtime | `session.repair.preview`, then `session.repair.confirm` | `session.repair` |
| Restore a missing assigned ref | `session.ref.repair.preview`, then `session.ref.repair.confirm` | `session.ref.repair` |
| Reissue the session Git principal | `session.principal.repair.preview`, then `session.principal.repair.confirm` | `session.principal.repair` |
| Remove an unrecoverable session record | `session.record.repair.preview`, then `session.record.repair.confirm` | `session.record.repair` |
| Replace a blocked session creation | `session.create.replace.preview`, then `session.create.replace.confirm` | `session.create.replace` |
| Clean up a blocked session creation | `session.create.cleanup.preview`, then `session.create.cleanup.confirm` | `session.create.cleanup` |
| Delete a project | `project.delete.preview`, then `project.delete.confirm` | `project.delete` |
| Delete a retained branch | `project.retained.delete.preview`, then `project.retained.delete.confirm` | `project.retained.delete` |
| Publish to the origin | `origin.publication.preview`, then `origin.publish` | `origin.publish` |

Cached environment images are no longer managed through the API:
`environment.cache.list`, `environment.cache.preview` and
`environment.cache.collect` are removed, and P manages image lifetime itself
(a separate decision; see the [plan](REWORK-PLAN.md)).

Calls without a preview stay as they are: create, start, stop, rename,
service actions, `session.repair.prepare` (whose image now comes from the
session's source commit; see [commit-images.md](commit-images.md)), and
`origin.change`, which already guards itself with the expected current URL.

`session.create.replace` is the one method whose preview takes a `key` today:
the idempotency key of the new session it creates. Its dry run leaves `key`
out, and the run's `key` serves as both the replacement's key and the new
session's creation key. The key names the new operation only, so it stays out
of the plan and the hash.

### One request and reply

Before:

```text
→ session.removal.preview  {"v":1,"uuid":"…","kind":"delete","loss_operation_id":"…"}
← {"v":1,"preview":{…,"confirmation_token":"<32 hex>","expires_at":"…"}}
→ session.delete           {"v":1,"key":"k1","uuid":"…","confirmation_token":"<32 hex>"}
← {"v":1,"operation":{…}}
```

After:

```text
→ session.delete  {"v":1,"uuid":"…"}
← {"v":1,"plan":{…},"change_tag":"20261102T153012Z-<64 hex>"}
→ session.delete  {"v":1,"uuid":"…","key":"k1","if_match":"20261102T153012Z-<64 hex>"}
← {"v":1,"operation":{…}}
  or a precondition_failed error saying "expired" or "changed", with no plan and no tag
```

### The code

Before: ten copies of the token handling, one per kind of change.

```text
internal/daemon/
  lifecycle.go            ten preview stores (maps from token to preview)
  removal_preview.go      token, expiry, store limit, lookup, recheck: discard and delete
  repair.go               the same for missing-runtime repair
  ref_repair.go           the same for ref repair
  …                       seven more copies
```

After: one module does the dry run and the precondition check; each kind
keeps only what is specific to it.

```text
internal/daemon/
  plan.go                 dry run, change hash and tag, key replay, lock, recheck: written once
  removal_preview.go      read the state and plan discard and delete
  repair.go               read the state and plan missing-runtime repair
  …
```

Each kind of change supplies only three things: what it acts on, which is
locked and hashed; a function that reads the state and plans the change; and
the operation it writes once confirmed.

This removes the debt entry "Preview confirmation tokens are issued by copied
code" in [docs/debts/structure.md](../docs/debts/structure.md).

## How the change hash and tag are built

### The change hash

The change hash names one planned change. It is the lowercase hex SHA-256 of
one JSON object holding the state before the change and the state after it:

| Field | Holds | Example |
|---|---|---|
| `format` | The version of this recipe | `p.plan/v1` |
| `instance` | The daemon's durable instance UUID | the `instance_id` from `system.hello` |
| `action` | The method name | `session.delete` |
| `target` | What the change acts on | `session/<uuid>` |
| `inputs` | The caller's choices | `choice` and `source` for a replacement |
| `state` | The state before: the facts the plan depends on, read now | the runtime's identity and state, the image reference, ref OIDs |
| `plan` | The state after: the planned effects, and whether the change is allowed | what will be removed or created |

The object is encoded with Go's `encoding/json` from fixed structs, which keeps
field order and sorts map keys, so the same values always give the same bytes.

Three rules keep the change hash honest:

1. **Hash the facts the plan is built from.** That is what each confirm
   compares today, for example `sameEnvironmentImage` in
   `internal/control/environment_collection.go` for the cached images project
   deletion removes, minus the observations the maintainer removed: the
   workspace contents and the origin. It leaves out display text and the
   preview's own token and expiry, and it keeps timestamps that identify a
   generation, such as a cached image's `created_at`, which tells a
   re-created image from the one the user reviewed. So a change inside a
   session's workspace never invalidates a deletion's tag.
2. **Read the same way at preview and at run.** The daemon calls the same
   `plan` function both times.
3. **Same state, same change hash.** Two previews of an unchanged state give
   the same change hash.

The change hash stays inside the daemon: replies never include it, and the
operation journal records it as the exact change that ran.

### The change tag

The change tag is what clients see and send back. It has two parts joined by
a dash:

```text
20261102T153012Z-9f2c4e…   (64 hex characters after the dash)
└── expiry ────┘ └── SHA-256 of (expiry, change_hash) ─┘
```

1. **The expiry**, in UTC, to the second, in ISO 8601 basic format
   (`YYYYMMDDTHHMMSSZ`). It has no colons, so the tag is safe in shell
   arguments and file names, and a person can read when it expires.
2. **A hash that binds the expiry to the change**: the lowercase hex SHA-256
   of the expiry and the change hash.

Because the expiry is readable, the daemon checks it first and can tell an
expired preview from a changed state. Because the expiry is in the second
hash, editing it breaks the tag: rebuilding the tag needs the change hash,
which the daemon never returns, and recomputing it from every hashed fact
takes as much work as previewing again. The tag is not a secret and the hash
has no key, so this keeps a preview recent rather than stopping a client
determined to skip it; the expiry guards against running an old preview by
accident.

**How long a tag lasts.** Two minutes, as previews do today. Only the
daemon's clock sets and checks the expiry.

Because `action` and `target` are in the change hash, a tag for deleting one
session cannot delete another session or repair this one. Because `plan` is in
the change hash, a daemon upgrade that changes how a change is planned makes
older tags stop matching.

## What stays the same

- A dry run starts no operation and destroys nothing. Like today's
  publication preview, it may refresh P's cached view of the origin.
- A change runs only when the state it was planned from is unchanged, and
  only within two minutes of the dry run. The checks that decide "unchanged"
  are today's, except the ones the maintainer removed: deletions no longer
  look at the workspace or the origin, and rename and ref repair require a
  stopped session.
- A repeated run with the same idempotency key returns the same operation,
  also after a restart. The same key with a different tag is refused.

## What changes for callers

- One method per change, called twice: first a dry run, then the run.
- Removal no longer looks inside the container, by the maintainer's decision
  of 2026-11-02: no workspace loss inspection first, no `loss_operation_id`,
  and no pause or re-check while deleting. The plan states the loss by
  category ([plan-shape.md](plan-shape.md)), and P stops a running container
  as part of deleting it. Rename and ref repair require the session stopped.
- The run call repeats the dry run's inputs, because the daemon no longer
  keeps them.
- `change_tag` and `if_match` replace `confirmation_token`. The tag carries its
  expiry, so `expires_at` is no longer a separate field.
- Refusals are `precondition_failed` and `precondition_required` instead of
  `busy`. Like every error, they carry no change tag and no plan; a new dry
  run returns both. The [errors decision](REWORK-PLAN.md#before-and-after)
  places these two kinds among the others.
- A preview survives a daemon restart until it expires. A full preview store
  no longer causes a `busy` refusal, because there is no store.
- Publication to a divergent destination is refused at the dry run, with the
  refusal `destination_divergent`, instead of returning `status: "refused"`
  from `origin.publish`.

## `--force` in the CLI

The CLI task commands from the [plan](REWORK-PLAN.md) take a `--force` flag.
It works for every change, including changes that lose work. The maintainer
decided this on 2026-11-02, on condition that a forced loss is impossible to
miss.

Every plan has a `loss` list: what the change would destroy, by category,
such as a session's container and everything in it, or the commits that
leave P's repository. P does not look inside the container to list files. An
empty list means nothing is lost. P does not check
whether the origin has those commits: that is the user's concern. A loss that
P cannot rule out is listed with `certain: false`. What is already lost before the change runs, such as the
contents of a missing runtime, is listed in `gone` instead: the CLI prints it
as information, and it never starts the countdown.
[plan-shape.md](plan-shape.md) defines the plan and its loss items.

What the CLI does:

| | Plan loses nothing | Plan loses work |
|---|---|---|
| Without `--force` | Print the plan, ask, then run. | Print the plan with the loss warning, ask, then run. |
| With `--force` | Print the plan and run at once. | Print the plan with the loss warning, count down 10 seconds, then run. Ctrl-C during the countdown cancels, and nothing is sent. |

The loss warning is a large block, in bold red on a terminal, that lists every
item in `loss`. An illustrative example:

```text
$ p session delete 4f1c… --force
Delete session 4f1c… (project team/app, branch work)

!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
!!  THIS WILL LOSE WORK
!!
!!  - the container and everything in it: uncommitted changes, files
!!    outside Git, commits not pushed to P; P does not look inside
!!  - 2 commits on branch work will be removed from P's repository;
!!    P does not check whether the origin has them
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

Deleting in 10 seconds. Press Ctrl-C to cancel.  10 9 8 7 …
```

The countdown runs in the CLI, before the run call is sent. If the state
changes during the countdown, the daemon refuses with `precondition_failed`;
the CLI then makes a new dry run, prints the new plan and stops without
running it, so a forced run never acts on a plan nobody saw.

The daemon has no force option. A change that is not allowed is refused with or without `--force`.

The TUI keeps asking before each risky change, shows the same loss warning,
and offers no force.

## Options set aside

| Option | Why not |
|---|---|
| Keep random tokens and write the preview store once | The daemon still holds state: previews die on restart, and the store needs a size limit and an expiry. |
| A signed token that carries the whole plan | A plan can reach about 63 KB, the run must still recheck the state, and the daemon gains a signing key to manage. |
| A UUID as the tag | P already names sessions, operations and the instance with UUIDs, so a tag shaped like one invites mix-ups. A random UUID adds nothing when nothing is stored, and a UUID derived from a hash is a shorter hash in a misleading shape. |
| The change hash itself in the tag, next to the bound hash | Anyone could recompute the bound hash after editing the expiry, so the binding would protect nothing. |
| The expiry and the facts in one hash (an earlier draft) | Binds the expiry as well, but leaves no stable name for the change: the journal could not record which change ran independent of when it was previewed. |
| The expiry outside any hash (an earlier draft) | Set aside by the maintainer on 2026-11-02: the expiry should be bound to the change. |
| No expiry | Set aside by the maintainer on 2026-11-02: a confirmation should follow a recent preview. |
| One `plan.preview` method for every change (an earlier draft) | Its params and its plan would be a union of all eleven changes' shapes, so it could not be documented or typed on its own. |
| An explicit `dry_run` flag | Not needed: the call without `key` or `if_match` is already the dry run, and a `key` without `if_match` catches a forgotten tag. |
| A preview method per change, as today | Eleven more methods doing what the dry run already does. |
| `if_match: "*"` | A risky change would run a plan nobody saw, which the loss warning rule forbids. |
| Force in the daemon, skipping the check | A second way to run a change, and the journal would hold operations whose plan nobody checked. |
| `--force` only for changes that cannot lose work | Set aside by the maintainer: forcing should work everywhere. The loss warning and the countdown keep PROJECT.md's success signal, "destructive actions explain what will be lost before proceeding." |

## How it will be checked

- Unit tests for each kind of change: two previews of the same state give the
  same change hash; changing any fact in `state` changes it; editing the
  tag's expiry breaks the tag; an expired tag is refused with
  `precondition_failed`; a changed state is refused with `precondition_failed`;
  a dry run of a change that is not allowed returns its plan and refusals but
  no change tag; no error carries a change tag or a plan; a call with a `key`
  but no `if_match` is refused with
  `precondition_required`; a call with `if_match` but no `key` is refused as
  invalid; a tag that expires while
  the run re-checks the state is refused at admission; a re-created cached
  image with otherwise identical facts gets a different change hash; a dry
  run starts no operation and destroys nothing; a tag for one target or action cannot
  run another; key replay works after a daemon restart.
- CLI tests for `--force`: a plan that loses work prints every `loss` item and
  sends the run call only after 10 seconds; Ctrl-C during the countdown sends
  nothing; a plan that loses nothing runs at once; a refusal after the
  countdown prints the new plan and stops.
- The VM scenarios that use previews (session removal, the repairs, project
  deletion, publication) move to the new methods and keep their outcome
  checks.
