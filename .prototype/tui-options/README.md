# P session browser prototype

A disposable Bubble Tea application with simulated sessions, agent instances,
project services, terminal contents, boot logs, and journals. It does not
connect to real runtimes, agents, services, repositories, or credentials.

[Current interaction decisions](DECISIONS.md) record the user-reviewed direction,
key maps, behavior, and remaining integration questions. Start there when
continuing this prototype; the older gallery observations are historical.

## Run the current browser

From the repository root:

```sh
cd .prototype/tui-options
nix develop path:.
go run .
```

Alternatively, use the existing `.envrc` with `direnv allow` yourself. The
default view is Resource topology, refined into the session browser. Gallery
controls are disabled by default.

| Key | Picker action |
|---|---|
| `j/k`, arrows | Select session |
| Enter | Enter session, showing boot logs first if stopped |
| `s` | Stop session |
| `P` | Select project |
| `/` | Fuzzy search below the list |
| `A` | Agents and conversation previews |
| `S` | Project services |
| `?` | Help |
| `q`, Esc, Ctrl-C | Back/cancel; quit when idle |

Inside the fake terminal, **Ctrl+B** opens **[D]etach / [A]gents / [S]ervices**.
Those inspection pages return to the same attached terminal. Ordinary terminal
keys do not navigate away. Detaching silently returns to the picker and
preserves the fake terminal buffer; stopping discards it.

Configure the terminal bar's context fields and order:

```sh
go run . --session-bar project,branch,status
go run . --session-bar branch,project
go run . --session-bar id,status
```

The prefix hint remains visible. Supported fields are `project`, `branch`,
`status`, and `id`; an empty value omits context fields.

On Services, `s` toggles start/stop, `r` restarts, and Enter opens the full
journal. In the journal, use arrows/Page Up/Down, `g/G`, `h/l`, `/`, `n/N`, and
`f`; the on-screen help explains each. All actions and journal entries are
simulated. On Agents, `j/k` selects an instance and Page Up/Down browses its
recorded mock preview.

## Validate and build

Inside the development shell:

```sh
go test ./...
go vet ./...
go build -o .cache/bin/p-sessions .
```

Generate a deterministic browser frame without an interactive terminal:

```sh
go run . --snapshot --width 120 --height 35
go run . --snapshot --dataset dense --width 160 --height 50
```

The standard fixture uses fixed random UUIDs and named Codex instances.
Specialized stress data retain synthetic identities. The picker has a compact
presentation below 72×22 and an explicit too-small view below 48×16; inspect
new pages and unusually small terminal sizes separately before support claims.

## Retained comparison gallery and stress fixtures

The earlier layouts remain available for comparison:

```sh
go run . --gallery --variant table
go run . --gallery --variant topology
go run . --gallery --mode stress --dataset baseline
go run . --gallery --mode stress --dataset churn --stress-step 3
go run . --mode explore --list-datasets
go run . --mode stress --list-datasets
```

Only gallery mode exposes `Tab` to cycle layouts, `v` for the variant picker,
and the original `1`–`6` shortcuts. Gallery headers show fixture/stress context.
The browser deliberately omits that comparison chrome. `--variant` can also
select an initial alternative explicitly, but the reviewed browser direction
is topology.

The 18 comparison variants are table, navigator, attention, command, focus,
lanes, outline, matrix, operations, workspace, minimal, cards, attachment,
governance, compare, topology, actions, and integrity. Previous tradeoffs and
captures are in [observations](evidence/observations.md) and
[stress observations](evidence/stress/observations.md). They are not current
browser baselines and do not override [the decision record](DECISIONS.md).

Regenerate gallery evidence only when intentionally reviewing those captures:

```sh
sh capture.sh
sh capture-stress.sh
sh capture-churn.sh
```

These scripts build the comparison executable and render deterministic
fixtures. Generated binaries, Go caches, and module downloads stay in ignored
`.cache/`; do not commit them.
