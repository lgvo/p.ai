# P — Nix environment images

How P prepares and caches a Nix development environment as an immutable Incus
image without sharing writable Nix state between sessions.

> **Status: design.** This document is authoritative for environment
> selection, isolated realization, Incus image caching, activation, and cache
> collection. [Runtime and isolation](runtime-isolation.md) owns the final
> session instance and its private writable state.

## Contents

- [The rule](#the-rule)
- [Terms and authorities](#terms-and-authorities)
- [Reusable builder boundary](#reusable-builder-boundary)
- [Environment selection](#environment-selection)
- [Immutable source and lock policy](#immutable-source-and-lock-policy)
- [P base image](#p-base-image)
- [Environment key](#environment-key)
- [Build and publish pipeline](#build-and-publish-pipeline)
- [Nix activation compatibility gate](#nix-activation-compatibility-gate)
- [Cached Nix store model](#cached-nix-store-model)
- [Session activation and later Nix work](#session-activation-and-later-nix-work)
- [Caching, concurrency, and collection](#caching-concurrency-and-collection)
- [Security boundary](#security-boundary)
- [Failures and retry](#failures-and-retry)
- [Performance and evidence](#performance-and-evidence)
- [API and channel boundary](#api-and-channel-boundary)
- [V1 boundary](#v1-boundary)
- [Acceptance criteria](#acceptance-criteria)

## The rule

V1 prepares one immutable Incus environment image for each resolved default
Nix devShell. The image contains the P base, the realized devShell closure, a
coherent Nix store database, and activation material. Incus supplies private
writable instance storage on top of that image for every session.

```text
P Nix base image
  → isolated builder realizes committed devShell
  → immutable cached Incus image fingerprint
      ├─ session A private root and Nix additions
      └─ session B private root and Nix additions
```

No session mounts the host `/nix`, uses the host Nix daemon, or shares a
writable Nix database. The cached image is disposable acceleration; the
committed Git definition remains reproducible source authority.

The workspace, session UUID, branch, credentials, home, and interactive state
are never part of the cached image.

## Terms and authorities

| Term | Meaning |
|---|---|
| **P base image** | P-owned Incus system-container image containing Nix, Git, SSH, tmux, basic userland, the fixed user, and runtime kit. |
| **Environment selection** | The committed repository's conventional default devShell, or no project environment. A bootstrap session without a commit uses only the P base image. |
| **Environment key** | Project-scoped reproducible identity of the base, resolved Nix environment, system, and builder contract. |
| **Builder instance** | Disposable restricted Incus instance that evaluates and realizes one committed environment. |
| **Environment image** | Project-scoped private immutable Incus image published from a verified builder and addressed by fingerprint. |
| **Session delta** | Private writable Incus root derived from the image, including later `/nix` changes. |
| **Activation material** | Versioned output used to enter the selected devShell inside a session. |

| Fact | Authority | SQLite role |
|---|---|---|
| Source definition and lock | committed Git tree | records selected commit/input digest |
| Nix derivations and store validity | Nix in isolated builder/session | records resolved environment identity |
| Environment image bytes and fingerprint | Incus image store | indexes project-scoped environment key to fingerprint |
| Session-private Nix database and additions | Incus instance root | no duplicate store records |
| Build request/progress | P operation plus current Incus builder operation | bounded presentation only |

If an indexed Incus image is absent, P has a cache miss, not corrupt source
state. It rebuilds from the committed definition.

## Reusable builder boundary

The environment seam remains reusable:

```go
type EnvironmentBuilder interface {
    Resolve(context.Context, EnvironmentRequest) (EnvironmentPlan, error)
    Build(context.Context, EnvironmentPlan, EnvironmentTarget) (EnvironmentHandle, error)
}
```

`EnvironmentHandle` contains a target kind/contract version, content identity,
and opaque immutable locator. Lifecycle and generic runtime code do not inspect
the locator. V1 returns target kind `incus-system-image`; only the Incus adapter
interprets its locator as an image fingerprint.

V1 has one implementation: default Nix devShell to an Incus system-container
image. The interface preserves separation from session identity and runtime
lifecycle; it does not require V1 capability negotiation among hypothetical
formats or additional providers.

The Incus backend supplies the builder isolation and image publication target.
A future environment provider or backend may implement another image form
without changing Git/session identity, but no Dockerfile or OCI contract is
specified in V1.

## Environment selection

Selection for the Incus host system is deterministic:

1. for the bootstrap session of a blank/empty project, use the P base image;
2. otherwise use `devShells.<system>.default` when it exists at the selected
   commit;
3. otherwise use the P base image directly; and
4. fail when a present default devShell is invalid rather than silently
   falling back.

There is no custom P flake output, repository-controlled provider selection,
language detection, or inferred package installation. The execution system is
the Linux architecture of the Incus host, initially `x86_64-linux` or
`aarch64-linux` as validated.

The TUI presents the selected commit, system, devShell attribute, base image
fingerprint, cache hit/miss, and environment-image fingerprint before runtime
creation completes.

## Immutable source and lock policy

Resolution receives a read-only tree for the exact committed source. It never
reads the user's current checkout or dirty session workspace.

For flakes, P:

- evaluates in pure mode;
- disables lock creation and updates;
- disables ambient registry and `NIX_PATH` dependence;
- rejects untrusted repository Nix settings;
- supplies an explicit system; and
- fails with an actionable instruction when the committed lock is insufficient.

The repository may run arbitrary code through Nix evaluation/build semantics,
which is why resolution and realization occur only inside the restricted
builder instance.

## P base image

P publishes and pins one base image per supported architecture and runtime-kit
contract. It contains:

- a functional local Nix daemon and client configured for a private instance
  store, with build-sandbox posture recorded;
- Git, OpenSSH, CA certificates, shell, and basic userland;
- tmux and the P runtime helper;
- the fixed unprivileged session user and fixed runtime paths; and
- no project source, user dotfiles, host credential, Incus socket, or origin
  configuration.

The base is an Incus-native system-container image identified by fingerprint,
not a mutable alias. Updating P or Nix produces a new base fingerprint and
therefore a new environment key; existing sessions keep their original root.

## Environment key

The key follows actual build identity:

```text
provider and adapter version
execution system
P project path (V1 cache-isolation scope)
P base-image fingerprint and runtime-kit contract
committed flake/lock inputs required to resolve the default devShell
resolved derivation identity
trusted Nix substituter/sandbox policy affecting realization
environment-image format version
```

Two commits in one project may reuse an immutable image when the resolved
derivation and all other key inputs match. V1 does not reuse images across P
projects, even when derivations match: a Nix closure may retain source-derived
store paths, so cross-project reuse would widen source visibility. A
source-only commit does not invalidate the cached image merely because its Git
commit ID changed when the resolved environment identity is unchanged.

SQLite stores `environment_key → Incus image fingerprint` plus bounded build
metadata. It never stores image bytes or Nix store records.

## Build and publish pipeline

### 1. Resolve

P creates a disposable restricted builder from the pinned base, materializes
the immutable source under a build-only path, and resolves the default devShell
without writing the source.

### 2. Realize

The builder's local Nix daemon realizes the environment in its own writable
`/nix` using only configured public substituters/fetches. It receives no host
Nix daemon or store. Nix's build sandbox remains enabled unless a pinned
validation records a specific safe limitation.

### 3. Capture

P uses the pinned Nix adapter, initially `nix print-dev-env --json`, to capture
variables and shell functions and generate versioned activation material. The
project shell hook still executes only inside the builder smoke test and final
session. Neither the P daemon nor Incus host process sources generated or
project activation code.

### 4. Root

The builder creates a P-owned garbage-collector root for the selected
environment so session-local Nix collection cannot remove the cached initial
closure from its logical image state.

### 5. Verify

Inside the builder, P verifies:

- expected system and derivation identity;
- Nix database/store consistency;
- activation bounds and required executables;
- fixed user, home, workspace, and runtime-kit paths; and
- a smoke activation without P Git, model, origin, or user credentials.

### 6. Scrub

P removes the materialized checkout, temporary build files, transient logs,
network configuration, and other builder-only data, then collects unreachable
temporary Nix paths. A retained devShell closure may still contain committed
source-derived store paths required by Nix. The image is therefore private and
project-scoped. It must contain no writable workspace, session UUID, private
key, origin credential/remote, or material from another P project.

### 7. Publish

P stops the builder and asks Incus to publish it as a private immutable image.
Incus supplies the fingerprint and owns the bytes. P verifies the fingerprint
and image metadata before registering the environment-key index.

### 8. Clean builder

P deletes the builder instance after the image is verified. A failed cleanup
leaves a labeled builder orphan for explicit cleanup; it does not invalidate a
verified image or become a session.

No project branch ref is created or changed by environment building.

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

The supported version and adapter schema version contribute to the environment
key. Builder startup refuses activation capture when the compatibility gate has
not passed. Upgrading Nix requires rerunning the suite before the version is
accepted; cached images retain their recorded adapter contract.

See the upstream [`nix print-dev-env` reference](https://nix.dev/manual/nix/stable/command-ref/new-cli/nix3-print-dev-env),
including its experimental-interface warning.

## Cached Nix store model

The published image contains one coherent `/nix` view:

- all initial store paths required by the base and selected devShell;
- the matching Nix database and trusted-key/substituter configuration;
- the P-owned GC root; and
- profiles/activation material required by `p-interactive.service`.

It may also contain committed source-derived store paths retained by that
closure. These are immutable project inputs, not the session workspace, and
are one reason environment-image reuse is scoped to a P project in V1.

When Incus creates a session instance, its private root begins from those
bytes. The exact block sharing is Incus storage-driver behavior; logically the
image is immutable and the session root is private.

The design deliberately avoids:

- a shared writable store or daemon across sessions;
- a host store/daemon mount;
- individually mounted closure paths with an unrelated database;
- manual overlay construction by P; and
- automatic promotion of session-built paths into the cache.

## Session activation and later Nix work

After Incus creates the instance, P installs only session-scoped endpoints and
creates the standalone clone at `/workspace`. For the bootstrap exception,
that clone has an unborn `main`. When systemd starts `p-interactive.service`,
the unit then:

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
disappear with the instance. They do not modify the cached image or another
session.

Only committed source can produce a new shared environment image. P never
turns dirty or uncommitted session state into a cache image automatically.

## Caching, concurrency, and collection

Incus is the image cache and byte authority. P performs a cache hit by looking
up the environment key and verifying that exact fingerprint in the configured
Incus project.

Concurrent requests for one missing key may share one in-memory build job and
progress stream. Durable single-flight scheduling is not required: after a P
restart, a verified image is a hit and an incomplete build may be retried.
Deterministic builder names/metadata prevent accidental adoption as sessions.

P does not maintain artifact leases. Existing Incus instances own their
private roots independently of the cache index. Removing an environment image
never occurs as a side effect of stop, discard, or session deletion.

Cache collection is explicit. It itemizes P-owned environment keys, image
fingerprints, last-use metadata, logical size, and currently related sessions
before removing the index and image. The preview warns that an existing
instance continues without the image but may no longer be exactly recreatable
if it is later lost and its branch environment has changed. This warning does
not introduce a lease or implicit retention rule. A missing externally removed
image is a cache miss. V1 has no automatic age- or pressure-based collection.

The other explicit removal path is confirmed **Delete project and all P data**.
Its aggregate project preflight includes these project-scoped keys and images,
and its ensure-absent retry applies the same image-authority rules. Session
Stop, Discard, and Delete never remove a cached image.

## Security boundary

The builder is a separate unprivileged Incus system container in the same
confined P execution project but is never a session. It receives:

- immutable committed source;
- its private root and bounded scratch space;
- configured public Nix substituters/fetch traffic; and
- no other P runtime or host authority.

It receives no Incus socket, host filesystem grant, workspace, SSH agent,
origin/P Git private key, model key, event-handler credential, host Nix state, or
general host/LAN route. Public egress must pass the same destination-isolation
evidence required by runtime policy.

V1 supplies no build secrets. Private flake inputs, authenticated caches,
remote builders, KVM devices, and deployment credentials require separate
future capabilities rather than ambient credential mounts.

## Failures and retry

| Failure | Behavior |
|---|---|
| No default devShell | Use the pinned P base image directly |
| Invalid devShell or lock mutation required | Fail resolution; do not fall back |
| Fetch/substitution/realization failure | Report bounded Nix diagnostic; explicit retry starts from current Incus/Nix cache facts |
| P loses contact with an Incus build operation | Query Incus; do not duplicate its operation state machine |
| Builder disappears | Treat as interrupted cache build and retry |
| Verification fails | Do not publish/register; retain bounded diagnostic and remove or expose builder cleanup |
| Publish succeeds but P loses response | Inspect P-labeled images/build metadata; use exact verified match or retry without guessing |
| Indexed fingerprint missing | Cache miss and rebuild |
| Activation fails during creation | Capture bounded systemd/journal diagnostics, stop the container, and leave the creation operation failed; exact Retry rebuilds derived resources idempotently |
| Activation fails during established Start | Capture bounded systemd/journal diagnostics and stop the container; ordinary Start retries activation |
| Disk exhaustion | Preserve already valid Incus/Nix data and show cleanup options |

Environment building is cache work, not source authority. It may fail and be
retried without deleting the committed P branch.

## Performance and evidence

P specifies no universal cold-build threshold. It records:

- base-image hit;
- environment-image hit/miss;
- evaluation, realization, verification, publication, and instance-create
  durations;
- substituted and locally built paths/bytes;
- logical environment-image size;
- storage-driver-reported physical use when available; and
- private per-session root growth after representative work.

The acceptance target is fast session creation after an environment-image hit.
Cold build performance depends on the project and cache state. Evidence must
cover empty, substituted, and warm cases on supported architectures and the
real homelab repository.

## API and channel boundary

- **Git** supplies immutable committed source to the builder and creates the
  session workspace only after runtime creation.
- **Host RPC** requests builds, observes bounded progress, and receives image
  identity; it never transports image bytes.
- **Incus API/operations** create the builder, publish the image, create the
  session root, and report runtime/image truth.
- **Nix fetch traffic** reaches only configured public sources under the
  builder network policy.
- **Incus image/storage** retains cached image and instance bytes.

Independent P instances rebuild the same environment or obtain inputs from
ordinary Nix substituters. They do not exchange P cache metadata or images.

## V1 boundary

V1 includes:

- conventional default flake devShell or base-only behavior;
- pure committed-source resolution;
- one pinned P Nix base per supported architecture;
- restricted Incus builder instances;
- private Incus environment images with coherent cached Nix stores;
- private writable session Nix state;
- exact activation inside sessions;
- a pinned `nix print-dev-env --json` compatibility gate;
- cache index by environment key and fingerprint;
- bounded progress and explicit retry; and
- explicit cache collection.

V1 excludes Dockerfile/devcontainer providers, OCI packaging, raw closure
mounts, shared writable Nix stores, host Nix access, automatic promotion from
sessions, build secrets, remote builders, automatic cache collection, and
portable image distribution between P instances.

## Acceptance criteria

The V1 environment path is supported when tests prove:

1. resolution and realization use only immutable committed source and cannot
   reach ambient host authority;
2. two requests for the same environment key reuse one verified Incus image;
3. the image contains a coherent Nix store/database and exact activation for
   the selected devShell;
4. the materialized checkout, transient builder state, credentials, session
   identity, and other-project material are absent; any retained committed
   source exists only as a Nix store path required by the project-scoped
   closure;
5. two sessions start from that image while new Nix paths/database changes are
   private and persist across each session's stop/start;
6. changing a workspace flake realizes only into the session delta and never
   mutates the cached image;
7. removing or losing an image yields a rebuildable cache miss rather than
   source/session corruption;
8. the configured Incus storage driver's actual sharing and private growth are
   measured rather than assumed;
9. the homelab validation completes its accepted evaluate/build workflow with
   no host Nix daemon, Incus socket, ambient credentials, or fleet route;
10. every supported Nix version passes the experimental `print-dev-env --json`
    schema/activation compatibility suite and unsupported versions fail
    closed;
11. a blank/empty-origin bootstrap without a source commit uses the P base
    image and performs no repository evaluation; and
12. session destruction preserves cached images while confirmed whole-project
    deletion idempotently removes the project's cache resources.
