# P

**A control plane for the work you have in flight.** One screen for every piece of work across every project, an isolated container for each one, and a notification when something needs a human.

> **Status: design.** Nothing here is implemented. This document describes the product we're working backwards from. See [PR.md](PR.md) for the press release and [FAQ.md](FAQ.md) for the decision record — including what is still open.
>
> **Convention:** **Decided** — settled during design. **Direction** — proposed, likely, changeable. **Open** — genuinely undecided; see [FAQ.md](FAQ.md).

---

## Contents

- [The idea](#the-idea)
- [Concepts](#concepts)
- [A day with P](#a-day-with-p)
- [Session lifecycle](#session-lifecycle)
- [The overview](#the-overview)
- [Command surface](#command-surface)
- [Configuration](#configuration)
- [Backends](#backends)
- [Git as the interface](#git-as-the-interface)
- [Status protocol](#status-protocol)
- [Isolation and security](#isolation-and-security)
- [Requirements](#requirements)
- [Non-goals](#non-goals)
- [v1 scope](#v1-scope)

---

## The idea

Work used to be organized in tmux sessions scoped to a project. That was fine when one project meant one thing at a time.

Two problems arrived together.

**Agents need a workspace that isn't yours.** An agent editing files and running commands in your normal checkout means you can't use that checkout while it works. Two agents in one repository is worse. So the number of live workspaces goes up sharply, and each one runs on its own schedule.

**Nothing shows you what's in flight.** Finding out what's happening means walking tmux sessions by hand and reading scrollback to guess whether an agent is working, finished, or stuck on a question you never answered. With three projects and six pieces of work, the friction isn't the isolation — it's the *bookkeeping*.

P solves the bookkeeping problem, uses isolation to make solving it safe, and uses git as the boundary between the two.

It does three things: it **knows** what work exists and tells you when a piece of it needs a human; it **places** each piece in an isolated environment on some backend; and it **governs** what that work can produce and where it can go. The first is the point. The other two make the first safe.

> **P shows you everything in flight across every project, lets you drop into any of it in one keystroke, keeps each piece of work in its own container, and tells you when something needs a human. Code moves over git — that's how sessions reach P, and how P instances reach each other.**

P is not an orchestrator. It doesn't launch work, schedule it, or sequence it. You and your agents do the work; P is where it's known.

---

## Concepts

| Concept | Meaning |
|---|---|
| **P instance** | The control plane on one machine: the TUI, a daemon, and a git server. One per machine. |
| **Project** | A git repository P knows about. P's git server is the hub; every working copy is a spoke. |
| **Host checkout** | Your ordinary `~/src/...` clone. A spoke like any other — same relationship a session has, minus the restrictions, because it's on your side of the boundary. |
| **Session** | **A specific piece of work to be done.** Its identity is its name. Its machinery — container, isolated clone, tmux, on a backend — is disposable and rebuildable; the name and its commits are what last. |
| **Session namespace** | `name/*` — where a session splits work across branches to try several approaches. Local to this machine; never published. |
| **Scratch session** | A session created from a commit, not yet named. Identified by timestamp. Cannot push. Disposable. |
| **Named session** | A session that has been given its name. That name is its durable, cross-machine identity. |
| **Backend** | Where a session physically runs. `local-container` by default; `ssh-container` in v1; `k8s` and `vm` later. |
| **Session state** | Running, blocked, idle, stopped, or without machinery — the thing the overview exists to show. |
| **P git server** | P's interface to sessions and to other machines. Sessions clone and push here; other P instances are remotes. |
| **Broker** | Host-side proxy holding model/agent API credentials, so containers never hold secrets. |
| **Publish** | The explicit host-side push of a session's name from P's git server to a remote — `origin`, or another machine. Only the name publishes; the namespace never does. |

### Session states

| Mark | State | Meaning |
|---|---|---|
| `●` | running | A process is producing output. |
| `◐` | blocked | An agent is waiting on a human. **This is the state the whole tool exists to surface.** |
| `○` | idle | Container up, nothing running. |
| `■` | stopped | Container exists but isn't running. Selecting it restarts and reattaches. |
| `─` | no machinery | The session exists as commits; nothing is running it here. Either you discarded it, or it came from another machine. `↵` rebuilds. |
| `⚠` | unreachable | The backend didn't answer. Sessions on it are unknown, not gone. |
| `?` | unknown | Session running, but not reporting status and no heuristic matched. |

---

## A day with P

### Tuesday, 09:04 — what happened overnight

```console
$ p
```

```text
  P · 3 projects · 6 sessions                   sort: urgency    ⚠ desktop unreachable
 ┌──────────────────────────────────────────┬─────────────────────────────────────┐
 │ ▸◐ api-gateway  tmp 14:02      19h local │ api-gateway/tmp 14:02                │
 │  ◐ billing      feat/dunning…   2h local │                                      │
 │  ● api-gateway  fix/token-re…   4m local │ state    ◐ blocked · 19h            │
 │  ● billing      feat/invoice…  22m desk  │ backend  local-container             │
 │  ○ api-gateway  chore/bump-d…   2h local │ base     main 7c1a904                │
 │  ■ dotfiles     tmp 09:41       3d local │ refs     — scratch, not yet named    │
 │                                          │                                      │
 │                                          │ blocked on                           │
 │                                          │   "The refresh endpoint returns 401  │
 │                                          │    for expired tokens — should I     │
 │                                          │    treat that as retryable?"         │
 └──────────────────────────────────────────┴─────────────────────────────────────┘
  > _                                          6 of 6   ^S sort  ↵ attach  ? help
```

Flat, ranked by urgency — blocked work is at the top without a header to announce it. The `◐` on `tmp 14:02` is the one that matters: an agent has been sitting on a question since yesterday afternoon.

The preview has already told you what the question *is*. You know whether this needs two seconds or twenty minutes before deciding to attach at all.

**You shouldn't have found this out by running `p`.** P should have told you at 14:02; see [Notifications](#notifications). The overview is for when you're already looking.

`↵` attaches you into that session's tmux, with the agent's prompt exactly where it stopped. You answer it, watch it resume, and hit your tmux detach binding — landing back in the overview, because that's where you came from.

### 09:12 — starting something new without leaving what you're in

A bug report lands against `billing`. You don't want to disturb `feat/invoice-pdf`, where an agent is mid-run.

```console
$ cd ~/src/billing
$ p .
```

`p .` scopes the overview to the repository you're standing in. Press `^O`:

```text
  new session · billing · from main (a3f19c2) · backend local-container

  ⚠ ~/src/billing is 1 commit ahead of p://billing (c4d1e08)
    3 files uncommitted — never included

  [enter] continue from a3f19c2   [u] push checkout to P first   [esc] cancel
```

P tells you what you're about to base the session on and what you're leaving behind. It doesn't reach into your checkout to fix it — `u` runs the push you would have run yourself. Press `u`, then enter:

```text
  ✓ config      flake.nix → .#p                        0.4s
  ✓ image       nix devShell → x86_64-linux            cached
  ✓ workspace   clone from p://billing @ c4d1e08       1.1s
  ✓ container   billing-tmp-0912                       0.8s
  ✓ tmux        p/billing/tmp-0912
  attaching…
```

You're in a fresh container with its own working copy — built from a commit, not copied from anywhere. It has no path to `~/src/billing` and none to the `invoice-pdf` session running beside it. You start an agent on the bug and detach.

Three sessions in one repository — one yours, two agents — none aware of each other.

> **Note:** a session's workspace is cloned at a **commit**. Uncommitted changes are never included, from any source. See [FAQ.md](FAQ.md#why-doesnt-my-dirty-working-tree-come-along).

### 11:30 — the exploration turned out to matter

The bug session found the cause and has a working fix. It's still a scratch session: created from `main`, no identity beyond a timestamp — you didn't know at 09:12 what the work would turn out to be. Now you do.

```text
  ▸◐ billing   tmp 09:12   blocked   3m   local
```

Press `^K`:

```text
  name this work
  › fix/duplicate-invoice-rows_

  ✓ named · may now push to fix/duplicate-invoice-rows and fix/duplicate-invoice-rows/*
```

That's the whole transition. The container keeps running, the agent keeps its conversation, the tmux layout is untouched. Nothing restarted, and **no commit was created** — you're naming the work, not deciding what's worth recording.

The session can now push. Inside it:

```console
[billing/fix-duplicate-invoice-rows] $ git commit -am "Fix duplicate rows in invoice aggregation"
[billing/fix-duplicate-invoice-rows] $ git push
```

That push goes to P's git server, not to GitHub. Nothing left this machine. The session may write its own name and the namespace beneath it, and nothing else; non-fast-forwards are rejected.

### 11:34 — publishing, deliberately

```text
  ▸○ billing   fix/duplicate-invoice-rows   idle   1 ahead of origin   local
```

Press `^U`:

```text
  publish fix/duplicate-invoice-rows →
    ▸ origin      github.com/you/billing
      desktop     p://desktop/billing
      workstation p://workstation/billing
  [enter] publish   [esc] cancel
```

P pushes from its own git server to the chosen remote using host-side credentials — credentials that were never inside the container. `origin` is not special: `desktop` is another P instance, and pushing there is how work moves between your machines.

### 13:00 — killing a dead end

```text
  ▸■ dotfiles   tmp 09:41   stopped   3d   local
  ^X

  delete dotfiles/tmp 09:41?

    container + working copy          removed
    4 files uncommitted               lost
    (no refs — never named)

  the session will no longer exist. [y/N] y
  ✓ deleted
```

`^X` because there's nothing here worth keeping. Had the work been worth keeping but not worth a running container, `^D` would have thrown away the machinery and left the commits.

Destructive actions are always explicit and always itemized — P shows you what you're about to lose rather than asking yes/no about an abstraction. **P never reclaims sessions on its own** — a session idle for a month is still there when you come back.

### Wednesday, desktop — same work, different machine

Different machine. No container, no tmux, no agent memory. What crossed was the commit.

```console
$ p billing
```

```text
  billing · desktop                             sort: urgency
   ○ feat/invoice-pdf          idle              1d   local
  ▸─ fix/duplicate-invoice-rows  no machinery      —   via laptop
```

The session is visible because this P instance has the laptop as a remote and fetched it. It shows `─` for the same reason a discarded session does: the work exists as commits, and nothing is running it *here*.

`↵` (or `l`) builds machinery for it locally. **It's the same session** — same name, same identity, same piece of work — now with machinery on two machines. What doesn't cross is everything the machinery held: the container, the tmux layout, the agent's conversation, the attempts in `fix/duplicate-invoice-rows/*`, and anything uncommitted. Those were only ever on the laptop.

### Friday, 17:20 — clearing a week of machinery

Twenty-three sessions have accumulated across five projects. Most are finished — work committed, published, done. What's left is containers holding disk.

`/` starts filtering; from there, typing narrows live:

```text
  P · 5 projects · 23 sessions                  sort: urgency
 ┌──────────────────────────────────────────┬─────────────────────────────────────┐
 │ ▸○ billing      fix/dup-invoic…  2d local │ billing/fix-dup-invoice-rows         │
 │  ○ api-gateway  fix/token-refr…  3d local │                                      │
 │  ○ billing      feat/dunning-e…  4d local │ state    ○ idle · 2d                │
 │  ○ parser       spike/pratt      5d local │ backend  local-container   1.2 GB    │
 │  ○ dotfiles     feat/zsh-comp…   6d local │ refs     fix/dup-invoice-rows        │
 │  ○ api-gateway  chore/bump-dep…  8d local │          ✓ published to origin       │
 └──────────────────────────────────────────┴─────────────────────────────────────┘
  /idle                                        6 of 23   ^A mark all   ^D discard
```

`^A` marks everything the filter left. `^D` discards all six at once — one confirmation, itemized per session:

```text
  discard 6 sessions?

    billing/fix-dup-invoice-rows      container + copy   1.2 GB   clean
    api-gateway/fix-token-refresh     container + copy   0.9 GB   clean
    billing/feat/dunning-emails       container + copy   1.1 GB   clean
    parser/spike/pratt                container + copy   0.4 GB   2 files uncommitted
    dotfiles/feat/zsh-completions     container + copy   0.3 GB   clean
    api-gateway/chore/bump-deps       container + copy   0.8 GB   clean

    deleting 3 attempt branches:
      billing/fix-dup-invoice-rows/attempt-a       2 commits
      billing/fix-dup-invoice-rows/attempt-b       4 commits
      parser/spike/pratt/attempt-a                 1 commit

    reclaims 4.7 GB · all 6 sessions survive as commits

  [y/N]
```

Two things that list is doing. It flags `parser/spike/pratt` as having uncommitted changes, so the one session where discard *isn't* free stands out. And it names every attempt branch about to go, because [that's the one thing discard destroys that isn't rebuildable](#stop-discard-delete).

Say no, attach to `parser/spike/pratt`, deal with those two files, come back, `^A`, `^D`.

### First run on a directory that isn't a project yet

```console
$ cd ~/experiments/parser
$ p .
```

```text
  ~/experiments/parser is not registered with P.
  ✓ git repository found
  ✗ no `p` output in flake.nix — using defaults
  register as project "parser"? [Y/n] y
  ✓ cloned into p://parser
  ✓ registered
  ^O  new session
```

---

## Session lifecycle

### Stop, discard, delete

**Decided.** Three verbs, an escalation ladder. Each destroys strictly more than the one before, and they are not interchangeable:

| | `^T` **stop** | `^D` **discard** | `^X` **delete** |
|---|---|---|---|
| Container | stopped, restartable | removed | removed |
| Working copy + uncommitted changes | preserved | **lost** | lost |
| `name/*` attempts | kept | **deleted** | deleted |
| `name`, unpublished | kept | kept | **deleted** |
| `name`, published | kept | kept | kept — project history now |
| Session still exists? | yes | **yes** | **no** |

In one line each:

- **`^T` stop** — pause it.
- **`^D` discard** — reduce it to its answer.
- **`^X` delete** — end it; anything unpublished is lost.

**`^T` stop** — the container isn't running. Nothing is lost, nothing reclaimed but CPU and memory. Fully reversible.

**`^D` discard** — the session is reduced to its answer: the name and its commits. Machinery goes, and so does the exploration, because `name/*` exists to serve *active* work. Attempts with no container to work them in are clutter, and keeping them would mean a session that's supposedly finished still carries every dead end that led there.

Since attempts are destroyed, discard lists every one of them before it does anything:

```text
  discard billing/fix-dup-invoice-rows?

    container + working copy          removed        ~1.2 GB
    working copy is clean             nothing lost

    deleting 2 branches:
      fix/dup-invoice-rows/attempt-a  2 commits
      fix/dup-invoice-rows/attempt-b  4 commits

    fix/dup-invoice-rows              kept

  [y/N]
```

**Merging the winning attempt into the name is the agent's job, not P's.** Trying three approaches and deciding which one is the answer is the work itself — the same work as writing them. P doesn't reach into a session's refs to finish it for you; it just refuses to delete branches without showing you what they are.

So the normal sequence is: the agent converges, the name holds the answer, and discard is throwing away branches already contained in it. If you see four commits on `attempt-b` in that list and you know nothing merged it, don't confirm.

**`^X` delete** — the work is over. Machinery, attempts, and any ref that only ever existed here:

```text
  delete billing/fix-dup-invoice-rows?

    container + working copy          removed
    fix/dup-invoice-rows/attempt-a    deleted — never published
    fix/dup-invoice-rows/attempt-b    deleted — never published
    fix/dup-invoice-rows              kept    — published to origin,
                                                stays as a project branch

  the session will no longer exist. [y/N]
```

**The rule for `^X`: delete removes what only ever existed here.** Anything published survives, because publishing is what made it exist somewhere else. What's left isn't a session anymore — it's an ordinary branch in the project, and you'd start a new session from it with `^O`.

### A discarded session and a remote one are the same shape

Not analogous — **the same**. After discard, a session holds exactly what would cross to another machine: its name and its commits. No machinery, no attempts, nothing local left over.

```text
  ─ billing   fix/dup-invoice-rows    no machinery    discarded 2h ago
  ─ billing   feat/dunning-emails     no machinery    via desktop
```

Both show `─`, and `↵` on either builds machinery from committed state. P therefore has no "restore" concept at all, because restoring locally and materializing from a remote are one operation — and one operation cannot drift out of agreement with itself.

It's also what makes `^D` a comfortable habit rather than a decision: whatever you can do with work that arrived from another machine, you can do with work you discarded.

---

## The overview

The TUI is where everything happens. **Decided:** it's fzf-shaped — a flat, sorted, live-filtered list with a preview pane — with vim navigation and every action on an unconflicting chord.

### Two modes, mode-independent actions

**Decided.** Typing filters. That means plain letters can't be verbs, and `hjkl` can't navigate while you're filtering — so there are two modes. What keeps that from mattering: **every action is `Ctrl`+letter and works in both.** The mode only decides what plain letters do; it never decides what you can do.

```text
  NAVIGATE                    normal mode      every mode
    down / up                 j / k            ↓ ↑   ^N ^P
    back / attach             h / l            ← →
    first / last              g / G            Home / End
    page                                       PgDn / PgUp   ^F ^B

  FILTER
    /               start filtering — typing narrows, live and fuzzy
    Esc             leave filter, keep it        ^W  clear it

  ACT — chords, every mode
    ↵    attach          ^O  new              ^U  publish
    ^K   name            ^T  stop
    ^D   discard         ^X  delete
    Tab  mark            ^A  mark all visible
    ^V   preview         ^R  rescan           ^S  cycle sort

  NORMAL MODE ONLY — these are plain characters, so they type while filtering
    :    command line    ?   help             q   quit
```

**Arrows work everywhere** — in normal mode and while filtering — and so do `^N`/`^P`, because those are down and up in both fzf and readline. `hjkl` is the same movement for hands that prefer it, in normal mode only.

**Navigation keys are never actions.** That rule is why *new* is `^O` and *publish* is `^U` rather than the more obvious `^N` and `^P`: a navigation reflex must never open a dialog, least of all a publish picker.

`hjkl` is unprefixed because it has to be: `^H` `^I` `^J` `^M` **are** Backspace, Tab, LF and CR at the terminal level, so `Ctrl`+`hjkl` cannot be bound at all.

Two readline bindings are overridden rather than preserved: `^D` (delete-char) discards, and `^A` (start-of-line) marks all. Backspace, `^W` and Home cover what they displaced, and both are confirmed or reversible — but they're the first bindings to change if they grate.

`h`/`l` follow vim's spatial sense rather than being dead keys — `l` drills in (attach), `h` backs out (clear filter, leave preview).

Filtering starts on `/` alone, deliberately. Binding `i` to it as well — vim's reflex — is silently lossy: typing `invoice` would consume the `i` entering filter mode and leave you looking at `nvoice` with no hint why. It's available in config for anyone who wants it, but it isn't a default.

### Flat and sorted

**Decided.** No group headers. A flat list ranked by **urgency** keeps blocked work on top without a section to announce it, and stays coherent while filtering — which grouped lists don't, since groups empty out and the list reshapes under you.

`^S` cycles the sort, shown in the header:

| Sort | Orders by |
|---|---|
| `urgency` | blocked → running → idle → stopped → no machinery. The default. |
| `age` | most recently active first |
| `project` | project, then name |
| `name` | alphabetical |
| `backend` | where it runs, for spotting an overloaded machine |

### Marks and bulk actions

**Decided.** `Tab` marks a session; `^A` marks everything currently visible — **after filtering**, which is the point:

```text
  /idle             filter to idle sessions
  ^A                mark all 6
  ^D                discard — itemized, all 6, one confirmation
```

Marks survive the list reordering underneath you, which it will as agents change state. That's why marks rather than a visual-mode range.

### Preview

**Decided.** Always shown when the terminal is wide enough, `^V` toggles. It carries what you'd otherwise have to attach to learn: state and age, backend, base commit, the namespace with per-attempt check results, and the question a blocked agent is waiting on.

### Detach behavior

**Decided.** Where you land on detach follows how you got there:

- Entered through the overview → **back to the overview.**
- Attached directly (`p attach billing/fix-token-refresh`) → **back to your shell.**

No mode to remember, and P is only a captive UI when you asked it to be.

### Keys are configurable

**Decided.** Everything above is a default. The instance config remaps any of it:

```nix
# ~/.config/p/config.nix
ui = {
  sort = "urgency";           # initial sort
  preview = true;             # preview pane when width allows

  keys = {
    # multiple bindings per action; these replace the defaults
    discard  = [ "C-d" "d" ];       # normal-mode letter alias, if you want one
    delete   = [ "C-x" ];
    publish  = [ "C-u" ];
    name     = [ "C-k" ];
    new      = [ "C-o" ];
    stop     = [ "C-t" ];
    down     = [ "j" "Down" "C-n" ];
    up       = [ "k" "Up" "C-p" ];
    filter   = [ "/" ];             # add "i" if you want vim's insert reflex
  };
};
```

Two notes on remapping. `^S`/`^Q` are XON/XOFF flow control on many terminals, so the sort binding is the first thing to change if your terminal eats it. And P validates the map at startup — a binding claimed twice is an error at load, not a surprise at 2am.

---

## Command surface

**Decided:** the TUI is the human interface. It is also *only a client* — every action it performs is a call into an action layer that other things can call too.

| Invocation | Effect |
|---|---|
| `p` | Overview of every session on every backend. |
| `p .` | Overview scoped to the repo containing the cwd; offers to register it if unknown. |
| `p <project>` | Overview scoped to a named project. |
| `p attach <session>` | Attach directly, bypassing the overview. Detach returns to your shell. |
| `p api <method> [args]` | Call the control-plane API directly. |
| `p daemon` | Run the control plane in the foreground. Normally managed by launchd/systemd. |

There is deliberately no flag-driven CLI mirroring every TUI key — but there is no capability gap either, because the API is complete.

### The API

**Decided.** The daemon exposes a local socket API. The TUI dispatches to it; so can you.

```console
$ p api sessions.list --state blocked
$ p api sessions.create --project billing --from main
$ p api sessions.name --session billing/tmp-0912 --name fix/dup-invoice-rows
```

The TUI's keymap is a thin mapping onto it:

```text
  ^O  →  sessions.create       ^K  →  sessions.name
  ↵   →  sessions.attach       ^U  →  remotes.publish
  ^T  →  sessions.stop         ^D  →  sessions.discard
  ^X  →  sessions.delete
```

**This is an architecture constraint, not a convenience.** If session lifecycle logic ever lives inside a TUI event handler, every future surface — scripting, a second frontend, agents managing their own work — is blocked behind a rewrite.

### What a session can call · Decided

A session reaches the same API through a session-scoped credential, and gets a **deliberately narrow** slice of it:

| | |
|---|---|
| **Allowed** | report its own status; act on itself — name itself, annotate |
| **Denied** | `sessions.create`, `sessions.attach`, anything touching another session or another project |

```console
# from inside a session
$ echo '{"state":"blocked"}' > $P_STATUS
$ git push p HEAD:refs/p/name/fix-dup-invoice-rows
```

The reasoning is [in the FAQ](FAQ.md#what-can-a-session-ask-p-to-do): a session that can create sessions has moved session creation inside the isolation boundary, and an agent that can spawn work can spawn unbounded work.

---

## Configuration

**Decided:** Nix is v1's configuration language. **Decided:** configuration is loaded through a *provider interface* internally, with Nix as the first implementation — so TOML, `devcontainer.json`, and plain JSON can be added later as providers without changing anything else.

### Project configuration

A project configures P through a `p` output in its flake:

```nix
{
  outputs = { self, nixpkgs, ... }: {
    devShells.x86_64-linux.default = # … your normal devShell

    p = {
      # where sessions run by default
      backend = "local-container";

      # the environment sessions get
      env.devShell = "x86_64-linux.default";

      # the branch scratch sessions start from
      defaultBranch = "main";

      # host paths shared into every session (read-only unless stated)
      mounts = [
        { host = "~/.cache/cargo"; guest = "/cache/cargo"; mode = "rw"; }
      ];

      # long-running processes started with the session
      services.db = {
        command = "postgres -D /var/lib/pg";
        ports = [ 5432 ];
      };

      # upstream hosts the broker will forward to
      broker.allow = [ "api.anthropic.com" ];
    };
  };
}
```

P evaluates this with `nix eval --json` and caches the result against the flake lock. A project with no `p` output gets defaults.

### Instance configuration

```nix
# ~/.config/p/config.nix
{
  backends = {
    local-container.runtime = "podman";
    desktop = {
      type = "ssh-container";
      host = "desktop.lan";
    };
  };

  remotes = {
    desktop = "p://desktop.lan/";
  };

  sync = {
    origin.push = "never";              # GitHub: always an explicit keystroke
    desktop = { fetch = "always"; push = "all"; };
  };

  # see "The overview" for the full ui.keys surface
  ui.sort = "urgency";

  notify = {
    ambient = true;                       # status-line integration
    push.on = [ "blocked" "failed" ];
    push.via = "ntfy";
    push.topic = "p-lgvo";
  };
}
```

---

## Backends

**Decided:** a session's execution backend is a pluggable abstraction. **Decided:** containers are the default unit; VM-per-session is a future backend, not a future rewrite.

| Backend | Status | What it is |
|---|---|---|
| `local-container` | v1 | Container on this machine. The default. |
| `ssh-container` | v1 | Container on another machine over SSH. |
| `k8s` | later | Pod in a cluster. |
| `vm` | later | Micro-VM per session, for stronger isolation. |
| `local-worktree` | later | No container. Escape hatch for environments where one is impossible. |

A backend implements a narrow contract: create, list, exec, attach, stop, remove, expose a git endpoint, and stream status.

### Discovery · Decided

**P has no session database.** Sessions are self-describing: every container carries P metadata as labels, and every workspace carries a manifest.

```text
p.session   = billing/tmp-0912
p.project   = billing
p.name      = (none — scratch)
p.commit    = a3f19c2
p.created   = 2026-08-10T09:12:04Z
p.instance  = laptop
```

`p` enumerates each configured backend and reconstructs the world. Nothing can desync, and a container removed behind P's back simply stops existing. The cost is that startup is as slow as your slowest backend, and a backend that's down shows as `⚠ unreachable` rather than silently dropping its sessions.

---

## Git as the interface

This is the load-bearing design decision.

**Code moves only over git.** Every P instance runs a git server, and that server is the entire contract for code moving in and out of a session:

- A session **clones** its workspace from P at a base commit.
- A session **pushes** its name, and anything under it, back to P.
- That's it.

Everything P enforces on code is enforced at that boundary — one every tool in the ecosystem already speaks, with no client library and nothing to install for code to move.

P does expose a small API for things that *aren't* code — [status events](#status-protocol) and notifications. The split is deliberate: **code over git, everything else over the API.** Nothing about a session's work product depends on a session knowing what P is.

### What the server enforces · Decided

The server identifies the calling session by a per-session credential injected at creation. **Two different scopes apply at two different boundaries**, and keeping them separate is what makes the namespace safe:

| Boundary | Scope |
|---|---|
| Session → P's git server | its name **and** `name/*` |
| P → any remote (`origin`, `desktop`) | its name **only** |
| Everything else | denied |

```text
  session billing/fix-dup-invoice-rows

    may push to P:      fix/dup-invoice-rows
                        fix/dup-invoice-rows/*
    publishable:        fix/dup-invoice-rows
    denied:             everything else
```

| Session type | Read | Write |
|---|---|---|
| Scratch | full history | **nothing** — it has no name, so it has no write target |
| Named | full history | its name and namespace |

No other refs. No tags. No non-fast-forwards. A session cannot rewrite history it didn't create, cannot touch another session's work, and cannot reach GitHub at all.

### Why the namespace stays local · Decided

A session is a specific piece of work, and it may want to try several approaches to it. [The agent is sovereign inside its session](#inside-a-session-the-agent-is-sovereign), so it can fan out however it likes — three worktrees, three subagents, three candidate fixes — and hand each one back separately:

```text
  ○ billing   fix/dup-invoice-rows      idle    1 ahead of origin
      └ /attempt-a                              ✓ test  ✗ integration
      └ /attempt-b                              ✓ test  ✓ integration
```

But **`name/*` is exploration, not output.** It exists so approaches can be built, checked, and compared; it is working state, and it never leaves this machine — not to GitHub, not to your desktop, not under any sync policy. What publishes is the session's name: its answer.

The two consequences, stated plainly:

- **Attempts don't travel.** Move the work to another machine and you get the answer, not the exploration that produced it. Consistent with everything else here — the durable unit is the commit you decided on.
- **Something has to make an attempt into the answer.** That's the agent's job — merge or fast-forward the name onto the attempt it picked. P deliberately doesn't do it for you; [it would put P in the business of authoring commits](FAQ.md#if-discard-deletes-my-attempts-how-do-i-keep-one).

### Pushes are triggers · Direction

A push is an event, and P's git server is on the *host* side of the isolation boundary. So a push can run work the session itself could never run:

```nix
p.checks.test = {
  on = [ "push" ];
  run = "nix flake check";
};

p.checks.integration = {
  on = [ "push" ];
  backend = "vm";                 # provisioned per run, outside the session
  run = "./scripts/integration.sh";
};
```

The agent pushes. P runs the check on the control plane — with host credentials, a VM, a database, whatever the check needs — and reports the result back through the [status protocol](#status-protocol) and as a ref the session can fetch:

```text
  ● billing   fix/duplicate-invoice-rows   running   ✓ test   ✗ integration   local
```

```console
[billing/fix-duplicate-invoice-rows] $ git fetch && git log -1 refs/p/checks/integration
```

This inverts the usual tradeoff. Normally, letting an agent verify its own work means handing it access to the infrastructure that does the verifying. Here it gets the feedback and never gets the reach — the check runs somewhere the container cannot see, using credentials it never held, triggered by the one action it *is* allowed to take.

**Direction.** v1 ships host-side commands on push; provisioning a backend per check comes with the `vm` backend.

### Hub and spoke · Decided

Because the interface is git, it composes outward without inventing anything. P's git server is the hub. **Everything else is a spoke — including your own checkout.**

```text
      ~/src/billing  ──┐   you, unrestricted
                       │
   session ──name+ns──►  p://billing  ◄──►  p://desktop
                       │
                       └──►  origin (github)
```

Your `~/src/billing` is not privileged. It's a working copy with two remotes:

```console
$ git remote -v
origin   github.com/you/billing   (fetch/push)
p        p://billing              (fetch/push)
```

You pull from either and push to either. Pulling a session's branch straight out of P needs no cloud round trip. Getting hand-written work *into* a session is `git push p` — the same thing a session does, because you are the same kind of spoke, just without the restrictions.

Consequences worth stating:

- **P never reaches into your checkout.** It warns when you're ahead and offers to run the push for you; it does not sync you behind your back.
- **A project needs no host checkout at all.** If all your work happens in sessions, that spoke simply doesn't exist.
- **Another machine's P server is just a remote.** Sending work to the desktop is a push to `desktop`.
- **No sync protocol, no state replication, no message bus** between machines. Git already is one.

### Sync policy · Decided

Remotes are not equally consequential. Pushing to your own desktop moves work between your machines; pushing to GitHub is publication. So policy is **per-remote**:

```nix
p.sync = {
  origin  = { fetch = "always"; push = "never"; };  # publication — always your decision
  desktop = { fetch = "always"; push = "all";   };  # your machine — converge freely
};
```

| Mode | Meaning |
|---|---|
| `fetch = "always"` | The daemon fetches in the background. Inbound changes nothing anyone else sees, so it carries none of the risk that makes publishing explicit. |
| `push = "all"` | Branches sync outward automatically. |
| `push = "published"` | Only branches published at least once. |
| `push = "never"` | Every outbound push is an explicit act. |

**`origin` defaults to `push = "never"`, and publishing does not establish a tracking relationship.** Publish once, let the agent commit again, and the branch sits at `1 ahead of origin` until you press `P` again. Deliberate: nothing an agent produces reaches GitHub without a human keystroke *that time*.

```text
  ○ billing   fix/duplicate-invoice-rows   idle   1 ahead of origin   ✓ synced desktop
```

This is the one place the design distinguishes "outbound" from "dangerous." They're not the same thing, and collapsing them made publishing feel heavier than it needed to be.

---

## Status protocol

**Decided:** P defines a status protocol first; agents report into it. Heuristics are a fallback, not the mechanism.

P injects into every session:

```sh
P_SESSION=billing/tmp-0912
P_STATUS=/run/p/status.sock
```

Anything in the session can append newline-delimited JSON events:

```json
{"state":"running","at":"2026-08-10T09:12:44Z"}
{"state":"blocked","reason":"awaiting input","at":"2026-08-10T14:02:11Z"}
{"state":"idle","at":"2026-08-10T14:31:09Z"}
```

### Claude Code, with no wrapper

Claude Code reports through its own hooks — the integration is configuration, not a shim:

| Hook | Reports |
|---|---|
| `UserPromptSubmit`, `PreToolUse` | `running` |
| `Notification` | `blocked` — this is the one that matters |
| `Stop` | `idle` |

### Fallback

A session that reports nothing gets a pane heuristic — foreground process plus output quiescence — and if that's inconclusive, it shows `?` rather than guessing. **Decided:** P shows `unknown` instead of lying, because the overview's entire value is that you trust the marks.

### Notifications

**Decided:** both, configurable:

- **Ambient** (default): a compact count for your shell prompt, tmux status bar, or editor — `p api sessions.list` is the integration point. `⏸2 ●3` wherever you already are.
- **Push** (opt-in): the daemon fires on state transitions you choose — `blocked`, `failed` — via desktop notification, `ntfy`, webhook, or an arbitrary command.

This is what makes the nineteen-hour blocked agent a solved problem rather than a story about a nice list.

---

## Isolation and security

**Objective:** safe enough to leave a coding agent running unattended.

### Inside a session, the agent is sovereign

**Decided.** P draws exactly one boundary: **between sessions, and between a session and the host.** It draws none inside a session.

An agent in a session may spawn subagents, `git worktree add` for parallel attempts, run background processes, bind whatever ports it likes, install whatever it wants. Claude Code, Codex, or anything else can do all of this natively. P neither provides these capabilities nor prevents them — they happen inside a boundary that's already drawn, and their blast radius is already the session.

This is why P doesn't need worktree management, fan-out, or agent orchestration features: **agents orchestrate themselves, and P is the layer underneath.** The two things a session cannot do on its own are the two P actually governs — reach outside its container, and write refs it wasn't given.

### The rest of the boundary

**Credential broker.** No raw credentials in containers. Model and agent API traffic goes to a host-side broker that holds the keys, enforces the project's `broker.allow` list, forwards the request, and attributes usage to the session. The container gets a base URL, not a secret.

**No GitHub in the container.** Sessions only ever talk to P's git server. Publishing credentials live on the host and are used only by the explicit publish action.

**Default access.** Outbound network is available; host directories, container sockets, and SSH agents are not exposed. Project config may grant specific mounts and services.

**What isolation does not preserve or distribute:** running processes, tmux state, agent conversations, package caches, container filesystem state, uncommitted changes. Only committed git content crosses machines.

---

## Requirements

**v1 targets Linux. Decided.**

- **Linux** — for the P instance: daemon, git server, containers
- **tmux**
- **git**
- **Nix** — v1's config provider and environment source
- **podman or docker**

### macOS and Windows

**P doesn't port to them — they run Linux. Direction.**

The P instance lives in a Linux VM; the TUI runs on your host and talks to the daemon inside it. That works because [the TUI is already a client of the API](#the-api), so the only thing cross-platform support needs is a transport that crosses the VM boundary.

```text
   macOS host                      Linux VM
   ┌──────────┐                    ┌──────────────────────────┐
   │  p (TUI) │ ──── socket ─────► │  daemon                  │
   └──────────┘                    │  git server              │
                                   │  containers ── sessions  │
                                   └──────────────────────────┘
```

This is a deployment story, not a port: no second container runtime, no cross-architecture Nix builds, no linux builder, one code path. It also means **the API transport cannot assume a shared kernel** — it must work over a forwarded socket, vsock, or SSH. Cheap to design in now, painful to retrofit, so it's a v1 constraint even though macOS support isn't a v1 feature.

---

## Non-goals

- P does not replace git commands inside sessions.
- P does not create commits automatically.
- P does not publish automatically, to GitHub or to another machine.
- P does not move or synchronize live containers, terminals, tmux layouts, agents, or uncommitted changes.
- P does not launch agents in v1 — it gives you an isolated workspace and reads status.
- P does not manage worktrees, subagents, or fan-out. Agents do that themselves, inside a boundary P already drew.
- P is not a CI system. Push-triggered checks are a local feedback loop, not a build farm.
- P does not reclaim sessions on its own. Stop, discard, and delete are always explicit and always itemized.
- P does not federate with other P *installations* as peers beyond git remotes.
- P is single-user in v1. Shared backends are the arc, not the starting point.

---

## v1 scope

**In:**

- Linux host; native containers and Nix
- fzf-shaped TUI: flat sorted list, live fuzzy filter, preview pane, remappable keys
- Self-describing backends: `local-container`, `ssh-container`
- Nix config provider; `devShell` → session image
- Scratch sessions, named without restarting
- Local socket API; TUI as a client of a complete action layer
- Session-scoped API slice: status + act-on-self only
- P git server as the session interface, with name + namespace authorization
- Push-triggered host-side checks, reported back into the overview
- Hub-and-spoke git topology; per-remote sync policy; explicit publish to `origin`
- Status protocol, Claude Code hook integration, ambient + push notifications
- Credential broker

**Later:**

- macOS and Windows, via a Linux VM running the instance
- `k8s` and `vm` backends, including per-check provisioning
- Additional config providers (TOML, `devcontainer.json`, JSON)
- Non-Nix environment detection
- Agent launching and fan-out
- Shared backends and multi-user policy

**A successful v1:** you run `p`, see every piece of work across every project with state you trust, get told when something blocks, attach in one keystroke, start isolated sessions agents can run in safely, name an exploration without restarting it, push against a policy-enforcing server, publish deliberately to GitHub or to another machine, and pick the work back up there.
