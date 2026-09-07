# Prototype evidence record

Date: 2026-09-06

## Owner and empirical question

The governing user owns the deferred TUI interaction decision identified by
`PROJECT.md`, `docs/mvp-status.md`, and `docs/technology-stack.md`.

Question: which top-level TUI organization best lets one developer govern
concurrent cross-project sessions while preserving lifecycle, confirmed
attachment presence, latest unattended agent signal, policy condition, and
daemon-provided actions?

The six candidates are a fleet table, project navigator, attention workspace,
search-first command center, single-session focus deck, and actionability
lanes. This artifact supplies evidence; it does not own or record the product
decision.

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

Use `1`–`6` or `Tab` to switch candidates. `?` shows the action map. The
deterministic evidence frames can be regenerated with:

```sh
go build -o .cache/bin/p-tui-probe .
sh capture.sh
```

The artifact is disposable but intentionally preserved for inspection. It is
not staged, committed, promoted, or connected to production behavior.

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
- nine generated sessions across five projects, three generated retained
  branches, and local in-memory state only

The fixture covers `creating`, `starting`, `ready`, `stopped`, `missing`,
`unreachable`, `discarding`, and `deleting`; confirmed attachment and
unattended states; `running`, `attention`, `idle`, `failed`, `unknown`,
and empty agent signals; and `current`, `outdated`, and `invalid` policy.

## Cases exercised

- all six candidates at 120×35 and 80×24;
- long project/branch names and dense cross-project state;
- fuzzy search over project, branch, lifecycle, presence, agent, and policy;
- attach, client switch, detach, and first-confirmed-entry signal clearing;
- failed Create, exact Retry, and **Try again with changes** replacement;
- retained branches as Git resources rather than sessions;
- typed outdated-policy comparison;
- aggregate project-deletion preview;
- partial ensure-absent progress and successful retry; and
- real Bubble Tea event-loop launch/input/clean exit for every initial variant.

## Direct observations

1. All six final variants launched in pseudo-terminals, accepted `q`, and
   exited with status 0.
2. `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...`
   passed. The tests check fixture coverage, both target sizes, maximum width
   and height, absence of clipped primary frames, independent fact visibility,
   search, keyboard routing, attach semantics, retry identity, replacement
   identity, and monotonic deletion retry.
3. Thirty final text frames are preserved under `evidence/frames`: two sizes
   for six overview candidates and two sizes for nine material interaction
   states. None contains the prototype's clipping marker.
4. The first rendering pass exposed accidental wide-column wrapping and
   narrow-screen loss of action help. Deliberate compact rows, vertical fact
   inspectors, and narrow layouts removed those failures in the preserved
   frames.
5. A confirmed switch decremented only this fixture client's prior attachment,
   incremented the target, cleared the target's unattended signal on the
   zero-to-one transition, and explicitly reported that persistent hosts
   remained alive. Detach left the session `ready`.
6. Exact Retry retained UUID `s-new-019` and operation `op-create-77`.
   **Try again with changes** presented UUID `s-new-020` and operation
   `op-create-78` as a superseding creation.
7. Project deletion first exposed `deleted`, `remaining`, and `unreachable`
   targets. Retry moved every fixture-owned target toward `deleted` without
   reconstructing prior targets.
8. Fuzzy search ranked the confirmed attached session first for `attached`,
   but also admitted a weaker fuzzy candidate. Search ranking is useful for
   navigation but is not exact filtering evidence.
9. The attention and lane labels had to be changed from decision-like wording
   to “Urgent to inspect” and “Inspect first.” The underlying agent field is a
   lossy latest unattended signal, not proof of an unresolved request.

## Candidate interpretation

| Candidate | Strongest observed property | Material observed cost |
|---|---|---|
| Fleet table | Highest simultaneous cross-project fact density; works at both target sizes with an inspector | Dense rows require truncating long branch names |
| Project navigator | Clearest project context and smallest per-project working set | Other projects expose counts rather than their session details |
| Attention workspace | Fastest route to urgent unattended, failed, invalid, missing, and unreachable facts | A derived urgency order can be mistaken for durable task state unless wording stays precise |
| Command center | Fast known-target lookup and action dispatch | Fuzzy results can include weak matches and passive overview is secondary |
| Focus deck | Clearest comprehension of one session's identity, facts, and available actions | Lowest simultaneous overview density; relies on the radar for context |
| Actionability lanes | Makes intervention categories spatially obvious while retaining raw conditions | Introduces derived categories, heavily truncates names at 80 columns, and resembles the generic cockpit/board pattern P does not want to lead with |

## Interpretation and result

Result: **inconclusive** for the user-owned preference question until the
governing user drives the candidates. The artifact successfully discriminates
the alternatives and supports a narrower recommendation for that review:

- evaluate the fleet table first as the default overview candidate;
- evaluate attention ordering and project grouping as alternate views or
  facets rather than separate sources of truth;
- evaluate command search as a global overlay/action path;
- reuse the focus deck's explicit fact treatment for a detail surface; and
- treat lanes as a control candidate, not the presumptive direction.

That recommendation follows the documented priority on a cross-project view of
concurrent streams and the requirement that P not lead as a generic Kanban
cockpit. It is not a substitute for the governing user's observable response.

## Limitations

- Fixture RPC-shaped data replaces a real daemon and real latency, refresh,
  partial response, and concurrent-update behavior.
- Attach uses in-memory state; no Incus, tmux, terminal handoff, or attachment
  helper is exercised.
- Captured text frames preserve content and geometry but not interactive color
  perception. The live program supplies adaptive color.
- Only 120×35 and 80×24 are decision surfaces. Widths below 60 are clamped and
  are not supported evidence.
- No external user study, behavioral comparison, accessibility audit, or
  production performance benchmark was performed.

## Mutation, cleanup, and handback

Every mutation is under `.prototype/tui-options`:

- environment: `.envrc`, `flake.nix`, `flake.lock`;
- Go module: `go.mod`, `go.sum`;
- artifact source: `main.go`, `app.go`, `view.go`, `app_test.go`;
- use and capture instructions: `README.md`, `capture.sh`, `.gitignore`;
- evidence: `evidence/observations.md` and `evidence/frames/*.txt`; and
- ignored disposable material: `.cache/**`, including module/build caches and
  `.cache/bin/p-tui-probe`.

No external resources require cleanup. The repository index and project design
documents were not changed. Preserve the artifact until the governing user has
completed comparison; deletion, staging, committing, or promotion requires a
separate decision.

After user observation, a design workflow should record the chosen interaction
contract in the implementation authority or assign a dedicated TUI authority
through `PROJECT.md` before production implementation.
