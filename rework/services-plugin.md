# Project services in the runtime driver

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** a project service is a systemd user unit,
`p-project-<name>.service`. The user, the agent or a project script writes it
into `~/.config/systemd/user` inside the running session and loads it with
`systemctl --user daemon-reload`. P lists those units, starts, stops or
restarts one, and reads a bounded journal tail (`session.services`,
`session.service.action`, `session.service.journal`). It does that through the
WASI runtime plugin's `runtime.services` broker, which runs fixed
`systemctl` and `journalctl` commands as the session user
(internal/runtimeincus/services.go). Core and the API carry systemd's unit
names and state pairs.

**Asked by the maintainer on 2026-11-02:** a way to change the services
implementation later, for example to containers in a Kubernetes pod, designed
"starting from the actual usage". The maintainer then settled its shape:

- Images are runtime-specific. How a runtime runs the session's interactive
  host and its project services is that runtime's own business, and it does
  not expose it. The Incus runtime keeps systemd for both, as today.
- Services are therefore not a separate plugin. They are a small group of
  operations in the [runtime driver](runtime-driver.md)'s contract, and each
  driver implements them its own way. Replacing the driver replaces the
  services implementation.
- P targets fresh installs only: no upgrade path.

**Proposed:** three driver operations, derived from what P's API, TUI and
tests use: list the session's services with their states, start, stop or
restart one, and read a recent log tail. States are P's: stopped, starting,
running, stopping, failed, with the driver's own words as display detail.
Defining a service stays inside the session, in the runtime's own way; P never
defines one. The Incus driver implements the group with today's code.

