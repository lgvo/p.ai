# P — every piece of work you have in flight, on one screen

**Press release · working-backwards draft · target: v1**

> Written as if v1 already shipped. It's a design artifact, not an announcement. See [README.md](README.md) for how it works and [FAQ.md](FAQ.md) for the decision record, including what's still open.

---

## Summary

**P is a control plane for the work you have in flight** — terminal-native, and built for developers running several things at once, increasingly with agents doing them.

It does three things. It **knows** what work exists and tells you when a piece of it needs a human. It **places** each piece in an isolated environment, wherever you want that to run. And it **governs** what that work can produce and where it can go. The first is the point; the other two are what make the first safe.

One screen shows every session across every project with live state: which agent is producing output, which one is blocked waiting on an answer, which is idle, which isn't running. Enter attaches you to any of them. And when an agent blocks while you're doing something else, P tells you instead of waiting to be asked.

A session is **a specific piece of work to be done**. It gets its own container and its own working copy, built fresh from a commit — not a copy of anything you already have, and not connected to your checkout. That's what makes leaving an agent running unattended a reasonable thing to do.

## The problem

Developers working across several repositories have organized that work in tmux sessions for twenty years. It worked when one project meant one thing at a time.

Coding agents broke that in two ways at once.

**An agent needs a workspace that isn't yours.** An agent editing files and running commands in your checkout means you can't use that checkout while it works. Two agents in one repository is worse. So the number of live workspaces goes up sharply — and each one now runs on its own schedule.

**Nothing shows you what's in flight.** With three projects and six pieces of work, finding out what's happening means walking tmux sessions by hand and reading scrollback to guess whether an agent is working, finished, or stuck on a question you never answered. The friction isn't the isolation. It's the *bookkeeping*.

The second problem is the expensive one. An agent that blocked on a question at 14:02 and sat there until 09:04 the next morning cost you nineteen hours, and the only reason is that nothing told you.

## The solution

Run `p` and you get every session across every project, sorted by what needs you. Arrow to the blocked one, press Enter, and you're attached exactly where the agent stopped. Answer it, detach, and you're back in the overview.

You shouldn't have needed to run `p` to find that out, so P reports state out of band too. A status protocol lets agents report their own state — Claude Code does it through hooks, no wrapper — so P can keep a count in your status line and push a notification when something needs a human. The overview is for when you're already looking; the notification is for when you aren't.

## Sessions are cheap, and so is throwing them away

Press `^O` and P builds a container from your project's Nix `devShell`, clones a working copy from its own git server at the commit you picked, starts tmux, and attaches you. Three sessions in one repository — one yours, two agents — none of them aware of the others.

Most work starts unnamed, because at 09:12 you don't yet know what it is. When it turns out to matter, one keystroke names it. The container keeps running, the agent keeps its conversation, nothing restarts, and no commit is invented on your behalf — you're naming the work, not deciding its history.

A named session owns a namespace, so an agent can try three approaches and hand all three back separately. Each push runs the project's checks, so you compare approaches on evidence rather than description.

Cleanup is three verbs, because "I'm done running this" and "I'm done with this work" are different intentions. **Stop** pauses it. **Discard** reduces it to its answer — machinery and dead ends gone, commits kept. **Delete** ends it, and anything never published is lost. Each one itemizes what it's about to destroy. P never reclaims anything on its own.

## Git is the interface

Every P instance runs a git server, and that server is how code moves in and out of a session. A session fetches its base commit from P and pushes its work back to P. That's the entire contract for code — one every tool already speaks, with no client library and nothing to install. (A small API carries the things that aren't code: status and notifications.)

Because the contract is a push, it's also a **trigger**. Pushing to P runs checks on the host, *outside* the container — run the suite, bring up a VM, provision whatever the check needs. The agent gets real feedback on its work without ever holding the credentials or the reach to produce it. The usual tradeoff, where giving an agent CI means giving it access to CI, doesn't apply.

Because the contract is git, it composes outward. P's server is a hub; everything else is a spoke, **including your own checkout**, which is an ordinary working copy with `origin` and `p` as remotes. Another machine's P instance is a remote too, so sending work to the desktop is a push. There's no sync protocol, no state replication, and no message bus between machines, because git already is one.

That also lets P draw a line the usual tooling blurs: **outbound is not the same as dangerous.** Your own machines converge freely. GitHub doesn't — publication is a keystroke you press every time, so nothing an agent writes becomes visible to anyone else without a human in the loop at that moment.

## Safe enough to walk away from

- **No credentials in the container.** Model and agent API traffic goes through a host-side broker that holds the keys and forwards the request. The container gets a base URL, not a secret.
- **Containers never talk to GitHub.** Sessions only ever reach P's git server, and only for their own name and the namespace beneath it — no other refs, no force-pushes, no tags. An unnamed session has no write target at all.
- **Verification happens outside the boundary.** Checks run on the host, with resources the session was never given.
- **Publishing is a decision you make**, using credentials the agent never had.

Inside a session, P draws no boundaries at all. An agent may spawn subagents, use worktrees, run background processes — its blast radius is already the session, and P is the layer underneath, not a supervisor.

## Illustrative quotes

> "I stopped counting how many times I found an agent that had been waiting on me overnight. The overview isn't the feature — knowing I'll be told is the feature."
>
> — *P's author, on why the status protocol shipped in v1*

> "Three repos, five things going at once, two of them agents. I used to lose one for a day. Now it's one screen and I can see the one that's stuck."
>
> — *illustrative user, representative of the target developer*

## Availability

P v1 targets Linux, with `local-container` and `ssh-container` backends. It requires tmux, Git, Nix, and podman or docker.

macOS and Windows aren't a port — a P instance runs in a Linux VM and the terminal UI talks to it from the host. One code path, one container runtime, no cross-architecture builds.

Nix is v1's configuration language: a project is configured by a `p` output in its flake, and environments come from its `devShell`. That's a v1 choice, not an architectural one — configuration loads through a provider interface with Nix as the first implementation, so TOML, `devcontainer.json`, and JSON can follow without touching the rest of P.

P is single-user today. Shared backends and multi-user policy are the intended arc, and the backend and authorization models were designed with that in mind.

## Getting started

```console
$ p .          # register the repo you're standing in
$ p            # everything, everywhere, one screen
```
