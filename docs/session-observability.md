# P — session observability

How P reports runtime health, attachment presence, and the latest agent signal
received while a session is unattended.

> **Status: design.** This document is authoritative for session status and
> agent-event reduction. [communication-boundaries.md](communication-boundaries.md)
> defines the RPC channels that carry these facts, while
> [session-lifecycle.md](session-lifecycle.md) defines the operations that
> change runtime condition and attachment eligibility.

## Purpose

P needs to answer three small questions:

1. Does the session runtime exist and can P reach it?
2. Is the user currently attached to the session?
3. What is the latest supported agent event received while nobody was attached?

V1 does not implement a lossless activity history, read/unread state, multiple
outstanding attention records, service supervision, or semantic inference from
terminal contents.

## Status model

The persisted and live model has three independent fields:

```text
runtime_condition             authoritative lifecycle state
attached_count                live attachment count
latest_unattended_condition   nullable last-write-wins agent signal
```

### Runtime condition

The daemon derives runtime condition from the lifecycle registry and backend
inspection. Registry states `creating` and `removing` project directly; an
established session uses the observed backend result:

| Condition | Meaning |
|---|---|
| `creating` | A persisted creation operation is incomplete. |
| `running` | The backend reports the runtime running. |
| `stopped` | The runtime is intentionally stopped and restartable. |
| `missing` | SQLite expects a runtime that the backend cannot find. |
| `unreachable` | The backend cannot currently be queried. |
| `removing` | A persisted discard/delete operation is incomplete. |

Attachment never clears or rewrites runtime condition.

### Attachment presence

`attached_count > 0` means at least one host client is currently inside the
session. Zero means the session is unattended.

An attach operation creates a connection-bound lease before starting the
interactive channel. The client keeps that RPC connection alive while it runs
the returned `AttachSpec`, then closes the lease on normal exit. Transport
closure expires the lease. Daemon restart drops all connections, so attachment
leases are live state and are not restored from SQLite.

The transition from zero to one attachment clears
`latest_unattended_condition`. Additional simultaneous attachments do not
clear anything again.

### Latest unattended condition

This nullable SQLite field is deliberately lossy. It contains:

- `condition`: `running`, `attention`, `idle`, `failed`, or `unknown`;
- short `source` and `reason` fields when supplied;
- daemon receive time and receive sequence; and
- the adapter/version that produced it.

Rules:

1. While `attached_count == 0`, every valid semantic report replaces the field
   in daemon receive order.
2. Entering the session clears the field.
3. While attached, semantic reports are validated but neither retained as
   status nor notified.
4. Returning to zero attachments leaves the field empty until a new report
   arrives.
5. P never describes the field as current agent truth or as an unresolved
   request set. It is only the latest unattended signal.

An agent permission event may therefore be replaced by later `running`,
`idle`, or `failed` activity. V1 does not correlate permission resolution.

## Status protocol

The per-session Unix socket accepts newline-delimited JSON-RPC 2.0
notifications. Runtime binding authenticates the session UUID; a reporter
cannot select another session.

One generic event is sufficient:

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
as diagnostic metadata but never control ordering.

Validation is strict and bounded:

- unknown protocol versions or conditions are rejected;
- strings and complete lines have configured maximum sizes;
- per-session rate limits protect the daemon;
- malformed events do not replace the last valid value; and
- session RPC cannot inspect or change another session.

## Agent adapters

Adapters live as inspectable cookbook configuration rather than wrappers around
agent execution. Their only responsibility is to map native events to the
generic condition vocabulary.

Typical mappings are:

| Native meaning | P condition |
|---|---|
| prompt submitted, tool starting or continuing | `running` |
| permission prompt, elicitation, explicit input request | `attention` |
| normal main turn or identified subagent stop | `idle` |
| agent/API failure | `failed` |
| lifecycle event with no safe semantic mapping | omitted or `unknown` |

Claude Code and Codex mappings must be validated against real versioned traces
before support is claimed. The development evidence is tracked in
[development-validations.md](development-validations.md#5-agent-hook-mappings).
Unsupported versions emit no semantic condition rather than guessed status.

Ordinary natural-language questions are not reliably distinguishable from
turn completion unless the agent emits an explicit input event. P does not
parse terminal output to compensate.

## Presentation

The overview combines the independent fields without inventing a single
durable session state:

1. `missing` or `unreachable` runtime conditions remain prominent;
2. a live attachment renders as attached and suppresses semantic emphasis;
3. when unattended, `attention` and `failed` are urgent;
4. other latest unattended conditions are informational; and
5. a running runtime with no unattended condition is neutral, not inferred
   idle or active.

The preview shows the runtime condition, backend, branch, attachment presence,
and the latest unattended condition when one exists. It labels the latter with
its age and source. An incomplete or failed lifecycle operation is displayed
separately with its phase; it is never encoded as an agent condition.

## Notifications

Notification sinks consume transitions written to
`latest_unattended_condition`, normally `attention` and `failed`. P sends no
semantic notifications while attached.

Delivery deduplication is notifier bookkeeping, not seen/unseen state. Remote
notification bodies contain only session name and condition by default;
including `reason` requires explicit opt-in because it may contain project
context.

## Restart and failure behavior

- SQLite preserves runtime records and the latest unattended condition.
- Attachment leases disappear on daemon restart.
- Backend reconciliation restores runtime condition without manufacturing
  semantic agent status.
- A missing hook or adapter leaves the semantic field empty.
- Duplicate reports are harmless last-write-wins events; reordered arrival is
  ordered by daemon receive sequence.
- A stopped runtime retains its latest unattended condition until the user
  enters, a later report replaces it, or the session is removed.
- Discard/delete removes the semantic field with the session row.

## Security boundary

The session socket accepts status reports and the narrow act-on-self methods
defined in [communication-boundaries.md](communication-boundaries.md#session-rpc-audience).
It is not a general command channel.

The daemon treats agent reports as authenticated session input, not trusted
facts about the host. Reports affect presentation and notifications only. They
cannot grant capabilities, mutate Git authorization, publish, create another
session, or execute host commands.

## V1 boundary

V1 includes:

- authoritative runtime condition;
- connection-bound attachment count;
- one nullable latest unattended condition per session;
- clear-on-enter and suppression while attached;
- bounded session status notifications;
- versioned Claude Code and Codex cookbook mappings after validation; and
- overview, preview, and optional notifications derived from those fields.

V1 excludes seen/unseen history, observation cursors, participant inventories,
multiple attention records, causal permission resolution, terminal parsing,
tmux pane inspection, and P-managed services.
