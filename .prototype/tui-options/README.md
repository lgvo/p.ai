# P session browser prototype

A disposable Bubble Tea application with simulated sessions, agent instances,
project services, terminal contents, boot logs, and journals. It does not
connect to real runtimes, agents, services, repositories, or credentials.

[Current interaction decisions](DECISIONS.md) record the user-reviewed direction,
key maps, behavior, and remaining integration questions. Start there when
continuing this prototype; the older gallery observations are historical.
The [iteration history](ITERATIONS.md) explains the earlier exploration and
links its preserved evidence. Continue from the default browser below;
`--gallery` is for revisiting alternatives.

## Next iteration

Resource topology is the selected layout. Continue from its current browser
and focus the next review on **creation and policy workflows**.

The `c` creation flow now selects **Project → Branch → Policy**. Choose a
branch that has no session to use it directly, with no source question.
Alternatively, choose **Create new branch**, choose its source, and then enter
its name. Confirm the current project policy to create, boot, and enter the
simulated session.
Use `/` in Project, Branch, or Source to fuzzy-search the choices. The
`fuzzy search> query` replaces the first line inside the list panel during
search. The panel keeps its height as matches change. Enter applies the query,
then Enter selects a result. Search resets when moving to another step.
`q`, Esc, and Ctrl-C clear search first, then go back one step; leaving Project cancels. The separate
`p` policy screen remains an earlier fixture probe for the next review.
Policy currently has one choice: the project configuration; no alternate
profiles or session-specific grant overrides are defined.

## Run the current browser

From the repository root:

```sh
cd .prototype/tui-options
nix develop path:.
go run .
```

Alternatively, use the existing `.envrc` with `direnv allow` yourself. The
default view is Resource topology, refined into the session browser. Gallery
controls are disabled by default. The default `portfolio` fixture contains
**24 projects, 120 sessions, and 124 unassigned branches** (32 in forge; 4 in each other project).
There are **112 stopped sessions and 8 running sessions, 2 of them waiting**.
Active work shares three projects: forge (4), orbit (2), and p.ai (2).
Branches describe project-specific work; waiting agents ask for a concrete
decision. Agents and project services match that work instead of copying one
resource tree everywhere.
Policy differences, service previews, and interaction timestamps are simulated.
Fixture identities and initial data are deterministic. Use `go run . --dataset standard` for the original nine-session fixture.

| Key | Picker action |
|---|---|
| `j/k`, arrows | Select session |
| PgUp / PgDn | Move by the visible list capacity |
| Home / End or `gg` / `G` | First / last result |
| Ctrl+B / Ctrl+F | Previous / next page outside the terminal |
| Ctrl+U / Ctrl+D | Half page up / down |
| Enter | Enter session, showing boot logs first if stopped |
| `s` | Stop session |
| `c` | Create: Project → Branch → Policy |
| `P` | Select project |
| `/` | Fuzzy search in the first list row |
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
journal. In the journal, use arrows/Page Up/Down, `gg/G`, `h/l`, `/`, `n/N`, and
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

Session, project-filter, and creation Project/Branch/Source lists support
PgUp/PgDn and Home/End, with Ctrl+B/F, Ctrl+U/D, and `gg`/`G` Vim aliases.
Text inputs receive ordinary `g`/`G` characters; Ctrl+B inside a session still
opens its prefix menu. Agents retain page keys for their preview; Services and
Journal use them for their lists and logs. Page movement stops at the first/last result; it does
not wrap. Long lists show the visible range, total results, and selected
position. Apply fuzzy search before using page keys to browse its results.

Sessions always appear in this order: **waiting → other running → all remaining
states**. Projects are grouped within each priority band, so another project's
waiting session always precedes running or stopped work. Project scope and
fuzzy search still apply. Within a project and priority band, recent interaction
breaks ties before branch name. There are no sorting modes or sorting shortcuts.
Interaction timestamps are explicit fixture data, updated on entry and stop;
they do not infer agent activity from terminal output.

The shared Bubbles list adapter owns browser filtering, pagination and row
rendering for sessions, creation selectors, project filtering, Agents, and
Services. A single layout plan supplies widths and row capacities to rendering
and navigation. The custom first-row search and command placement remain.

List capacity comes from the current terminal height. Resizing keeps the
selected item visible; filtering retains the panel height. The session
browser uses a 65% list column at 110 columns and wider, stacks details below
list controls at medium widths, and uses compact disclosure below 72×22.
The responsive checks cover 48×16, 60×20, 80×24, 120×35, and 160×50, including
creation selectors, project filtering, and selections near the end of a list.

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

Stopping a session with `s` opens a confirmation naming its project and branch.
`y` confirms; `n` or `Enter` declines; `q`, `Esc`, or `Ctrl-C` cancels. A simulated container shutdown
log then shows processes and services stopping before returning to the picker.
Leaving the log does not cancel shutdown. Branch and files are retained.

Confirmation prompts use highlighted `[y/N]` inside the relevant panel.
`y` / `Y` accepts; `n` / `N` / `Enter` declines (No is the default).
This applies to session stop, final creation, and project deletion.
Destructive confirmations are red; creation confirmations are amber.
Ordinary selection still uses Enter; back controls remain below the panel.

The `P project` selector supports `/` fuzzy search in the first list row,
without resizing the panel. Enter applies the query, then Enter selects a
project. Esc clears the search before leaving the selector.

Each project row shows right-aligned `!waiting · running · stopped * total`
counts. Projects sort descending by waiting, then other running, then total;
name breaks ties. All projects stays first with inventory totals. Fuzzy search
preserves this ordering among matches. Narrow terminals abbreviate the labels
as `!w`, `r`, `s`, and `t`. Total includes transitional and other lifecycle states.

Selector cards and their controls are centered, with a 96-column maximum
width. Project names use at most 28 columns and branch choices 48; full names
remain searchable. State count columns reserve consistent numeric widths
across all project rows, so labels and totals align.

Management screens share a horizontally centered, bounded viewport, including
inspectors, journal, startup/shutdown views, and historical layouts.
Single-column pages cap at 96 columns; the wide session browser caps at 148
columns to preserve its approximately 65/35 list/detail split. Controls and
status bars align with that viewport. Narrow terminals still use their
available width, and paging uses the same layout dimensions as rendering.
The in-session terminal fills the entire terminal width and height, like tmux
inside the container. Its status bar spans the full width; management views
opened through the prefix remain centered.
