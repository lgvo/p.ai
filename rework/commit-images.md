# One image per source commit

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).
It replaces an earlier draft that kept today's environment images and only
changed when they are deleted.

## Summary

A session always starts from a source commit: the tip of an existing branch,
or the commit a new branch is cut from.

**Today** P builds an image per *environment*: the P base image plus the Nix
packages of the commit's devShell, keyed by what that devShell resolves to.
Commits that resolve to the same environment share an image. The code is not
in the image: P clones it into `/workspace` when the session starts. Images
are kept until a user deletes them through `environment.cache.*` or the
project is deleted.

**Decided by the maintainer on 2026-11-02:** an image belongs to a source
commit. There is no separate environment.

- Starting a session from commit C uses the image tagged C. If there is none,
  P builds one from everything at C, the Nix environment and the code, and
  tags it C.
- Every session started from C is a container created from that same image.
  From there each container drifts on its own: commits, packages, files. The
  image never changes, and a session's own work never rebuilds anything.
- An image is kept while some session started from its commit exists. When
  the last one is gone, P deletes the image. Any image can be rebuilt from
  its commit.

## Before and after

| | Today | After |
|---|---|---|
| What an image holds | P base and one environment's Nix packages | P base, the Nix packages and the code, all at one commit |
| What names an image | The environment key: what the devShell resolves to | The source commit, with P's base image and builder version |
| Who reuses an image | Every commit that resolves to the same environment | Every session started from the same commit |
| The code | Cloned into `/workspace` at session start | Already in `/workspace` in the image |
| Looking up an image | Resolve the devShell in a disposable builder, then look up the key | Look up the commit; a hit needs no builder |
| When P builds | The first session of a new environment | The first session from a new commit |
| How long an image lives | Until deleted by hand or with its project | While a session started from its commit exists |
| `environment.cache.*` | Manual list and delete | Removed |

The creation phases, before:

```text
source-ready       capture commit C
branch-assigned    create or claim the branch
environment-ready  resolve C's devShell in a builder; reuse the image for that environment, or build one
principals-ready   session Git principal and plugin bindings
runtime-created    create the container from the environment image
workspace-ready    clone the branch into /workspace; set the P upstream; install endpoints
systemd-ready      start the container
```

After:

```text
source-ready       capture commit C
branch-assigned    create or claim the branch
image-ready        reuse the image tagged C, or build one from C and tag it C
principals-ready   session Git principal and plugin bindings
runtime-created    create the container from the commit image
workspace-ready    put /workspace on the session's branch; set the P upstream; install endpoints
systemd-ready      start the container
```

Example, with `main` moving under active work:

| When | What happens |
|---|---|
| `main` at 4321; session A from `main` | No image tagged 4321: P builds one and tags it 4321 |
| Session B from `main`, still at 4321 | Reuses the 4321 image: no build, no clone |
| A and B work | Each container drifts; nothing is rebuilt |
| `main` moves to 4322; session C from `main` | No image tagged 4322: P builds one, even if only code changed |

## The image

**What names it.** The project, the source commit, the base image
reference, the execution system and the builder version. A P or Nix upgrade
that changes the base image therefore builds new images even for a commit
that already has one.

**What it holds:**

- the P base image;
- the commit's default devShell realized into `/nix`, with its activation
  material; a commit without a default devShell gets the base packages only;
- `/workspace`: a clone of P's repository at the commit, detached, with no
  branch, remote or credentials.

**What it never holds:** anything that belongs to one session: its UUID, its
branch, its Git principal and keys, its endpoints, its home directory.
`workspace-ready` adds those when a session starts, so one image serves every
session from its commit.

**Where the tags live.** P's SQLite index is the authority: it maps (project,
commit, base image, system, builder version) to the
[runtime driver](runtime-driver.md)'s image reference. The driver stores the
same facts as image labels; the Incus driver keeps them as image properties
such as `p.source_commit`, next to today's `p.project_path` and
`p.base_image`, so they show in `incus image list`. P checks them before using
or deleting an image, as it checks labels today.

**Building.** P asks the [runtime driver](runtime-driver.md) to build the
image for a commit (`BuildImage`). The driver does the whole build: a
disposable builder from the pinned base image, the Nix steps that realize the
commit's devShell (today's Nix environment plugin folds into this), and the
published image, with no builder left behind. The builder's input is a Git
bundle of the commit, which it clones into `/workspace`; the
[source driver](source-driver.md)'s `Export` writes it, and gitservice until that driver lands in step 9. The host no
longer checks the commit's tree (gitservice's snapshot.go), since the checkout
happens in the isolated builder. An image only has to work with the runtime
that built it. The builder gets
public internet access only, with no host or local-network route, as
[environment building](../docs/environment-building.md) already designs
("Realize" and "Security boundary"); today's builder is still offline, and
implementing that access is step 0g. Builds fetch from the public
package caches directly. A commit that changes only code therefore costs a
full build; with the Incus driver the published image is a full copy on disk,
because Incus images are not layered. A P-run package cache to speed builds
up is left for later, possibly as a plugin.

