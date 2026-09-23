# Data-stress evidence record

Date: 2026-09-06

## Question and boundary

Empirical question: which TUI organizing models remain readable, truthful, and
navigable when realistic pathological data replaces the curated comparison
fixture?

This round is deliberately independent from the ordinary exploration path:

```sh
go run . --mode explore --dataset standard
go run . --mode stress --list-datasets
go run . --mode stress --dataset massive --variant table
go run . --mode stress --dataset churn --stress-step 3 --variant navigator
```

Every value remains deterministic in-memory fixture data. No daemon,
repository, Incus host, tmux server, credential, or external project state is
read or mutated.

## Stress families

| Dataset | Material pressure |
|---|---|
| `baseline` | Curated control rendered through the separate stress harness |
| `massive` | 1,000 sessions across 25 projects |
| `many-projects` | 180 projects with one session each |
| `skewed` | 300 sessions, 299 carrying the same attention signal |
| `unicode` | Wide CJK, Arabic, Hebrew, combining marks, emoji, and natural RTL text |
| `hostile-text` | ANSI/OSC, control bytes, bidi controls, and an oversized diagnostic |
| `ambiguous-identities` | Near-identical project and branch labels with stable UUIDs |
| `high-attachments` | Presence counts from zero through 9,999 per session |
| `partial-observations` | Missing presence responses and facts that must not be inferred |
| `stale-observations` | Complete observations outside the trusted freshness window |
| `mixed-observations` | Nine current, nine partial, and nine stale observations together |
| `churn` | Step-zero control for a five-revision live-data sequence |

The churn sequence separately exercises reorder, insertion, fact update,
selected-session removal, and an empty terminal state. It is addressable with
`--stress-step 0..4`.

## Evidence and validation

- 1,224 stress frames are committed: 12 datasets × 18 candidates × four
  target viewports (864), plus five churn revisions × 18 candidates × four
  viewports (360).
- Target viewports are 120×35, 80×24, 60×20, and 48×16.
- The automated stress cross-product renders every dataset, candidate, and
  target viewport and checks cell width, line height, and absence of the
  clipping fallback.
- Focused tests additionally cover scale shape, unsafe-text neutralization,
  Unicode cell width, stable UUID visibility, client attachment arithmetic,
  unknown-presence semantics, stale/partial mutation refusal, churn selection,
  attachment survival, and observation-quality counts.
- Unit tests, race-enabled tests, `go vet`, deterministic regeneration, and
  real Bubble Tea pseudo-terminal launch/input/clean-exit probes pass in the
  locked Nix/direnv shell with Go 1.26.7.

Regeneration is self-contained and rebuilds its binary first:

```sh
sh capture-stress.sh
sh capture-churn.sh
```

## Supported findings

1. **Bounded responsive rendering is supported.** All 18 candidates render all
   12 stress datasets without horizontal overflow or the clipping fallback at
   every target size. Large collections use visible windows and explicit
   remainder counts rather than unbounded rows.
2. **Terminal-cell-aware text handling is supported.** Wide glyphs and
   combining sequences preserve frame geometry. Natural RTL scripts remain
   visible, while terminal controls, ANSI/OSC introducers, and bidi control
   characters are neutralized into bounded visible tokens at the fixture
   boundary.
3. **Stable identity is supported.** The selected UUID remains visible even
   when project and branch labels truncate to nearly identical strings.
4. **Presence remains a count.** Four-digit per-session values and an aggregate
   of 40,056 render without turning presence into a Boolean. Switching the
   prototype client only changes that client's two affected counts.
5. **Unknown is not unattended.** A missing presence observation renders
   `unknown`; the unattended agent signal becomes `not evaluated`, and attach
   or detach is not authorized from partial or stale evidence.
6. **Churn selection is identity-based.** Reorder, insertion, and update retain
   `s-churn-focus`; its removal chooses the deterministic surviving neighbor
   `s-churn-neighbor` and explains the fallback; the final empty revision
   clears both selection and vanished client attachment.
7. **Observation quality can be a primary navigation model.** The 18th
   candidate makes current/partial/stale counts and safe refresh actions
   visible together, including at 48×16 through compact disclosure.

## Refuted assumptions

1. **Rune or byte count is not display width.** The original padding approach
   failed under wide and combining text; terminal-cell-aware truncation and
   padding were required.
