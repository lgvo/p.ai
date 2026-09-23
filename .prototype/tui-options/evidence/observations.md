# Prototype evidence record

> Checkpoint scope: this file records the first responsive exploration through
> commit `cbd3c8d`. The separate data-stress round, later 18th candidate, and
> current totals are recorded in `stress/observations.md`.

Date: 2026-09-06

## Owner, question, and contract

The governing user owns the deferred TUI interaction decision identified by
`PROJECT.md`, `docs/mvp-status.md`, and `docs/technology-stack.md`.

Empirical question: which information architecture and responsive behavior
best lets one developer govern concurrent cross-project sessions while keeping
lifecycle, confirmed attachment presence, latest unattended agent signal,
policy condition, and daemon-provided actions distinct?

The confirmed exploration contract covered:

- seventeen structurally distinct interfaces;
- 160×50, 132×40, 120×35, 100×30, 80×24, 60×20, 48×16, and 40×12;
- standard, dense (40 sessions), empty, single-session, and long-name fixtures;
- complete selected-session facts and actions at supported sizes;
- progressive disclosure at 60×20 and 48×16; and
- an explicit unsupported-size response below 48×16 rather than a fabricated
  or clipped interface.

All state is generated in memory. Nothing connects to a daemon, repository,
Incus, tmux, credentials, or real project/session data. The artifact supplies
evidence; it does not own or record the product decision.

## Artifact and exact run path

Artifact: `.prototype/tui-options`

With developer-controlled direnv trust:

```sh
cd /home/lgvo/p.ai/.prototype/tui-options
direnv allow
go run .
```

Without direnv:

```sh
cd /home/lgvo/p.ai/.prototype/tui-options
nix develop path:.
go run .
```

Use `1`–`6` for the original shortcuts, `Tab` to cycle all candidates, or `v`
for the scalable gallery. The deterministic evidence can be regenerated with:

```sh
go build -o .cache/bin/p-tui-probe .
sh capture.sh
```

## Environment and inputs

- Linux `amd64`
- Nix 2.34.8
- locked nixpkgs revision `c043004d1c6985732bcc1cbc5a9c9aecbbb4e0f0`
- Go 1.26.7
- Bubble Tea 1.3.10
- Bubbles 1.0.0
- Lip Gloss 1.1.0
- `sahilm/fuzzy` 0.1.3
- xterm-256color terminfo and tmux 3.7c available
- nine-session standard fixture, forty-session dense fixture, empty fixture,
  single-session fixture, long-name fixture, and three retained branches

The fixtures cover `creating`, `starting`, `ready`, `stopped`, `missing`,
`unreachable`, `discarding`, and `deleting`; confirmed attachment and
unattended states; `running`, `attention`, `idle`, `failed`, `unknown`, and
empty agent signals; and `current`, `outdated`, and `invalid` policy.

## Evidence and validation

- 152 responsive overview frames are preserved under `evidence/expanded`:
  every candidate at all eight sizes on the standard fixture, plus four sizes
  for each non-standard table fixture.
- 52 interaction and compatibility frames are preserved under
  `evidence/frames`: two ordinary sizes for all candidates and nine material
  lifecycle scenarios.
- The automated cross-product renders every one of the 17 candidates against
  all five datasets at all eight viewport classes: 680 combinations.
- Unit tests, race-enabled tests, `go vet`, deterministic capture, and real
  Bubble Tea pseudo-terminal launch/input/clean-exit checks pass.
- Direnv resolves the repository-local `.envrc`, which delegates to the locked
  Nix shell; no host Go installation is required.

The tests assert fixture coverage, maximum width and height, truthful minimum
size behavior, absence of clipped supported frames, selected fact visibility,
fuzzy search, gallery and keyboard routing, outline collapse, workspace modes,
responsive card breakpoints, attachment switching, comparison pinning,
recovery plans, action eligibility, retry identity, replacement identity, and
monotonic deletion retry.

## Direct observations

1. The responsive shell is **supported**. At 48×16 it preserves identity,
   lifecycle, presence, agent, policy, and actions. At 40×12 it explicitly
   reports the minimum instead of rendering an oversized virtual frame.
2. Dataset generality is **supported** for rendering safety. The final audit
   initially found dense attention/lanes overflow, a premature 100-column
   focus split, and long-name inspector expansion; bounded windows, later
   split thresholds, and explicit truncation removed all cross-product
   failures.
3. Attachment semantics are **supported** in the fixture: switching decrements
   only this client's prior attachment, increments the target, clears an
   unattended signal only on the zero-to-one confirmed-entry transition, and
   leaves both persistent hosts alive. Detach leaves the session `ready`.
