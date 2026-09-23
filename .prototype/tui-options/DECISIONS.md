# Session browser — current interaction decisions

Status: user-reviewed prototype direction, recorded 2026-09-08.

This record captures the current decisions from hands-on review of the
Resource topology prototype. It replaces earlier alternatives in this
prototype's README and comparison notes where they differ. It is the reference
for continuing this UI exploration, not a production implementation claim.
[PROJECT.md](../../PROJECT.md) and its subject owners still govern the product
model. The integration questions below remain open.

## Starting point and layout

- The default browser fixture is `portfolio`: 24 projects, 120 sessions, and
  124 unassigned branches. `--dataset standard` retains the smaller fixture;
  gallery defaults remain unchanged. The mix is 112 stopped sessions, 8 running,
  including 2 waiting sessions. Active work is concentrated in forge (4),
  orbit (2), and p.ai (2), with project-specific branches and agent tasks.
  The branch picker has 32 unassigned forge branches and 4 per other project.
  These are all simulated data.
- Resource topology is the preferred direction. The default launch is a
  session browser, without gallery names, layout numbers, comparison copy,
  or gallery navigation keys. The older alternatives remain behind `--gallery`.
- There are two contexts: outside a session (the picker and its inspection
  pages), and inside a session (the terminal and its bottom status bar).
- The normal wide layout gives approximately 65% of the width to the project
  sessions list and the remainder to selected-session details. Smaller
  terminals use stacked or compact presentation.
- Keep session-list commands directly below the list.
  In the wide layout they belong to the list column, so taller session details
  do not move them. The stacked layout also keeps commands before details and
  reserves a consistent list height across selections.
- Keep one line per session. Show project, branch name, and runtime state.
  Do not restore the second statistics line. UUIDs are reference information
  in details, not the primary list label. The standard fixture uses fixed,
  randomly generated UUIDs; specialized stress fixtures retain synthetic IDs.
- Use available height, scroll the list with the selection, and show the
  visible range when entries are outside the window. Keep terminal frames
  bounded so wrapping does not leave earlier frames behind.
- The list heading is `All project sessions`, or `[forge] project sessions`
  when that project is selected. Remove the old portfolio/attachment heading.
- Display the attachable runtime condition `ready` as `running` in the browser.
  The internal lifecycle condition remains `ready`.
- A session with an active agent reporting attention shows `running !waiting`:
  green runtime text and amber `!waiting`. An agent failure can show `!failed`
  in red. The details identify the agent and reason. Waiting takes precedence
  if a session has both waiting and failed agents.
- Avoid routine explanatory messages after detaching; simply return to the
  picker. Operational failures and action outcomes can still explain results.

## Project selection and search

- `P project` opens an exact project selector close in meaning to the list it
  controls. Choose with `j/k` or arrows and apply with Enter. `All projects`
  removes the scope; cancelling the selector does not change the scope.
- `/` replaces the first line inside the list panel with `fuzzy search> query`.
  Search does not add an external row or resize the panel; reserve the list
  area and pad missing matches so its border and commands remain stable.
  Results update while typing. Enter retains the query and restores navigation.
- Search is fuzzy across the existing project/branch and status fields, plus
  the displayed activity labels of active agents. It composes with the exact
  project filter. Do not describe it as a name-only or exact-text search.
- Escape, Ctrl-C, or `q` cancels an active search. Back from an applied search
  clears it before clearing project scope or quitting.

## Outside-session navigation

Each interaction has one command block immediately below the list or content
it controls. Panel-based pages (including every creation step) put commands
outside the panel, without a second generic footer. Agents and Services put
commands after their lists and before selected-item details/previews. Journals
and boot logs put controls after their displayed entries, without empty rows
pushing them to the bottom. The attached terminal retains its prefix popup
and bottom status bar.

Use consistent short action labels. Uppercase letters are distinct shortcuts.

| Context | Key | Action |
|---|---|---|
| Picker | `j/k`, arrows | Select a session |
| Session/project/creation lists | PgUp/PgDn | Move by visible capacity, clamped at ends |
| Session/project/creation lists | Home/End or `gg`/`G` | First/last result |
| Browsing lists/logs | Ctrl+B/F | Previous/next page; terminal keeps its prefix |
| Browsing lists/logs | Ctrl+U/D | Half page up/down |
| Picker | Enter | Enter a running session; start a stopped session |
| Picker | `s` | Confirm stopping the selected session |
| Picker | `P` | Select project |
| Picker | `/` | Fuzzy search |
| Picker | `A` | Agents page |
| Picker | `S` | Project services page |
| Picker | `c` | Create: Project → Branch → Policy |
| Picker | `b`, `p`, `X` | Existing retained-branch, policy, and deletion probes |
| Picker | `?` | Help |
| Outside-session pages | `q`, Esc, Ctrl-C | Back/cancel one interaction |