2. **Labels alone are not sufficient identity.** Ambiguous project and branch
   names become indistinguishable after honest truncation; selected UUIDs are
   required across every organizing model.
3. **Zero attachments does not always mean unattended.** It is false when the
   presence response is incomplete, so neither unattended-agent evaluation nor
   attachment eligibility may be derived from that zero value.
4. **Position is not selection identity.** Keeping an integer row index across
   refresh selects a different object after reorder or insertion.
5. **Showing every object is not a viable large-fleet strategy.** The 1,000-
   session and 180-project fixtures require bounded windows, counts, search, or
   hierarchy.
6. **Derived summaries are not self-qualifying.** Attention, lanes, and the
   governance dashboard remain readable with stale data, but their aggregate
   labels can look current while only the selected evidence carries a freshness
   warning. They should not be treated as authoritative defaults unless a
   production design adds fleet-level observation quality.

## Candidate interpretation under stress

| Candidate | What survives | Stress cost exposed |
|---|---|---|
| Fleet table | Dense scan, explicit remainder, complete selection | A 1,000-row fleet becomes a small window |
| Project navigator | Project context and suffix-preserving labels | 180 projects still imply long sequential navigation |
| Attention workspace | A 299-item skew is immediately obvious | Derived urgency needs fleet-level freshness qualification |
| Command center | Known-target search plus selected UUID | Fuzzy ranking is not exact disambiguation by itself |
| Focus deck | Maximum selected-session comprehension | Almost no simultaneous fleet context |
| Actionability lanes | Distribution across intervention classes | Partial facts can make lane names sound too actionable |
| Expandable outline | Bounded project/session hierarchy | Collapsed or distant groups remain intentionally hidden |
| Project status matrix | Strong portfolio imbalance and large counts | Identity and per-session evidence compress into cells |
| Operations console | Bounded long diagnostics and recovery context | Stable sessions and data freshness are secondary |
| Task workspace | Separates sessions, activity, policy, resources | Non-active modes remain hidden during rapid churn |
| Minimal stream ledger | Hostile text remains legible with little chrome | Sparse orientation and no aggregate model |
| Responsive card grid | Complete facts stay together at each breakpoint | Cards are inefficient for hundreds of sessions |
| Attachment dock | High counts and client/host lifetime boundary | Other governance questions are secondary |
| Governance dashboard | Best compact cross-fleet summary | Stale aggregate counts can appear current |
| Session comparison | UUID-pinned pairwise changes | Anchor disappearance requires explicit fallback semantics |
| Resource topology | Unknown presence and resource lifetimes stay distinct | Highest vertical cost at 80×24 |
| Action eligibility sheet | Strongest explanation for partial/stale refusal | Weak fleet scanning by design |
| Observation integrity | Makes data confidence explicit before action | Lifecycle and project workflow become secondary |

## Inconclusive findings

- The best default interface remains a governing-user preference decision; the
  fixtures establish feasibility and reveal costs, not desirability.
- Real daemon latency, burst coalescing, input during refresh, and sustained
  event throughput are not measured by deterministic snapshots.
- Natural RTL display is geometry-safe in captured text, but bidirectional
  cursor interaction and screen-reader behavior remain untested.
- Text evidence cannot determine color perception, keyboard comfort over long
  sessions, or whether specialized views should be separate screens or
  composable overlays.

## Review result and stopping rule

The stress review added one nonredundant candidate—Observation integrity—after
partial/stale fixtures exposed a confidence-axis gap. The loop stops at 18:
another table, tree, board, picker, detail pane, aggregate dashboard, action
sheet, topology, or timeline would repeat a tested organizing model. Further
useful work now requires the governing user to operate the candidates or a
real daemon/event contract; adding more fixture-only arrangements would not
resolve the remaining preference and runtime questions.

Suggested review order:

1. Compare Fleet table, Governance dashboard, Minimal ledger, and Observation
   integrity as competing defaults.
2. Compare Navigator, Outline, and Matrix for project-oriented scanning.
3. Compare Attention, Operations, and Lanes for interruption/recovery work.
4. Inspect Attachment, Comparison, Topology, and Action eligibility as
   specialized secondary surfaces.
5. Use Command, Focus, Workspace, and Cards as composable interaction patterns.

All changes remain under `.prototype/tui-options` on
`prototype/initial-tui-options`; nothing was pushed or published.
