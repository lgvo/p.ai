# P — commit images

How P builds one immutable image per source commit, holding the commit's Nix
development environment and its code, without sharing writable Nix state
between sessions, and when it deletes that image.

> **Status: design.** This document is authoritative for environment
> selection, isolated realization, commit images and their lifetime, and
> activation. [Runtime and isolation](runtime-isolation.md) owns the final
> session instance and its private writable state.

## Contents

- [The rule](#the-rule)
- [Terms and authorities](#terms-and-authorities)
- [Runtime driver image operations](#runtime-driver-image-operations)
- [Environment selection](#environment-selection)
- [Source input and lock policy](#source-input-and-lock-policy)
- [P base image](#p-base-image)
- [Image identity and contents](#image-identity-and-contents)
- [Build and publish pipeline](#build-and-publish-pipeline)
- [Nix activation compatibility gate](#nix-activation-compatibility-gate)
- [Image Nix store model](#image-nix-store-model)
- [Session activation and later Nix work](#session-activation-and-later-nix-work)
- [Lookup, concurrency, and image lifetime](#lookup-concurrency-and-image-lifetime)
- [Security boundary](#security-boundary)
- [Failures and retry](#failures-and-retry)
- [What users notice](#what-users-notice)
- [Performance and evidence](#performance-and-evidence)
- [API and channel boundary](#api-and-channel-boundary)
- [MVP boundary](#mvp-boundary)
- [Acceptance criteria](#acceptance-criteria)

## The rule

An image belongs to a source commit. Starting a session from commit C uses the
image P keeps for C; when there is none, P builds one from everything at C and
keeps it for C. The image contains the P base, the commit's realized default
devShell with a coherent Nix store database and activation material, and
`/workspace` holding a clone of the commit. Incus supplies private writable
instance storage on top of that image for every session.

```text
P Nix base image + Git bundle of commit C
  → isolated builder clones C, realizes its devShell
  → immutable commit image for C
      ├─ session A private root: its branch, work and Nix additions
      └─ session B private root: its branch, work and Nix additions
```

Every session started from C is created from that same image. From there each
session drifts on its own: commits, packages, files. The image never changes,
and a session's own work never rebuilds anything. P keeps the image while some
session started from C exists and deletes it once the last one is gone; any
image can be rebuilt from its commit.

No session mounts the host `/nix`, uses the host Nix daemon, or shares a
writable Nix database. The committed Git definition remains source authority.

Commit images apply only when the host configures `runtime.environment`
([control API](control-api.md)). Without it, sessions use the P base image and
the runtime kit clones `/workspace` from P at start, as described in
[runtime isolation](runtime-isolation.md#fixed-paths). The first session of a
blank project has no commit and also uses the P base image, with an empty
workspace.

## Terms and authorities

| Term | Meaning |
|---|---|
| **P base image** | P-owned Incus system-container image containing Nix, Git, SSH, tmux, basic userland, the fixed user, and runtime kit. |
| **Source commit** | The commit a session starts from: the tip of an existing branch, or the commit a new branch is cut from. Creation records it and it never changes over the session's life. |
| **Environment selection** | The commit's conventional default devShell, or no project environment. A bootstrap session without a commit uses only the P base image. |
| **Commit image** | Project-scoped private immutable image built from one source commit: the P base, the commit's environment, and `/workspace` holding a clone of the commit. |
| **Builder version** | The runtime driver's build recipe, recorded in every image's identity; it changes when the way images are built changes. |
| **Builder instance** | Disposable restricted Incus instance that clones one commit and realizes its environment. |
| **Build reference** | A UUID naming one build attempt. Every attempt gets a new one. |
| **Session delta** | Private writable Incus root derived from the image, including the session's branch, work and later `/nix` changes. |
| **Activation material** | Versioned output used to enter the selected devShell inside a session. |

| Fact | Authority | SQLite role |
|---|---|---|
| Source commit, its tree and history | P bare Git repository | creation records the captured commit |
| Nix derivations and store validity | Nix in the isolated builder or session | records the devShell selection and activation-material digest |
| Commit image bytes, reference and labels | the runtime driver's backend: the Incus image store | indexes (project, commit, base image, system, builder version) to the image reference |
| Session-private Nix database, workspace and additions | Incus instance root | no duplicate store or file records |
| Build attempt | P operation plus the builder | records the build cycle and its state in creation evidence |

If an indexed image is absent, P forgets the index row and rebuilds from the
commit; a missing image is never corrupt source state.

## Runtime driver image operations

Core reaches images only through the image part of the runtime driver,
`internal/runtimedriver`:

| Operation | What the driver does |
|---|---|
| `Describe` | Names the driver's builder version. |
| `BuildImage` | Builds an image of one commit from a Git bundle and labels it. It calls core's gate once, right before its first effect. On success, and on any failure other than an unknown outcome, it leaves no builder behind. |
| `Images` | Lists every image carrying this P instance's labels, or fails; never a partial list. |
| `InspectImage` | Reads one image by its reference; reports absence, or a mismatch for an image that is not this instance's. |
| `DeleteImage` | Deletes exactly one image, the same reference with the same labels, after calling core's gate. An image already gone succeeds. Runtimes created from the image are never affected. |
| `Settle` | Says from native state whether a build or image delete whose call returned an unknown outcome was applied. |
| `Delete` | Removes what a build left behind, such as its builder, and never an image. |

The driver stores the labels core gives an image immutably and returns them
verbatim: `project`, `source_commit`, `base_image`, `system`,
`builder_version` and `build`. Each image also records its devShell outcome,
the selection and the activation-material digest, beside those labels.

The Incus implementation, `runtimeincus.Driver`, runs inside the daemon
process. It owns the whole build: the disposable builder, the Nix steps and
the published image. No plugin takes part in resolving, realizing or
publishing. An image only has to work with the runtime that built it; no
Dockerfile or OCI contract is specified in MVP. The rest of the runtime
driver, for sessions, workspaces, terminals and project services, is not yet
implemented.

## Environment selection

Selection for the Incus host system is deterministic:

1. for the bootstrap session of a blank/empty project, use the P base image;
2. otherwise use `devShells.<system>.default` when it exists at the source
   commit;
3. otherwise the image holds the base packages only; and
4. fail when a present default devShell is invalid rather than silently
   falling back.

There is no custom P flake output, repository-controlled provider selection,
language detection, or inferred package installation. The execution system is
the Linux architecture of the Incus host, initially `x86_64-linux` or
`aarch64-linux` as validated.

The image records which case applied: `no-flake` (the commit has no root
`flake.nix`), `no-default` (its flake has no default devShell for the system)
or `devshell`. The TUI presents the source commit, system, devShell attribute
or `base`, image state (`building`, `built` or `reused`), base image
fingerprint, and image fingerprint before runtime creation completes.

## Source input and lock policy

The build's one input is a Git bundle of the source commit. It never reads the
user's current checkout or a session workspace.

P's Git service writes the bundle into a private directory under P's state for
that one build. It refuses a missing commit, an object that is not a commit,
and a commit no P head reaches. A temporary bare repository borrows P's
objects through its `objects/info/alternates` and holds the bundle's only ref,
`refs/p/source`, so P's repository is only read. The bundle holds the commit
and every commit it reaches. The export has a ten-minute deadline and never
leaves a partial bundle; core removes the directory after the build.

The driver streams the bundle through the confined Incus file API into the
stopped builder at `/opt/p/build/source.bundle`, root-owned and read-only. The
builder user clones it into `/workspace` with fixed Git arguments and no
hooks, filters, credential helpers, or system or global configuration. It
fetches the bundle's ref without submodules, checks out the commit detached,
and deletes the fetched ref. The clone is then proven: HEAD at the commit and
detached, no ref, no remote, and a clean tree. Submodules are never fetched;
their paths stay empty in the clone.

Nix evaluates the flake as `git+file:///workspace?rev=<commit>`, pinned with
the NAR hash `nix flake metadata --json` reports for that commit. Nix exports
exactly the commit's tree from the clone's Git objects, so working-tree
changes, including those a shell hook makes during the smoke test, cannot
alter what it evaluates.

For flakes, P:

- evaluates in pure mode;
- disables lock creation and updates;
- disables ambient registry and `NIX_PATH` dependence;
- rejects untrusted repository Nix settings;
- supplies an explicit system; and
- fails with an actionable instruction when the committed lock is insufficient.

The repository may run arbitrary code through Nix evaluation/build semantics,
which is why cloning, resolution and realization occur only inside the
restricted builder instance. The host never checks out the commit's tree.

The builder requires a trusted, separately selected btrfs storage pool because
the pinned Incus `dir` driver may accept a root size while silently skipping
its quota. The driver creates a stopped, resource-bounded private builder root
from a pinned base fingerprint, verifies its identity before transitions or
cleanup, and refuses to adopt an unknown instance at the expected name. The
pinned guest activation compatibility gate has passed, as recorded in
[implementation progress](implementation-progress.md). VM scenario 19 checks
that the exported bundle clones to exactly the commit; scenarios 21, 22 and 24
cover the builder, its Nix steps and an image built end to end, and scenarios
25 and 26 the daemon's commit images. These scenarios build offline fixtures.
No committed check builds through a public builder; one exploratory VM run
did, from a locked remote nixpkgs input ([checks debt](debts/checks.md)).

## P base image

P publishes and pins one base image per supported architecture and runtime-kit
contract. It contains:

- a functional local Nix daemon and client configured for a private instance
  store and the [container Nix isolation policy](runtime-isolation.md#runtime-process-model);
- Git, OpenSSH, CA certificates, shell, and basic userland;
- tmux and the P runtime helper;
- the fixed unprivileged session user and fixed runtime paths; and
- no project source, user dotfiles, host credential, Incus socket, or origin
  configuration.

The base is an Incus-native system-container image identified by fingerprint,
not a mutable alias. Updating P or Nix produces a new base fingerprint and
therefore new commit images, even for a commit that already has one; existing
sessions keep their original root.

## Image identity and contents

**What names an image.** The project, the source commit, the base image
fingerprint, the execution system and the builder version. P never reuses an
image across P projects: the image holds the project's code, and a Nix closure
may retain source-derived store paths.

The builder version is the Incus driver's build recipe: the image contract
and compression, the builder's resources, the Nix version and rules it runs,
and the activation-material schema and adapter version. The builder's
network is not part of it, so the same commit built offline or through public
egress names the same image.

**What it holds:**

- the P base image;
- the commit's default devShell realized into `/nix`, with its activation
  material and a P-owned garbage-collector root; a commit without a default
  devShell gets the base packages only; and
- `/workspace`: a clone of P's repository at the commit, owned by the session
  user, detached, with no branch, ref, remote or credentials.

**What it never holds:** anything that belongs to one session: its UUID, its
branch, its Git principal and keys, its endpoints, its home directory, its
interactive state. The `workspace-ready` creation phase adds those when a
session starts, so one image serves every session from its commit. The image
also holds no source bundle, builder scratch, transient logs, builder network
configuration or material from another P project.

**Where the identity lives.** P's SQLite commit-image index is the authority
for which image a session from a commit uses. It maps the five identity facts
to the driver's image reference and records the build reference, the labels,
the devShell selection, the activation-material digest, the logical size, a
state (`ready` or `releasing`) and the creation time. The Incus driver stores
the same facts as image properties, so they show in `incus image list`:

| Property | Value |
|---|---|
| `p.contract`, `p.compression` | `p.incus-commit-image/v1`, `none` |
| `p.instance`, `p.incus_project` | the P instance ID and the confined Incus project |
| `p.project_path`, `p.source_commit`, `p.base_image`, `p.system`, `p.builder_version`, `p.build` | the labels core gave the build |
| `p.selection` | `no-flake`, `no-default` or `devshell` |
| `p.material`, `p.capture_store_path` | for `devshell` only: the activation-material digest and the captured environment's store path |

The base image's own properties are inherited as well. The driver lists the
images whose `p.instance` is this instance. One of them that it cannot read as
a commit image, with the commit-image contract and valid labels, fails the
listing instead of being left out. P checks an image's labels before using or
deleting it.

## Build and publish pipeline

### 1. Stage and transfer

Core stages the Git bundle of the commit (see
[source input](#source-input-and-lock-policy)) and asks the driver to build
under a new build reference. After its checks the driver calls core's gate,
creates the stopped builder from the pinned base, and transfers the bundle.

### 2. Clone and resolve

The builder boots, clones the bundle into `/workspace`, and resolves the
default devShell from the clone: `git ls-tree` decides whether the commit has
a root `flake.nix`, then pure Nix evaluation of the pinned flake reference
decides whether a default devShell exists and reads its derivation.

### 3. Realize

The builder's local Nix daemon realizes the environment in its own writable
`/nix`. It receives no host Nix daemon or store, and P runs no package cache.
With the host's public egress, Nix fetches the committed flake inputs over
HTTPS and substitutes from the public binary cache `https://cache.nixos.org`,
the one substituter the base image trusts; the host, LAN and private ranges
stay unreachable ([security boundary](#security-boundary)). Without public
egress, Nix runs offline with no substituters, so only an environment that
needs nothing from the network builds. Each Nix command in the builder has a
five-minute ceiling. Resolution and realization each end after seven minutes,
their wait included: at most two of them run at once across the daemon. The
builder follows the
[container Nix isolation policy](runtime-isolation.md#runtime-process-model):
Incus provides the required isolation, and Nix build sandboxing is disabled
inside the builder. Pure evaluation and the committed-source/lock rules remain
in force.

A commit without a default devShell skips realization, capture and the smoke
test.

### 4. Capture

P uses the pinned Nix adapter, initially `nix print-dev-env --json`, to capture
variables and shell functions and generate versioned activation material. The
project shell hook still executes only inside the builder smoke test and final
session. Neither the P daemon nor Incus host process sources generated or
project activation code.

### 5. Root

The builder creates a P-owned garbage-collector root for the selected
environment so session-local Nix collection cannot remove the image's initial
closure from its logical image state.

### 6. Verify

Inside the builder, P verifies:

- expected system and derivation identity;
- Nix database/store consistency;
- activation bounds and required executables;
- fixed user, home, workspace, and runtime-kit paths;
- a smoke activation in the clone without P Git, model, origin, or user
  credentials; and
- that resolving again yields the same NAR hash and derivation.

### 7. Scrub and collect

With the builder stopped, P empties the builder user's writable trees, the
workspace included, and the user Nix roots, so nothing a shell hook left can
keep store paths alive. It boots the builder again, collects unreachable Nix
paths, and makes a fresh clone of the bundle in `/workspace`, after the last
hook has run. With the builder stopped again, the final scrub removes the
scratch trees, the source bundle, transient logs and network configuration,
and keeps the fresh clone. A public builder's address and route end with each
boot; its resolver file gives way to the image's own `/etc/resolv.conf` link.

A retained devShell closure may still contain committed source-derived store
paths required by Nix. Before publishing, P verifies the activation material
and the workspace: the session user's clone with HEAD detached at the commit
and no initialization marker, so every session initializes it.

### 8. Publish

P asks Incus to publish the stopped builder as a private immutable image with
the labels as properties. Incus supplies the fingerprint and owns the bytes. P
verifies that the fingerprint is new, that the image's properties are exactly
the base's plus the labels, and that it is private, unaliased, not
auto-updating and of the system's architecture.

The publisher explicitly selects an uncompressed Incus archive. This avoids
the pinned NixOS gzip wrapper's incompatibility with Incus's confined
extractor without changing the extractor or container isolation. The archive
choice is part of the builder version; it must not depend on mutable host
compression defaults. This costs more image storage than compression, which
the reported logical image size reflects.

### 9. Clean builder

The driver deletes the builder once the image is verified, and returns the
image with its reference, labels, devShell outcome and size. Core then indexes
it. A builder that cannot be deleted after publication makes the build's
outcome unknown; core settles it ([failures and retry](#failures-and-retry)).

No project branch ref is created or changed by image building.

## Nix activation compatibility gate

`nix print-dev-env` is an experimental Nix command whose interface may change.
It is an adapter contract, not an assumed stable platform API. Each supported
Nix version must pass a pinned compatibility suite covering:

- required experimental-feature flags and complete structured argv;
- the `--json` top-level schema and every accepted variable/function type;
- path, array, quoting, unset/export, shell-function, and shell-hook behavior;
- exit status and bounded diagnostic behavior for evaluation/build failures;
- generated activation equivalence against `nix develop` fixtures; and
- rejection of unknown fields/types or an unsupported Nix version without
  falling back to sourcing raw output.

The supported version and adapter schema version are part of the builder
version. Builder startup refuses activation capture when the compatibility
gate has not passed. Upgrading Nix requires rerunning the suite before the
version is accepted; existing images retain their recorded adapter contract.

See the upstream [`nix print-dev-env` reference](https://nix.dev/manual/nix/stable/command-ref/new-cli/nix3-print-dev-env),
including its experimental-interface warning.

The internal adapter in `internal/nixenv` targets Nix `2.34.8` and
material schema `p.nix-activation/v1`. It reads only the `variables`,
`bashFunctions`, and optional `structuredAttrs` fields emitted by that release;
accepted variable types are `var`, `exported`, `array`, and `associative`.
There is no JSON unset type in this release. Activation clears `shellHook`
before applying captured values and clears each applied variable before
setting its captured type. It rewrites derivation output paths to
`/workspace/outputs/<output>` and structured-attribute file references to
`/etc/p/devshell/.attrs.{sh,json}`. The fixed session identity, workspace,
runtime, Git, and SSH settings take precedence over captured values.
Material carries the SHA-256 of the captured JSON, and a canonical material
digest binds an image to the bytes selected by a trusted builder. These hashes
provide identity, not authentication: image assembly must still verify image
ownership and root-owned activation files before guest use.

This adapter has host-side parser, renderer, sourced fixture, and Bash syntax
tests, plus a passing pinned VM compatibility gate comparing generated
activation against `nix develop`, including shell hooks and structured
attributes. The sourced host fixture runs only known test code; repository
Nix expressions and captured project hooks run only in the guest. See
[implementation progress](implementation-progress.md) for the evidence and its
limits. Trusted environment configuration enables the devShell path; omitting
it retains base-only creation. No committed check covers public fetching
through the builder's public egress.

## Image Nix store model

The published image contains one coherent `/nix` view:

- all initial store paths required by the base and selected devShell;
- the matching Nix database and trusted-key/substituter configuration;
- the P-owned GC root; and
- profiles/activation material required by `p-interactive.service`.

It may also contain committed source-derived store paths retained by that
closure. These are immutable project inputs, and one reason images are never
shared between P projects.

When Incus creates a session instance, its private root begins from those
bytes. The exact block sharing is Incus storage-driver behavior; logically the
image is immutable and the session root is private. Incus images are not
layered: each commit image is a full copy on disk.

The design deliberately avoids:

- a shared writable store or daemon across sessions;
- a host store/daemon mount;
- individually mounted closure paths with an unrelated database;
- manual overlay construction by P; and
- automatic promotion of session-built paths or session work into an image.

## Session activation and later Nix work

After Incus creates the instance from the commit image, P installs only
session-scoped endpoints and credentials. On first start the runtime kit puts
the image's clone on the session's branch: as the session user, it checks that
HEAD is at the captured commit and that the commit's root flake matches the
image's selection, adds P as `origin`, fetches the assigned branch and requires
it still at that commit, then creates and checks out the branch with `origin`
as its upstream ([runtime isolation](runtime-isolation.md#fixed-paths)). For the
bootstrap exception, the base image's workspace gets an unborn `main`. When
systemd starts `p-interactive.service`, the unit then:

1. establishes the fixed user, `HOME`, workspace, and P paths;
2. applies the captured devShell activation;
3. runs the shell hook inside the session;
4. starts the configured persistent host (tmux by default); and
5. keeps that host under systemd supervision.

Activation or host-start failure is captured in the container journal and
causes the container to stop. Creation remains failed; a later exact Retry
cleans verified partial derived resources and reruns creation. An established
session uses ordinary Start to try activation again. P never runs a fallback
environment.

Nix remains available through the session-local Nix daemon. If `flake.nix`,
`flake.lock`, or other inputs change, ordinary Nix commands may realize new
paths into that session's private `/nix`. Those paths survive stop/start and
disappear with the instance. They do not modify the image or another session.

Only a source commit can produce an image. P never turns session state,
committed or not, into an image; a later session from the commit the session
pushed builds that commit's own image.

## Lookup, concurrency, and image lifetime

**Lookup.** Creation's `image-ready` phase looks up the commit-image index by
(project, commit, base image, system, builder version). A hit needs no
builder: P inspects the indexed image through the driver and uses it when its
labels and devShell outcome equal the row's. An image that differs is never
used or deleted: the creation fails. An indexed image that is gone is a miss:
P forgets the row and builds. A row marked `releasing` is never reused: P
finishes that image's delete exactly, forgets the row and builds. If the
accepted image is gone before the runtime is created, P forgets the row and
builds again.

**One image per commit at a time.** A per-commit lock in the daemon,
keyed by project and commit, is held through lookup, build and acceptance, so
two creations from one commit build at most one image and the second, which
waits for the lock, reuses it. Each build has its own build reference, and its
builder is named from it.

**When P deletes an image.** An image is kept while some session started from
its commit exists, in any registry state: `creating` (including a failed
creation waiting for Retry), `established` or `removing`. When the last such
session is gone, the daemon's release step deletes the image. Creation records
each session's source commit, and it never changes, so "is this image in use?"
is "does any session of this project have this source commit?". The rule looks
at the commit, not the whole identity: after an upgrade changes the base image
or builder version, a commit's older image is kept as long as any session from
that commit exists, even one created from its newer image.

Release runs in the background after each operation of a project leaves its
worker and once per project at daemon start, one run per project at a time; it
is skipped for a project being deleted. An operation never waits for it or
fails because of it. Release takes each commit's lock only when it is free, so
it leaves a commit a creation is using to a later run. Under that lock,
release:

- for a `ready` row whose commit has no session, marks the row `releasing` in
  the gate of the driver's `DeleteImage`, deletes that exact image, and
  forgets the row; SQLite refuses the mark while any session from the commit
  exists;
- finishes a `releasing` row the same way; and
- deletes exactly an image the driver lists with this project's labels that
  the index does not hold, such as a late or unaccepted build, unless a
  `creating` session from that commit exists.

If the driver cannot be reached or a delete fails, everything stays for the
next run. Because release and creation share the per-commit lock, a session is
never created from an image that is being deleted.

**Project deletion** deletes all of the project's images.
[Project deletion](control-api.md#project-deletion) plans each indexed image
through the driver, deletes it exactly after the project's runtimes are proven
absent, and removes its row. It plans at most twenty
indexed images, fewer when the deletion's evidence cannot record them all
(eight with four sessions and the longest names P accepts), and refuses a
project with more. An image labeled with the
project that the index does not hold also refuses the deletion; release
normally deletes such an image first.

Existing Incus instances own their private roots independently of the image:
deleting an image never affects a session already created from it. A missing
externally removed image is a miss for the next creation.

A session's image is fixed at its creation. If a missing session runtime also
lost its image, P does not rebuild it or infer a replacement from the branch
name. A new session on the retained branch starts from the branch's current
commit like any other creation.

## Security boundary

The builder is a separate unprivileged Incus system container in the same
confined P execution project but is never a session. It receives:

- a Git bundle of the source commit, read-only;
- its private root and bounded scratch space;
- the public-egress network, when the host configures it; and
- no other P runtime or host authority.

It receives no Incus socket, host filesystem grant, session workspace, SSH
agent, origin/P Git private key, model key, event-handler credential, host Nix
state, or general host/LAN route. Public egress must pass the same
destination-isolation evidence required by runtime policy.

Builders reach the public internet only, as the maintainer decided on
2026-11-02. When the host configures the
[public-egress substrate](runtime-isolation.md#network-contract), every builder
gets the one NIC a public-egress session gets, whatever its project's session
policy: on the trusted bridge, behind its ACL, with the same proofs before each
create and start. It allows outbound TCP 80 and 443 and DNS to the pinned
resolvers, and denies the host, LAN, private, carrier-grade NAT, link-local,
metadata, multicast and sibling destinations over IPv4 and IPv6.

- **Address.** The builder uses its creating session's static address slot. A
  public-egress session lends its own; for any other session P holds a free
  slot for the builder until the session's image is ready. With all four
  slots held, the build fails and can be retried.
- **Guest network.** After each boot, before any Nix or project code runs, the
  image's runtime kit runs as root with fixed arguments and configures the
  address, the default route and the loopback DNS-over-HTTPS resolver: the steps
  a public session's unit runs.
- **Intent.** The builder's network and the substrate's digest belong to the
  builder policy recorded in creation intent, so a creation never resumes under
  another network.

A host without the substrate, such as the hardened NixOS service, keeps its
builders offline: no NIC and no substituters.

MVP supplies no build secrets. Private flake inputs, authenticated caches,
remote builders, KVM devices, and deployment credentials require separate
future capabilities rather than ambient credential mounts.

## Failures and retry

A build is a mutation with a gate. Before the gate fires nothing has happened.
When it fires, core records in the creation's evidence that the build of the
next cycle was attempted. After that, a failure is either definite or of
unknown outcome:

- **Definite.** The driver failed before publishing and proved its builder
  deleted. Core records the cycle as settled absent and the creation fails.
- **Unknown.** The publish command was sent, or the builder could not be
  removed. The creation fails with the cycle still attempted.

Exact Retry first settles an attempted cycle through the driver's `Settle`.
When the build is proven applied, core finds the image by its build label,
removes the build's leftovers and accepts the image. Otherwise it removes the
leftovers, records the cycle absent and builds again. A build is never sent
again under the same reference: the next attempt gets a new build reference,
and an image a delayed publish of the old reference produces later is deleted
by release.

| Failure | Behavior |
|---|---|
| No default devShell | The image holds the base packages only |
| Invalid devShell or lock mutation required | Fail resolution; do not fall back. The creation blocks with reason `image_build_failed` |
| Fetch/substitution/realization failure | Report bounded Nix diagnostic; the builder is deleted and the failure is definite (reason `image_build_failed`) |
| All four public address slots held | Fail before the builder exists; retry once a slot is free |
| P loses contact with an Incus build operation | The outcome is unknown (reason `outcome_unknown`); Retry settles it from Incus state, without duplicating its operation state machine |
| Verification fails | Do not publish or index; retain a bounded diagnostic and delete the builder |
| Publish succeeds but P loses the response | Unknown outcome (reason `outcome_unknown`); Retry settles it and accepts the exact labeled image, or a later release deletes it |
| Indexed image missing | Forget the row and rebuild |
| Activation fails during creation | Capture bounded systemd/journal diagnostics, stop the container, and leave the creation operation failed; exact Retry rebuilds derived resources idempotently |
| Activation fails during established Start | Capture bounded systemd/journal diagnostics and stop the container; ordinary Start retries activation |
| Disk exhaustion | Preserve already valid Incus/Nix data and show cleanup options |

Image building is derived work, not source authority. It may fail and be
retried without deleting the committed P branch.

## What users notice

- A session from a commit that already has an image starts with no build and
  no clone.
- A session from a new commit builds an image, even when only code changed: a
  full build, and a full image on disk while sessions from that commit exist.
- Discard followed by Create on the retained branch normally builds an image:
  if the discarded session pushed work, the branch moved, so the new session
  starts from a different commit; if not, the commit's image is deleted once
  its last session is gone, unless another session from that commit still
  exists, and is rebuilt.
- Code now also lives in images. Deleting a branch does not remove its code
  from images built from its commits; the release rule or project deletion
  does. A secret committed by mistake therefore stays in an image until that
  image is deleted.
- Release never fails a session creation. A creation from a commit whose image
  release is deleting at that moment waits for the delete, then builds a new
  image. There is no manual image cleanup command.

## Performance and evidence

P specifies no universal cold-build threshold. It records:

- base-image hit;
- commit-image hit/miss;
- evaluation, realization, verification, publication, and instance-create
  durations;
- substituted and locally built paths/bytes;
- logical image size;
- storage-driver-reported physical use when available; and
- private per-session root growth after representative work.

The acceptance target is fast session creation from a commit that already has
an image. Cold build performance depends on the project and the public cache.
Evidence must cover empty, substituted, and warm cases on supported
architectures and the user-selected real repository. The current target is
this project's latest remote `main`, as recorded in
[implementation progress](implementation-progress.md#real-repository-fixture).

## API and channel boundary

- **Git** supplies a bundle of the source commit to the builder, through the
  Incus file API, and the session's assigned branch to its workspace after
  runtime creation.
- **Host RPC** requests builds, observes bounded progress, and receives image
  identity; it never transports image bytes.
- **Incus API/operations** create the builder, publish and delete the image,
  create the session root, and report runtime/image truth.
- **Nix fetch traffic** reaches only public destinations through the
  builder's public-egress network, and nothing on a host without one.
- **Incus image/storage** retains image and instance bytes.

Independent P instances build their own images or obtain inputs from ordinary
Nix substituters. They do not exchange P index metadata or images.

## MVP boundary

MVP includes:

- conventional default flake devShell or base-only behavior;
- pure committed-source resolution from a clone of a Git bundle;
- one pinned P Nix base per supported architecture;
- restricted Incus builder instances, with public egress where the host
  configures it;
- private Incus commit images with coherent Nix stores and a clone of the
  commit;
- private writable session Nix state;
- exact activation inside sessions;
- a pinned `nix print-dev-env --json` compatibility gate;
- a commit-image index by project, commit, base image, system and builder
  version;
- deletion of an image once no session from its commit exists;
- bounded progress and explicit retry; and
- removal of a project's images with the project.

MVP excludes Dockerfile/devcontainer providers, OCI packaging, raw closure
mounts, shared writable Nix stores, host Nix access, automatic promotion from
sessions, build secrets, remote builders, a P-run package cache, submodule
fetching, and portable image distribution between P instances.

## Acceptance criteria

The MVP commit-image path is supported when tests prove:

1. cloning, resolution and realization use only a bundle of the source commit
   and cannot reach ambient host authority;
2. a second session from the same commit reuses its image and runs no builder;
3. a session from a new commit, including one that changes only code, builds
   and indexes a new image;
4. the image contains a coherent Nix store/database, exact activation for the
   selected devShell, and a clean clone of the commit, detached, with no ref,
   remote or credentials;
5. the source bundle, transient builder state, credentials, session identity,
   and other-project material are absent;
6. two sessions start from that image while their branches, work, new Nix
   paths and database changes are private and persist across each session's
   stop/start;
7. changing a workspace flake realizes only into the session delta and never
   mutates the image;
8. the index and the image's labels agree, and removing or losing an image
   yields a rebuild rather than source/session corruption;
9. an image is deleted when the last session started from its commit is gone,
   and kept while a creation waiting for Retry or a session being removed
   still has that commit; release never deletes an image while a creation
   holds its commit's lock, and a `releasing` row is never reused;
10. the configured Incus storage driver's actual sharing and private growth are
    measured rather than assumed;
11. the selected real-repository validation completes its accepted workflow with
    no host Nix daemon, Incus socket, ambient credentials, or fleet route;
12. every supported Nix version passes the experimental `print-dev-env --json`
    schema/activation compatibility suite and unsupported versions fail
    closed;
13. a blank/empty-origin bootstrap without a source commit uses the P base
    image and performs no repository evaluation; and
14. confirmed whole-project deletion idempotently removes every image of the
    project.