A shared Bubbles list adapter owns selector filtering, paging, and row
rendering. Domain selection is retained by the app; the adapter derives its
view from it, without a second independently stored cursor. A shared layout
plan supplies sizing. `gg` is a two-key sequence scoped to the current browsing
context; another key cancels it. Input fields receive literal `g`/`G`.

Session ordering is always **waiting → other running → all remaining states**.
Priority applies globally before project grouping. A project may therefore
appear in multiple priority bands; within each band its sessions stay together.
Waiting in one project must never be hidden behind stopped work from another.
This replaces the earlier selectable ordering modes; their shortcuts are removed.
Project scope and fuzzy filtering remain active. Within the same project and
priority, recent interaction then branch/UUID provide deterministic tie-breaking.
Interaction timestamps are explicit fixture data updated on entry and stop,
not inferred agent activity. Lifecycle reordering preserves selected identity.

List capacity is derived from terminal dimensions and used by both rendering
and page navigation. Long lists show visible range, total, and selected
position. Resize preserves selection and adjusts the window to keep it visible.
Creation selectors use available height instead of a fixed five-row window.

Back closes a popup/page before clearing an enclosing search or project scope.
At the idle all-project picker, all three exit keys quit. Journal navigation
has an extra level: clear its find operation, then return to the service page,
then return to the caller. Inspector pages opened from the picker return to the
picker with its session selection preserved.

The prototype currently reserves `q` for navigation in outside-session text
inputs too. Real text-input behavior should be reviewed before production;
inside a terminal, `q` is always ordinary input.

## Inside-session interaction

Entering a session takes over the terminal, as with tmux. It does not leave the
user in the picker with a separate attached mode or a detach action on the
list. The current terminal is a simple simulation headed `TERMINAL SESSION`;
typed commands are echoed, never executed.

A status bar stays on the bottom row. It identifies the in-session context and
can show project, branch, runtime status, and/or UUID in configurable order:
`--session-bar project,branch,status`. Its prefix hint remains visible even
when context fields are omitted; very narrow terminals prioritize the hint.

Ctrl+B opens a popup:

| Popup action | Result |
|---|---|
| `[D]etach` | Return to the picker, retaining the running session |
| `[A]gents` | Open the attached session's Agents page |
| `[S]ervices` | Open the attached session's project services page |

Both cases of D/A/S work. Escape cancels the popup. An unrecognized prefix key
cancels it without sending that key to fake terminal input.

Agents and Services opened here return to the same terminal when closed,
preserving input, contents, and attachment. A journal returns to Services
first. Visiting these pages does not detach the session.

Outside the popup, `q` types normally, Esc stays in the terminal, and Ctrl+C
cancels fake command input without navigating or quitting. Do not advertise
`q | Esc | Ctrl-C` as terminal exit controls. The bottom bar says `Ctrl+B actions`.

## Creating, starting, stopping, and detaching

- Creating establishes a new session identity and branch assignment,
  environment, and runtime; first startup is part of that flow. The reviewed
  order is Project → Branch → Policy.
- **Existing branch:** select an unassigned branch and go directly to Policy.
  The session uses that same branch; no source is requested and no new branch
  is created. Branches already assigned to a session are excluded.
- **Create new branch:** select its source within the chosen project, then
  enter the new branch name before proceeding to Policy. This path creates a branch;
  it is the only path that asks for a source. These two paths were clarified
  on 2026-09-09 and replace the initial source-only Branch step.
- Project, Branch, and Source selectors support `/` fuzzy search with the
  prompt replacing the first line inside the panel, matching the session
  picker. Filtering and no-match results preserve the panel height; commands
  remain below the panel. Enter applies the query; arrows or
  `j/k` navigate results, then Enter selects. While typing, arrows navigate
  and letters remain query text. Back clears active/applied search before
  leaving the step; queries reset between steps. Leaving creation returns
  silently without a “Cancelled” message. New-branch Back follows
  Policy → Branch name → Source → Branch → Project.
- Policy currently offers only the current project configuration. Confirming
  it allocates a random UUID, shows simulated boot logs, and enters the
  terminal. Back unwinds each substep before cancelling; changing project
  clears dependent choices. Name validation rejects invalid or conflicting
  names. The old failure/retry probes remain in the gallery; richer policy
  selection remains open.
- Branch inventories use fixture `main`, session, and retained branches; no
  Git refs or policy configuration are read. A retained branch selected for
  a session leaves the retained-branch list when it is assigned.
- Starting boots an existing stopped runtime. Show a progressively updating
  machine-like boot log, then automatically enter its terminal on completion.
- Leaving the boot viewer returns to the picker while startup continues.
  Completion must not pull the user back into the terminal after leaving it.
- Picker `s` opens a confirmation for the captured session identity; `y`
  confirms and `q` / `Esc` / `Ctrl-C` cancels without changing the session.
  Confirmation rechecks stop preconditions. Show a simulated shutdown log for
  agents, project services, terminal host, and container, then return to the
  picker. Leaving the viewer lets shutdown continue without reopening it.
