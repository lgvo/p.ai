# P — every piece of work you have in flight, on one screen

**Press release · working-backwards draft · target: v1**

> Written as if v1 already shipped. It is a product narrative, not an
> announcement or protocol authority. The subject-specific design documents are
> authoritative; [README.md](../README.md) summarizes the current design and
> [FAQ.md](FAQ.md) explains its tradeoffs.

---

## Summary

**P is a control plane for the work you have in flight** — terminal-native, and built for developers running several things at once, increasingly with agents doing them.

It does three things. It **knows** what work exists and tells you when a piece of it needs a human. It **places** each piece through an instance-local isolation provider. And it **governs** what that work can produce and where it can go. The first is the point; the other two are what make the first safe.

One screen shows every session across every project with its session condition,
confirmed attachment count, latest unattended agent condition, and whether its
immutable policy snapshot is current. Enter attaches you to any ready session.
Typed reduced events also feed a local log handler and leave room for other
trusted handlers later.

A project is a Git repository. A session is an immutable UUID that owns one
real branch and has an instance-local runtime tagged with that UUID. In V1 it
gets its own unprivileged Incus system container, working copy, private
writable root, and systemd-supervised persistent interactive host, built from
committed source—not copied from the user's checkout. Tmux is the default
host, not the grouping model.

## The problem

Developers working across several repositories have organized that work in tmux sessions for twenty years. It worked when one project meant one thing at a time.

Coding agents broke that in two ways at once.

**An agent needs a workspace that isn't yours.** An agent editing files and running commands in your checkout means you can't use that checkout while it works. Two agents in one repository is worse. So the number of live workspaces goes up sharply — and each one now runs on its own schedule.

**Nothing shows you what's in flight.** With three projects and six pieces of work, finding out what's happening means walking tmux sessions by hand and reading scrollback to guess whether an agent is working, finished, or stuck on a question you never answered. The friction isn't the isolation. It's the *bookkeeping*.

The second problem is the expensive one. An agent that blocked on a question at 14:02 and sat there until 09:04 the next morning cost you nineteen hours, and the only reason is that nothing told you.

## The solution

Run `p` and you get every session across every project, sorted by what needs
you. Select the blocked one and—with the default persistent interactive
host—you return to the program where you left it. Answer, detach, and you are
back in the overview.

You shouldn't have needed to infer that from terminal output. A status protocol
lets agents report their own state—Claude Code does it through hooks, no
wrapper—so P can reduce one latest unattended condition reliably. The V1 event
handler writes redacted structured events to a local log; another handler can
later turn the same interface into a notification without changing session
semantics.

## Sessions are cheap, and so is throwing them away

Create a session and P prepares or reuses a private Incus environment image
from your project's Nix `devShell`—or its base image when the project has
none—creates a new session-owned branch at the committed source you picked,
assembles an isolated working copy, and starts the persistent host through
systemd. Attach is a separate lifecycle action; the prototype will decide
whether the initial TUI combines them as one flow. A disposable Incus builder
realizes committed Nix inputs;
each session starts with that immutable store and receives its own writable
Nix/workspace/home delta.

Projects are explicit: create one from an SSH origin or create a blank local
repository. A blank project—or a successfully contacted empty origin—starts
with one bootstrap session on unborn `main`; its first push creates the branch.
There is no command that registers or imports the checkout you are standing in.

When work has no good name yet, P suggests a timestamp. Every session still has
a UUID and a real branch immediately. A Rename action changes that Git branch
transactionally while the runtime and conversation continue; no commit is
invented on the user's behalf.

A session may write only its assigned branch. An agent may still fan out with
worktrees or subagents inside its runtime, but it must consolidate the result
onto that branch before handing it back.

Cleanup is three verbs, because “pause this runtime,” “remove this runtime,” and
“delete this instance's work” are different intentions. **Stop** preserves a
restartable runtime. **Discard** ends the session and removes its machinery
while keeping the ref as a retained branch. **Delete** also removes
the session-owned branch. It
itemizes commits that lose their last retained ref; when an origin exists, a
fresh fetch also identifies what survives upstream. Local-only projects bypass
that comparison. P never reclaims anything on its own.

## Git is the interface

