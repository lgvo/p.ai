# P — Nix project workflow validation

A manual experiment for validating P's Incus/Nix environment model against a
real Nix repository before relying on P automation.

> **Status: development validation, not product authority.** The authoritative
> design is [Nix environment images](environment-building.md) and
> [runtime and isolation](runtime-isolation.md). Record evidence here or beside
> the eventual tests; do not weaken those boundaries merely to make the
> experiment pass.

## Goal

Use the Nix repository that manages the homelab fleet as the demanding fixture.
Prove that an unprivileged, confined Incus system container can:

- evaluate and realize the committed default devShell;
- become a reusable private Incus environment image;
- launch independent sessions with private writable Nix state;
- evaluate and build the repository's ordinary outputs after workspace edits;
- use public substitutes/fetches without host or LAN authority; and
- keep secrets, deployment authority, and the host Nix store outside the
  environment unless separately and explicitly granted later.

This validation is deliberately executable without P. It maps each manual step
to the future adapter rather than inventing a separate Docker/Compose workflow.

## What this does not prove

- deployment to fleet machines;
- private flake inputs, authenticated caches, or remote builders;
- KVM/nested virtualization or VM tests;
- secret injection or access to a password/SSH agent;
- correctness of every homelab output;
- identical physical deduplication on every Incus storage driver; or
- safety of Incus's full administrative socket.

Those require separate capabilities and threat models. A successful build does
not authorize deployment.

## Test matrix and evidence

Record before each run:

```text
date
host distribution and kernel
architecture
Incus version
Incus storage driver and pool
confined user project restrictions
base-image fingerprint
Nix version and nix.conf
repository commit and flake.lock digest
substituters/trusted keys
network profile
CPU/memory/disk limits
```

Test at least:

| Dimension | Required cases |
|---|---|
| Architecture | `x86_64-linux`, `aarch64-linux` before both are claimed |
| Build cache | cold, public-substituted, already-published image hit |
| Network | `none`; gated `public-egress` |
| Storage | the actual production driver; compare `dir` with a CoW driver when choosing defaults |
| Repository | a small fixture and the real homelab repository |
| Session count | two sessions from one environment fingerprint |

For each phase capture elapsed time, peak disk/logical size where practical,
Incus operation/result, image fingerprint, private instance growth, command
exit, and bounded failure diagnostic.

## Prerequisite: confined Incus project

Provision Incus outside P and create one user project whose restrictions are
the ceiling intended for P. The experiment account must not have unrestricted
Incus administration. Verify before building:

- it can operate only in the selected project;
- it cannot attach arbitrary host paths/devices or use privileged/raw LXC
  settings;
- instances cannot reach an Incus socket;
- builders/sessions cannot reach other Incus projects; and
- the selected storage/network resources are explicit.

If the experiment requires the host-root-equivalent admin socket, privileged
containers, a host `/nix` mount, or the host Nix daemon, record failure. Do not
treat the workaround as a candidate V1 design.

## Phase 1: build the P base image

Produce an Incus-native system-container image for the tested architecture
containing:

- Nix with the intended sandbox/substituter posture;
- a local Nix daemon configured to own only the instance-private `/nix` and
  authorize the fixed session user;
- Git, OpenSSH, CA certificates, shell, tmux, and basic userland;
- fixed unprivileged user `p` and `/workspace`, `/home/p`, `/run/p`, `/opt/p`,
  `/var/p` paths; and
- no project source, host configuration, credential, Incus socket, SSH agent,
  or origin remote.

Publish it privately and record the immutable fingerprint. Launch a smoke
instance and prove Nix database/store coherence, user/paths, and absence of
ambient authority.

## Phase 2: resolve and realize the committed devShell

Create a disposable unprivileged builder from the exact base fingerprint.
Materialize a read-only snapshot of the selected Git commit at a build-only
path. Do not use a dirty checkout.

Inside the builder:

1. resolve `devShells.<system>.default` in pure mode;
2. prevent flake-lock creation/update and ambient registry/`NIX_PATH` use;
3. realize the environment using only declared public substituters/fetches;
4. capture versioned activation material with the pinned, compatibility-tested
   `nix print-dev-env --json` adapter;
5. create a P-owned GC root for the initial environment; and
6. smoke-activate it as the fixed session user without P Git/model credentials.

Record evaluation and realization separately. Confirm shell-hook execution
occurs only during the smoke activation/session launch, never in the host
daemon context.

Expected failures include an invalid default devShell, required lock mutation,
unsupported system, sandbox limitation, blocked fetch, or resource exhaustion.
They must be actionable and must not silently fall back to the base image when
a default devShell exists but fails.

## Phase 3: scrub and publish the environment image

Before publication, remove the materialized checkout, unreachable temporary
Nix paths, temporary build output, transient logs/network material, and any
credential. Verify the builder contains:

