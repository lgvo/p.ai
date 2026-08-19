# P — environment and image building

How an immutable project revision becomes the executable environment used by a
session, without treating every session start as a Docker build or executing
repository-controlled material in the daemon's ambient host context.

> **Status: design.** This document is the authority for environment
> selection, build isolation, artifact identity, packaging, caching, runtime
> activation, and build failure behavior. [README.md](../README.md) describes
> the product, [technology-stack.md](technology-stack.md) records implementation
> choices, [runtime-isolation.md](runtime-isolation.md) owns runtime assembly
> and grants, and
> [communication-boundaries.md](communication-boundaries.md#image-build-communication)
> defines what crosses Git and RPC while a build is running.
>
> **Convention:** **Decided** — settled semantics. **Direction** — proposed
> implementation detail to validate while building. No product or architecture
> decision in this document remains open.

---

## Contents

- [The rule](#the-rule)
- [Terms](#terms)
- [Responsibility boundaries](#responsibility-boundaries)
- [Provider model](#provider-model)
- [Environment selection](#environment-selection)
- [Immutable source and lock policy](#immutable-source-and-lock-policy)
- [The P substrate](#the-p-substrate)
- [Build pipeline](#build-pipeline)
- [Closure packaging](#closure-packaging)
- [Self-contained image packaging](#self-contained-image-packaging)
- [Dockerfile provider](#dockerfile-provider)
- [Environment activation](#environment-activation)
- [Runtime assembly](#runtime-assembly)
- [Artifact identity and caching](#artifact-identity-and-caching)
- [Concurrency, progress, and cancellation](#concurrency-progress-and-cancellation)
- [Garbage collection](#garbage-collection)
- [Security boundary](#security-boundary)
- [Failures and recovery](#failures-and-recovery)
- [Performance posture](#performance-posture)
- [API and channel boundary](#api-and-channel-boundary)
- [v1 boundary](#v1-boundary)
- [Acceptance criteria](#acceptance-criteria)

---

## The rule

**Decided.** P separates four operations that are often collapsed into “build
the image”:

1. select one project environment from an immutable Git revision;
2. realize that environment under an isolation boundary;
3. package the result in a form the selected backend can consume;
4. assemble a disposable runtime from that artifact, a workspace clone, and
   P's session endpoints.

Only steps 1–3 are environment building. The Git working copy, home directory,
interactive-host state, credentials, writable caches, and session identity are
runtime inputs. They are never baked into a reusable environment artifact.

The central invariant within one provider is:

> **One resolved environment produces equivalent session toolchains across
> every packaging strategy that provider supports. Packaging may change; the
> selected environment and activation semantics may not.**

For the v1 Nix provider on `local-container`, the normal result is not a new OCI
image per project or session. P starts its immutable substrate image and mounts
the exact realized Nix closure read-only. Providers such as Dockerfile already
produce a self-contained OCI filesystem, and later targets may require Nix to
produce one too.

## Terms

| Term | Meaning |
|---|---|
| **Source snapshot** | Read-only tree for one committed Git revision selected when creating the session branch. P never snapshots a dirty checkout. |
| **Environment selection** | Conventional Nix default devShell, trusted later-provider selection, or no project layer. |
| **Environment plan** | Validated, provider-tagged description of what must be realized for one execution system. It contains no runtime identity or credentials. |
| **Environment artifact** | Immutable realized toolchain plus activation material and a manifest. It is not a running container. |
| **Substrate** | P-owned minimal base filesystem used when P supplies the image base, versioned with P. |
| **Runtime kit** | P-owned shell, Git, API helper, launcher, default interactive-host tools, and dependencies mounted into every session, including sessions based on a project OCI image. The substrate contains this same kit. |
| **Closure packaging** | Exact Nix store paths exposed read-only beside the substrate. Default in v1. |
| **Self-contained packaging** | Project environment emitted as a portable backend image, such as OCI; P's compatible runtime kit is included or supplied separately. |
| **Runtime assembly** | Backend creation of the writable session around an environment artifact. |
| **Build worker** | Isolated P-owned machinery that evaluates and realizes untrusted project environment definitions. |

“Image” in the UI is friendly shorthand for the prepared environment. APIs and
logs use the precise terms above so a closure mount is not mistaken for an OCI
build.

## Responsibility boundaries

**Decided.** Three extension seams have distinct ownership:

| Component | Owns | Must not own |
|---|---|---|
| Environment provider | Resolve one typed selection into an immutable `EnvironmentPlan` | Interpret unrelated providers, container lifecycle |
| Environment builder | Realize and package the plan for an `EnvironmentTarget` under isolation | Session identity, Git publication, runtime lifecycle |
| Runtime backend | Declare its target capabilities and assemble a runtime from an accepted artifact | Provider evaluation/building or interpretation of project environment definitions |

Conceptually:

```go
type EnvironmentBuilder interface {
    Build(context.Context, EnvironmentRequest) (EnvironmentArtifact, error)
}

type Backend interface {
    EnvironmentTarget() EnvironmentTarget
    Create(context.Context, SessionSpec, EnvironmentArtifact) (RuntimeLocator, error)
    // inspect, start, pause/resume, isolated workspace operations,
    // attach, stop, remove, events ...
}
```

The v1 Nix environment provider/builder understands devShells. The
`local-container` backend accepts both closure and OCI artifacts; it prefers
closure packaging for Nix and consumes the resulting manifest without calling
`nix` itself. A Dockerfile provider can later emit OCI to the same backend
without changing session identity or lifecycle semantics.

## Provider model

**Decided.** Nix is the first implementation, not a type embedded throughout
P. Every environment provider must implement the same lifecycle:

```text
resolve default/trusted selection → build immutable artifact
→ emit normalized manifest → pass backend conformance checks
```

An `EnvironmentPlan` names its provider and advertises the artifact kinds it
can produce. An `EnvironmentTarget` advertises the kinds a backend can consume.
Creation proceeds only when the two sets intersect:

| Provider | Initial availability | Artifact kinds |
|---|---|---|
| `nix-devshell` | v1 | closure for `local-container`; self-contained image later |
| `dockerfile` | first additional provider | OCI image |
| `devcontainer` | later | normalized OCI image plus supported development metadata |
| substrate-only | v1 built-in | P substrate, no project build |

A provider owns the meaning of its source definition. P does not translate a
Dockerfile into a fake devShell or a devShell into Dockerfile instructions.
They converge only at the normalized artifact manifest and runtime contract.

Provider-specific build code is behind `EnvironmentBuilder`, while provider
selection and artifact manifests are stable core concepts. Adding a provider
must not add conditionals to the TUI, Git server, session registry,
observability reducer, or runtime lifecycle.

## Environment selection

**Decided.** Selection for the target execution system is deterministic:

1. in v1, the repository's ordinary
   `devShells.<system>.default`, when present;
2. otherwise no project environment—the P substrate alone; and
3. after v1, a provider such as Dockerfile may be selected explicitly by
   trusted host configuration at project scope.

There is no custom `p` flake output or repository-controlled provider selector.
Nix remains ordinary Nix: P discovers and realizes the default devShell from
the selected committed source.

P does not infer the Dockerfile provider merely because a file named
`Dockerfile` exists. Repositories commonly use Dockerfiles for production,
release, CI, or one service rather than the interactive environment. Choosing
one changes the session filesystem and must be explicit.

There is no language detection and no inferred package installation. The
substrate fallback applies only when no default devShell or later trusted
provider selection exists. Once a default devShell is present, resolution or
build failure is a build error and P does not silently fall back.

The execution system comes from the backend host, such as `x86_64-linux` or
`aarch64-linux`; it is not inferred from the client machine. P does not
cross-build a foreign session environment in v1.

The selected value and reason are visible before creation:

```text
environment  nix-devshell · devShells.x86_64-linux.default · repository default
packaging    closure · local-container
substrate    p/0.1 · sha256:…
```

## Immutable source and lock policy

**Decided.** Evaluation receives a read-only source snapshot materialized from
the exact Git commit chosen for the session. It never reads the host checkout,
uncommitted files, a mutable branch path, or the daemon's current directory.

For flakes, P invokes Nix in pure mode and refuses to modify source:

- lock-file writes and updates are disabled;
- registry-dependent resolution is disabled unless the instance pins that
  registry as trusted configuration;
- repository-provided Nix settings are not accepted automatically;
- impure evaluation is disabled;
- the execution system is explicit.

If evaluation would need to create or update `flake.lock`, it fails with an
actionable diagnostic. A project that wants a devShell therefore commits the
effective lock file. The optional new-project scaffold may generate both
`flake.nix` and `flake.lock`, but leaves them uncommitted; the user must commit
them before a session can use that environment.

This is stricter than interactive `nix develop` and intentional: the same Git
revision must not silently resolve different inputs on two P instances.

Other providers define equivalent immutable-input rules. A Dockerfile receives
the commit tree as its build context, honors the selected `.dockerignore`, and
cannot read files above that context or substitute the host checkout as a
bind-mounted build directory.

## The P substrate

**Decided.** The substrate is an immutable P build input, produced and tested
per P release and Linux architecture. It contains only what makes any session
usable:

- a POSIX shell and Bash for environment activation;
- minimal userland and process utilities;
- Git and its SSH client;
- the default interactive host (`tmux`) and direct-entry support;
- CA certificates;
- `nc` or an equivalent one-line Unix-socket client;
- P's session API helper and runtime launcher;
- passwd/group entries and fixed filesystem locations required by the runtime.

It contains no coding agent, language toolchain, project dependency, origin
credential, host credential, or mutable user state.

The same tools and dependencies also form a read-only **runtime kit**. When a
provider supplies the base filesystem—as Dockerfile does—P mounts this kit at
fixed paths and uses its launcher. P therefore does not require an arbitrary
project image to contain Git, SSH, the P helper, the selected P-provided
interactive host, or even a compatible system package manager.

The runtime contract reserves:

| Path | Ownership |
|---|---|
| `/opt/p` | Read-only P runtime kit |
| `/run/p` | Runtime-scoped sockets and generated metadata |
| `/workspace` | Writable Git working copy and initial working directory |
| `/home/p` | Writable session home |
| `/tmp` | Writable session temporary storage |

The logical session user is `p`; the backend maps its numeric UID/GID safely
for the local engine. Provider images must tolerate that non-host user and may
not claim the reserved paths.

The substrate is identified by content digest plus P compatibility version.
Upgrading P does not mutate an existing running session. A newly created
runtime uses the current compatible substrate; restarting an existing runtime
uses the substrate recorded at creation. V1 has no voluntary recreate
operation; a later one must follow the lifecycle and isolation contracts.

## Build pipeline

**Decided phases; exact commands are Direction.** A build is one persisted
operation with these phases:

```text
source → resolve → realize → capture → package → verify → register
```

### 1. Source

P materializes the selected commit into a read-only snapshot and records the
project, commit ID, execution system, provider version, and relevant trusted
builder-policy digest. Source arrives from P's local Git repository; the RPC
request does not carry source files.

### 2. Resolve

The selected provider resolves its immutable build description in the build
worker. For Nix, the result is a devShell derivation/store identity, not a shell
command invented by P. For Dockerfile, it is the selected file, target stage,
context digest, platform, and validated build arguments. Substrate-only
selection skips project evaluation and realization entirely.

### 3. Realize

The provider-specific worker realizes the plan. The Nix worker uses its P-owned
build store and configured public binary caches. A Dockerfile worker uses a
rootless isolated BuildKit/Buildah-equivalent without the host engine socket.
Network policy allows public fetches but denies host, LAN, metadata, and
sibling-container access. No build secret is injected in v1.

### 4. Capture

The worker captures provider-specific activation. The Nix worker uses the
pinned [`print-dev-env`](https://nix.dev/manual/nix/2.26/command-ref/new-cli/nix3-print-dev-env)
interface and records both activation material and
structured metadata for validation and diagnostics. P treats that Nix command
as a version-pinned adapter because the upstream interface is experimental. A
Dockerfile artifact preserves image `ENV` values but has no implicit shell-hook
language.

### 5. Package

The builder emits closure or self-contained packaging according to the
backend's declared target. Packaging consumes the already resolved plan; it
does not reevaluate a different environment definition.

### 6. Verify

P verifies manifest schema, expected system, substrate/runtime-kit
compatibility, store-path or OCI-layer integrity, activation-file bounds, and
required executables. It performs a smoke start with no project credentials
before making a newly built artifact available.

### 7. Register

The successful immutable artifact is registered by its build key. SQLite
records operational metadata and leases; Nix-store or engine-native storage
owns the actual bytes. A failed or partial artifact is never returned to a
runtime backend.

## Closure packaging

**Decided for the v1 Nix provider.** The `local-container` target uses:

```text
P substrate OCI image
  + exact environment store closure mounted read-only at /nix/store/…
  + generated activation material mounted read-only
```

The manifest enumerates the exact store paths required by the resolved
environment. P does not expose the whole host store or its database. Store
paths retain their canonical `/nix/store` names so embedded references and
runtime linking remain valid.

The build worker may export closure objects through a P-owned artifact store or
local binary-cache representation. Importing verified store objects is a data
operation; the daemon never sources activation code or runs a project command.
Only manifest-listed closure paths are mounted into the session.

Consequences:

- no project OCI archive is assembled or loaded for every new session;
- several sessions can share immutable closure bytes safely;
- a cache hit reduces environment preparation to manifest validation and
  runtime assembly;
- closure packaging is instance-local and is rebuilt or substituted on another
  P instance rather than copied through Git.

## Self-contained image packaging

**Decided semantics; later Nix implementation.** A target without access to the
instance-local artifact store requests self-contained packaging. For Nix on a
container or Kubernetes backend this is an OCI-compatible image containing the
P substrate, resolved environment closure, activation material, and manifest.
A VM backend may package the same inputs into its native immutable disk/base
artifact instead; “self-contained” does not require pretending a VM disk is
OCI. Dockerfile already produces an OCI project filesystem and receives the
compatible P runtime kit during normalization/runtime assembly.

For Nix, the implementation should use the pinned
[Nixpkgs shell-image helpers](https://nixos.org/manual/nixpkgs/stable/)
or an equivalent expression built on `dockerTools`, rather than P manually
copying libraries or reconstructing `PATH`. The image is loaded or published
by digest, never by a mutable `latest` tag.

Portable packaging is not promised to be byte-identical to closure packaging.
It must be behaviorally equivalent for:

- executable and library availability;
- exported development-shell variables;
- activation and shell-hook ordering;
- user, home, workspace, and P endpoint locations;
- substrate helper versions.

A packaging conformance suite runs the same fixture commands against both
forms before a second strategy is accepted.

## Dockerfile provider

**Decided contract; implemented after the Nix path.** Dockerfile is an explicit
environment provider for users whose development environment is already an
image recipe. It builds from the immutable commit context and emits an OCI
archive plus normalized manifest.

Example later trusted project selection:

```text
# Trusted host configuration; concrete file syntax is implementation-owned.
projects["lgvo/p"].environment.provider = "dockerfile"
projects["lgvo/p"].environment.file = "Dockerfile.dev"
projects["lgvo/p"].environment.target = "development"
projects["lgvo/p"].environment.buildArgs.EDITION = "community"
```

Build arguments are non-secret, validated strings and enter the artifact key.
Neither trusted provider selection nor repository Dockerfile syntax can request
host networking, privileged build, host bind mounts, SSH forwarding, or secret
mounts. Authenticated build inputs
require the same later isolated credential-fetch design as private Nix inputs;
the model gateway is not a general build-input proxy.

The resulting image supplies the project filesystem/toolchain, but P—not image
metadata—owns the interactive session contract. During normalization P:

- retains filesystem layers and declared `ENV` values;
- records but does not execute image `ENTRYPOINT`, `CMD`, or `HEALTHCHECK`;
- replaces runtime `USER`, `WORKDIR`, and entrypoint with P's fixed session
  user, workspace, and launcher;
- does not turn `EXPOSE` into host/LAN port publication;
- rejects image-declared volumes that target `/workspace`, `/home/p`, `/run/p`,
  or `/opt/p`, and strips other volume declarations so the engine does
  not create anonymous writable mounts outside P's declared mount policy;
- mounts the P runtime kit read-only and composes its paths with the image
  environment without erasing unrelated image variables.

The session workspace is always the standalone Git clone at `/workspace`.
Files copied into an image by `COPY . ...` are immutable image contents, not
the live workspace or a second source of Git truth. Development Dockerfiles
should normally install dependencies and leave source mounting to P, but P does
not prohibit copied source when a build requires it.

An image that cannot start under the fixed P runtime contract fails provider
verification with the incompatible field named. Users may choose a different
Dockerfile/target; P does not silently run a production entrypoint or weaken
isolation to make the image start.

Dockerfile cache identity includes the context digest after `.dockerignore`,
Dockerfile path and contents, target, platform, build arguments, frontend and
builder versions, P runtime-kit compatibility, and parent-image digests. A
mutable base tag is resolved to a digest for one build record; rebuilding may
resolve it differently, so reproducible projects should pin base digests.

## Environment activation

**Decided.** Project activation code is untrusted session code. The Nix build
worker captures it, but neither the daemon nor the image importer sources it.
Dockerfile `ENV` is declarative image metadata and is normalized without
executing the image's configured entrypoint.

On runtime start, P's runtime-kit launcher:

1. establishes the fixed session user, `HOME`, workspace, and P endpoint paths;
2. applies the provider environment—sources generated Nix activation or applies
   normalized image environment;
3. runs the selected devShell's shell hook inside the session, when the provider
   defines one;
4. prepares the configured interactive host; and
5. starts or re-enters the configured interactive command when its attachment
   semantics require it.

The command may be a shell, Claude Code, Codex, or custom argv. `tmux` is the
default persistent interactive host; `direct` starts the command for one
attachment. P v1 does not declare, launch, health-check, or supervise project
services.

The activation file is generated by the pinned Nix adapter and treated as an
opaque executable artifact after validation. P does not attempt to translate
arbitrary Bash functions into environment variables. Structured
`print-dev-env --json` output supports inspection and validation; it is not a
replacement implementation of shell activation.

A shell hook may change files in the session workspace or home and use the
session's allowed public network. That is within the session boundary. Its
failure stops startup with the failing phase and log; P does not continue into
a subtly incomplete environment.

## Runtime assembly

**Boundary.** After an environment artifact exists, the backend combines it
with P's compatible substrate/runtime kit and a normalized `SessionSpec`. The
spec, writable storage, project-scoped grants, endpoints, labels, and cleanup
rules are defined by [runtime and isolation](runtime-isolation.md).

The workspace is never an image layer. A new commit that does not change the
resolved environment can therefore reuse the same environment artifact while
cloning different source.

Runtime assembly is transactional with the SQLite lifecycle operation. If
assembly fails, the branch reservation and partial backend machinery reconcile
through the normal session lifecycle; the immutable environment artifact
remains reusable.

## Artifact identity and caching

**Decided.** Cache identity follows actual build inputs, not a guessed project
name or wall-clock TTL.

The common logical build key includes:

- environment provider and adapter version;
- provider-specific immutable plan identity;
- execution system;
- packaging kind and format version;
- P substrate digest and compatibility version;
- relevant trusted builder policy and Nix version.

For Nix, the resolved derivation identity determines reuse: two commits or
projects that resolve to the same environment may share the immutable artifact,
and a workspace-only source change does not invalidate it. For Dockerfile, the
filtered build-context digest is an input because Dockerfile instructions may
copy arbitrary context files; only files excluded by `.dockerignore` are
irrelevant to its build key.

There are three independent caches:

1. environment-resolution metadata;
2. realized Nix store objects and substitution results;
3. packaged closure manifests or OCI/portable images.

P validates that referenced bytes still exist before reporting a hit. SQLite
is an index and lease ledger, never the artifact store or source of truth for
Nix paths and engine images.

Failures are not durable cache hits. P may retain their bounded logs and a
short retry backoff, but a changed input or explicit retry starts a new build.

## Concurrency, progress, and cancellation

**Decided.** Builds are single-flight by build key. Ten sessions requesting the
same missing artifact observe one build operation and acquire separate leases
on success.

One caller cancelling does not cancel work still needed by another caller. If
the last waiter cancels, P requests cancellation from the builder; cleanup is
idempotent, and a late successful immutable artifact may still be registered.

The host RPC reports phases and bounded progress:

```text
resolve · evaluating devShell
realize · 37/52 store paths · downloading 184 MiB
capture · development environment
package · closure manifest
verify  · smoke start
```

Progress is factual when Nix exposes it and phase-only otherwise. P does not
invent percentages. Full build logs stay local and are available on demand;
the TUI preview shows a bounded tail.

Daemon restart reloads the persisted operation, asks the build worker and
artifact authorities what exists, and either resumes observation, registers a
completed artifact, or records an interrupted failure. It never assumes that
connection loss means the build failed.

## Garbage collection

**Decided.** Every environment artifact used by a registered runtime holds a
lease. In-progress lifecycle operations also hold temporary leases. P never
collects a leased artifact.

Removing a runtime releases its lease but does not immediately delete shared
bytes. The overview may show reclaimable build-cache size. v1 collection is an
explicit action that itemizes unleased manifests, store objects, and engine
images before removal; there is no automatic space-pressure deletion.

Nix and the container engine remain the byte authorities. P removes its roots
or image references and lets those tools determine which content is actually
unreferenced. A missing externally collected artifact is a cache miss, not
registry corruption.

## Security boundary

**Decided.** Repository-controlled environment evaluation, Nix derivations,
Dockerfile instructions, fetchers, and activation are untrusted.

The build worker:

- runs through `IsolationProvider`, separate from the daemon and sessions;
- receives only the immutable source snapshot, target description, bounded
  scratch/output space, and provider-specific P cache/store capability;
- has public-internet egress for declared fetches but no host/LAN/private,
  metadata, sibling-container, or arbitrary local access;
- receives no daemon environment, host filesystem, container-engine socket,
  SSH agent, origin/P Git private key, model credential, or notification secret;
- never receives a general host Nix daemon or container-engine socket;
- writes only its disposable/P-owned build state and declared artifact output.

P supplies no build secrets in v1. Private flake inputs, authenticated binary
caches, and private Dockerfile build inputs therefore require a later isolated
credential-fetch capability; mounting ambient user credentials into the
builder is not an accepted workaround.

Nix's own build sandbox and a Dockerfile builder's rootless execution are
defense in depth, not the outer boundary. The `IsolationProvider` boundary
remains required.

Activation runs later inside the final session, with that session's authority.
It cannot influence another runtime merely because its bytes came from a
shared immutable artifact.

## Failures and recovery

| Failure | Required behavior |
|---|---|
| No default devShell or trusted later-provider selection exists | Use substrate only; this is not a warning. |
| Default devShell is invalid | Fail selection; never fall back. |
| Explicit Dockerfile/target is missing or invalid | Fail selection; never try Nix or substrate. |
| Lock file would change or be created | Fail with the required lock action; never write the repository. |
| Evaluation fails | Show resolve phase and bounded diagnostic. |
| Fetch/substitute fails | Preserve cache already obtained; report upstream and retry explicitly/background according to operation policy. |
| Realization fails | Do not package or register partial output. |
| Worker disappears | Reconcile operation and store; report interrupted only after authorities are queried. |
| Packaging/import fails | Keep realized closure reusable; retry packaging without rebuilding it. |
| Verification fails | Quarantine the package and retain diagnostics; never create a runtime from it. |
| Activation/shell hook fails | Runtime startup fails visibly with log; do not claim the session is ready. |
| Cached bytes were externally removed | Treat as cache miss and rebuild/repackage. |
| Wrong architecture | Fail before realization with requested and available systems. |
| Disk exhaustion | Stop cleanly, retain valid shared objects, and show reclaimable unleased cache. |

Build failure is an operation failure before a session becomes runnable. It is
not a runtime or agent failure and never marks the session established. After
the lifecycle creation commit point, the reserved session row and branch remain
visible in `creating` state; its preview names the failed build phase and offers
the retry/discard/delete paths defined by
[session lifecycle](session-lifecycle.md#failure-cancellation-and-retry).

## Performance posture

**Decided.** P does not specify one universal cold-build threshold. Build time
depends on the selected project environment, target architecture, derivations,
binary-cache availability, network, and which store paths are already present.
The same is true of Dockerfile/OCI builds.

The product contract is instead:

- substrate-only creation performs no project build;
- an exact resolution-metadata hit performs no evaluation, and an artifact hit
  performs no realization;
- a realized-closure hit does not rebuild merely because a new session or
  source commit requests it;
- concurrent identical requests share work;
- every non-trivial wait exposes its real phase, log tail, and cancellation;
- P records phase durations, cache-hit level, downloaded/built path counts, and
  bytes so optimization is based on actual projects.

Dockerfile builds obey the same posture but have different invalidation: a
context change included by `.dockerignore` may legitimately rebuild layers. P
reports provider and cache-hit detail instead of comparing that build to a
devShell threshold.

The warm interaction target remains “a few seconds to attach” when the exact
artifact and source objects are local, but it is a measured UX objective, not a
correctness gate or a promise that arbitrary cold devShells finish within one
minute.

The implementation validation benchmarks representative small, medium, and
large project fixtures on `x86_64-linux` and `aarch64-linux` where available,
with cold, substituted, and fully warm stores. Those numbers tune progress UX,
cache roots, and defaults; they do not reopen the architecture unless evidence
disproves closure mounting or isolated realization itself.

## API and channel boundary

The build preserves the project-wide communication rule:

- **Git** supplies the immutable source commit.
- **Host JSON-RPC** requests creation/build, observes progress, cancels, retries,
  and returns artifact metadata.
- **Session RPC** is absent until a runtime exists and never builds images.
- **Builder isolation IPC/mounts** carry a validated job description, immutable
  source, logs, and artifact output inside one P instance.
- **Provider fetch traffic** reaches public Nix substituters, OCI registries,
  and Dockerfile fetch URLs under the build network policy.
- **Artifact bytes** remain in instance-local Nix/engine storage; they do not
  travel inside JSON-RPC or Git.

Independent P instances rebuild or substitute the environment from the same
committed definition. They do not exchange images or cache metadata through P
in v1.

## v1 boundary

**In:**

- Linux-native `x86_64-linux`/`aarch64-linux` as available on the instance
- Nix devShell environment provider as the first implementation
- Strict immutable-source and lock policy
- P-owned versioned substrate
- Isolated build worker with no ambient host authority or build secrets
- Closure packaging for `local-container`
- Exact environment activation inside the session
- Pluggable interactive command/host preparation, with `tmux` as the default
  persistent host and `direct` as the minimal alternative
- Substrate-only fallback
- Build-key caching, single-flight operations, progress, logs, cancellation,
  leases, explicit collection, restart reconciliation

**Later:**

- Explicit Dockerfile environment provider producing normalized OCI artifacts
- Self-contained OCI and VM image packaging
- Cluster-native builders and caches
- Additional environment providers such as `devcontainer.json`
- Cross-compilation or emulated foreign-architecture builds
- Isolated credential fetch for private provider inputs, registries, and caches
- Remote/shared artifact cache managed by P
- Automatic cache collection policy

## Acceptance criteria

The v1 Nix path is implemented when tests prove:

1. two sessions using the same build key cause one realization;
2. a source-only commit change reuses an unchanged resolved environment;
3. default devShell failure never falls back to substrate;
4. missing devShell starts a useful substrate-only session;
5. no repository evaluation, Dockerfile build, Nix realization, or activation
   executes in the daemon;
6. the build worker cannot reach host/LAN endpoints or receive ambient
   credentials;
7. a normal session sees only its manifest-listed closure paths and has no host
   Nix daemon socket;
8. activation and shell-hook failure prevent a false-ready session;
9. cancellation, daemon restart, missing cached bytes, and partial packaging
    reconcile without returning a corrupt artifact;
10. Git/RPC traces contain source identity and build metadata, respectively,
    but no image bytes cross either control channel;
11. representative cold/substituted/warm measurements are recorded without
    turning project-dependent timings into a universal promise.

The provider seam is accepted before the first additional provider ships when
tests also prove:

1. a Dockerfile artifact cannot replace P's entrypoint, publish ports, mount
   host paths, or hide P's workspace/runtime endpoints;
2. Dockerfile context, target, argument, parent digest, and builder changes
   invalidate the correct cache layer;
3. adding a fixture environment provider changes no session/TUI/Git logic;
4. Nix closure and Dockerfile OCI sessions satisfy the same runtime-kit,
   workspace, identity, network, and attachment conformance suite.

The design issue is closed now. These tests are implementation gates, followed
by measurement and optimization against the contract—not unresolved choices
about what P builds or where trust is placed.