Every P instance runs a Git server, and that server is how code moves in and out
of a session. A session starts its branch at a selected committed source and
pushes subsequent work back to that assigned P branch. That is the complete
contract for code. A small RPC API carries lifecycle and status.

Because the contract is Git, it composes outward. Each P server is its instance's local hub; sessions and your ordinary checkout are spokes. Independent P instances never address one another. For a project with a shared `origin`, continuing on another machine means publishing from the first P server, fetching on the other machine, and creating a fresh session there. A project with no origin is local-only and has no P-managed cross-machine handoff. There is no P sync protocol, state replication, discovery, or message bus.

For a project with an origin, publication is a separate host-authorized action
every time. The TUI can make that a direct action, and `p api` can automate it, but
a session cannot invoke it or receive its credentials. P refreshes origin state
when current evidence is required but never pushes automatically. A host
checkout can perform the same handoff manually by fetching `p` and pushing
`origin`. Origin-less projects bypass fetch, comparison, and publication rather
than treating their absence as an error.

## Safe enough to walk away from

- **No upstream model credentials in sessions by default.** Bifrost holds them;
  P obtains one virtual key per model-enabled session under its configured
  Bifrost policy. P exposes Bifrost's OpenAI-compatible interface first;
  Anthropic-compatible clients follow in a second phase. A secret-bearing
  filesystem grant remains an explicit, itemized containment downgrade.
- **Sessions have no `origin` authority.** Their configured Git
  path is P's server, and they hold no publication credential. Public internet
  access may make a forge network-reachable, but P supplies neither an `origin`
  remote nor authority to write there. A session credential can update only its
  UUID-assigned branch, always fast-forward-only. The host P credential is
  read-only; origin operations use the user's ordinary SSH credentials.
- **Project-controlled evaluation has its own boundary.** Environment
  evaluation and builds run in a disposable restricted Incus builder;
  repository-controlled material never executes with ambient daemon or host
  authority.
- **Network starts closed.** The baseline has no general network. A validated
  public-egress profile may fetch public packages and documentation while still
  denying the host, LAN/private/link-local/metadata destinations, sibling
  instances, and Incus administration. P's Git, status, and Bifrost inference
  endpoints are narrow exceptions.
- **Incus authority stays confined.** P uses one pre-provisioned confined user
  project; builders and sessions never receive an Incus socket, and P does not
  use the host-root-equivalent administrative socket.
- **Publishing is a separate host-authorized action**, using credentials P
  never supplies to the session.

Inside the runtime boundary, P does not supervise relationships between agents,
worktrees, or background processes. They share the session's granted authority;
P enforces the boundary around that session, not an orchestration model within
it.

## Illustrative quotes

> "I stopped counting how many times I found an agent that had been waiting on me overnight. The overview isn't the feature — knowing I'll be told is the feature."
>
> — *P's author, on why the status protocol shipped in v1*

> "Three repos, five things going at once, two of them agents. I used to lose one for a day. Now it's one screen and I can see the one that's stuck."
>
> — *illustrative user, representative of the target developer*

## Availability

P V1 targets Linux with one local Incus runtime backend. It requires Git, an
initialized Incus daemon with a confined user project, and a verified P base
image containing the pinned Nix toolchain and systemd runtime contract. Nix
runs inside builder and session instances rather than being a host runtime
dependency. Tmux is the default persistent interactive host. The daemon
manages only runtimes belonging to its own instance.

On macOS or Windows, v1 means SSHing to a Linux machine and running the TUI there. Linux clients ship both local-Unix and SSH-to-Unix transports from day one; later native thin-client binaries reuse the SSH transport. The daemon never initiates SSH or treats another machine as a runtime backend.

Nix devShell is the first environment provider, not P's session configuration
language. A repository contributes only its ordinary default devShell; all
P-specific settings are trusted host configuration at project scope. A
repository without a default devShell uses P's immutable minimal session
substrate, so project creation never requires generating a file. The environment
builder and runtime backend remain reusable interfaces, but V1 specifies only
Nix-to-Incus-image and local Incus containers.

P is single-user today. Multi-user policy is a later arc, and the runtime and authorization models leave room for it without making it a v1 claim.

## Getting started

Run `p`, create an explicit SSH-origin or blank project, then create its first
session. The exact TUI layout and key bindings will be selected after the
prototype rather than promised by this narrative.