4. Retry identity is **supported**: Exact Retry retains UUID `s-new-019` and
   operation `op-create-77`; **Try again with changes** creates UUID
   `s-new-020` and operation `op-create-78` as a superseding request.
5. Monotonic deletion is **supported**: partial `deleted`, `remaining`, and
   `unreachable` targets converge only toward confirmed absence on retry.
6. Fuzzy search is useful but **partly refuted as an exact filter**: it ranks
   the confirmed attached session first for `attached` while admitting weaker
   fuzzy matches. Search is navigation evidence, not exact query evidence.
7. Attention wording remains a risk. “Urgent to inspect” and “Inspect first”
   are accurate; decision-like labels are refuted because the latest
   unattended agent signal is lossy and not proof of an unresolved request.
8. Card reflow is **supported**, but uniform visual rhythm is refuted when
   operation-rich cards grow taller. That visible cost is intentionally
   preserved for comparison.

## Candidate interpretation

| Candidate | Strongest observed property | Material observed cost |
|---|---|---|
| Fleet table | Highest simultaneous fact density | Long identities must truncate |
| Project navigator | Clearest project context | Cross-project detail becomes counts |
| Attention workspace | Fast route to degraded and unattended signals | Derived urgency can look more authoritative than it is |
| Command center | Fast known-target lookup and dispatch | Passive overview is secondary; fuzzy matches are inexact |
| Focus deck | Best single-session comprehension | Lowest simultaneous overview density |
| Actionability lanes | Intervention categories are spatially obvious | Derived categories and board-like framing |
| Expandable outline | Projects and sessions share one navigable hierarchy | Collapsed groups conceal detail by design |
| Project status matrix | Portfolio imbalance is immediately comparable | Individual identity is compressed into cells |
| Operations console | Progress, failure, and recovery are chronological | Stable sessions receive less emphasis |
| Task workspace | Sessions, activity, policy, and resources stay separated | Mode switching hides non-active domains |
| Minimal stream ledger | Fast reading with almost no chrome | Sparse structure offers fewer orientation cues |
| Responsive card grid | Complete facts travel together across breakpoints | Variable card height weakens grid rhythm |
| Attachment dock | Client location and host-preserving switches are explicit | Other governance questions become secondary |
| Governance dashboard | Directly answers attention, attachment, change, and risk | Summaries depend on carefully named derived counts |
| Session comparison | Removes memory burden from pairwise fact comparison | Requires pinning and managing a second selection role |
| Resource topology | Makes resource ownership and lifetime boundaries explicit | Lower operational density; best as explanation/inspection |
| Action eligibility sheet | Explains both available and unavailable actions | Inventory scanning is deliberately subordinate |

## Interpretation and result

Result: **inconclusive** for the user-owned preference question until the
governing user drives the candidates. The technical feasibility and responsive
safety claims above are supported.

A productive review order is:

1. start with **Governance dashboard**, **Fleet table**, and **Minimal ledger**
   as competing default-overview philosophies;
2. compare **Project navigator**, **Expandable outline**, and **Project status
   matrix** for project-oriented work;
3. compare **Attention workspace** and **Operations console** for interruption
   and recovery work;
4. inspect **Attachment dock**, **Session comparison**, **Resource topology**,
   and **Action eligibility sheet** as specialized secondary surfaces; and
5. treat **Command center**, **Focus deck**, **Task workspace**, **Cards**, and
   **Lanes** as interaction controls or composable patterns.

The exploration loop stopped after candidate 17 because remaining obvious
ideas were rearrangements of tested models: another Kanban/pipeline duplicates
lanes and matrix; another picker duplicates command search and the gallery;
another master-detail layout duplicates table, navigator, focus, or cards.

## Limitations

- Fixture RPC-shaped data replaces real daemon latency, refresh, partial
  response, and concurrent-update behavior.
- Attach uses in-memory state; no Incus, tmux, terminal handoff, or attachment
  helper is exercised.
- Text frames preserve content and geometry but not interactive color
  perception. The live program supplies adaptive color.
- No external user study, accessibility audit, screen-reader evaluation, or
  production performance benchmark was performed.
- The 40×12 response proves truthful degradation, not application usability.

## Mutation, cleanup, and handback

Every mutation is under `.prototype/tui-options`. The branch
`prototype/initial-tui-options` preserves a separate commit for the responsive
foundation and every subsequent exploration. Production design documents and
runtime code were not changed, and nothing was pushed or published.

Ignored `.cache/**` material is disposable. Preserve the committed artifact
until the governing user completes comparison. After observation, record the
chosen interaction contract in the appropriate implementation authority (or
assign a dedicated TUI authority through `PROJECT.md`) before production work.
