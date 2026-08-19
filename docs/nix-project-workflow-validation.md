# P — Nix project workflow validation

How to validate, before P implements the environment path, that a real Nix
repository can be developed inside a strongly isolated Linux environment.
The target fixture is a flake-based repository that manages a homelab fleet.

> **Status: validation plan.** This document records experiments and evidence;
> it does not change the product contract. Results may require changes to
> [environment building](environment-building.md),
> [runtime isolation](runtime-isolation.md), or a later deployment design.
> Run it on disposable infrastructure and never against the production fleet
> until the deployment phase explicitly says to do so.
> The unresolved V1 choice is tracked in
> [missing pieces](missing-pieces.md#1-settle-the-v1-nix-runtime-and-artifact-model).

## Contents

- [Question to answer](#question-to-answer)
- [Critical distinction](#critical-distinction)
- [Recommended starting hypothesis](#recommended-starting-hypothesis)
- [Profiles to validate](#profiles-to-validate)
- [Test inventory](#test-inventory)
- [Reproducible test fixture](#reproducible-test-fixture)
- [Outer isolation boundary](#outer-isolation-boundary)
- [Phase 1: establish the host baseline](#phase-1-establish-the-host-baseline)
- [Phase 2: prove pure evaluation](#phase-2-prove-pure-evaluation)
- [Phase 3: realize and activate the devShell](#phase-3-realize-and-activate-the-devshell)
- [Phase 4: prove closure-only runtime behavior](#phase-4-prove-closure-only-runtime-behavior)
- [Phase 5: validate project-scoped Nix](#phase-5-validate-project-scoped-nix)
- [Phase 6: build the fleet](#phase-6-build-the-fleet)
- [Phase 7: validate caches and network](#phase-7-validate-caches-and-network)
- [Phase 8: validate secrets](#phase-8-validate-secrets)
- [Phase 9: validate deployment separately](#phase-9-validate-deployment-separately)
- [Phase 10: validate lifecycle and failure behavior](#phase-10-validate-lifecycle-and-failure-behavior)
- [Negative security tests](#negative-security-tests)
- [Performance measurements](#performance-measurements)
- [Evidence record](#evidence-record)
- [Decision gates](#decision-gates)

## Question to answer

Can the homelab repository perform its complete development workflow inside an
isolated runtime that receives only:

- one Git workspace;
- an ordinary Nix devShell;
- session-private home/workspace storage and project-scoped Nix storage;
- deliberately selected network access; and
- no host Nix daemon, host home, SSH agent, engine socket, or ambient secrets?

“Complete development workflow” must be made concrete. At minimum it normally
includes editing, formatting, evaluating every supported machine, running
checks, building every system closure, and reviewing the result. Deployment,
secret editing, remote builds, and VM tests are separate capabilities because
each requires additional authority or devices.

The validation should produce an explicit answer for every operation:

```text
works in isolated build profile
works only with a named additional capability
must remain a host-side/manual operation
not supported initially
```

## Critical distinction

P's environment builder realizing `devShells.<system>.default` proves only that
the development toolchain can be prepared. It does not prove that a running
session can execute Nix after the repository changes.

A Nix-native project usually needs to run commands such as `nix flake check`,
`nix eval`, and `nix build` during normal iteration. Those commands need a Nix
store and a build facility. The currently designed closure-only runtime mounts
the prepared devShell closure read-only and deliberately receives no host Nix
daemon. Unless the devShell is used only for non-Nix tools, that runtime cannot
be assumed to support the full homelab workflow.

This is the principal fact this validation must resolve before P's Nix runtime
path is implemented.

## Recommended starting hypothesis

Test a **project-scoped Nix daemon and store** first:

- rootless local container on a Linux host;
- no mount of the host `/nix`, Nix daemon socket, container-engine socket,
  home directory, SSH agent, or credentials;
- one writable `/nix` store and database owned by the project facility;
- sessions connect as untrusted daemon clients, mount the store read-only, and
  receive separate homes and workspaces;
- sessions from the same project may reuse immutable store paths, while other
  projects receive different stores;
- the devShell closure is realized and registered coherently in that store,
  rather than overlaid as unregistered read-only paths;
- workspace as the only ordinary host bind mount;
- Nix's own build sandbox enabled where the nested namespace permits it;
- public caches/fetches through constrained egress; and
- deployment credentials and fleet routes absent.

The project store should survive individual session and daemon stop/start, and
be removable only through explicit project cleanup. Session homes remain
session-owned. No store may be shared across projects or with the host.

This option has the strongest chance of supporting normal dirty-worktree
iteration without transferring ambient host authority, while avoiding a full
store per branch. Its costs—project-level information sharing, disk use,
cold-start latency, nested-sandbox compatibility, garbage collection, and
daemon lifecycle—must be measured rather than assumed acceptable.

Compare it against these alternatives:

| Model | Isolation | Dirty-tree iteration | Main concern |
|---|---|---|---|
| DevShell closure only | strongest and smallest | cannot assume Nix builds work | likely insufficient for a Nix-native project |
| Project-scoped Nix daemon/store | strong across projects; shared within one project | yes | daemon boundary, nested sandbox, project-level visibility |
| Private per-session Nix/store | strongest branch separation | yes | duplicated storage and cold starts without a clear V1 benefit |
| Isolated build worker reached by a narrow API | strong | only if dirty source transfer is designed, or work is committed first | new protocol and lifecycle design |
| Remote Nix builder | depends on scoped network and credentials | yes | remote authority, SSH keys, availability |
| Host Nix daemon socket | weak coupling to a broad host service | yes | violates the intended host-isolation boundary; do not accept as the baseline |

Do not weaken container isolation merely to make nested Nix pass. Record the
missing kernel feature or permission and decide whether it belongs in the
runtime contract.

## Profiles to validate

Use separate profiles so one convenient command does not silently grant every
session deployment authority.

| Profile | Purpose | Network | Credentials/devices |
|---|---|---|---|
| `evaluate` | flake discovery and evaluation | none after inputs are available | none |
| `build` | checks and system closure builds | public caches/fetches, no host/LAN | none |
| `interactive` | editing and repeated local builds | same as build | none |
| `vm-test` | NixOS VM/integration tests | none or explicit public egress | no KVM initially; test KVM as a separate device grant |
| `secret-edit` | explicitly edit/rekey encrypted material | only endpoints the tool genuinely needs | dedicated test identity only |
| `deploy-test` | deploy to disposable targets | only named target addresses and ports | dedicated restricted SSH identity |

The first four profiles should never receive fleet credentials. `secret-edit`
and `deploy-test` are not evidence that ordinary sessions should receive those
capabilities.

## Test inventory

Before creating the container, inventory the real repository. Record:

- supported Linux systems and fleet architectures;
- `devShells`, `checks`, `packages`, `apps`, NixOS configurations, modules,
  formatters, and repository-specific scripts;
- every command used locally and in CI;
- binary caches, substituters, trusted public keys, and private inputs;
- use of registries, `NIX_PATH`, environment variables, absolute paths,
  `builtins.getEnv`, import-from-derivation, or impure evaluation;
- deployment tools such as deploy-rs, Colmena, morph, or `nixos-rebuild`;
- remote builders, emulation, cross-compilation, and architecture assumptions;
- sops-nix, agenix, hardware tokens, password stores, or other secret tooling;
- commands that contact DNS, Git forges, caches, fleet hosts, hypervisors, or
  cloud APIs;
- VM tests and whether they require `/dev/kvm`, TAP devices, or privileged
  networking; and
- expected writable paths under the workspace, home, XDG directories, `/tmp`,
  and `/nix`.

Classify each command as `evaluate`, `build`, `test`, `secret`, or `deploy`.
Do not trust a command named `check`, `dry-run`, or `build` without observing
its filesystem, process, and network behavior.

## Reproducible test fixture

Use an exact committed revision of the real repository plus a small synthetic
fixture designed to fail visibly when isolation is broken.

For every run, record:

```text
repository commit
flake.lock digest
host distribution and kernel
CPU architecture
rootless engine and version
container image digest
Nix version and configuration
network profile
mounted paths and writable storage
cache state: empty, substituted, or warm
command and exit result
```

Use only fake credentials and disposable hosts in the synthetic fixture. Never
copy production secrets merely to make the test realistic.

Run the representative matrix on native `x86_64-linux` and
`aarch64-linux` hosts when the fleet contains both. A successful evaluation for
another system is not proof that its derivations can be built on the current
machine.

## Outer isolation boundary

The test container should begin with:

- a rootless engine and an unprivileged host user;
- private PID, IPC, mount, user, and network namespaces;
- no added Linux capabilities, privileged mode, host devices, published
  ports, or disabled security profile;
- `no-new-privileges` and the engine's normal seccomp/LSM confinement;
- no host process, cgroup, engine, SSH-agent, or Nix-daemon socket;
- a fresh allowlisted environment and session-specific `HOME`;
- a read-only container base with explicit writable workspace, home, temporary
  space, project Nix storage, and bounded cache/data storage;
- resource limits sufficient to expose runaway builds without making ordinary
  large closures fail accidentally; and
- a pinned container image, Nix version, and Nix configuration.

Use a clean clone or exported commit as the initial workspace. The immutable
builder test receives it read-only. The interactive test receives a private
writable clone so edits cannot alter the host checkout.

Rootless-container networking alone is not proof of isolation: common defaults
can still reach the host and LAN. Observe traffic outside the container and
test the exact filtering mechanism.

## Phase 1: establish the host baseline

Run the complete classified workflow normally on a clean trusted Linux host.
This is the functional control, not the security result.

Record:

- commands and their real dependencies;
- input downloads and cache hits;
- build time, peak memory, disk growth, and output closure size;
- all files written outside the repository;
- all contacted addresses and protocols;
- SSH-agent, secret-store, daemon-socket, KVM, or remote-builder use; and
- which operations mutate a machine or remote ref.

If the baseline itself depends on uncommitted files, an unlocked input, an
ambient registry, or an undocumented credential, fix or explicitly classify
that dependency before evaluating isolation.

## Phase 2: prove pure evaluation

From the exact commit and committed `flake.lock`, exercise at least:

```sh
nix flake metadata --no-update-lock-file --no-write-lock-file --no-use-registries .
nix flake show --no-update-lock-file --no-write-lock-file --no-use-registries .
nix eval --json --no-update-lock-file --no-write-lock-file --no-use-registries \
  .#nixosConfigurations.<host>.config.system.build.toplevel.drvPath
```

Use the pinned Nix version's equivalent flags when they differ. The required
properties are more important than spelling:

- no lock-file creation or update;
- no host registry or `NIX_PATH` dependency;
- no source outside the commit;
- no ambient environment or host filesystem read;
- no credential request;
- deterministic output identities on repeated clean runs; and
- useful errors for missing inputs or unsupported systems.

Repeat with network disabled after inputs are present. Unexpected network use
during pure evaluation must be explained and assigned to the correct profile.

Test hostile fixture expressions that try absolute host paths, environment
variables, registry lookups, and unlocked inputs. They must fail or see only
the intentionally supplied synthetic values.

## Phase 3: realize and activate the devShell

Resolve `devShells.<system>.default`, realize it inside the isolated builder,
and capture its activation without sourcing it in the host or supervising
process.

Validate:

- every expected executable and library is in the realized closure;
- `nix print-dev-env --json` or the pinned adapter's equivalent can describe
  the environment for inspection;
- exact shell activation preserves variables, Bash functions, and the shell
  hook behavior needed by the project;
- the shell hook runs only inside the final interactive container;
- activation succeeds with a fresh home and no host dotfiles;
- activation cannot acquire additional host mounts, external endpoints,
  devices, or network routes; runtime-local files and Unix sockets remain
  ordinary session behavior;
- shell-hook failure prevents a false successful environment; and
- the environment works under the fixed unprivileged user and workspace path.

Run the repository formatter, linters, generators, editor/language tooling,
and non-Nix unit tests from the activated environment. Identify anything that
was available only because of the host PATH or home directory.

## Phase 4: prove closure-only runtime behavior

Create a second clean runtime that has:

- the minimal base/runtime tools;
- only the manifest-listed devShell closure mounted read-only at canonical
  `/nix/store` paths;
- captured activation material;
- a fresh workspace and home; and
- no builder store, Nix daemon socket, or host Nix state.

Prove that activation and non-Nix development commands work. Then deliberately
try the normal Nix evaluation/build commands.

Record the exact boundary:

- Does the devShell include the Nix CLI?
- Can it evaluate without a writable store or daemon?
- At what first operation does it need store mutation?
- Are diagnostics understandable rather than permission-denied noise?

A failure to build here is an expected and useful result. It proves that the
generic closure-only environment is insufficient for this Nix-native project;
it is not justification to mount the host daemon.

## Phase 5: validate project-scoped Nix

Give the project a persistent Nix daemon, store, and database. They must not
share the host store or any writable Nix state with another project. Sessions
in this project connect as untrusted clients and see the store read-only in
their filesystem; store mutations happen only through the project daemon.

The project store owns the complete logical `/nix/store` view and its database.
Build the devShell in it directly or seed it through a verified Nix
copy/export/import mechanism. Do not combine an unrelated writable store
database with individually bind-mounted paths from another store and assume
Nix will regard those paths as valid. Overlay or copy-on-write stores are not a
V1 requirement; test them later only if measured storage pressure justifies
the added ownership and garbage-collection complexity.

Validate:

- installation/bootstrap under the fixed unprivileged runtime model;
- Nix sandbox status and every reason it is disabled or degraded;
- nested user/mount/PID namespace behavior on the supported kernels;
- build-user and trusted-user configuration;
- store ownership, signatures, database consistency, and repair behavior;
- untrusted-client behavior and socket permissions from more than one session;
- verified realization or transfer/registration of the devShell closure into
  the project store;
- public substituter and trusted-key configuration supplied by trusted policy;
- concurrent Nix commands within and across project sessions;
- clean behavior after command cancellation and container termination;
- persistence across stop/start;
- reuse between two sessions in the same project and isolation from a second
  project store;
- explicit handling of the fact that project sessions can observe shared store
  paths and build metadata;
- bounded disk use, project-level garbage collection, and complete removal only
  during project cleanup; and
- no dependency on the host's `/nix`, daemon socket, registry, profiles, or
  garbage-collector roots.

Run the workflow first with an empty store, then a substituted store, then a
fully warm store. A cache optimization is acceptable only if it does not make
store mutation or trust cross project boundaries. Never place secrets in the
project store: immutable paths are still readable by other sessions in the
same project.

Inspect the runtime after bootstrap. The only Nix endpoint should be its own
project endpoint. `NIX_REMOTE`, profiles, and configuration must not fall back
to host locations.

## Phase 6: build the fleet

For every supported host, build the exact closure without activating or
deploying it. The low-level form is conceptually:

```sh
nix build --no-link --no-update-lock-file --no-write-lock-file \
  --no-use-registries \
  .#nixosConfigurations.<host>.config.system.build.toplevel
```

Also run all repository checks, packages, evaluation tests, and project-native
build commands. Keep build and deploy commands separate even if a tool offers
one command that does both.

Validate:

- every configuration evaluates;
- every native-architecture system closure builds;
- cross-architecture targets either build through an explicitly accepted
  strategy or fail before expensive work with a clear explanation;
- NixOS tests run without KVM first and report the performance cost;
- any proposed KVM use is isolated as a separate device capability;
- derivation builders do not receive arbitrary network access;
- repository checks do not contact or mutate the fleet;
- build outputs contain no unintended plaintext secrets;
- repeated builds of the same commit produce the expected output identity; and
- a dirty workspace changes only the intended inputs and never reads the host
  checkout through an alternate path.

Include both modifications to tracked files and newly created files. Git-backed
flakes commonly exclude untracked files until they are added to the index; the
interactive workflow must surface that behavior clearly rather than making a
new module appear to be ignored mysteriously.

If remote builders are required, stop and classify them as a separate
capability. Record the endpoint, authentication, allowed systems/features,
result-signing policy, failure behavior, and whether the remote builder can
return or observe sensitive inputs.

## Phase 7: validate caches and network

Test these states independently:

1. empty store with public egress;
2. substituted store with public egress;
3. warm store with network disabled;
4. unavailable cache;
5. corrupt or incorrectly signed cache response; and
6. source fetch redirected toward a denied private address.

The public-egress profile must deny:

- host gateway aliases and loopback escape paths;
- RFC1918, carrier-grade NAT, link-local, multicast, and metadata ranges;
- IPv6 private, link-local, and mapped-address equivalents;
- sibling containers and the engine API; and
- DNS rebinding and HTTP redirects to denied destinations.

Record every required public endpoint. Determine whether all flake inputs are
public and pinned. Private Git inputs or authenticated caches do not belong in
the ordinary V1 build profile; they require a later scoped credential-fetch
design or a repository change.

Verify cache correctness, not only speed: signatures, substituter priority,
fallback builds, timeout behavior, and absence of secret-bearing headers or
URLs in logs.

## Phase 8: validate secrets

Separate three questions:

1. Can configurations containing encrypted secret declarations evaluate?
2. Can system closures build without decryption keys or plaintext secrets?
3. Which explicit workflow edits, rekeys, or deploys secrets?

The first two should normally pass without a private key. Inspect store paths,
derivations, closures, build logs, environment, process arguments, and cache
uploads for plaintext. Use a synthetic sentinel secret so detection is safe.

Secret editing is a separate profile. Validate it only with a disposable key
and fixture repository. Determine the smallest feasible carrier—such as one
named hardware device, agent endpoint, or secret file—and whether it can be
made unavailable to ordinary builds and child processes that do not need it.

Never solve a failed secret test by mounting the entire SSH, GnuPG, password-
store, or home directory. Production secret authority should remain outside P
until a dedicated scoped design is accepted.

## Phase 9: validate deployment separately

Building a system closure is not deployment. Deployment necessarily adds a
route to a target and some form of authentication, and may grant root-equivalent
authority on that target.

First prove a manual handoff:

1. build and inspect the closure in the isolated environment;
2. commit the intended repository state;
3. leave the isolated build profile; and
4. deploy with the existing trusted host workflow.

This is the safe initial answer if deployment from inside a session is not a
hard requirement.

If in-session deployment is required, use only disposable test machines and a
dedicated restricted SSH identity. Validate:

- exact target allowlist and port, including DNS changes and IPv6;
- pinned host identity and non-interactive known-host behavior;
- no SSH agent forwarding and no access to unrelated agent identities;
- the minimum remote account, sudo rule, command set, and store-copy authority;
- whether the tool opens additional control sockets or reverse forwards;
- closure copy, activation, rollback, partial failure, reconnect, and repeated
  invocation;
- multiple-target ordering and blast radius;
- removal/revocation of the session credential; and
- complete redaction from logs, process listings, environment, and artifacts.

The present P V1 network contract denies host, LAN, private, and Tailscale/CGNAT
destinations and has no deployment grant. Therefore successful in-session
deployment to such targets would identify a **new product capability**, not a
minor environment setting. Keep it host-side or design a narrowly scoped later
deployment boundary; do not silently widen `public-egress`.

## Phase 10: validate lifecycle and failure behavior

Exercise:

- stop/start during evaluation and during a long build;
- abrupt container/host termination and store recovery;
- cancellation during fetch, build, and closure registration;
- full disk and inode exhaustion;
- unavailable DNS, cache, Git source, and remote builder;
- corrupt store path and failed `nix-store --verify`/equivalent checks;
- garbage collection while the workspace still needs outputs;
- two concurrent builds of the same and different outputs;
- project branch rename without moving project-owned Nix storage;
- discard/delete cleanup without touching external cache or host data; and
- Nix/container version upgrades against an existing stopped project store.

For every interruption, establish whether the next action is retry, repair,
explicit removal, or rebuild from committed state. No test should treat a lost
connection as proof that an external mutation failed.

## Negative security tests

The synthetic repository should attempt to:

- read host home, `/etc`, process environment, and sibling workspace data;
- connect to the host, LAN, metadata services, sibling containers, engine API,
  host Nix daemon, and deployment targets;
- obtain SSH-agent, Git, cache, model, or notification credentials;
- create a privileged container, mount a host path, publish a port, or access a
  host device;
- escape through symlinks in source or granted paths;
- modify read-only source, devShell closure, activation material, or cache;
- leak a synthetic secret into a derivation, store path, log, cache request,
  process argument, or build output; and
- persist outside declared workspace, home, temporary, and project Nix storage.

Run the probes during evaluation, derivation execution, shell-hook activation,
ordinary interactive commands, VM tests, and deployment-tool startup. Passing
one phase does not establish the others.

## Performance measurements

Measure rather than set a universal build-time requirement:

- worker/container bootstrap;
- devShell resolution and realization;
- project Nix daemon/store bootstrap;
- full fleet evaluation;
- one small and one large system closure;
- empty, substituted, and warm-store cases;
- repeated edit/evaluate/build loop;
- closure export/import if tested;
- stop/start recovery;
- store disk use and garbage-collection time; and
- x86_64 versus aarch64 behavior.

Compare the project-store result with the trusted host baseline. Record path
counts, downloaded and locally built bytes, peak disk, memory, CPU, and the
reason for every material difference. The important product question is
whether isolation makes normal iteration impractical and which cache boundary
improves it without transferring host authority.

## Evidence record

Use one row per meaningful operation:

| Field | Record |
|---|---|
| Requirement | What the user needs to accomplish |
| Profile | Evaluate, build, interactive, VM, secret, or deploy |
| Exact command | Structured argv and tool version |
| Inputs | Commit, lock digest, target system, cache state |
| Granted authority | Mounts, writable paths, network, credentials, devices |
| Observed access | Files, sockets, destinations, subprocesses |
| Result | Pass, fail, degraded, or not tested |
| Performance | Duration, downloads/builds, peak resources, disk growth |
| Failure behavior | Diagnostic, cleanup, retry/recovery result |
| P consequence | Existing contract, implementation constraint, later feature, or unsupported |

Attach sanitized logs and traffic traces. Record absence tests as evidence, not
as “nothing unexpected was noticed.”

## Decision gates

The experiment is complete only when it answers:

1. Is the ordinary devShell sufficient for all non-Nix project tools?
2. Which normal workflow commands require Nix inside the final session?
3. Can project-scoped Nix run with the required sandbox on supported Linux
   kernels without privileged containers or host sockets?
4. Should P realize the devShell externally and import it into the session
   store, or should the project daemon perform that realization itself?
5. What storage and cache model makes repeated builds usable while preserving
   project isolation and session-private mutable state?
6. Can all native fleet closures build without secrets or fleet reachability?
7. What is the explicit answer for foreign architectures, remote builders, and
   KVM tests?
8. Can encrypted-secret declarations evaluate and build without decryption
   authority?
9. Is host-side deployment acceptable initially? If not, what exact target,
   credential, and remote privilege must a later capability grant?
10. Do the negative tests prove no host/LAN/credential access in every ordinary
   profile?
11. Which findings require changing P's environment or runtime designs before
    implementation?

The preferred outcome is:

```text
ordinary work and complete fleet builds
  → project-scoped Nix, session-private home/workspace,
    no ambient credentials, no fleet route

deployment or secret administration
  → explicit separate workflow with narrower authority and stronger review
```

If that outcome does not work, preserve the evidence and revise the relevant
P boundary explicitly. Do not hide the requirement behind a host daemon mount,
privileged container, broad home-directory grant, or unrestricted LAN access.
