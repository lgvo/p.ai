# One request type and one result type per method

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** the control API has no shared shapes. The daemon dispatches 56
methods (53 advertised, plus the attachment helper's three); `rpc.cancel` is
handled before dispatch. Each handler decodes its params into an anonymous
struct declared inside the handler, and most handlers in `internal/control`
build their result as `map[string]any`. The TUI writes its requests as
`map[string]any` and decodes results into struct shapes of its own. The TUI
test daemon (`tests/integration/cmd/tui-mock`) keeps a third copy: a list of
field names per method in `schema.go`. Every request and every result
carries `"v":1`. A field renamed in the daemon therefore compiles everywhere and fails at run
time, often first in a VM scenario.

**Proposed:**

- `internal/api` declares one request type and one result type per method and
  binds them to the method's name in one value, `api.Method[Req, Res]`.
- The daemon registers each handler with that value, and the TUI, the CLI,
  the attachment client, the TUI test daemon and the tests call through it. A
  changed shape then fails to compile at every caller.
- Each request type validates its own bounds (`Validate`). The daemon runs it
  on every request; clients run it before sending.
- No request or result carries `v`. Each connection starts with
  `system.hello`, which agrees the protocol, `p.control/v2`.
- `system.capabilities` goes. `system.hello` reports which capabilities the
  daemon has configured; the Git connection details stay in `system.inspect`.

**What users notice:** nothing in the TUI. `p api` sends the hello itself, and
its params lose `"v":1`. Scripts that call `system.capabilities` call
`system.hello` instead. A client speaking to the socket directly sends a hello
first.

## Before and after

| | Today | After |
|---|---|---|
| Request shape | Anonymous struct inside each handler; the TUI and tui-mock write their own | `api.<Method>Request`, one per method |
| Result shape | `map[string]any`, or a type in `internal/api` or `internal/control` | `api.<Method>Result`, one per method; methods may share one, such as `api.OperationResult` |
| Bounds (limits, key and UUID length) | Inside each handler's decode condition | `Validate()` on the request type |
| Version | `"v":1` in every request and result, checked per handler | Agreed once per connection at `system.hello` |
| Which methods exist | `system.capabilities` lists the dispatched names | The protocol fixes the methods; `system.hello` lists the configured capabilities; a method whose capability is missing answers `not_configured` |
| Preview types (`RemovalPreview` and nine more) | `internal/control`, so clients decode them by hand | `internal/api`; step 5 wraps them in the plan envelope |
| Services fields | `unit`, `active_state`, `sub_state`, `session.service.journal` | `name`, `state`, `detail`, `session.service.logs`, as [services-plugin.md](services-plugin.md#what-ps-api-and-tui-keep) decided for this step |

On the wire:

```text
Before
→ {"jsonrpc":"2.0","id":1,"method":"session.inspect","params":{"v":1,"uuid":"4f1c…"}}
← {"jsonrpc":"2.0","id":1,"result":{"v":1,"session":{…}}}

After
→ {"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"protocol":"p.control/v2"}}
← {"jsonrpc":"2.0","id":1,"result":{"protocol":"p.control/v2","build_version":"…","instance_id":"…","capabilities":["git","runtime"]}}
→ {"jsonrpc":"2.0","id":2,"method":"session.inspect","params":{"uuid":"4f1c…"}}
← {"jsonrpc":"2.0","id":2,"result":{"session":{…}}}
```

In the daemon, before (`sessionAction` in `internal/control/rpc_lifecycle.go`,
abridged):

```go
var p struct{ V int `json:"v"`; UUID string `json:"uuid"` }   // declared in the handler
…
return map[string]any{"v": 1, "session": view}, nil
```

After:

```go
register(t, api.SessionInspect, func(ctx context.Context, r api.SessionInspectRequest) (api.SessionResult, error) {
	view, err := life.InspectSession(ctx, r.UUID)
	return api.SessionResult{Session: view}, err
})
```

In the TUI, before (`internal/tui/workflows.go` and `model.go`, abridged):

```go
m.begin("session.inspect", params{"v": 1, "uuid": id})   // params is map[string]any
var r struct{ Session api.SessionView `json:"session"` }  // the TUI's own shape
```

After:

```go
r, err := api.Do(ctx, conn, api.SessionInspect, api.SessionInspectRequest{UUID: id})
// r.Session is an api.SessionView
```

## The types

```go
// internal/api: one value per method binds its name to its two types.
type Method[Req Request, Res any] struct{ Name string }

// Every request type checks its own bounds.
type Request interface{ Validate() error }

var (
	SessionInspect = Method[SessionInspectRequest, SessionResult]{"session.inspect"}
	SessionList    = Method[SessionListRequest, SessionListResult]{"session.list"}
	OperationRetry = Method[OperationRetryRequest, OperationResult]{"operation.retry"}
	…
)

// Methods lists every method of the protocol; tests use it.
func Methods() []MethodInfo
```

The sketch is illustrative; names may change in implementation.

The rules:

- **One request type per method,** even where two methods take the same
  fields, because the bounds differ: `session.list` allows a limit of 1 to 8,
  `operation.list` 1 to 20 and `project.list` 1 to 100, so each needs its own
  `Validate`.
- **One result type per method;** several methods may share it. Every change
  that returns an operation returns `api.OperationResult{Operation}`.
- **Strict decoding stays:** the daemon decodes with `internal/strictjson`, so
  unknown and duplicate fields are refused, as today.
- **Lists keep their field names** (`projects`, `sessions`, `operations`,
  `refs`, `branches`) with `next`. `boundedLifecyclePage`, which shortens a
  page to fit the 64 KiB frame, becomes generic over the result type instead
  of building a map.
- **Risky changes** (step 5) have one method called twice, so their result
  holds either a plan or an operation:

  ```go
  type ChangeRequest struct { // embedded in each risky change's request
  	Key     string `json:"key,omitempty"`
  	IfMatch string `json:"if_match,omitempty"`
  }
  type ChangeResult[D any] struct {
  	Plan      *Plan[D]   `json:"plan,omitempty"`
  	ChangeTag string     `json:"change_tag,omitempty"`
  	Operation *Operation `json:"operation,omitempty"`
  }
  ```

  `Plan[D]`, `Refusal` and `Loss` are [plan-shape.md](plan-shape.md#go-shape)'s
  types.
- **Operation evidence and request stay raw JSON** (`json.RawMessage`), as
  today. Evidence is each worker's durable record, its shape belongs to the
  operation kind, and clients only display it. Facts that clients act on are
  projected into typed fields, as `environment` is today
  (`CreationEnvironmentView`).
- **Errors** are `api.Error`, defined in [errors.md](errors.md).
- **`p api` stays raw:** it takes a method name and JSON params, as today. It
  is the one caller that sends untyped params, and only through
  `api.CallRaw`.

## The method table

The table keeps its role: what dispatch serves is exactly what the daemon has
configured. `add`, which takes an untyped `Handler`, becomes a generic
`register`:

```go
// internal/control/rpc.go
func register[Req api.Request, Res any](t *methodTable, m api.Method[Req, Res],
	h func(context.Context, Req) (Res, error))
```

`register` builds the untyped dispatch function once: decode the params into
`Req` with `strictjson`, run `Validate`, call `h`, map its error with the one
mapper from [errors.md](errors.md), and encode `Res`. Handlers no longer see
JSON, `v` or RPC error codes.

Every name in `api.Methods()` is registered, served or not:

| Case | Today | After |
|---|---|---|
| Capability configured | `add` | `register` |
| Capability not configured (no Git, no runtime) | `unconfiguredMethod` guesses from the name's prefix and answers `unavailable` | `notConfigured(m, capability)` answers `not_configured` with the capability's name |
| Optional lifecycle interface missing | `t.unavailable` answers "record was not found" | Gone. In the daemon `*lifecycle` implements every optional interface; I checked by building `internal/daemon` with an extra file asserting all 21. Test fakes register only what they implement. |
| Name not in the protocol | `method_not_found` | `method_not_found` |
| Attachment helper methods | `addHidden` | `register` on a table the daemon serves only on the helper's dedicated connection |

## The version, agreed at `system.hello`

The first request on every connection is `system.hello`:

| | |
|---|---|
| Params | `{"protocol": "p.control/v2"}` |
| Result | `protocol`, `build_version`, `instance_id`, `capabilities` |
| Protocol the daemon does not speak | Error `unsupported_version` with `data.supported`, the protocols it speaks |
| Any other request before a successful hello | Error `invalid_request` with reason `hello_required` |
| A notification before hello | Ignored, as unknown notifications are today |

The protocol becomes `p.control/v2` because every shape changes. The daemon
speaks one protocol at a time: P targets fresh installs, and the daemon, CLI
and TUI ship from one build. The check still matters, because upgrading the
system replaces the daemon under a TUI that is already running; that TUI then
gets a clear refusal when it reconnects, instead of `invalid` errors in the
middle of a workflow.

`ServeConn` (`internal/control/rpc.go`) answers hello inline in its read
loop, as it does `rpc.cancel`, and records the protocol on the connection
before reading the next line. A client may therefore write the hello and its
first request together. `rpc.cancel` loses `v` and keeps its other behavior.

`capabilities` lists what the daemon has configured, today from
`internal/daemon/daemon.go`:

| Capability | Configured when | Methods |
|---|---|---|
| `git` | Host configuration has Git | Project reads, origins, publication, retained branches |
| `runtime` | Host configuration also has a runtime | Lifecycle, operations, attachment |
| `services` | The runtime driver offers services ([services-plugin.md](services-plugin.md#sketch)) | The three services methods |

Clients use `api.Dial`, which connects and sends the hello, and `api.Do`,
which runs `Validate` and makes one typed call. The TUI may keep its one
connection per call; on a local socket the hello costs one more line.

**The private session RPC keeps `v` in this step.** It is the agent-facing
socket of [communication boundaries](../docs/communication-boundaries.md#session-rpc-audience),
part of the plugin contract, and its main message, `status.report`, is a
notification that receives no reply and so cannot take part in a hello. Its
three methods get typed request and result types, and its errors take the new
shape because they share `api.Error`. See open question 1.

## What happens to `system.capabilities`

It goes. Today it returns `available` (the advertised method names),
`lifecycle` (`"unavailable"`, or `"partial"` whenever a runtime is
configured) and the Git connection details. Under one protocol the method
names are fixed, and only the configured capabilities vary; `lifecycle`
carries no information beyond that.

| Reader today | After |
|---|---|
| The TUI's `loadInventory` (`internal/tui/client.go`), to skip lists that are not available | Reads `capabilities` from its hello |
| VM step scripts check `.result.available` | Check `.result.capabilities` from `system.hello` |
| Scenario 07 reads `.result.git` | Reads `system.inspect`, which already returns the same details and which scenario 07 already compares with them |
| The tui-mock answers it | Answers `system.hello` |

## How tests prove a shape change fails to compile

1. **The binding.** `register` and `api.Do` both take the same
   `api.Method[Req, Res]` value. Changing a field's type, or a method's
   request or result type, breaks the handler and every call at compile time.
   Today `TestConfiguredReadOnlyProjectMethods` and others assert on
   `result.(map[string]any)["projects"]`, which compiles whatever the shape;
   they move to typed results.
2. **One list of methods.** A test asserts that the daemon's table registers
   exactly `api.Methods()`, and another that the tui-mock serves only those
   names with those types. The mock's `schema.go` field list goes: the mock
   decodes into the request types with the same strict decoder.
3. **No loose shapes.** A test parses the Go files of `internal/control`,
   `internal/daemon`, `internal/tui`, `internal/attachment`, `cmd/p` and the
   tui-mock and fails on a call to `api.CallRaw` outside `cmd/p`, and on a
   `map[string]any` passed as params or returned as a result.
4. **JSON names.** A renamed JSON tag compiles everywhere and still breaks
   shell clients. A golden-file test writes every method's request and result
   fields (JSON name, JSON type, optional or not) from the types by
   reflection and compares them with `internal/api/testdata/methods.golden`.
   A changed tag fails until the file, `docs/control-api.md` and the VM
   scripts change with it.

The VM scenarios still check the wire end to end: their `"v":1` params go,
and their outcome checks stay.

## Departures from current docs

- [Control API](../docs/control-api.md#host-framing-and-request-envelope):
  "System methods take exactly `{"v":1}`", every params and result table, the
  `system.capabilities` row, and `p api`'s default params.
- [User guide](../docs/user-guide.md): its `"v":1` examples and
  `api system.capabilities '{"v":1}'`.
- [Session lifecycle](../docs/session-lifecycle.md): its `"v":1` examples.
- `p version` reports `control_api_version: 1`; it reports the protocol
  string instead.

## Options set aside

| Option | Why not |
|---|---|
| An optional hello, with no per-request version | A TUI left running across a daemon upgrade would fail in the middle of a workflow with `invalid` errors instead of being refused at connect. |
| The version in the socket path or the method names | Renames every method or the socket; the hello already returns the instance identity and capabilities in one place. |
| A list of protocols in the hello | The daemon speaks one protocol, and installs are fresh. |
| Generate the types from a schema (JSON Schema, OpenRPC) | A second toolchain for one implementation language. The Go types are the schema; the golden file catches drift. |
| One generic page type with an `items` field | Renames the list fields that scripts read, for no gain in the Go clients. |
| Type each operation kind's evidence | A union over 17 kinds of the workers' durable records, which clients only display. |
| Keep `system.capabilities` | Its method list repeats the protocol, and `lifecycle` is always `partial` with a runtime. |

## Open questions

1. **Does the private session RPC move to a hello too?** Recommendation: not
   in this step. Its clients are agent adapters (the bundled Codex adapter
   sends `status.report`), it is part of the plugin contract, and a
   notification cannot negotiate. Decide it with the plugins step (9).

## How it will be checked

- The tests in [How tests prove a shape change fails to compile](#how-tests-prove-a-shape-change-fails-to-compile).
- Unit tests for the hello: a request before hello is refused with
  `hello_required`; an unknown protocol is refused with `unsupported_version`
  and the supported list; hello and a request written together are both
  answered; `capabilities` matches each configuration (none, Git, Git and
  runtime).
- Unit tests for each request type's `Validate`, carrying over today's bounds
  from the handlers.
- The TUI tests and the TUI driver tests run against the typed tui-mock.
- The VM scenarios pass with `"v":1` removed and `system.capabilities`
  replaced, keeping their outcome checks.