**The first session of a blank project** has no commit, and keeps using the
P base image with an empty workspace, as today.

## When P deletes an image

**The rule, decided by the maintainer on 2026-11-02:** an image is kept while
some session started from its commit exists. When the last such session is
gone, P deletes the image.

The links already exist. Creation records each session's source commit
(`CapturedOID` in its creation's evidence), and it never changes over the
session's life. Every image
names its commit. So "is this image in use?" is "does any session of this
project have this source commit?". A session counts in every state:
`creating` (including a failed creation waiting for Retry), `established` and
`removing`.

What the rule means in practice:

- **Discard, then Create on the retained branch** usually needs a new image
  anyway: if the session committed work, the branch has moved, so the new
  session starts from a different commit. Only a branch that did not move
  rebuilds the same commit's image.
- **Several sessions from one commit** share the image until the last of them
  is gone.

The mechanics:

- **Release**, a step the daemon runs after an operation of the project
  finishes and at daemon start, deletes the images no session uses. An
  operation never waits for it or fails because of it; if the driver cannot
  be reached, the image stays and release retries.
- **A per-commit lock** serializes release with every creation that starts
  using an image, so a session is never created from an image that is being
  deleted. Release first marks the index row `releasing`, then has the driver
  delete the exact image by its reference and labels, then removes the row. A `releasing` row is never reused,
  even if the delete outcome is unknown, until the delete is settled.
- **Project deletion** deletes all of the project's images, as today.

## What users notice

- A session from a commit that already has an image starts with no build and
  no clone.
- A session from a new commit builds an image, even when only code changed:
  a full build, as long as today's build of a new environment, and a full
  image on disk while sessions from that commit exist.
- Code now also lives in images. Deleting a branch does not remove its code
  from images that were built from its commits; the deletion rule or project
  deletion does. A secret committed by mistake therefore stays in an image
  until that image is deleted.
- `environment.cache.*` is gone, and nothing blocks session creation for the
  cache any more.

## Existing installations

None are supported: P targets fresh installs, by the maintainer's decision of
2026-11-02. There is no migration of today's environment images, index or
unfinished collections.

## What is removed

- The environment key as an image's name, and the resolve step before every
  lookup; resolving now happens only inside a build.
- `environment.cache.list`, `environment.cache.preview`,
  `environment.cache.collect`, the `environment.collect` operation kind, its
  preview store and the SQLite guards that block creation during a
  collection.
- The `environment_images` index keyed by environment, replaced by the commit
  index above.

## Departures from agreements

These documents describe today's model and change with this step:

- [environment building](../docs/environment-building.md): the image holds
  the environment only, is named by the environment key, and "the workspace
  … [is] never part of the cached image"; MVP has no automatic collection;
  resolution receives a tree checked on the host, not a bundle.
- [session lifecycle](../docs/session-lifecycle.md): the `environment-ready`
  and `workspace-ready` phases, and "Session lifecycle never leases or deletes"
  images.
- [control API](../docs/control-api.md): the cache collection section.

## Options set aside

| Option | Why not |
|---|---|
| Today's environment images, with the code cloned at session start | Fewer builds when only code changes, but two identities to keep apart, an environment and a commit. Set aside by the maintainer: there is no point in separating them. |
| One baseline image, with each session realizing its devShell from the repository | No images to manage, but every session copies its packages and starts slower. |
| A package-level Nix binary cache instead of images | Weighed separately; not needed to decide what an image is. |
| Keep the most recently used images up to a budget per project | A setting to choose, and images kept with no session to explain them. Set aside by the maintainer for the session rule. |
| Keep the newest image per branch | Needs a new branch-to-image record kept in step with renames and deletions. |

## How it will be checked

- Unit tests: a second session from the same commit reuses the image and runs
  no builder; a session from a new commit builds and tags an image; an image
  is deleted when the last session started from its commit is gone, and kept
  while a creation waiting for Retry or a session being removed still has
  that commit; the tag in
  the index and the label on the image agree; release never deletes an image
  while a creation holds its commit lock; a `releasing` row is never reused;
  project deletion deletes every image of the project.
- VM scenarios: two sessions from one commit start from one image; a commit
  that changes only code builds a new image; deleting the last session from a
  commit deletes its image. Scenario 19 checks that the exported bundle clones
  to exactly the commit, instead of checking the tree.
