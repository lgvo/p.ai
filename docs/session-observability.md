# P — session observability

How P reports session condition, policy drift, confirmed attachment presence,
and the latest agent signal received while a session is unattended.

> **Status: design.** This document is authoritative for public session and
> policy condition, attachment-presence meaning, agent-event reduction, and
> typed P event emission. [Session lifecycle](session-lifecycle.md) owns the
> operations that change these facts; [runtime isolation](runtime-isolation.md)
> owns their Incus/systemd observations; and
> [communication boundaries](communication-boundaries.md) owns their RPC
> channels and audiences.

## Purpose

P needs to answer four small questions:

1. What can the user currently do with this session?
2. Is anyone currently attached?
3. What is the latest supported agent signal received while unattended?
4. Does the session still use current trusted project policy?

V1 does not implement a lossless activity history, read/unread state, multiple
outstanding attention records, service supervision, or semantic inference from
terminal contents.

## Status model

The public model has four independent facts:

```text
session_condition             reconciled lifecycle/Incus/systemd condition
attached_count                confirmed live attachment count
latest_unattended_condition   nullable last-write-wins agent signal
policy_condition              current/outdated/invalid policy comparison
```

### Session condition

Session condition combines the facts needed to answer whether the session can
be entered. It does not expose a separate runtime condition and startup-
readiness matrix:

| Condition | Meaning |
|---|---|
| `creating` | One durable creation request owns the reserved session but has not established it. |
| `starting` | Incus boot, endpoint/environment activation, or `p-interactive.service` startup is in progress. |
| `ready` | Incus is running and the systemd-owned persistent host is active and attachable. |
| `stopped` | The retained container is stopped and may receive an ordinary Start. |
| `missing` | SQLite expects a runtime that Incus cannot find. |
| `unreachable` | P cannot inspect enough current Incus/runtime state to decide. |
| `discarding` | Confirmed Discard is ending the session while retaining its assigned branch. |
| `deleting` | Confirmed Delete is ending the session and removing its assigned branch. |

Registry intent supplies `creating`, `discarding`, and `deleting`. For an
established session P freshly inspects Incus and `p-interactive.service`.
`ready` requires the systemd unit to have completed endpoint validation,
environment activation, persistent-host startup, and its attachability
contract. Incus merely reporting a running container is insufficient.

Activation or host startup failure records a bounded diagnostic and systemd
shuts down the container. The stable result is `stopped`, not a durable
`running/not_ready` combination. While shutdown or startup is still converging,
the active operation and systemd diagnostic accompany `starting`; a stuck
contract inconsistency requires repair and is never relabeled ready.

Clean or failed persistent-host exit also shuts down the container. Detach,
client loss, and switching sessions end only temporary attach clients and do
not change a ready session's condition.

### Attachment presence

`attached_count > 0` means at least one trusted host helper currently owns a
confirmed live attachment lease. Zero means the session is unattended. A
request, pending token, terminal process, or tmux client observed without its
lease is not presence.

An attach request creates a short-lived, one-use pending token bound to the
session and authenticated host request and returns it with the structured
`AttachSpec`. The client transfers the token over private control input to the
trusted helper. After establishing the Incus PTY and fixed attach command, the
helper confirms the token on its dedicated attachment RPC connection. Only
then does the daemon promote it to an active connection-owned lease and
increment `attached_count`.

Failure to launch, token expiry, or RPC closure before confirmation removes
pending state without changing presence. While the daemon remains reachable,
the helper keeps its confirmed lease until temporary-client teardown finishes.
Client crash, SIGKILL, carrier/SSH loss, or client-machine loss starts teardown
without client cooperation. Daemon-restart lease loss starts teardown
immediately; pending tokens and leases are not restored or re-registered.
Teardown preserves the systemd-owned persistent interactive host.

The confirmed transition from zero to one attachment clears
`latest_unattended_condition`. Additional simultaneous attachments do not
clear anything again.

### Latest unattended condition

This nullable SQLite field is deliberately lossy. It contains:

- `condition`: `running`, `attention`, `idle`, `failed`, or `unknown`;
- short bounded `source` and `reason` fields when supplied;
- daemon receive time and sequence; and
- the adapter/version that produced it.

Rules:

1. While `attached_count == 0`, every valid semantic report replaces the field
   in daemon receive order.
2. Confirming the first live attachment clears the field.
3. While confirmed attached, semantic reports are validated but neither
   retained as overview state nor emitted as unattended-change events.
4. Returning to zero attachments leaves the field empty until a new report
   arrives.
5. P never describes the field as current agent truth or an unresolved request
   set. It is only the latest unattended signal.

An agent permission event may therefore be replaced by later `running`,
`idle`, or `failed` activity. V1 does not correlate permission resolution.

### Policy condition

Every session retains its normalized effective-policy snapshot and digest.
P compares that digest and typed fields with current trusted project policy:

| Condition | Meaning |
|---|---|
| `current` | The snapshot matches current normalized project policy. |
| `outdated` | Current policy differs, but the established session continues using its immutable snapshot. |
| `invalid` | A safety-critical external fact required by the snapshot can no longer be applied safely. |

`outdated` is a warning, not a lifecycle failure. Presentation identifies the
effective differences, such as a removed filesystem grant the old session
still has, a new model grant it lacks, or a changed persistent host/network
policy. Narrowing/removal receives stronger wording because the existing
session retains authority that a new session would not.

