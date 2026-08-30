# P — session observability

How P reports runtime health, startup readiness, confirmed attachment presence,
and the latest agent signal received while a session is unattended.

> **Status: design.** This document is authoritative for session status and
> agent-event reduction. [communication-boundaries.md](communication-boundaries.md)
> defines the RPC channels that carry these facts, while
> [session-lifecycle.md](session-lifecycle.md) defines the operations that
> change runtime condition and attachment eligibility.

## Purpose

P needs to answer four small questions:

1. Does the session runtime exist and can P reach it?
2. Is that runtime currently ready for attachment?
3. Is the user currently attached to the session?
4. What is the latest supported agent event received while nobody was attached?

V1 does not implement a lossless activity history, read/unread state, multiple
outstanding attention records, service supervision, or semantic inference from
terminal contents.

## Status model

The persisted and live model has four independent fields:

```text
runtime_condition             authoritative lifecycle state
startup_readiness             reconciled current-start preparation result
attached_count                confirmed live attachment count
latest_unattended_condition   nullable last-write-wins agent signal
```

### Runtime condition

The daemon derives runtime condition from the lifecycle registry and runtime
backend inspection (Incus in V1). Registry states `creating` and `removing`
project directly; an established session uses the observed runtime result:

| Condition | Meaning |
|---|---|
| `creating` | A persisted creation operation is incomplete. |
| `running` | Incus reports the V1 instance running. |
| `stopped` | The runtime is intentionally stopped and restartable. |
| `missing` | SQLite expects a runtime that Incus cannot find. |
| `unreachable` | Incus cannot currently be queried. |
| `removing` | A persisted discard/delete operation is incomplete. |

Attachment never clears or rewrites runtime condition.

### Startup readiness

Runtime condition answers what Incus is doing; startup readiness answers
whether preparation succeeded for the current running generation. It does not
claim that the interactive host or configured command has survived since
startup. It is a reconciled projection:

| Startup readiness | Meaning |
|---|---|
| `ready` | The current start generation completed endpoint validation, Nix/environment activation, and interactive-host preparation. |
| `not_ready` | The instance is running, but the current generation is starting, failed, or has an invalid/missing marker. A bounded stable reason code and diagnostic accompany it. |
| `inactive` | The registry/runtime condition is creating, stopped, missing, removing, or otherwise not currently attachable. |
| `unknown` | P cannot inspect enough current runtime state to decide. |

While registry state is `creating`, startup readiness is always `inactive` even
if a partial instance is running. Creation activation/preparation failure is
presented solely through the durable creation operation and its current phase.
Only `established` sessions project `ready` or `not_ready` from the startup
marker.

Before asking Incus to start an instance, P records a new start generation. The
P-owned launcher writes a versioned marker for that generation as it moves from
`starting` to either `ready` or `failed`. Reconciliation compares the expected
generation with that marker; a marker from an earlier start never establishes
startup readiness.

The current `not_ready` reason is durable session state, not merely an operation
diagnostic. It remains visible while that generation is running and not ready,
even after terminal operation records expire. A later successful start marks
the new generation ready; stop makes startup readiness inactive;
missing/unreachable state never becomes ready by inference.

Attach requires both `runtime_condition == running` and
`startup_readiness == ready`, then performs a fresh independent
`InteractiveHost` check. A failed host check reports the current host condition
and recommends stop/start; it does not rewrite startup readiness.

### Attachment presence

`attached_count > 0` means at least one host client is currently inside the
session. Zero means the session is unattended.

An attach request first creates a short-lived, one-use pending token bound to
the session and authenticated host request and returns it with the `AttachSpec`.
Pending is not attachment presence. The client transfers that token over
private control input to the trusted host helper. After establishing the
interactive channel, the helper confirms the token on its dedicated attachment
RPC connection. Only then does the daemon promote it to an active lease owned
by that helper connection and increment `attached_count`.

Failure to launch, token expiry, or RPC closure before confirmation removes the
pending token without clearing status. While the daemon remains reachable, the
helper keeps its confirmed lease connection alive until channel teardown
finishes and closes it afterward. Client crash/SIGKILL, carrier/SSH loss, or
client-machine loss triggers helper/transport teardown without client
cooperation. If daemon restart drops the lease first, the helper begins teardown
immediately and establishes no new lease or channel until it finishes; neither
pending tokens nor active leases are restored from SQLite. V1 does not
re-register a surviving channel. Tmux teardown detaches only that client while
preserving its server/session. Direct's runtime wrapper terminates and waits for
the command when its exec channel disappears.

The confirmed transition from zero to one attachment clears
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
2. Confirming the first live attachment clears the field.
3. While confirmed attached, semantic reports are validated but neither
   retained as status nor notified.
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
[development-validations.md](development-validations.md#9-agent-hook-mappings).
Unsupported versions emit no semantic condition rather than guessed status.

Ordinary natural-language questions are not reliably distinguishable from
turn completion unless the agent emits an explicit input event. P does not
parse terminal output to compensate.

## Presentation

The overview combines the independent fields without inventing a single
durable session state:

1. `missing` or `unreachable` runtime conditions remain prominent;
2. a confirmed live attachment renders as attached and suppresses semantic
   emphasis;
3. a pending attachment is shown only as short-lived connection progress and
   does not suppress unattended status;
4. when unattended, `attention` and `failed` are urgent;
5. other latest unattended conditions are informational; and
6. a ready running runtime with no unattended condition is neutral, not inferred
   idle or active.

The preview shows runtime condition and startup readiness, placement, branch,
confirmed attachment presence, and the latest unattended condition when one
exists. It labels the latter with its age and source. A current not-ready reason
remains visible independently of bounded operation history. An incomplete or
failed lifecycle operation is displayed separately with its phase; it is never
encoded as an agent condition.

## Notifications

Notification sinks consume transitions written to
`latest_unattended_condition`, normally `attention` and `failed`. P sends no
semantic notifications while attached.

Delivery deduplication is notifier bookkeeping, not seen/unseen state. Remote
notification bodies contain only session name and condition by default;
including `reason` requires explicit opt-in because it may contain project
context.

## Restart and failure behavior

- SQLite preserves runtime records, the expected startup generation/current
  bounded startup-readiness projection, and the latest unattended condition.
- Pending attachment tokens and active leases disappear on daemon restart.
- The trusted helper retains a reachable lease until teardown completes.
  Client/carrier loss triggers teardown independently; daemon-restart lease loss
  triggers it immediately and permits no new helper channel before completion.
  No existing-channel registration exists.
- Incus plus the versioned launcher marker restore runtime condition/startup
  readiness without manufacturing semantic agent status.
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
- reconciled current-generation startup readiness with a durable bounded
  not-ready reason;
- connection-bound attachment count;
- one nullable latest unattended condition per session;
- clear-on-confirmed-entry and suppression while confirmed attached;
- bounded session status notifications;
- versioned Claude Code and Codex cookbook mappings after validation; and
- overview, preview, and optional notifications derived from those fields.

V1 excludes seen/unseen history, observation cursors, participant inventories,
multiple attention records, causal permission resolution, terminal parsing,
tmux pane inspection, and P-managed services.