**What users notice:** nothing on Incus. Agents keep writing
`p-project-*.service` user units and using `systemctl --user`; the TUI shows
the same names, states and journals. API clients see new field names (`name`,
`state`, `detail`) in the [wire types step](REWORK-PLAN.md#steps). On another
runtime, how an agent defines services is that driver's documented
convention.

## Before and after

| | Today | After |
|---|---|---|
| Who runs project services | The session's systemd user manager | The runtime, through the driver; on Incus the same user manager |
| How P reaches them | `plugin.RunRuntime` kind `runtime.services`, the WASI module, then the broker, then `runtimeincus.Backend.Services` | `Driver.Services`, `Driver.ServiceAction`, `Driver.ServiceLogs` |
| Where systemd knowledge lives | `internal/api` (the unit pattern), `internal/control`, `internal/daemon`, `internal/runtimeincus` | The Incus driver only |
| States in the API | `active_state` and `sub_state`, systemd's words | `state`, P's closed set; `detail`, the driver's words for display |
| How services are defined | Unit files in the session, `systemctl --user daemon-reload` | Unchanged on Incus; each driver's own convention elsewhere |
| Replacing the implementation | Not possible | Replace the runtime driver |

```text
Before
TUI, CLI ─► session.service* ─► daemon ─► runtime.wasm ─► broker ─► runtimeincus.Backend.Services ─► incus exec systemctl / journalctl

After
TUI, CLI ─► session.service* ─► daemon ─► runtimedriver.Driver: Services, ServiceAction, ServiceLogs
                                             └─ Incus driver ─► incus exec systemctl / journalctl, as today
agent in the session ─► the runtime's own tools (Incus: systemctl --user, journalctl --user), unchanged
```

## How services are used today

| Question | Today | Seen in |
|---|---|---|
| Who defines a service | The user or agent by hand, or a committed project script that renders committed unit templates | [User guide](../docs/user-guide.md#project-services) example; scenario 56; `examples/notes/install.py` |
| When and where | Any time after the session is ready, in `~/.config/systemd/user/` inside the container; again in every new session, which gets a fresh home | [Notes walkthrough](../examples/notes/README.md): "Run these commands inside `/workspace` in each new session" |
| What a unit uses | `Description`, `ExecStart`, `WorkingDirectory`, `Environment`, `ExecStartPre` and `ExecStartPost` as readiness steps, `Requires`, `BindsTo` and `After` between services, `KillSignal`, `KillMode`, the start and stop timeouts, `Type=exec`, `Restart=no`, `UMask` | The five project units in the repository: the three notes units, the user guide's example, scenario 56's demo unit |
| Start with the session | `systemctl --user enable`; the base image's lingering user manager starts enabled units at boot | `dev/vm/seed-notes.sh` enables the notes units; lab-public-check expects them active after a VM relaunch; scenario 57 and notes-lab-persistence start them by hand after each Start |
| Done in the session, never through P | Writing and reloading units, `enable`, `status`, `kill`, `reset-failed`, longer `journalctl` reads | Notes walkthrough; scenario 57 simulates a crash with `systemctl --user kill --signal=KILL` |

What P and its clients use:

| Need | Today | Used by |
|---|---|---|
| Inventory with states | `session.services`: `unit`, `description`, `active_state`, `sub_state`; loaded units plus installed unit files, at most 64; an installed unit the manager has not loaded is `unknown (not-loaded)`, which the TUI shows as `installed` | TUI Services page (`S`), the selected-session panel and details (internal/tui/view.go); scenarios 56, 57, 58; lab-public-check |
| Start, stop or restart one | `session.service.action`, synchronous | TUI `s` (start or stop, chosen by whether the unit is active) and `r`; scenarios 57, 58; notes-lab-persistence |
| Recent logs | `session.service.journal`: at most 100 entries, a 16,000-byte tail, a truncation flag | TUI Enter or `J`; scenarios 56, 57, 58 look for lines the service printed |
| Only project services | The `p-project-*.service` pattern | Scenario 56: `p-interactive.service`, `p-session.target`, `sshd.service` and `../p-project-demo.service` are refused |

The CLI has no services command; it reaches them only through `p api`.

So P needs, for one running session: an inventory, three actions and a log
tail. P never defines a service, enables one or reads its definition. Those
stay inside the runtime.

## The services operations

### Sketch

Added to the driver interface of [runtime-driver.md](runtime-driver.md#operations).
The sketch is illustrative; names may change in implementation.

```go
// Project services inside one running session. How the runtime defines and
// runs them is its own business; core sees only these values.
Services(ctx context.Context, t Target) ([]Service, error)
ServiceAction(ctx context.Context, t Target, name string, a Action) error
ServiceLogs(ctx context.Context, t Target, name string) (Logs, error)

type Service struct {
	Name        string       // the driver's name for it; opaque to core
	Description string
	State       ServiceState
	Detail      string       // the driver's words, for display only, such as "active (running)"
}

type ServiceState string // stopped, starting, running, stopping, failed

type Action string // start, stop, restart

type Logs struct {
	Text      string // newest last
	Truncated bool
}

var ErrNoService = errors.New("no such project service in this session")
```

`Capabilities` gains `Services bool`. A driver without services returns
`ErrUnsupported`, and the daemon then registers the three methods as
unavailable, as it does today when no service API is configured
(internal/control/rpc_lifecycle.go).

### States

| State | Meaning | Incus driver |
|---|---|---|
| `stopped` | No process: never started, stopped, or exited cleanly | `inactive`; an installed unit the manager has not loaded |
| `starting` | Started, not yet ready | `activating` |
| `running` | Running and ready | `active`, `reloading`, `refreshing` |
| `stopping` | Stop under way | `deactivating`, `maintenance` |
| `failed` | The last run failed; stays until the next start | `failed` |

An unloaded unit is `stopped`, not unknown: the user manager unloads only
inactive units, which scenario 57 works around today by asking systemd
directly whether such a unit is inactive. The Incus driver keeps today's
words as `detail`, `active (running)` or `installed`, so the TUI line reads
as today.

### Guarantees

| The driver must | Today in the Incus code |
|---|---|
| Act only on the project services of the target session, at the target's incarnation, while it runs. Never reach the runtime's own units (the interactive host, P's base units) or anything outside the session. | Only `p-project-*.service` names; fixed argv; UUID and generation checked before and after |
| Run every service command with the session user's authority and nothing more | `incus exec --user 1000 --group 1000` with the user-manager endpoint |
| Refuse unless the session's host is ready | `ObserveHost` before the call |
| Return a complete inventory or an error, never a subset: at most 64 services, names, descriptions and details bounded and printable, sorted by name | 32,000-byte reads, 64 units, printable checks |
| Queue an action and return as soon as the runtime accepts it; do not wait for it to finish. Progress shows in the inventory: `starting` or `stopping`, then `running`, `stopped` or `failed`, with the logs explaining a failure. Starting a running service or stopping a stopped one does nothing. Refuse at once what cannot be queued, such as an unknown service. | Today `systemctl start`, `stop`, `restart` wait for the job (up to the unit's `TimeoutStartSec`, 45 seconds for the notes database); the Incus driver uses `systemctl --no-block` instead |
| Return at most 100 entries and 16,000 bytes of log, valid UTF-8, with truncation reported | `journalctl -n 100`, the tail cut |
| Return `ErrNoService` for a name the session does not have, `ErrUnavailable` when its service manager cannot be reached | Today both are one `unavailable` message |

Core keeps the session lock, the check that the session is established and
ready, a generic name check (printable, at most 128 bytes), the mapping to
API errors, and the 48,000-byte bound on the encoded result.

## The Incus driver

The Incus driver implements the group with internal/runtimeincus/services.go,
moved into the driver unchanged except for the state mapping:

| Operation | Native effect, as today |
|---|---|
| `Services` | `systemctl --user list-units --all --type=service --output=json 'p-project-*.service'`, merged with `list-unit-files` for installed units not loaded; `emptyUnitFileInventory` still accepts the exit status 1 that comes with an empty list |
| `ServiceAction` | `systemctl --user --no-pager <start|stop|restart> <unit>` |
| `ServiceLogs` | `journalctl --user-unit=<unit> --no-pager --output=short-iso -n 100`, cut to a 16,000-byte tail |

All three run through `/usr/libexec/p/systemctl` or `/usr/libexec/p/journalctl`
as UID and GID 1000, with `XDG_RUNTIME_DIR=/run/user/1000` and
`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus`. The Incus image
keeps what makes this work: the user `p` with lingering enabled and the two
`/usr/libexec/p` links (runtime/image.nix).

The agent's experience does not change: unit files in
`~/.config/systemd/user`, `systemctl --user daemon-reload`, `enable` to start
with the session, `systemctl` and `journalctl` for everything else. Services
still run as the session user, inside the container, with its network, files
and grants, and publish no host ports.

## A Kubernetes driver

Not designed here. A Kubernetes driver would implement the same three
operations in its own way: with a service manager inside the session's
container, or with containers in the session's pod. The second has limits the
driver would carry, not the contract. A pod's containers are fixed when it is
created ("you cannot add a container to a Pod once it has been created",
[ephemeral containers](https://kubernetes.io/docs/concepts/workloads/pods/ephemeral-containers/)),
so a new or changed service waits for the next session start. Kubernetes has
no call that stops or starts one container; it restarts an exited container
by its restart policy only
([pod lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)),
so per-service stop and start need a small process manager inside each
container. Containers share the pod's network but only the directories
mounted into each of them. How an agent defines services there would be that
driver's documented convention.

## What P's API and TUI keep

- The three methods, their bounds (64 services, 100 log entries, 16,000-byte
  tail, 48,000-byte result), the ready-session requirement and the refusal
  of anything but project services. Actions now return once queued instead
  of waiting for the service, by the maintainer's decision of 2026-11-02; the
  TUI already re-reads the list, so it shows the service moving through
  `starting` to `running` or `failed`.
- The TUI's Services page, its keys (`S`, `s`, `r`, Enter or `J`), the journal
  reader and the selected-session panel. The TUI picks `s` as stop when the
  state is `running`, colors `running` and `failed` as today, and shows
  `detail` where it shows `active (running)` today.

What changes in shape, in the wire types step: `unit` becomes `name`;
`active_state` and `sub_state` become `state` and `detail`;
`session.service.journal` becomes `session.service.logs`, with `logs` and
`logs_truncated`. The error message that mentions a "compatible selected
runtime package" goes.

## What leaves the runtime-plugin path

| Today | After |
|---|---|
| internal/daemon/services.go: `serviceBroker` and `plugin.RunRuntime(..., "runtime.services", ...)` | The daemon calls the driver's three operations |
| `RuntimeServicesBroker` and the `runtime.services` kind (internal/plugin/runtime.go), and its case in plugins/bundled/runtime-incus/main.go | Gone with the WASI runtime plugin |
| internal/runtimeincus/services.go | The Incus driver, with the state mapping added |
| `api.ValidProjectServiceUnit` and its pattern, used by internal/control and internal/daemon | The Incus driver; core keeps only the generic name check |
| The UUID and generation recheck after each call | The target's incarnation |
| `checkNoWorkspaceInspect` in the services call | Gone with `workspace.inspect` ([runtime driver](runtime-driver.md#summary)) |

This lands with the runtime driver's migration (step 8), where the
`plugin.RunRuntime` calls move to the driver.

## Departures from current docs

- [Control API](../docs/control-api.md#project-services): "uses selected WASI
  `runtime.services`", the "compatible selected runtime package"
  requirement, and the field names above.
- [Runtime isolation](../docs/runtime-isolation.md#project-user-services):
  "uses the selected runtime WASI package with a core-bound
  `runtime.services` broker"; the section becomes the Incus driver's.
- [Plugin contract](../docs/plugin-contract.md#wasi-runtime-abi): the
  `runtime.services` paragraph, which goes with the WASI runtime ABI.
- [Glossary](../GLOSSARY.md#internal-session-service): an internal session
  service is "supervised through the runtime's systemd contract"; it becomes
  "supervised by the runtime, through the runtime driver".
- The [user guide](../docs/user-guide.md#project-services) and the notes
  walkthrough stay as they are: they describe the Incus runtime.

## Options set aside

| Option | Why not |
|---|---|
| A separate services plugin, chosen apart from the driver | Services run inside the runtime and depend on how it boots its guest; a separate plugin would need runtime authority, a second owner of runtime effects. Set aside by the maintainer: replacing the driver replaces services. |
| A WASI services plugin | P's WASI plugins give way to native executables where that makes sense, as the runtime driver does; a WASI module here would decide nothing. |
| Core building `systemctl` commands over a generic run-a-command driver operation | Puts the Incus runtime's internals in core, so a Kubernetes driver could not run services its own way. |
| Pod or sidecar containers as a contract requirement | Kubernetes-specific, and pod containers cannot be stopped or started one by one. Every driver would have to model services as containers. |
| A P services file and an in-session `p-service` command, portable across runtimes | A product change with no current user; definitions are the runtime's business. |
| A durable operation for each service action | P records and recovers nothing about services; see open question 2. |

## Open questions

None.

- **The name the Incus driver reports** is the unit name, such as
  `p-project-notes-db.service`, as today (chosen by the agent; nothing visible
  changes). Core treats it as opaque.
- **Service actions are fire and observe,** by the maintainer's decision of
  2026-11-02: an action returns once the runtime has queued it, and the
  inventory shows its progress. They are not P operations: restarting a
  service is cheap and repeatable, so there is nothing to journal or recover,
  and the 30-second request limit no longer matters.

## How it will be checked

- **Conformance suite** of the driver, against the in-memory driver and the
  Incus driver with its stubbed runner: a complete inventory or an error,
  never a subset; the bounds; scenario 56's four names never reach the
  service manager; a call at another incarnation refused; an action returns
  once queued, and the inventory then shows `starting` and later `running`
  or `failed`; a driver without services returns `ErrUnsupported`.
- **Incus driver unit tests:** today's internal/runtimeincus/services_test.go,
  moved, plus the state mapping, including an installed unit not loaded as
  `stopped` with detail `installed`.
- **Core tests** on the in-memory driver: the session lock is held, a session
  that is not ready is refused, driver errors map to API errors, nothing is
  retried.
- **TUI tests:** `s` follows `state`; the row shows `detail` as today.
- **VM scenarios** 56, 57, 58, lab-public-check and notes-lab-persistence
  keep their outcome checks on the new field names (`state` instead of
  `active_state`). Scenario 57's workaround for `unknown (not-loaded)` goes,
  because such a unit is now `stopped`.