- Stop ends processes, including the persistent terminal, while preserving
  session identity, branch, policy, and files in the product model. The fake
  implementation changes lifecycle/process fixtures and discards its terminal
  buffer. Later Enter starts a fresh terminal.
- Ordinary Stop requires current observations and no attached terminals. It
  must not close another client's terminal. Stale/unknown observations or
  incompatible lifecycle conditions explain why stopping is unavailable.
- Detach preserves running processes and the fake terminal buffer. Stop does
  not. Agent and project-service processes are not automatically relaunched
  by the current mock boot sequence.

## Agents

- Keep a tree in session details and a dedicated `A agents` page.
- List only observed active agent instances. Omit stopped agents. Unknown
  observations remain unknown rather than claiming there are no agents.
- Distinguish multiple Codex instances within one P session by task labels,
  with independent instance UUIDs, descriptions, reports, and previews.
- Show one primary activity status: `working`, `waiting`, `idle`, or `failed`.
  Missing reports display `unknown`. Do not show redundant process `running`
  beside `last: running`, or a separate process/report status pair on the page.
- The tree shows the reported condition and explanatory context, without a
  `last:` prefix. The page shows instance identity, task, status, context, and
  a recorded conversation preview. `j/k` selects an instance; Page Up/Down
  browses its preview.
- `working` presents the existing `running` agent report; `waiting` presents
  `attention`. Reasons such as permission required should remain visible.
  Separate waiting-for-approval/input/dependency categories were discussed,
  but no new reporting protocol or automatic classification was implemented.
- Current mock reporting still clears unattended reports on attachment and
  suppresses them while attached. Opening Agents through the terminal popup
  can therefore show `unknown` activity despite an active process and preview.

## Project services and logs

- Show only project services in the tree and `S services` page; exclude
  internal P services such as `p-interactive.service`.
- Present systemd-style unit names and active/substate pairs:
  `api.service · active (running)`, `inactive (dead)`, or `failed (failed)`.
- The page shows the selected unit's description, state, session-local listen
  port, and a short recent-journal preview. Ports are not host-published links.
- `j/k` selects a unit. `s` toggles start/stop and changes its help label with
  state; inactive or failed units offer start. `r` restarts. Unknown or
  unavailable actions are marked `[—]` and explain their refusal.
- Actions update only the selected mock project unit and append simulated
  journal entries. They require a running session and current observations.
- Enter opens a full-page journal reader for the selected unit (`J` also
  opens it). Keep a clear distinction between choosing services and scrolling
  logs.

| Journal key | Action |
|---|---|
| `j/k`, arrows | Scroll lines |
| Page Up/Down | Page through entries |
| `gg/G`, Home/End | Beginning/end |
| `h/l`, left/right | Pan long lines |
| `/`, Enter | Case-insensitive text find; this is not fuzzy filtering |
| `n/N` | Next/previous match |
| `f` | Toggle following the newest recorded entries |
| `q`, Esc, Ctrl-C | Clear find, then return to the selected service |

The reader shows line numbers, range, follow state, and match feedback. There
is no live journal stream in the prototype.

## Implementation boundary and remaining work

The next prototype iteration focuses on **creation and policy workflows**
within the selected Resource topology browser. Layout selection is settled
for this iteration; the comparison gallery remains historical reference.
The existing creation/retry and policy probes are the starting material, not
finished workflows. Detailed interaction changes remain to be reviewed.

The checked-in Go application is a disposable Bubble Tea simulation. It does
not run a daemon, Incus, systemd, tmux, real commands, Codex conversations, or
service discovery. State lasts only for the application process. Previews,
boot steps, unit actions, and logs are fixture data. Existing gallery captures
are historical comparison evidence, not screenshots of this reviewed browser.

The following need design/integration work before production claims:

- Real terminal attachment, PTY lifecycle, input forwarding, and potential
  conflicts with nested tmux/prefix controls.
- Agent instance discovery, report attribution, conversation preview sourcing,
  freshness, and behavior while attached. The current authoritative MVP
  observability model retains one latest unattended signal, not an inventory.
- Project-service discovery, permissions, systemd control, and journal access.
  The current MVP design does not orchestrate project services. The accepted
  UI exploration does not by itself settle these product/API boundaries.
- Complete creation, policy, retained-branch, and destructive-action flows in
  the new browser; production RPC integration remains unimplemented.
- Broader manual terminal/viewport review, including long identities, many
  agents/units, live arrivals/removals, and failure/recovery transitions.

Existing and added Go tests cover layout bounds, selection windows, filtering,
terminal navigation, prefix popup return paths, boot/cancellation races,
stop/start semantics, agent reporting/previews, service actions, and journal
paging/find. These checks validate the simulation, not real-machine behavior.

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
