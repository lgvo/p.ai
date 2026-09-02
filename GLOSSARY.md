# Project glossary

Canonical domain language for this repository.

## Terms

### Assigned branch

The one Git branch assigned to a session while that session exists.

- **Related:** [Session](#session), [retained branch](#retained-branch).
- **Rules:** Its complete address is `(project path, branch name)`. The session
  principal may update it only by fast-forward. Discard releases the assignment;
  Delete removes the branch.

### Attachment

A temporary terminal channel from one client to a session's persistent
interactive host.

- **Boundaries:** It is not the persistent host and does not own container
  lifetime.
- **Rules:** Only a confirmed helper-owned lease contributes to
  `attached_count`. Detach, switching, or transport loss ends the attachment
  without stopping the host.

### Bootstrap session

The first session reserved on unborn `main` for a new blank project or a
successfully contacted empty origin.

- **Rules:** It is the only MVP session without a committed source. Its first
  push creates `main`; no artificial commit is created by P.

### Event handler

A host-side capability that receives versioned, reduced P events after domain
state has been updated.

- **Boundaries:** It does not receive arbitrary raw agent payloads and is not a
  source of session truth.
- **Rules:** Handler failure cannot roll back session state. MVP provides a
  structured file-log handler and no durable delivery queue.

### Exact retry

Re-execution of one failed creation request with the same operation, UUID,
branch, source, policy snapshot, and idempotency key.

- **Rules:** P cleans or safely reuses verified partial derived resources and
  rebuilds the same request. Retry does not form a new operation chain.

### External session service

A service outside a session that is made available through an explicitly
granted network endpoint and service-native, session-scoped authentication.

- **Boundaries:** The service may be local to the P instance or remote;
  external describes its position outside the session boundary.
- **Rules:** The service retains authority over its protocol, data, and native
  authorization. P governs provisioning, scoped binding, validation,
  injection, and revocation of session access.

### Grant

A named capability authorized by trusted developer configuration and made
available across a P isolation or extension boundary.

- **Rules:** A session or plugin cannot grant itself authority. Each grant has
  explicit scope and is enforced by the authority that owns the capability.

### Host-side capability

A plugin-provided capability that participates in P lifecycle work outside a
session without being exposed to that session.

- **Examples:** A runtime provider or event handler.
- **Rules:** It receives only the authority granted for its declared role and
  does not become a session service merely because its result affects one.

### Internal session service

A service that runs inside a session's isolation boundary and is supervised
through the runtime's systemd contract.

- **Rules:** It operates with the session's effective grants and receives no
  ambient host authority from the plugin that provides it.

### Origin

The optional ordinary Git remote configured for a project and accessed only by
trusted host-side P operations.

- **Boundaries:** It is not another P instance and is never installed in a
  session workspace by P.
- **Rules:** Origin publication is explicit and create-or-fast-forward only.

### P core

The trusted part of P that owns identity, durable control state, lifecycle
orchestration, policy and grant enforcement, plugin activation, recovery, and
user confirmation.

- **Boundaries:** P core invokes plugins through typed contracts but does not
  delegate its authoritative lifecycle or security decisions to them.

### P instance

One independent P control plane with its own daemon, registry, Git server,
projects, sessions, and configured local Incus execution project.

- **Rules:** P instances do not discover, address, synchronize, or federate
  with one another.

### Persistent interactive host

The systemd-managed internal session service that anchors a running session
and accepts temporary attachments, such as tmux.

- **Boundaries:** A temporary attachment client is not the persistent
  interactive host.
- **Rules:** Detachment leaves the host alive. Host exit or failure shuts down
  the session container. MVP ships tmux as the default implementation.
- **Avoid:** `direct`, which is not an MVP host.

### Plugin

An independently authored, installable implementation of one or more stable,
documented P capability contracts.

- **Boundaries:** A plugin may provide a first- or third-party runtime,
  internal or external session service, host-side capability, integration, or
  workflow. It is not P core.
- **Rules:** Developers or agents may author, register, and test plugins.
  Registration does not activate a plugin or grant authority. Trusted
  developer configuration controls activation, implementation selection, and
  grants; an activated plugin may operate autonomously only within them.

### Policy condition

The comparison between a session's immutable effective-policy snapshot and the
project's current trusted policy.

- **Rules:** `current` matches, `outdated` warns and remains usable, and
  `invalid` means the snapshot can no longer be applied safely and blocks
  Start.

### Project

The complete path of one bare repository on a P instance's Git server,
together with its trusted project policy and optional origin.

- **Rules:** Project paths are immutable in MVP. Projects are created explicitly
  from an SSH origin or as blank P repositories; host checkouts are not
  registered source locations.
- **Boundaries:** An Incus project is an infrastructure confinement boundary,
  not a P project.

### Replacement creation

A new Create request presented as **Try again with changes** after a failed
creation.

- **Rules:** It has a new UUID, operation, source/policy input, and idempotency
  key; supersedes the old request; and integrates safe cleanup and loss review
  so the user does not manually discard/delete first.

### Retained branch

An ordinary unassigned P branch preserved after its session is discarded.

- **Boundaries:** It has no runtime, attachment, policy, or agent status.
- **Rules:** It may be listed, renamed, published, deleted after loss review,
  or selected as committed source for a new session.
- **Avoid:** `unassigned ref` when the user-facing concept is a branch.

### Runtime

The replaceable execution machinery associated with a session.

- **Boundaries:** A runtime is not session identity, an attachment, or an
  agent. MVP realizes it as one Incus system container, but the concept does not
  depend on that implementation.

### Session

P's durable identity for one piece of branch work, represented by an immutable
UUID and exactly one assigned branch while the session exists.

- **Boundaries:** A session is not an agent, task, terminal, tmux server, or
  container identity.

### Session condition

The single public lifecycle condition describing whether a session is being
created, started, is ready, stopped, missing, unreachable, discarding, or
deleting.

- **Boundaries:** Attachment presence, latest unattended agent condition, and
  policy condition remain independent facts.
- **Avoid:** Separate public `runtime_condition` and `startup_readiness` fields.