P never claims an outdated session has updated merely because configuration
was reloaded. **Recreate with current policy** is the guided Discard-and-Create
path: the old branch becomes a retained source and the new UUID/branch receives
current policy. `invalid` blocks a later Start; it does not silently mutate or
forcibly stop an already-running session.

## Session status protocol

The private per-session Unix socket accepts newline-delimited JSON-RPC 2.0
notifications. Runtime binding authenticates the session UUID; a reporter
cannot select another session.

One generic report is sufficient:

```json
{
  "jsonrpc": "2.0",
  "method": "status.report",
  "params": {
    "v": 1,
    "source": "claude/main",
    "condition": "attention",
    "reason": "permission requested",
    "adapter": "claude-code",
    "adapter_version": "tested-range"
  }
}
```

The daemon assigns receive sequence and time. Reporter timestamps may be kept
as diagnostic metadata but never control ordering. Validation is strict and
bounded:

- unknown protocol versions or conditions are rejected;
- strings and complete lines have configured maximum sizes;
- per-session rate limits protect the daemon;
- malformed reports do not replace the last valid value; and
- session RPC cannot inspect or change another session.

## Agent adapters

Adapters are inspectable cookbook configuration rather than wrappers around
agent execution. Their only responsibility is to map native events to the
generic condition vocabulary.

| Native meaning | P condition |
|---|---|
| prompt submitted, tool starting or continuing | `running` |
| permission prompt, elicitation, explicit input request | `attention` |
| normal main turn or identified subagent stop | `idle` |
| agent/API failure | `failed` |
| lifecycle event with no safe semantic mapping | omitted or `unknown` |

Claude Code and Codex mappings must be validated against real versioned traces
before support is claimed. Unsupported versions emit no semantic condition
rather than guessed status. Ordinary natural-language questions are not
reliably distinguishable from turn completion unless the agent emits an
explicit input event. P does not parse terminal output to compensate.

## Typed P events

After committing a reduced domain transition, P may emit one versioned event
through its common event-handler seam. The envelope contains only bounded,
redacted domain metadata:

```text
schema version and event ID
event kind and daemon occurrence time
P instance, project, session UUID, and branch when applicable
typed reduced fields for that event kind
```

Initial kinds cover changes to session condition, attachment presence, latest
unattended condition, policy condition, and operation progress. A
`session.unattended_changed` event contains the reduced value, not the original
arbitrary RPC line. No unattended-change event is emitted for a semantic report
received while attached.

Handler execution occurs after authoritative state changes. Handler failure is
diagnostic only and cannot roll back domain state. P has no durable event
outbox, acknowledgement/retry protocol, replay cursor, or authoritative event
history. A handler must tolerate process restart and duplicate external effects
according to its own needs.

V1's built-in handler appends structured events to a configured file. The
interface, handler choice, and file implementation are owned by
[technology stack](technology-stack.md#event-handlers). Other logging,
notification, webhook, or metrics handlers may implement the same seam later;
notification protocol is not session semantics.

## Presentation contract

The exact TUI layout and navigation remain deferred pending a prototype. Any
presentation must nevertheless preserve these meanings:

1. `missing`, `unreachable`, `discarding`, and `deleting` remain explicit;
2. `starting` shows current operation/systemd progress and a bounded failure
   when startup returns to stopped;
3. a confirmed live attachment renders as attached and suppresses unattended
   semantic emphasis;
4. pending attachment is connection progress, not presence;
5. while unattended, `attention` and `failed` are urgent and other semantic
   values are informational;
6. `outdated` policy shows typed drift without claiming failure; and
7. lifecycle/operation diagnostics are never encoded as agent condition.

## Restart and failure behavior

- SQLite preserves session identity, latest bounded Start diagnostic, latest
  unattended condition, and immutable policy snapshot/digest.
- Incus plus current systemd unit state restore session condition; there is no
  custom startup-readiness marker to replay.
- Pending attachment tokens and active leases disappear on daemon restart.
  Helpers tear down temporary clients, while the persistent host survives.
- A missing hook or adapter leaves the semantic field empty.
- Duplicate reports are harmless last-write-wins inputs; daemon receive order
  resolves arrival.
- A stopped session retains its latest unattended condition until confirmed
  entry clears it, a later valid unattended report replaces it, or session
  removal deletes it.
- Event-handler failure or restart does not alter or replay authoritative
  status.

## Security boundary

The session socket accepts status reports and the narrow act-on-self methods
defined in [communication boundaries](communication-boundaries.md#session-rpc-audience).
It is not a general command or event-handler channel.

The daemon treats agent reports as authenticated session input, not trusted
facts about the host. Reports affect only the reducer and derived events. They
cannot grant capabilities, mutate policy/Git authorization, choose handlers or
log paths, publish, create another session, or execute host commands. Event
handlers and destinations are trusted host configuration.

## V1 boundary

V1 includes one public session condition, confirmed attachment count, one
nullable latest unattended condition, immutable-policy comparison, strict
status reports, reduced typed events, one structured file-log handler, and
versioned Claude Code/Codex cookbook mappings after validation.

V1 excludes separate public runtime/startup-readiness fields, `not_ready`,
startup markers, seen/unseen history, event replay, observation cursors,
participant inventories, multiple attention records, causal permission
resolution, terminal parsing, pane inspection, built-in notification
protocols, and P-managed services.
