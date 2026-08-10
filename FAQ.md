# P — FAQ and decision record

Questions that produced requirements, with the reasoning attached. This is the document to read before arguing with a design choice, and the one to update when a choice changes.

**Convention:** **Decided** — settled. **Direction** — proposed, likely, changeable. **Open** — genuinely undecided, listed again at the bottom.

---

## Contents

- [Product and scope](#product-and-scope)
- [Sessions and lifecycle](#sessions-and-lifecycle)
- [Git as the interface](#git-as-the-interface)
- [Backends and isolation](#backends-and-isolation)
- [Configuration](#configuration)
- [Status and notifications](#status-and-notifications)
- [Security](#security)
- [Open decisions](#open-decisions)

---

## Product and scope

### What problem is P actually solving?

**Bookkeeping.** Not isolation — isolation is the enabling mechanism, not the point.

With several projects and several agents, the expensive failure is an agent that blocked on a question at 14:02 and sat there until 09:04 the next morning because nothing told you. P exists to make the set of in-flight work visible and to interrupt you when a piece of it needs a human. Everything else in the design serves that.

**Decided.**

### Does `p` on my laptop show sessions on my desktop?

**Yes, if the desktop is one of your backends — no, if it's running its own P instance.**

This distinction matters and is easy to get wrong. P is always a *local control plane*. What's pluggable is where a session physically *runs*:

- A session you placed on `desktop` via the `ssh-container` backend is **your** session. It appears in your overview with live state, and you attach to it from your laptop.
- A session someone (including you) created from the desktop's *own* P instance is not yours to see. P instances don't federate as control planes.

What crosses between P instances is git — see [P instances are remotes](#why-are-p-instances-just-git-remotes).

**Decided.**

### Why not federate P instances properly?

Because it buys little and costs a lot. Federating control planes means an identity model, a sync protocol, conflict handling, and a security boundary between machines you'd have to get right before anything works. The remote-backend model gets you "run this on the big machine and watch it from the laptop" with an SSH connection, and the git-remote model gets you "continue that work over here" with a fetch.

If both machines genuinely need to be independent control planes, the honest answer today is: run `p` on each, and move work with git.

**Decided for v1.** Multi-user shared backends are the intended arc.

### Who is P for?

A solo developer with several repositories and several agents. **Decided.**

Team use — shared backends, handoff, who-owns-what — is the arc the design leaves room for, not a v1 claim. The backend abstraction and the per-session git authorization were both chosen with multi-user in mind, but no part of v1 is designed or tested for more than one person.

### Why is there no real CLI?

**Decided: the TUI is the human interface. Decided: it is only a client.**

This was originally framed as "TUI vs. CLI," which is the wrong question — that's a presentation choice, and the TUI wins it easily. P is a thing you live in, the overview is the product, and maintaining a parallel flag-driven surface doubles what has to be designed, documented, and kept consistent.

The question underneath is **whether a complete action layer exists at all**, and it has nothing to do with the TUI:

- If session lifecycle lives in the TUI's event handlers, "add a CLI later" is a rewrite — and *every* future surface is blocked behind it.
- If the TUI is a client of a complete action layer, TUI-first costs nothing and future surfaces are thin adapters.

So: `sessions.create`, `attach`, `name`, `publish`, `stop`, `discard`, `delete` are core operations. The TUI calls them. `p api` calls them. The keymap is a mapping table, not an implementation.

**This is an architecture constraint, not a product one.** It is the thing to enforce in review.

### What can a session ask P to do?

**Decided: report its own status, and act on itself. Nothing else.**

| | |
|---|---|
| **Allowed** | status reporting; name or annotate *itself* |
| **Denied** | `sessions.create`, `sessions.attach`, anything touching another session or another project |

The host and a session are **different audiences with different risk**, and the mistake would be serving them the same API:

| Caller | Position | Risk |
|---|---|---|
| You, your scripts, your status line | on the host, trusted | none |
| A session — i.e. possibly an agent | inside the isolation boundary | a session that can create sessions has moved session creation *inside* the boundary; an agent that can spawn work can spawn unbounded work, or reach a project it was never given |

Scoping is by the per-session credential P already injects for git — no second auth mechanism.

Act-on-self is expressible over git, which means it needs no client library at all:

```console
$ git push p HEAD:refs/p/name/fix-dup-invoice-rows
```

**The obvious objection — "then how does an agent parallelize?" — is answered elsewhere:** it [already can, inside its own session](#can-an-agent-use-worktrees-or-spawn-subagents-inside-a-session), and it can [hand back multiple branches](#if-an-agent-fans-out-internally-can-it-hand-back-several-branches). What's denied isn't parallelism; it's *crossing the boundary* to get it.

That narrows the residual question considerably. A session would only need `sessions.create` to obtain something its own container can't give it — a different environment, a different backend, more resources. **Open**, and it wants a quota and a parent-child model before it's safe.

### Does P launch agents for me?

**No, not in v1. Decided.**

`n` gives you an isolated workspace with your project's environment and attaches you. You start whatever you want in it. P's relationship with agents in v1 is one-directional: it *reads* their status.

Agent launching (`p new -m "fix the invoice bug"`) and fan-out (three sessions racing the same task) are deliberately deferred. They're additive — they don't change the session model — so deferring them costs nothing architecturally.

### Can an agent use worktrees or spawn subagents inside a session?

**Yes, and P has nothing to say about it. Decided.**

P draws exactly one boundary: between sessions, and between a session and the host. It draws **none inside a session**. An agent may spawn subagents, `git worktree add` for parallel attempts, run background processes, bind ports, install things. Claude Code, Codex, and others do this natively. P neither provides these capabilities nor prevents them — they happen inside a boundary that already exists, and their blast radius is already the session.

This is the reason several features that look missing aren't:

| Not in P | Because |
|---|---|
| Worktree management | The agent has worktrees. Inside its container they're free and isolated already. |
| Fan-out / racing attempts | The agent can run parallel attempts itself. |
| Agent orchestration | Agents orchestrate themselves. P is the layer underneath. |

The two things a session genuinely *cannot* do for itself are exactly the two P governs: **reach outside its container**, and **write refs it wasn't given**.

### If an agent fans out internally, can it hand back several branches?

**Yes — a session owns a namespace, not a single ref. Decided.**

A session is a specific piece of work, and there's often more than one way to do it. With a single-ref rule, an agent running three candidate fixes in three worktrees could only push one: its internal parallelism would be a technique with no exit, and it would have to converge before handing back anything at all.

```text
  ○ billing   fix/dup-invoice-rows      idle
      └ /attempt-a                      ✓ test  ✗ integration
      └ /attempt-b                      ✓ test  ✓ integration
```

Since [checks run per-push](#can-a-push-trigger-work-outside-the-container), each approach gets validated independently and you can compare them on evidence rather than on description.

### If a session can push several branches, what gets published?

**Only the session's name. Decided — and this is the rule that makes the namespace safe.**

There are two boundaries here and they take different scopes:

| Boundary | Scope |
|---|---|
| Session → P's git server | its name **and** `name/*` |
| P → any remote (`origin`, `desktop`) | its name **only** |
| Everything else | denied |

**`name/*` is exploration, not output.** It exists so approaches can be built, checked, and compared. It's working state, and it never leaves the machine — not to GitHub, not to your desktop, not under any sync policy, because sync policy operates on session names.

What publishes is the session's name: its answer.

Two consequences worth being explicit about:

- **Attempts don't travel.** Move the work to another machine and you get the answer, not the exploration behind it. Consistent with the rest of the design — the durable unit is the commit you decided on.
- **Attempts don't survive discard.** They exist to serve active work, so [discard removes them](#whats-the-difference-between-stop-discard-and-delete) along with the machinery. The agent merges the winner into the name before then; P lists what it's deleting so you can catch it if not.
- **Something has to turn an attempt into the answer.** That's [the agent's job](#if-discard-deletes-my-attempts-how-do-i-keep-one) — merge or fast-forward the name onto the attempt it picked, then publish. P deliberately doesn't do it for you.

Nothing that mattered was given up by widening from one ref to one subtree. A session still cannot reach another session's work, force-push, write tags, or touch `main` — the isolation property was always about *whose* refs, never *how many*.

### Why is the TUI fzf-shaped instead of vim-modal?

**Because typing should never be able to destroy anything. Decided.**

vim and fzf disagree about what typing does — in fzf it filters, in vim letters are commands — and you can't have both on the same keys. P chose filtering.

The deciding argument wasn't authenticity, it was blast radius. Discard and delete are the two irreversible operations in P, and in a vim-modal list a stray `d` in normal mode opens a discard prompt. Under filter-first typing, a stray key narrows a list. Nothing else.

That has a cost, and it's the usual one: **plain letters can't be verbs**, so there are two modes anyway — filtering, and normal mode where `hjkl` navigates.

**What keeps the mode from mattering: every action is `Ctrl`+letter and works in both.** The mode decides what plain letters do; it never decides what you can do. You never have to check which mode you're in before acting.

### Do arrow keys work?

**Everywhere. Decided.** Arrows navigate in normal mode and while filtering, and so do `^N`/`^P` — down and up in both fzf and readline. `hjkl` is the same movement for hands that prefer it, in normal mode only. No hand is required to learn the other's keys.

This produced a rule worth stating on its own: **navigation keys are never actions.** An early draft put *new session* on `^N` and *publish* on `^P`, which meant an fzf navigation reflex would open a session-creation flow or, worse, a publish picker — the one action that pushes to GitHub. Both moved: new is `^O`, publish is `^U`. A key someone presses to move must never open a dialog.

### Why isn't `hjkl` on `Ctrl` like everything else?

**It can't be.** `^H`, `^I`, `^J` and `^M` *are* Backspace, Tab, LF and CR at the terminal level — a terminal cannot distinguish `Ctrl`+`h` from Backspace. Any design putting navigation on `Ctrl`+`hjkl` is broken before it starts.

So `hjkl` is unprefixed, in normal mode, and `h`/`l` carry vim's spatial sense rather than sitting dead: `l` drills in (attach), `h` backs out (clear the filter, leave the preview).

Related: `^S`/`^Q` are XON/XOFF flow control on many terminals and may never reach the application. The sort binding lives on `^S` because it's the best mnemonic available, and it's the first thing to remap if your terminal eats it.

### Why a flat list instead of grouping by state?

**Because grouping and live filtering fight each other. Decided.**

Group headers give shape at rest, but the list is filtered continuously and updated by agents changing state. Groups empty out, headers vanish, and rows jump between sections while you're reading them.

A flat list ranked by urgency keeps blocked work on top without a section header to announce it, and it behaves identically filtered or not. `^S` cycles to age, project, name, or backend when you want a different question answered.

### Can I change the keys?

**Yes. Decided.** Every binding is a default, remapped in `~/.config/p/config.nix` under `ui.keys`, with multiple bindings allowed per action — so you can add a normal-mode letter alias next to the `Ctrl` chord if you prefer.

P validates the whole map at startup. A key claimed by two actions is an error at load, not a discovery at 2am when you meant to stop something and deleted it. That check also enforces the navigation rule: binding an action over a movement key is rejected.

### Why not just tmux and a naming convention?

That's the current situation. It gives no state visibility, no isolation, and no lifecycle: nothing distinguishes a live agent from a dead one, nothing stops two of them colliding, and nothing tells you when one is stuck.

---

## Sessions and lifecycle

### What exactly is a session?

**A specific piece of work to be done.**

Not a container — the container is machinery. A session has a name (its durable identity), a namespace (`name/*`, where it may try several approaches), a base commit, and, while it's live, the machinery to work in: an isolated clone, a container, and tmux on some backend.

The machinery is rebuildable and disposable. The name and its commits are what last, and what cross machines.

### What's the difference between a scratch session and a named session?

A **scratch session** hasn't been named yet. It's created from a commit, identified by timestamp (`tmp 09:12`), cannot push, and is meant to be thrown away. Most work starts here, because at 09:12 you usually don't yet know what the work *is*.

A **named session** has been given its name. That name is its cross-machine identity, its only publishable ref, and the root of its namespace.

### Why can't a scratch session push?

It has no name, so there's no ref it could safely write to. Inventing one means P silently naming your work — and half the time the name would be wrong, because the whole reason the session is scratch is that you didn't know yet.

Naming it is one keystroke, available the moment you do know. **Decided.**

### Why doesn't naming a session create a commit?

Committing is a judgment about what's worth recording, and the working copy at naming time is usually half-finished. P assigns identity; you decide history. **Decided.**

The important property is that naming is *free*: the container keeps running, the agent keeps its conversation, tmux is untouched, nothing restarts. If it were expensive you'd avoid it, and you'd be back to naming things up front — before you know what they are.

### Where does my `~/src/billing` checkout fit?

**It's a spoke, like everything else. Decided.**

P's git server is the hub. Your checkout is a working copy with two remotes — `origin` and `p` — and it is not privileged:

```text
      ~/src/billing  ──┐   you, unrestricted
                       │
   session ──1 branch──►  p://billing  ◄──►  p://desktop
                       │
                       └──►  origin (github)
```

You pull from either remote and push to either. Getting hand-written work into a session is `git push p` — literally the same operation a session performs, because you are the same kind of spoke, just without the restrictions.

This resolves a question the design spent a while circling: *is P's server canonical, or is my checkout?* Neither, by privilege. P's server is canonical because it's the hub, and your checkout earns nothing for being yours. A project with no host checkout at all works exactly the same.

### Why doesn't my dirty working tree come along?

Because a session's workspace is **cloned at a commit**. It is never a copy of, or a mount of, your checkout.

This falls out of [git being the code interface](#why-is-the-git-server-the-interface-between-sessions-and-p): a container has exactly one channel for code, and that channel speaks commits. Uncommitted state has no representation in it.

**The failure that actually bites is the second-order one.** Uncommitted changes going missing is at least legible — the file is obviously not as you left it. But this:

```console
$ cd ~/src/billing
$ git commit -am "refactor invoice aggregation"   # committed, but only here
$ p .                                              # n
```

...gives you a session based on a commit *older than what you have*, and nothing looks wrong. The code is complete and coherent, it just predates your work, and the agent starts confidently modifying something you already restructured.

**Decided:** P warns at session creation rather than syncing behind your back.

```text
  new session · billing · from main (a3f19c2)
    ⚠ ~/src/billing is 1 commit ahead (c4d1e08)
      3 files uncommitted — never included

  [enter] continue   [u] push checkout to P first
```

`u` runs the push you would have run yourself. P never reaches into your checkout on its own — it's your spoke, and mutating it silently would break the only rule that makes the topology comprehensible.

**Open:** a `--from-worktree` that commits your dirty tree to a temp ref so a session can start from uncommitted work. Solves the remaining case at the cost of temp refs to garbage-collect. Not in v1.

### Does P ever clean up on its own?

**No. Decided.** Sessions last until you remove them. A session idle for a month is still there when you come back.

Automatic reclamation eventually deletes uncommitted work someone cared about, and the day it happens is the day you stop trusting the tool. Disk is cheaper than that.

### What's the difference between stop, discard, and delete?

**Three verbs, an escalation ladder. Each destroys strictly more than the last. Decided.**

| | `^T` stop | `^D` discard | `^X` delete |
|---|---|---|---|
| Container | stopped | removed | removed |
| Working copy + uncommitted | preserved | **lost** | lost |
| `name/*` attempts | kept | **deleted** | deleted |
| `name`, unpublished | kept | kept | **deleted** |
| `name`, published | kept | kept | kept |
| Session still exists? | yes | **yes** | **no** |

In one line each: **`^T` stop** pauses it, **`^D` discard** reduces it to its answer, **`^X` delete** ends it.

An earlier draft had only stop and discard, which conflated two different intentions — *"I'm done running this"* and *"I'm done with this work"* — and the missing middle rung is the one you'd use most.

**`^D` discard reduces a session to its answer: the name and its commits.** Once work is committed, a container and an isolated clone are just disk, several gigabytes per session, and the whole premise is that sessions are cheap enough to create freely. The attempts go too, because `name/*` exists to serve *active* work — attempts with no container to work them in are clutter, and a finished session that still carries every dead end that led to it isn't finished.

**`^X` delete ends the work.** The rule is: **delete removes what only ever existed here.** Attempts never left the machine, so they go. An unpublished name never left either, so it goes. A published name exists on a remote — publishing is precisely what made it exist elsewhere — so it stays as an ordinary project branch. What's left isn't a session; it's a branch, and you'd start a new session from it.

An earlier draft claimed a branch always survives removal. That was wrong in the dangerous direction: it implied naming a session made it safe to throw away, when a named-but-never-published session is exactly as destructible as a scratch one.

Both destructive verbs itemize rather than asking yes/no about an abstraction — P shows the specific refs and files at stake before you confirm.

### If discard deletes my attempts, how do I keep one?

**Merge it into the name — and that's the agent's job, not P's. Decided.**

Trying three approaches and deciding which one is the answer is *the work*, indistinguishable from writing them in the first place. It follows directly from [the agent being sovereign inside its session](#can-an-agent-use-worktrees-or-spawn-subagents-inside-a-session): P draws no boundaries in there, which also means it doesn't finish a session's git work on its behalf.

So in the normal flow the agent converges before it's done, the name holds the answer, and discard deletes branches already contained in it.

**P's obligation is narrower: never delete a branch without showing you what it is.**

```text
    deleting 2 branches:
      fix/dup-invoice-rows/attempt-a  2 commits
      fix/dup-invoice-rows/attempt-b  4 commits
```

That list is the guard. If four commits sit on `attempt-b` and you know nothing merged it, don't confirm — attach and merge, or delete on purpose.

We considered P offering an "adopt this attempt" keystroke, which it could do server-side without a container since it owns the refs. **Rejected:** it puts P in the business of authoring commits, which is the line the whole design holds — [P assigns identity, you decide history](#why-doesnt-naming-a-session-create-a-commit). A confirmation that's honest about what's being deleted does the job without crossing it.

### Why is a discarded session the same as one from another machine?

Not merely similar — **the same shape.** After discard, a session holds exactly what would cross to another machine: its name and its commits. No machinery, no attempts, nothing local left over.

```text
  ─ billing   fix/dup-invoice-rows    no machinery    discarded 2h ago
  ─ billing   feat/dunning-emails     no machinery    via desktop
```

Both show `─`, and `↵` on either builds a workspace from committed state. This isn't a coincidence to be papered over — it's a sign the model is right. There's no "restore" concept in P because restoring a local session and materializing one from a remote are the same operation, and implementing them separately would mean two code paths that must agree forever.

It also means discard is *safe in the way that matters*: whatever you can do with work that arrived from another machine, you can do with work you discarded.

### Can two machines have a session on the same branch?

Yes, independently. P doesn't lock or synchronize them, and normal git coordination applies — the second one to publish deals with the divergence.

**Decided.** Locking would require a coordination layer P deliberately doesn't have.

### Why can't I move a running session to another machine?

Moving one means moving container state, running processes, tmux layout, and agent memory. Every serious version of that is a migration system.

P's answer is that **the durable unit is the commit**. Push the branch, create a session on the other machine, and you have the work — you don't have the agent's conversation, and P is explicit that you never will.

**Decided.**

### What if I have forty sessions?

The overview is grouped by state and sorted so anything blocked is at the top, always. `/` filters by project, branch, or state; `g` regroups by project when you want the old mental model.

**Open:** whether idle sessions should collapse by default past some count, and whether P should ever suggest discarding old ones (suggest, never act).

---

## Git as the interface

### Why is the git server the interface between sessions and P?

Because it's a contract that already exists, that every tool already speaks, and that needs no agent-side integration.

The rule is **code over git, everything else over the API**. P does expose a small API — status events, notifications — but nothing about a session's *work product* travels over it. That's what keeps the boundary honest: no client library for code, no version skew, no reason for the tools inside a container to know P exists.

Instead:

- A session **clones** its workspace from P at a base commit.
- A session **pushes** its name, and anything under it, back to P.
- That is the whole contract.

It also means the security boundary and the data boundary are the *same* boundary. There's exactly one channel out of a session, and it's one where P can enforce policy per-ref. Nothing to audit twice.

**Decided.**

### Why not let containers push straight to GitHub?

Two reasons.

**Credentials.** A container running agent code would need a token that can write to your repositories. The whole point is that it has no such thing.

**Policy.** You cannot ask GitHub to reject a push from *this container* to any ref other than *that branch*. You can ask a server you run. A session can't force-push, can't touch another session's branch, can't push tags, and can't rewrite history it didn't create — because the server it talks to won't accept it.

**Decided.**

### Why are P instances just git remotes?
<!-- see also: hub and spoke, above -->


Because once the interface is git, machine-to-machine transfer is a solved problem and P shouldn't reinvent it.

```text
    session ──git──► P (laptop) ──git──► P (desktop)
                        │
                        └──git──► origin (github)
```

Sending work to the desktop is a push to a remote called `desktop`. `origin` is not special — GitHub is one remote among several, and the same publish step covers both. Your ordinary `~/src/billing` checkout can add `p://billing` as a remote and pull a session's branch with no cloud round trip.

This is why P needs no sync protocol, no state replication, and no message bus between machines. Git already is one.

**Decided.**

### Why is publishing explicit when fetching isn't?

Asymmetric consequences. A fetch changes nothing anyone else sees. Publication does — and automatic outbound publication from a container an agent is driving is precisely the failure everyone is afraid of.

**Decided:** the daemon fetches remotes in the background. Outbound is governed by [sync policy](#does-p-sync-with-origin).

### Does P sync with `origin`?

**Fetch yes, push no. Decided — and the reasoning here corrects an error in the original draft.**

The draft treated every outbound push as equally dangerous. That's wrong: **the risk isn't pushing, it's pushing somewhere other people can see.** Moving a branch from your laptop's P to your desktop's P is not a publication event; it's moving your own work between your own machines. Collapsing those two made publishing feel heavier than it needed to be.

So sync policy is **per-remote**:

```nix
p.sync = {
  origin  = { fetch = "always"; push = "never"; };  # publication — always your decision
  desktop = { fetch = "always"; push = "all";   };  # your machine — converge freely
};
```

Modes are `never`, `published` (only branches published at least once), and `all`.

### After I publish a branch once, does it keep syncing to GitHub?

**No. Decided.**

Publishing does **not** establish a tracking relationship. Publish, let the agent commit again, and the branch sits at `1 ahead of origin` until you press `P` again.

The tempting alternative — first publish is the decision, subsequent commits ride along — is how git normally feels, and it's wrong here. In normal git a *human* runs each push. Under auto-sync, an agent's later commits would reach GitHub with no human in the loop at any point, on the strength of a decision you made about a different commit. That's the exact property the isolation model exists to prevent, given away for a keystroke.

It will be mildly tedious on a branch you're iterating on. That's the price, and it's the right one.

### Is there one git server, or one per backend?

**One per backend. Decided.** A session's git endpoint is provided by the backend it runs on, so a session always talks to something local to it — no tunnel back to your laptop from a remote host or a cluster.

The consequence: a branch created on backend A isn't visible from backend B until it's relayed. The control plane does the relaying, using the same remote mechanism as everything else.

**Open:** whether the control plane should relay eagerly (fetch all backends into a single view) or on demand (only when you ask for that branch). Eager is nicer in the overview; on demand is much cheaper with several backends.

### What about a project with no `origin`?

Fine. P's git server holds the canonical copy; `origin` is optional and can be added later. A local-only project loses nothing except the ability to publish there.

**Direction.**

### Can a push trigger work outside the container?

**Yes — this is a first-class part of the design. Direction, with a minimal version in v1.**

P's git server sits on the *host* side of the isolation boundary, so a push is not just a write, it's an **event P can act on** with resources the session doesn't have:

```nix
p.checks.integration = {
  on = [ "push" ];
  backend = "vm";                 # provisioned per run, outside the session
  run = "./scripts/integration.sh";
};
```

Results come back through the [status protocol](#how-does-p-know-a-session-is-blocked) — visible in the overview next to the session — and as refs the session can fetch (`refs/p/checks/<name>`).

**Why this matters more than it first looks:** normally, letting an agent verify its own work means giving it access to the infrastructure that verifies. Here the agent gets the feedback and never gets the reach. The check runs somewhere the container cannot see, with credentials it never held, triggered by the one action it *is* allowed to take. The isolation boundary and the feedback loop stop being in tension.

**v1:** host-side commands on push. **Later:** provisioning a backend per check, which arrives with the `vm` backend.

### Could the git server do more than run checks?

**Open, and interesting.** If pushes are already events, pushing to a magic ref could *trigger P actions* — `git push p HEAD:refs/p/name/fix-invoice-rows` to name a session from inside it, Gerrit-style.

That would give sessions (and agents in them) a way to act on P without a CLI, which is exactly the gap the [TUI-first decision](#why-is-there-no-real-cli) creates. Not in v1, but it's the natural place to solve that problem if it becomes real.

---

## Backends and isolation

### Why containers per session rather than git worktrees?

Worktrees isolate *files*. They don't isolate processes, installed packages, listening ports, or the blast radius of an agent running `rm`, a migration, or a dev server. Two agents in two worktrees still share a machine.

The container is what makes "let it run unattended" a reasonable thing to do — which is the premise of the whole product.

**Decided.**

### Why is the backend an abstraction rather than just "containers"?

Because the same abstraction answers three different questions with one mechanism:

- *Where does this run?* — locally, or on the big machine over SSH.
- *How isolated is it?* — container now, micro-VM later, worktree as an escape hatch.
- *What can I add later?* — Kubernetes, cloud VMs.

Getting this boundary right in v1 means VM-per-session is a new backend, not a rewrite.

**Decided.** v1 ships `local-container` and `ssh-container`.

### How does P know which sessions exist?

**Self-describing backends. Decided.** There is no session database.

Every container carries P metadata as labels and every workspace carries a manifest. `p` enumerates each configured backend and reconstructs the world. Nothing can desync; a container removed behind P's back simply stops existing.

The costs, stated honestly: startup is as slow as your slowest backend, and an unreachable backend shows as `⚠ unreachable` rather than pretending its sessions are gone. Both are better than a local index that's quietly wrong.

### What platform does P run on?

**Linux. Decided.**

Native containers, native Nix, devShells built for the architecture they'll run on. No VM in the path, no cross-architecture store, no linux builder.

This was the right call largely because of what it removes. An earlier draft targeted macOS alongside Linux, and every hard problem in that draft was a darwin artifact: a Linux VM in the path, `aarch64-darwin` Nix building `aarch64-linux` images, a linux builder to make it possible, and a real risk that a first session would take minutes — which would have quietly killed the premise that `n` is free.

### Then how do macOS and Windows work?

**They don't run P. They run Linux. Direction.**

The P instance — daemon, git server, containers — lives in a Linux VM. The TUI runs on your host and talks to it.

```text
   macOS host                      Linux VM
   ┌──────────┐                    ┌──────────────────────────┐
   │  p (TUI) │ ──── socket ─────► │  daemon                  │
   └──────────┘                    │  git server              │
                                   │  containers ── sessions  │
                                   └──────────────────────────┘
```

This is a deployment story, not a port. One code path, one container runtime, no cross-architecture builds, and nothing platform-specific in P itself.

It works only because [the TUI is already a client of the API](#why-is-there-no-real-cli) — a decision made for entirely different reasons that turns out to pay for cross-platform support.

**It does impose one v1 constraint.** The API transport cannot assume the client and daemon share a kernel: it has to work over a forwarded socket, vsock, or SSH. Cheap to design in now, painful to retrofit — so it belongs in v1 even though macOS support doesn't.

This also matters immediately rather than later, because the author's own machine is darwin. Dogfooding v1 means running the instance in a VM from day one, which is the same path macOS users will take.

### How does a `devShell` become a session image?

**Open**, though much smaller than it was on darwin. Candidates:

1. `pkgs.dockerTools.buildLayeredImage` from the devShell's inputs — clean and reproducible, potentially cache-heavy.
2. Generic base image plus a per-session Nix store path bind-mounted in — fast, couples the container to the host store.
3. Share the Nix store read-only and run `nix develop` inside the container — simplest, weakest store isolation.

Still worth measuring before v1: `n` has to feel free, and it's the only remaining thing that could make it not.

### Why Nix `devShell` as the environment source?

It's the environment definition that's already declarative and reproducible enough to build a container from without guessing. Everything else — `Dockerfile`, `devcontainer.json`, language heuristics — is a later provider.

**Decided for v1.**

---

## Configuration

### Why is configuration written in Nix?

Because it's the language the author already uses for everything else, and because a P config that's a Nix expression composes with the modules already describing these machines. **Decided for v1.**

### Doesn't that lock out everyone who doesn't use Nix?

For v1, yes — and that's an accepted, temporary cost, not an architectural one.

**Decided:** configuration is loaded through a **provider interface** internally. Nix is the first implementation. TOML, `devcontainer.json`, and plain JSON are future providers that plug in without touching the session model, the backends, or the git server.

The thing to protect is that nothing downstream of the provider knows Nix exists. If backend code ever calls `nix eval` directly, the abstraction has failed.

### Where does project config live?

In the repo's `flake.nix`, as a `p` output. P evaluates it with `nix eval --json` and caches against the flake lock.

Instance config lives at `~/.config/p/config.nix`.

**Direction** on both paths; the mechanism is **Decided**.

---

## Status and notifications

### How does P know a session is blocked?

**A status protocol, with agents reporting into it. Decided.**

P injects `P_SESSION` and `P_STATUS` into every session; anything inside can append newline-delimited JSON state events. Claude Code reports through its own hooks — `Notification` → `blocked`, `Stop` → `idle`, `UserPromptSubmit`/`PreToolUse` → `running` — so the integration is configuration, not a wrapper.

### Why not just use heuristics?

Because the overview's entire value is that you *trust the marks*. A heuristic that's right 85% of the time produces a list you double-check, and a list you double-check is worth roughly nothing over `tmux ls`.

Pane inspection (foreground process, output quiescence) is the **fallback** for anything that doesn't report. If it's inconclusive, P shows `?`.

**Decided: P shows `unknown` rather than guessing.**

### What if a tool doesn't speak the protocol?

It shows as `running` or `?` depending on what the heuristic can tell. That's a real limitation and the reason the protocol is deliberately trivial — a shell one-liner appending JSON is a valid implementation.

### Why notify at all if there's an overview?

Because the overview only helps when you're looking at it, and the motivating failure is nineteen hours of *not looking*.

**Decided: both, configurable.**

- **Ambient** (default): a compact count for your prompt, tmux status bar, or editor, via `p api sessions.list`.
- **Push** (opt-in): the daemon fires on chosen transitions — `blocked`, `failed` — via desktop notification, `ntfy`, webhook, or an arbitrary command.

### Does P need a daemon?

Yes. Push notifications, the git server, the broker, and status streaming all need something running when `p` isn't. **Decided**, and worth naming as a real cost: P is a service on your machine, not just a command.

---

## Security

### What's the actual threat model?

An agent that does something destructive or careless — not a targeted attacker who has already compromised your machine. Concretely: an agent that runs a destructive command, mangles a branch, exfiltrates a key it found in the environment, or interferes with unrelated work.

P's answer is that the blast radius of a session is the session.

### Why a credential broker instead of just passing an API key?

Because a key in the container's environment is a key the agent can read, log, or send somewhere.

The broker holds credentials on the host, enforces the project's allowed upstream hosts, forwards the request, and attributes usage to the session. The container gets a base URL, not a secret.

**Decided, and in v1** — the author chose the complete security story over shipping sooner.

### If outbound network is open, what does the broker buy?

It buys **credential containment**, not network containment. An agent with open network but no key can't spend your money or act as you; an agent with a key and no network still can't do much. The two are separable, and P closes the more damaging one first.

**Direction:** default-deny outbound with a project allowlist. Not v1 — it breaks too many ordinary things (package fetches, documentation lookups) to enable before there's real usage data.

### What isolation does *not* protect

Stated so it isn't discovered later: running processes, tmux state, agent conversations, package caches, and container filesystem state are not preserved and never cross machines. Only committed git content does.

---

## Open decisions

| # | Area | Question |
|---|---|---|
| 1 | **devShell → session image** | Which of the three approaches, with a measured first-run time. `^O` has to feel free — this is the only remaining risk to that premise. |
| 2 | **API transport** | Socket protocol and encoding; how the session-scoped slice is enforced at the boundary; and it must work across a VM boundary (forwarded socket, vsock, SSH), since that's how macOS will run. A v1 constraint even though macOS isn't a v1 target. |
| 3 | **Name** | `p` is a placeholder. Unsearchable and likely to collide; a real project name with `p` as the binary is the likely resolution. |
| 4 | **Cross-backend relay** | Eager or on-demand fetching between per-backend git servers. |
| 5 | **Session credentials** | The exact per-session credential injected for git auth, and its lifetime. |
| 6 | **Git server implementation** | Which server, which transport, and where the policy hooks live. |
| 7 | **Broker scope** | Which providers in v1, and how streaming responses are handled. |
| 8 | **Check semantics** | Do checks block a publish, or only annotate? What happens to a check still running when the session is discarded? |
| 9 | **Git as control interface** | Which P *actions* (beyond naming and checks) are worth expressing as magic-ref pushes. |
| 10 | **Subordinate sessions** | Whether an agent may spawn child sessions to get what its own container can't give it — a different environment, backend, or resources. Needs a quota and a parent-child model first. |
| 11 | **Overview at scale** | Collapsing `─` sessions past a threshold; whether P ever *suggests* discarding machinery from long-idle sessions (suggest, never act). |
| 12 | **Dirty worktree** | Whether `--from-worktree` (commit to a temp ref, base a session on it) is worth it. |
| 13 | **New project bootstrap** | What `p .` creates in a directory that isn't a git repo yet. |
| 14 | **Services lifecycle** | When project-declared services start, stop, and what happens if one fails. |
| 15 | **Keymap defaults** | The bindings are Decided but unproven. `^D`/`^A` override readline, and `^S` may be eaten by flow control. Expect the defaults to move after real use. |