- the coherent base plus realized devShell store/database;
- the selected environment GC root;
- activation material and fixed runtime kit; and
- no writable/materialized repository checkout, session UUID, branch, private
  key, origin credential/remote, host path, other-project material, or
  builder-only state. Record any committed source-derived Nix store paths that
  the retained closure legitimately requires.

Stop the builder, publish it as a private Incus image, record its fingerprint,
and delete the builder. If cleanup is interrupted, verify its labels make it an
obvious builder orphan rather than an adoptable session.

## Phase 4: launch two isolated sessions

Create two unprivileged instances from the same environment fingerprint. For
each, create a standalone clone at `/workspace` only after instance creation;
give it a distinct branch and no origin remote/credential. Provide a private
`/home/p`, `/run/p`, and instance-root `/nix`.

Verify:

- both activate the expected devShell and shell hook;
- the environment image/fingerprint is identical;
- workspace, home, Nix database changes, profiles, roots, and new store paths
  are private to each instance;
- no host `/nix` path or daemon socket is mounted;
- stopping and starting retains files and private Nix paths but terminates
  tmux/processes; and
- deleting one instance does not affect the other or the cached image.

Measure logical root sizes and storage-pool physical use. Attribute observed
sharing to the tested Incus storage driver, not to a P guarantee.

## Phase 5: real interactive Nix workflow

In session A, exercise representative work that the homelab repository actually
needs:

1. evaluate flake metadata and selected configuration outputs;
2. build one small deterministic output;
3. build one representative system or fleet output that needs no deployment
   credential/device;
4. modify `flake.nix`, `flake.lock`, or a module in the workspace and run the
   ordinary Nix evaluation/build command again; and
5. verify any newly realized store paths/database changes exist only in
   session A's private root.

Session B must retain its original environment and must not observe session
A's Nix changes. The cached environment image fingerprint must remain
unchanged. Dirty workspace changes are never promoted to a new shared image.

Classify failures rather than broadening authority:

| Need discovered | V1 interpretation |
|---|---|
| public fetch/substitution | belongs to validated public egress |
| more CPU/memory/disk | trusted project resource policy |
| private input/cache credential | future explicit build-secret capability |
| SSH/deployment credential | outside environment building/session default |
| LAN target access | outside V1 network posture |
| KVM/device/nesting | future explicit capability or VM backend |
| remote Nix builder | future design |

## Phase 6: cache and failure behavior

Repeat creation under these conditions:

- existing registered image fingerprint: verify no builder is needed;
- SQLite/index entry missing but image present: record chosen safe discovery or
  rebuild behavior;
- index present but image externally removed: verify a cache miss/rebuild;
- two concurrent requests for one missing key: verify one shared in-memory job
  or harmless duplicate prevention through deterministic builder metadata;
- daemon interruption during build/publish/cleanup: query Incus and retry
  without inventing a durable phase workflow for image bytes; and
- explicit cache deletion while existing sessions run/stay stopped: verify
  their private roots continue and future creation rebuilds.

There are no session artifact leases and no automatic age/pressure collection.

## Phase 7: network and secret-negative tests

From both builder and session, attempt access to:

- host and gateway addresses;
- RFC1918, ULA, carrier-grade NAT, link-local, metadata, multicast, and sibling
  ranges over IPv4/IPv6;
- Incus APIs/sockets and other projects;
- host SSH agent, host Nix daemon/store, and undeclared paths; and
- Bifrost administration or P host-control endpoints.

Under `none`, public traffic must also fail. Under `public-egress`, only the
intended public destinations plus narrow mounted/session endpoints may work.
Test DNS rebinding, redirects, literals, and IPv4-mapped IPv6 forms.

Inspect Incus metadata/config, image contents, processes, environment, logs,
SQLite diagnostics, and bounded support output for credentials or unintended
other-project material. A committed project source path required by the Nix
closure is not itself a failure, but proves why the image must remain private
to that project. The V1 builder receives no build secret, so finding one is a
failure.

## Acceptance record

The Incus/Nix model is validated for one supported combination only when the
record shows:

1. confined user authority without administrative fallback;
2. reproducible committed default-devShell selection and no lock mutation;
3. coherent scrubbed immutable Incus environment image;
4. independent private session `/nix`, workspace, and home deltas;
5. representative homelab evaluate/build work after workspace changes;
6. correct stop/start/remove/cache-miss behavior;
7. documented cold/substituted/image-hit performance and physical storage for
   the tested driver; and
8. no host/LAN/credential/Incus-authority escape.

Record unsupported cases as unsupported. Do not claim a new architecture,
storage driver, network profile, private input, remote builder, deployment
operation, or device until its own evidence and design boundary exist.
