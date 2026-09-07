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
```

Use `1`–`6` or `Tab` to switch between the structurally distinct
comparison surfaces. Press `?` for the complete key map.

The alternatives deliberately organize the same facts differently:

1. **Fleet table** optimizes cross-project density and comparison.
2. **Project navigator** makes project hierarchy the primary navigation.
3. **Attention workspace** separates decisions from steady work.
4. **Command center** makes fuzzy search and action dispatch primary.
5. **Focus deck** emphasizes one stream while retaining a compact radar.
6. **Actionability lanes** groups streams by the kind of intervention needed.

Deterministic frames can be generated without an interactive terminal:

```sh
go run . --snapshot --variant table --width 120 --height 35
go run . --snapshot --scenario create-failed --width 80 --height 24
go run . --snapshot --scenario attached-switch --width 120 --height 35
go run . --snapshot --scenario delete-complete --width 80 --height 24
```

Regenerate the complete evidence set after building the binary:

```sh
go build -o .cache/bin/p-tui-probe .
sh capture.sh
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
