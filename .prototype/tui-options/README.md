# P TUI interaction options

Disposable, fixture-backed Bubble Tea prototypes for the interaction decision
deferred by `PROJECT.md`. Nothing here connects to a daemon, Git repository,
Incus, tmux, credentials, or real project/session data.

## Enter the prototype environment

```sh
cd .prototype/tui-options
direnv allow
```

`direnv allow` is intentionally left to the developer. Without direnv:

```sh
nix develop path:.
```

## Run

```sh
go run .
go run . --variant navigator
go run . --variant attention
go run . --variant command
go run . --variant focus
go run . --variant lanes
go run . --variant outline
go run . --variant matrix
go run . --variant operations
go run . --variant workspace
go run . --variant minimal
go run . --variant cards
go run . --variant attachment
go run . --variant governance
go run . --variant compare
go run . --variant topology
go run . --variant actions
```

Interface exploration and data stress are separate experiences:

```sh
go run . --mode explore
go run . --mode explore --list-datasets
go run . --mode stress --list-datasets
go run . --mode stress --dataset baseline
go run . --mode stress --dataset churn --stress-step 3
```

The active mode and fixture are always shown at the start of the header. Stress
captures are stored separately under `evidence/stress` and generated with
`sh capture-stress.sh`.

Use `1`–`6` for the original shortcuts, `Tab` to cycle, or `v` to open the
scalable variant gallery. Press `?` for the complete key map.

The alternatives deliberately organize the same facts differently:

1. **Fleet table** optimizes cross-project density and comparison.
2. **Project navigator** makes project hierarchy the primary navigation.
3. **Attention workspace** separates decisions from steady work.
4. **Command center** makes fuzzy search and action dispatch primary.
5. **Focus deck** emphasizes one stream while retaining a compact radar.
6. **Actionability lanes** groups streams by the kind of intervention needed.
7. **Expandable outline** places projects and sessions in one collapsible tree.
8. **Project status matrix** compares intervention load across the whole portfolio.
9. **Operations console** makes activity history and bounded recovery primary.
10. **Task workspace** separates sessions, activity, policy, and resources into stable modes.
11. **Minimal stream ledger** removes panel chrome and expands the current row inline.
12. **Responsive card grid** reflows complete session cards across one, two, or three columns.
13. **Attachment dock** centers the current client attachment and explicit host-preserving switches.
14. **Governance dashboard** continuously answers attention, attachment, change, and risk questions.
15. **Session comparison** pins one stable identity and exposes a four-fact A/B delta.
16. **Resource topology** maps project, ref, session, host, client, agent, and policy lifetimes.
17. **Action eligibility sheet** keeps unavailable actions visible with fact-based reasons.

Deterministic frames can be generated without an interactive terminal:

```sh
go run . --snapshot --variant table --width 120 --height 35
go run . --snapshot --dataset dense --variant table --width 60 --height 20
go run . --snapshot --dataset empty --variant table --width 48 --height 16
go run . --snapshot --scenario create-failed --width 80 --height 24
go run . --snapshot --scenario attached-switch --width 120 --height 35
go run . --snapshot --scenario delete-complete --width 80 --height 24
```

The responsive evidence matrix covers 160×50, 132×40, 120×35, 100×30,
80×24, 60×20, 48×16, and 40×12. At 48×16 the selected stream keeps all four
independent status facts and its actions. Below that, the prototype explicitly
reports the unsupported size instead of pretending a clipped frame is usable.
Fixtures cover `standard`, `dense`, `empty`, `single`, and `long` datasets.

Regenerate the complete evidence set after building the binary:

```sh
go build -o .cache/bin/p-tui-probe .
sh capture.sh
sh capture-stress.sh
sh capture-churn.sh
```

## Comparison task

For each variant, identify:

1. which stream most urgently needs attention;
2. whether that stream is attached and whether its policy is current;
3. which lifecycle action is available;
4. what changes when attaching, switching, and detaching; and
5. whether creation retry, replacement creation, retained branches, policy
   drift, and project deletion preserve the meanings described by the project
   design.

This artifact is not a production candidate and owns no product decision.
The full validation results, tradeoffs, and suggested review order are in
[`evidence/observations.md`](evidence/observations.md).
