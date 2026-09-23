# Session browser iteration history

This is historical context for the disposable prototype. Start new UI work
from [DECISIONS.md](DECISIONS.md) and the [current run guide](README.md).
Earlier findings describe their own checkpoints, not the current interface
or production support.

## Comparison gallery

The first exploration compared information architectures before a preferred
layout was selected. It grew from seventeen responsive candidates to eighteen
with the data-stress round. Table, navigator, attention, attachment, topology,
and other layouts explored different ways to present the same session facts.

Preserved records:

- [Initial observations](evidence/observations.md), dated 2026-09-06, cover the
  responsive exploration through commit `cbd3c8d`, candidate tradeoffs, and
  interaction probes.
- [Stress observations](evidence/stress/observations.md) cover the expanded
  gallery, dense and changing data, and remaining limitations.
- [Responsive frames](evidence/expanded/), [interaction frames](evidence/frames/),
  and [stress evidence](evidence/stress/) preserve the recorded renderings.

The initial preference result was inconclusive before hands-on user review.
That historical conclusion does not mean layout selection is still pending.
The gallery remains accessible with `go run . --gallery`; old commands in
the evidence records describe their original checkpoint. Current gallery
commands are in the [run guide](README.md#retained-comparison-gallery-and-stress-fixtures).

## Resource topology refinement — 2026-09-08

Hands-on review selected Resource topology and refined it into the default
session browser. Commit `dbef509` records this completed iteration and its
[decision record](DECISIONS.md).

The main changes from the comparison phase were:

- A focused browser without gallery chrome, with approximately 65% of the
  wide layout devoted to a one-line-per-session list.
- Project and branch labels, nearby project selection, and fuzzy search below
  the list; runtime colors and `!waiting` identify sessions to inspect.
- Separate picker and terminal contexts, simulated boot before entry, and a
  Ctrl+B popup for detach, Agents, and Services.
- Active agent trees and conversation previews, plus project service units,
  start/stop/restart actions, and a navigable journal.
- Consistent back/cancel behavior outside the terminal and silent detach.

The detailed controls, accepted behavior, and remaining questions live in
DECISIONS.md rather than this historical summary. The gallery captures were
not regenerated as evidence of this browser.

## Continuing from this checkpoint

The next iteration focuses on **creation and policy workflows** within the
selected browser. Start with the existing `c` creation/retry and `p` policy
probes, preserving the reviewed picker and terminal navigation. The next
review will determine the detailed improvements; it does not reopen the
layout comparison.

Launch `go run .` to resume the reviewed browser. Consult DECISIONS.md before
changing interactions and update it when another decision is accepted. Keep
earlier evidence linked here so it is possible to understand previous choices
without restarting the comparison exercise.

Real terminal attachment, agent inventory/report integration, project service
control, and complete lifecycle workflows remain open. The prototype uses
simulated data; [PROJECT.md](../../PROJECT.md) and its subject authorities
continue to govern product behavior and MVP scope.
