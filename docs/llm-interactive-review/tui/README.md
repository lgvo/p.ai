# TUI interactive LLM review evidence

These records distinguish LLM navigation from scripted integration acceptance.
The LLM read rendered frames, chose the next visible action, and inspected the
result before continuing. `actions.ndjson` records those choices. Selected PNGs
retain terminal coordinates, colors, selection and cursor. Complete cell JSON,
text and raw ANSI remain in ignored `.cache/p-vm/tui-review-evidence/`.
The serial-console correction record also archives those artifacts beside its
screenshots to make the original transport defect directly inspectable.
There are no developer credentials or authenticated agent runs in this evidence.

The screenshots and action records in this directory come from explicit
interactive LLM reviews. Automated integration tests live separately in
[tests/integration](../../../tests/integration/). Checkpoints here also retain
supporting automated test results; those results are distinct from the LLM
experience review. Successful automated runs currently do not retain a
screenshot report.

The initial provisioned-sample delivery passed its final `just test` checkpoint
on 2026-10-01: all 58 scenarios ran exactly once across three serial VMs, which
powered off and removed their disposable disks. The
[checkpoint audit](delivery-checkpoint.json) records exact counts and hashes;
[implementation progress](../../implementation-progress.md#final-delivery-checkpoint--2026-10-01)
records acceptance and retained limitations. The real devShell extension and
authenticated Codex acceptance remain separate pending milestones.

## VM serial-console redraw correction

The [before/after review](serial-console/README.md) reproduces the reported
UUID/service-text leakage in the unchanged public VM and reviews the corrected
`just lab-public` at 120×35, 80×24 and 48×16. Real service inventory/journals,
application health, entry/detach/re-entry, and mock error feedback passed.
The production console now preserves guest cursor movements; the observer no
longer changes output modes to conceal serial translation. All 29 automated
TUI checks passed separately. The persistent lab was preserved and shut down.

## Creation finishes with Enter

The [creation Enter checkpoint](creation-enter/checkpoint.json) records the
2026-10-01 control change. The LLM inspected the current prototype and production
before editing: both used creation `[y/N]`, and production Enter returned to
branch choices. The user's Enter direction supersedes that prototype control;
prototype code was preserved.

The changed production review keeps policy, request key, source commit and
origin visible, with Enter to create and Back to edit. Adaptive captures cover
[local project review](creation-enter/after-0005-local-project-review-120.png),
80×24 and compact scrolling, Back, actual local tmux entry,
[retained branch review](creation-enter/after-0019-retained-review.png), and
[captured origin review](creation-enter/after-0026-origin-source-review.png)
followed by Enter attachment. The
[Stop review](creation-enter/after-0015-stop-still-default-no.png) still defaults
to No; Enter visibly declines it. No affected experience defects remained.
The [action record](creation-enter/actions.ndjson) retains LLM choices separately
from the passing 27-test TUI gate. These are mock/socket and local transport
results; unchanged backend behavior received no new native VM acceptance claim.

The [independent fresh-context review](creation-enter/reviewer/checkpoint.json)
accepted the same build with zero findings. Its adaptive new-branch Back/edit,
local-project and retained-branch Enter paths reached the actual local terminal;
compact source scrolling and Stop Enter decline also passed. Four representative
PNG frames, the complete chosen-action record and a read-only fixture effect
record are retained separately under `creation-enter/reviewer/`.

## Persistent observation helper

Use `just tui-tests` for protocol, real-PTY, and mock-socket production TUI
regressions. It supplies pinned Go, tmux, Python, pyte, Pillow and a monospace
font through `dev/tui-test-shell.nix`. `just tui-mock` provides the disposable
socket-backed exploration environment. Its adaptive review evidence is in
[mock/checkpoint.json](mock/checkpoint.json); those captures establish mock
navigation and local terminal transport, separately from the native delivery
checkpoint and prototype visual acceptance.
Use that shell for the observer too:

```sh
nix-shell dev/tui-test-shell.nix
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock start \
  --out .cache/p-vm/tui-review-evidence/review --cols 120 --rows 35 -- p tui
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock observe --label picker
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock send --text P --label projects
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock send --hex 1b --label back
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock resize --cols 80 --rows 24
python3 tests/integration/tui-observe.py --socket /tmp/p-review.sock close
```

Use `start --background light` to emulate a light terminal; the default is
`dark`. The observer replies to background queries consistently with its PNG
background and preserves six-digit RGB colors, including numeric-only hex
values. `just tui-mock --theme light` or `--theme dark` also selects an explicit
production palette; omitting that argument exercises automatic detection.

The socket has mode 0600. The server remains active between invocations and
continuously services terminal queries. `--hex 0d` sends Enter; `--hex 0264`
sends the actual tmux detach sequence. `--wait` captures a settling interval
(at most five seconds), during which query replies still run. A helper is
transport, not an automatic navigation scenario.

The child command uses a stable `runtime-tmp/` directory inside the chosen
output directory. A detached QEMU must not retain nix-shell's temporary
`TMPDIR`: `nix-shell --run` removes it when the invoking command returns and
can delete a running VM's `store.img`. This protection also makes a one-shot
shell invocation safe for starting observation; keep the output directory
until its command has finished.

For a serial VM console, resize both the observer and the guest tty: return to
the guest shell, use `stty cols 80 rows 24`, and reopen `p tui`. QEMU does not
propagate the outer terminal's resize to a guest serial tty. Follow the serial
VM lock/slot coordination before starting any VM; preserve other lab disks.
Safely power off with `p-demo-poweroff` and confirm process exit before closing
the observer. Closing a still-running observer terminates its command group.

The shared `tui_terminal.py` handles pyte's scrolling omissions, primary and
secondary device attributes, DEC-private cursor reports, Kitty query/mode
controls, xterm modifyOtherKeys, color queries and negative termcap replies.
Alternate-screen save/restore includes resize and cursor bounds. The observer
leaves terminal output modes unchanged. Interactive VM launchers use the shared
production console wrapper to preserve guest newline/cursor semantics before
QEMU snapshots its modes. Regression tests cover real idle query servicing,
unchanged child modes and byte-preserving console output. See the
[serial-console review](serial-console/README.md) for the earlier observer
workaround that masked a production defect.

## Initial prototype and production baseline — 2026-09-30

Root revision: `0985bc2b26e3ccb2eea84a1420e8fb691ee807cb`. The initial working
tree had documentation changes; there were no TUI implementation edits before
the production baseline. Prototype built with Go 1.26.7; binary SHA256
`0aa1f50fb376e923b27ba101b86b83cc194a023397ecf1f0d700e92edaf3edef`.
Prototype dataset `portfolio` is simulated, not native lifecycle evidence.

Baseline runner: `/nix/store/9848vby0hlwhj8br6nvjfzf6wh92mdpy-p-demo-vm`.
Installed P: `/nix/store/mzxvm801y44x5dxci24id38xqjc6cdz5-p-0.1.0-dev`.
Dedicated lab disk: `.cache/p-vm/tui-review-baseline/disk.qcow2`, initially
fresh and then safely rebooted to correct observer fidelity. Guest NixOS
26.11.20260905.c043004, x86_64. Daemon health was `ready` and
`p-lab-repository` was active before `p tui`. Seeded `p-ai/main` was ready,
unattached, with no environment projection. The terminal was matched at
120×35, 80×24 and 48×16; guest TERM was set to `xterm-256color`.

The first provisional capture run exposed unsupported modifyOtherKeys styling
and missing pyte alternate-screen restoration. Parser corrections resolved
those capture defects; representative baseline frames were reconstructed from
recorded PTY bytes using the corrected parser. The helper also normalized outer
PTY output modes because QEMU's ONLCR translation displaced cursor-relative
output. That normalization masked a production serial-console defect, confirmed
later by the [native before/after review](serial-console/README.md). These
normalized baseline captures therefore do not establish fidelity of the
uncorrected VM transport. Provisional captures remain in ignored cache.

Navigation and findings:

- Read [prototype wide picker](prototype/0001-wide-picker.png), opened its
  [project selector](prototype/0002-wide-projects.png), then resized and read
  [stacked](prototype/0004-stacked-picker.png),
  [compact](prototype/0005-compact-picker.png) and
  [compact creation](prototype/0006-compact-create.png). The intended
  hierarchy reserves colored status, frames details and aligns project counts.
- Opened the [actual wide picker](baseline/0005-wide-picker-corrected.png),
  then `c`, chose `p-ai`, chose new branch, selected `P · refs/heads/main`, and
  typed `feat/very-long-branch-name-to-test-runtime-column-qgG`. The
  [confirmation](baseline/0010-creation-review.png) retained literal q/g/G and
  showed captured source and policy. Explicit `y` accepted it. Read
  [progress](baseline/0012-creation-progress.png), then chose Back. Startup
  continued without forcing terminal entry.
- Selected seeded main, used Enter, inspected transition and
  [native terminal](baseline/0015-terminal-entered.png), then executed
  `printf P_BASELINE_NATIVE; pwd`. The resulting
  [workspace output](baseline/0016-native-command.png) was `/workspace`.
  An attempted push to an unassigned branch was correctly denied by Git
  authority; it did not create a retained branch. Ctrl+B, lowercase d returned
  to the [picker](baseline/0017-detach.png). The reviewed handoff frames did
  not show the earlier console. These captures do not prove all intermediate
  output; native driver verification remains a separate gate.
- `S` on ready main produced
  [services unavailable feedback](baseline/0018-services-empty.png). The page
  kept controls and explained refusal. Service action/journal acceptance is
  pending native review on the updated notes runtime.
- Read [stacked long selection](baseline/0022-stacked-long-selection.png),
  then `P` and read [project counts](baseline/0023-stacked-projects.png).
  Counts were concatenated after variable-width names rather than aligned.
  Wide selected details were unframed and clipped unattended context; stacked
  details omitted UUID. At [48×16](baseline/0028-compact-long-selection.png),
  the long branch hid runtime entirely, and controls clipped the help label
  and omitted search/advanced actions. These are supported-interface findings.

Native creation did not complete within this baseline review. The
[operation evidence](baseline/0031-native-creation-diagnostic.png) retained
operation `b514bb05-da1c-4032-aef4-07664b751036`, session
`fed98c6a-7290-4b68-bd06-2de8395dcfcc`, `running / branch-assigned`, no
diagnostic, builder ownership and `runtime_init_state: not-attempted`.
Captured source is the root revision above; base image fingerprint is
`3c44955926486f275a51741948370f4cfcda09127ec8ecdd0dd011f24ac88506`.
Incus reported its builder stopped with no start timestamp/log. Code inspection
identified serial per-entry confinement/source-transfer work on the large P
source tree before builder start as a plausible delay; there is no evidence of
an offline Nix evaluation failure. Creation completion remains pending rather
than being accepted as a successful native workflow. The VM was safely powered
off; its disk and pending operation are retained.

Prototype-only agent inventories, previews and in-terminal management popups
are outside current product scope and are not included in the mismatch list.
Existing-branch completion, true empty inventory, sufficient native list
scrolling, Stop/Start, journals and reviewed removal still need candidate live
review; component fixtures do not establish those gates.

## Persistent notes lab: CLI/API evidence — 2026-09-30

This is a separate CLI/API check, not a TUI experience review. The primary
agent ran the [persistence helper](../../../tests/integration/notes-lab-persistence.sh) in a dedicated
`.cache/p-vm/notes-workflow-review/disk.qcow2`, using runner
`/nix/store/8a612yhrzc9s8d70xr72xg3x6xsh60i2-p-demo-vm` and installed P
`/nix/store/4mn7v32zlw45dyw403lb0divdkvnsg57-p-0.1.0-dev`.
The session's captured runtime fingerprint was
`cf823d1e8f5f2460e9dfdb2f238f4bae7a7962a847ed6529dda84209d388f3e8`.

[Preparation](persistence/prepare.png) created session
`26d77503-7577-44a1-9315-2ce82e54a52b`, two SQL rows, one processed job and
one pending job. Pushed source was `d115bbeefbe57e3e2c43ac7ed43befb404ae0c3a`;
the retained local-only commit was `db5d9d86f2d923e5e10b181f1cde10ea4b1065b7`.
Dirty workspace and private-home markers were also written. The initial boot
was `1f35ed0f-56c5-4b2b-91db-6b3fe23b85b8`. The helper run used a test-owned
copy whose SHA256 matched the corrected repository helper:
`dfc9e4d7cb8535abb7ac5316619fc1b6c1fd28e19aa0ab984ecb6874a810d9f7`.

Clean `p-demo-poweroff` exited 0. Relaunch used the same disk and runner, with
new boot ID `b4ec0e47-889a-4a76-82ee-a9cd5c2498ea`. The helper independently
verified retained UUID/principal, pushed/local Git tips, dirty/private files,
installed units and zero service PIDs before explicit startup. SQL still had
two rows and one pending job. Starting database, web and worker processed the
retained job; [health and result verification](persistence/relaunch.png)
showed both notes processed with word count 5 and
`P_NOTES_LAB_RELAUNCH_PASS`. The second clean poweroff also exited 0.
The verified disk remains available for inspection.

Preparation exposed three fixture/walkthrough issues that were corrected:
detached observers outlived a nix-shell temporary directory; interactive Incus
execution hung without explicit noninteractive mode; and copying the Nix
`/etc/p-notes-example` symlink produced immutable source. The corrected observer
owns stable temporary storage, the helper disables PTY/stdin, and the
walkthrough copies directory contents before making source owner-writable.
Failed preparation did not establish persistence acceptance. Full raw
records remain in `.cache/p-vm/notes-persistence-evidence/`.

## Initial candidate review and discovered defects — 2026-09-30

Runner `/nix/store/1i0z3xzaygzz23irjpqic9q8gl5ivkzf-p-demo-vm`, installed P
`/nix/store/x0a9dx5xjv7vg3qfhynaks7k8605b0a0-p-0.1.0-dev`, retained notes
lab `.cache/p-vm/notes-workflow-review`. This review precedes the second UI
correction and does not establish final experience acceptance.

The LLM read the [wide picker](candidate-initial/0003-candidate-wide.png),
opened services on persisted notes, then started its database. Three installed
unloaded identities remained selectable, although `unknown (not-loaded)` and
static `s start/stop` were ambiguous. The wide session UUID orphaned its final
character despite available terminal space. These findings prompted narrower
presentation corrections and require another live review.

The LLM selected seeded `p-ai/main`, opened Services, observed an unavailable
page, exited to the owner shell and ran the exact fixed native commands.
[Diagnostic output](candidate-initial/0012-empty-native-diagnostics.png)
shows ready control/session state, `list-units` JSON `[]` with exit 0 and
`list-unit-files` JSON `[]` with exit 1. The exact backend `list-unit-files`
flags were then repeated, also yielding `[]`, exit 1; actual raw bytes remain
in candidate `terminal.ansi`. The owner API returned unavailable. This exposed
an empty-inventory backend defect, rather than proving an empty service page.

Through `c`, the visible new-project choice, literal `notes-tui-qgG-review`,
blank local origin and explicit `y`, the LLM created and
[entered a real new workspace](candidate-initial/0019-new-project-ready.png).
Operation `4df084b6-d383-4a3e-a1ed-312a5374c270`; session
`a7ca0571-3881-41f5-82b3-ee9b04eb9011`. After native detach, the picker selected
the first existing row; the LLM read it, selected the new project, and
[reproduced unavailable services before installation](candidate-initial/0023-actual-fresh-services-before-install.png).

Inside that session, directory-content copy, commit and assigned-main push
succeeded. The default
[installer failed](candidate-initial/0025-sample-install-native.png) because
the attached shell lacked `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`.
An explicit diagnostic export allowed installation and the LLM detached,
started database, web and worker through visible service actions and read
[all three active](candidate-initial/0033-review-all-running.png). Enter on the
worker opened its actual journal with PostgreSQL readiness. The workaround
does not establish the default manual workflow; a new captured runtime and
unexported fresh-shell review are required.

The session and SQL lab were retained and the VM safely powered off, exit 0.
A localhost hot-client delivery attempt failed before launching that client;
its server was closed. The following review will use a combined packaged
runner containing the presentation, native empty-inventory and runtime-shell
corrections. Full action history is in
[candidate actions](candidate-initial/actions.ndjson); full frames/cells/raw
bytes remain in ignored `.cache/p-vm/tui-review-evidence/candidate/`.

## Changed product: adaptive sample and navigation review — 2026-09-30

These are live LLM-chosen actions, separate from VM58 driver acceptance. All
runs used the same task-owned `.cache/p-vm/notes-workflow-review/disk.qcow2`;
other developer disks were preserved. The three native session slots contained
notes-persistence, the seeded p-ai session, and the new notes-final session.
Stop retains a capacity slot; one of the configured four instance slots is
reserved for loss inspection. The initial generic `busy` refusal was therefore
capacity exhaustion, not evidence of stalled polling. No polling change was
made. The typed capacity diagnostic now explains the available remedy.

Build identities:

- Initial corrected kit/layout: runner
  `/nix/store/9pqgd76ymnk6aqcjx5qf38a2lnw8cz81-p-demo-vm`, P
  `/nix/store/akws46zc3jpw68lym3z1yb7bhblmg835-p-0.1.0-dev`;
  raw actions under `final/` in ignored cache.
- Selected-unit correction: runner
  `/nix/store/a5kl70jmfmpfnj0zc7g2crxd0iis6ya0-p-demo-vm`, P
  `/nix/store/idz0ygnqzip1jzjfibwvjvzwk77n4iix-p-0.1.0-dev`, runtime
  `/nix/store/65lz8psmpzahf4ak3p8x5ms3km3fry3j-nixos-lxc-image-x86_64-linux`.
- Wrapped feedback: runner
  `/nix/store/09i7z2hfb7r6wdr9zy0nhyddx6jalp30-p-demo-vm`, P
  `/nix/store/r15y8l6657823ia7hzppzlsb2c6b594z-p-0.1.0-dev`, runtime
  `/nix/store/rnypcj1rvxmqisn008v4lfkk9kpfd28z-nixos-lxc-image-x86_64-linux`.

The LLM stopped and reviewed Discard for the older test-owned
notes-tui-qgG-review session, including the full scrollable loss JSON. Native
Discard completed and retained its pushed branch. Through visible creation
controls, the LLM created notes-final/main, session
`fbdead9a-1100-469b-a70d-4338d9526e43`, operation
`b324d94f-062e-4024-b92b-85c71ad5ff92`, then inspected actual terminal entry.
The [default shell](candidate-final/0029-fresh-default-shell-bus.png) had its
user-manager environment and `systemctl --user` worked without manual exports.
The [truly empty services page](candidate-final/0032-fresh-empty-services.png)
now gives useful installation instructions. The documented contents copy,
commit/push and [installer](candidate-final/0034-sample-native-install.png)
succeeded. Captured source is `09a5bc2fa6c9845a1361065f913f4cdf49f8427e`.

The LLM started database, web and worker through visible controls, inspected
[all three active](candidate-final/0039-worker-started.png), and read the actual
worker journal. A discovered Back defect reset selection to database; Help
also reset it. Both were corrected to preserve unit identity. On the corrected
packaged browser, Help and
[Journal Back](selected/0019-latest-journal-back-preserved-worker.png)
retained the worker. The next `s` action
[stopped only that worker](selected/0020-latest-selected-worker-stop.png).
[Independent native verification](selected/0022-native-retention-and-worker-stop-proof.png)
proved inactive/dead, MainPID 0, database/web continuity, retained SQL row with
word count 5, zero pending jobs, and retained dirty/private files.

Inside the actual attached workspace, eleven sample tests passed. An
intentional word-count-zero edit produced a
[real failing assertion](candidate-final/0047-actual-edit-failure.png), then a
[corrected passing test](candidate-final/0048-edit-fixed-test.png). An initial
mistyped test name was corrected before recording that assertion; it was not
accepted as the edit/fail/fix proof. A labeled test-owned worker drop-in caused
an actual EXEC failure, shown in
[mixed service states](candidate-final/0051-mixed-worker-failed.png) and its
journal. The LLM used find, next match and horizontal pan, removed only the
failure drop-in in the terminal, then explicitly recovered services. No
project service is automatically enabled by the installer.

The final layout was inspected at matched 120×35,
[80×24](notice/0010-stacked-picker.png) and
[48×16](notice/0018-compact-picker.png). Runtime has reserved space, details
are framed, commands remain readable and project counts align. The native
[project selector](notice/0011-stacked-project-counts.png) and
[empty project scope](notice/0012-stacked-empty-scoped-project.png) were
reviewed. The actual five-choice creation inventory exceeds compact capacity;
`G` [scrolls to range 2–5/5](notice/0020-compact-create-native-scroll-end.png)
and `gg` returns to its first choice. Complete capacity feedback and
confirmation controls remain readable at
[wide](notice/0007-notice-wide-capacity-readable.png),
[stacked](notice/0015-stacked-capacity-readable.png) and
[compact](notice/0023-compact-capacity-readable.png) dimensions. Compact
confirmation fields are scrollable; the LLM reviewed their remainder before
explicit confirmation. These frames resolve the clipped-feedback finding.

A synthetic authentication-free PermissionRequest emitted through the bundled
adapter produced [green running and amber waiting](notice/0043-fixture-waiting-color-review.png).
At [48×16](notice/0046-waiting-compact-picker.png), both remain visible;
`A`, then `G`, shows the
[actual retained reason](notice/0048-waiting-compact-report-end.png).
This establishes report presentation only; it is not authenticated agent
execution or a live multi-agent inventory claim.

Default Enter declined Stop and kept notes-final running. Explicit `y`
[stopped it](notice/0035-sample-stop-completed.png); Enter started a new native
interactive host. [Native checks](notice/0039-stop-start-private-source-services-proof.png)
verified source, dirty/private files and all three service PIDs zero before
explicit recovery. A late attempted Back arrived after startup had already
entered the terminal and typed ordinary `q`; that attempt does not establish
progress-abandonment acceptance. The input was cleared with Ctrl+U.

An actual p-ai workspace inspection exposed a separate bounded SFTP defect:
[READDIR failed outside the 16 KiB packet bound](notice/0028-task-seed-full-loss-review.png).
No loss preview or destructive confirmation was accepted. `r` retained the
[exact operation/key/session](notice/0029-actual-failed-inspection-exact-retry.png);
`O`, then Enter, reopened the same
[durable failure](notice/0031-durable-failure-operations-inspect.png).
This established readable failure and captured identity. Later diagnosis found
that `r` on this finalized failed/cleaned inspection did not rerun analysis;
it is not recovery evidence. The final review below replaces that misleading
Retry control and completes the supported branch and cleanup walkthrough.

Each relaunch followed `p-demo-poweroff` and exit 0. Action histories are
[candidate](candidate-final/actions.ndjson), [selected-unit](selected/actions.ndjson)
and [wrapped feedback](notice/actions.ndjson). Raw PTY, cells and every frame
remain in ignored cache; these representative PNGs preserve presentation.

## Completed branch and recovery walkthrough — 2026-10-01 UTC

The supported live walkthrough passed before its remaining steps were encoded
in VM58. This section records LLM navigation and native effects; the separately
run VM gates are recorded in [implementation progress](../../implementation-progress.md).
The real devShell extension remains separately gated. The lab used the same
task-owned notes-workflow-review disk and preserved developer demo/demo-public
disks throughout.

Two final packaged builds supplied the evidence:

- Branch, isolation and retained-source paths: runner
  `/nix/store/jvy5fifw9sjpbadxhhjivqd0fw19y9cv-p-demo-vm`, P
  `/nix/store/zwglrmgb9rppjyqamnsn25d3qcw5hdv3-p-0.1.0-dev`, runtime
  `/nix/store/6j7y4hm4jfp5ybfzk90x4b01lfraln54-nixos-lxc-image-x86_64-linux`.
  This includes the bounded READDIR packet correction; the preceding reviewed
  layout and selected-unit behavior are unchanged.
- Finalized-inspection guidance and numeric inventory diagnostic: runner
  `/nix/store/l2mx5vbihqw2vlab0xfsmcpa9d8sikyl-p-demo-vm`, P
  `/nix/store/jpb15rgari018aiby5l6c8pklf574q7y-p-0.1.0-dev`, runtime
  `/nix/store/gmzbdgwhrnb7bl2axqp25ddaxh3kg0yx-nixos-lxc-image-x86_64-linux`.
  Only the affected error paths required renewed layout review.

To free a session slot, the LLM stopped and reviewed all loss pages for the
earlier task-owned notes-persistence fixture,
`26d77503-7577-44a1-9315-2ce82e54a52b`. Its CLI persistent-disk proof had already
completed. The reviewed Discard removed that fixture's private runtime and
retained its pushed main; operation `142e3670-c727-43fc-82d9-f21fd531b424`
[completed](directory/0017-owned-persistence-fixture-discard-complete.png).
The historical persistence evidence remains valid; that session is no longer
present on the review disk. The preserved p-ai session was not removed.

### Branches, private data and removal

The LLM used `c`, selected notes-final, chose a new branch, selected committed
P main, and entered literal
`feat/very-long-notes-qgG-review-branch-name`. The
[review](directory/0022-new-branch-policy-review.png) showed source OID
`09a5bc2fa6c9845a1361065f913f4cdf49f8427e` and current trusted policy. After
explicit `y`, `q` returned to the picker while creation was still accepted
and running. The [creating frame](directory/0024-new-branch-progress-abandoned.png)
and [ready picker](directory/0025-new-branch-background-native-ready.png)
establish real progress abandonment without later forced terminal entry.
The new session was `2d0f0c47-cea5-4507-a0f9-caa935b72d76`.

In its native terminal, HEAD and the assigned branch matched the captured
source. Main's dirty file and private marker were absent. The documented
[Python installer](directory/0030-new-branch-documented-installer.png)
succeeded without bus exports. An initially mistyped shell-installer filename
was corrected to `python3 examples/notes/install.py`; that failed command is
not installer evidence. Three visible service Starts produced
[three active units](directory/0035-new-branch-worker-start.png).
The branch's [empty SQL then processed note](directory/0037-new-branch-private-sql-proof.png)
contained only `branch private note`, word count 3. Main independently
[retained its original row](directory/0040-main-private-isolation-proof.png),
word count 5, dirty source and private file; the branch marker was absent.

`R` opened literal rename input. The
[confirmation](directory/0043-rename-branch-review.png) captured UUID, P tip
and private-data preservation. Explicit `y` completed operation
`ec0c5ba5-46f3-41be-9a48-3026dedb7f9f`, renaming to
`feat/renamed-notes-qgG`. The same UUID, local branch and private SQL remained.
`git ls-remote origin 'refs/heads/feat/*'`
[showed only the renamed ref at the same OID](directory/0048-renamed-p-ref-proof.png).
An earlier command mistakenly used nonexistent remote `p`; only the corrected
ordinary `origin` observation establishes this P-ref proof.

The LLM stopped the renamed session, used `d`, scrolled its complete fresh
loss preview, and confirmed the loss of the modified sample README and whole
private runtime. Discard operation `2e3281f0-ce17-4ced-b89a-0a45c9f07691`
[completed](directory/0057-renamed-discard-completed.png).
`b` then [listed the retained renamed branch](directory/0058-retained-renamed-branch-list.png).
In a new creation flow, the existing-branch choice led directly to
[captured retained tip and policy](directory/0061-retained-existing-source-policy.png).
Back returned to branch choice; reselection preserved the request key.

Reassignment created session `3d7e708a-22a6-4694-8b5f-c41b6135beae` with
[the same source and no old private marker](directory/0066-retained-fresh-native-source-home.png).
After installation and database initialization,
[SQL was empty](directory/0067-retained-empty-sql-schema-proof.png).
In main's attached terminal, `ALTER TABLE notes ADD COLUMN review_extra text`
[added one schema column and retained its row](directory/0070-main-schema-change-private.png).
Re-entering the concurrent branch proved
[column count zero, empty SQL and unchanged source](directory/0073-branch-schema-unchanged-proof.png).
Each database uses its session-private `$HOME/.local/state/p-notes/` and Unix
socket; sharing the same local path does not share runtime storage.

After Stop, `X` produced a full Delete preview including
[assigned-branch loss](directory/0078-retained-delete-loss-preview.png), native
runtime identity and ignored files. Enter
[remained on the preview](directory/0082-retained-delete-default-no.png);
only explicit `y` authorized deletion. Operation
`0f731bce-113b-4649-82c3-3398b840a62e`
[completed](directory/0084-retained-delete-completed.png), and `b`
[showed no retained branch](directory/0085-delete-retained-ref-absence.png).
Independent Incus and bounded project-ref observations showed only main and
the preserved p-ai runtime remained.

### Bounded failure and actual Retry recovery

The corrected READDIR reader admits bounded directory pages up to 128 KiB;
ordinary stat/read bounds remain 16 KiB. The seeded p-ai tree is still outside
the documented 512-entry workspace snapshot boundary. A fresh inspection on
the final package reported
[count 5, remaining 0, page maximum 128](terminal/0037-final-large-project-explicit-bound-diagnostic.png).
This is global inventory exhaustion, not proof of an oversized directory page.
No p-ai loss preview, deletion or successful large-tree inspection is claimed.

The LLM reviewed original failed operation
`117905e4-aeee-40ad-a7b8-54ebac2cb584` through `O`, selected its observed row,
and pressed Enter. The final
[120×35 page](terminal/0039-original-terminal-error-correct-guidance.png)
explains that the inspection finished with an error and requests a fresh
inspection. Retry is omitted and `r` is inert. The same affected page was
reviewed at [80×24](terminal/0050-final-error-stacked-remedy-controls.png) and
[48×16 after scrolling](terminal/0057-final-error-compact-remedy-readable.png).
The complete remedy and Back controls remained readable; `r` remained inert
at both dimensions.

A labeled owner-only native fault established genuine resumable cleanup.
The transferred `notes-tui-fixture.py` watcher required the exact stopped main
source, a running helper, persisted `init-issued` phase and matching native
identity/ownership labels. It narrowed only that helper's process limit from
256 to 255. Initial interpreter/socket setup mistakes exited before mutation;
the verified owner Python path and actual `$P_SOCKET` were then used.
The operation `e31d7687-aa7e-4699-ab57-d7ce3ca30f1d`
[became blocked/quiescent](terminal/0022-retry-correct-watcher-outcome.png),
with exact-helper cleanup unresolved. Restoration
[validated the same helper and restored 256](terminal/0024-exact-owner-helper-restored.png),
without changing labels, registry state or global limits.

The LLM used `O`, selected the actual blocked row, inspected its identity,
then pressed `r`. The
[same operation/key resumed](terminal/0028-same-operation-cleanup-retry.png)
and [finished failed/cleaned](terminal/0029-same-operation-cleanup-recovered.png).
This recovered held cleanup; it did not rerun or successfully finish the
original analysis. Independent native observations
[proved helper absence and readable stopped source](terminal/0032-cleanup-recovery-native-absence-guard-proof.png).
A new visible `d` request, operation
`71e7833a-97ac-41c4-a051-d0d98dcd7456`,
[completed a fresh loss preview](terminal/0035-fresh-post-recovery-preview-completed.png),
which the LLM canceled. Subsequent native entry
[retained source, private files, SQL row and main's schema column](terminal/0044-final-post-recovery-source-sql-continuity.png).

### Final cleanup and coverage boundaries

The LLM stopped its disposable notes-final main, reviewed all Delete loss
pages and explicitly confirmed. An approval review initially treated `main`
as possible developer data; read-only creation and disk-ownership evidence
established that this was the task-created sample fixture. The stale preview
then safely refused confirmation. A fully reviewed fresh preview was used;
Delete `b02c62f5-2214-4731-8472-cd2bdceb7430`
[completed](terminal/0076-final-disposable-fixture-delete-completed.png).
The empty-project owner API preview contained zero sessions, refs,
attachments or cache images; reviewed confirmation operation
`bcad27f1-5891-4499-a947-b0745f553267` completed.
[Final native and registry observations](terminal/0080-final-native-project-registry-absence-proof.png)
showed notes-final absent and only the unrelated stopped p-ai runtime remained.
The [final VM poweroff](terminal/0081-final-review-clean-shutdown.png) exited 0
before the serial slot was released for major integration checks.

| Plan journey | Evidence scope |
|---|---|
| Blank creation, first commit/push, edit/test/fix | Live attached terminal and native Git/SQL/HTTP observations above; CLI57 also checks unborn-main refusal and committed-source rules. |
| New and existing streams, setup, two-session source/data/schema isolation | Completed live paths above; later VM58 encodes their major native effects. |
| Entry/detach, services, worker queue, Stop/Start | Live transition, selected-unit, mixed-state and pending/processed records above; CLI57/VM56 cover additional native service effects. |
| Persistent lab relaunch | Dedicated historical CLI persistence proof plus live clean-relaunch source/private/SQL continuity; processes are explicitly restarted after VM shutdown. |
| Rename, Discard, existing reassignment, Delete | Completed live paths and independent ref/registry/native absence above; whole-project deletion is explicitly an owner API operation. |
| Bounded failure, Operations, Retry and fresh inspection | Final live cleanup recovery and new-request proof above; finalized errors expose no Retry. |
| Database outage/refused write and daemon reconnection | Supplemental live review below establishes deliberate outage, explicit service recovery, no refused row, browser re-entry and exact process/native/source continuity. |
| Interrupted worker transaction | The LLM ran the full 11-test sample suite in its actual attached terminal: [native result](candidate-final/0045-sample-real-queue-tests.png) includes `test_interrupted_worker_recovers` passing. That test kills the worker with its SQL transaction open, verifies the row remains pending, then processes it once. This is terminal/native application evidence; there is no separate TUI transaction control. CLI57 supplies additional queue effects. |
| SSH import/publication | Completed sample-source API and visible import/publication walkthrough below. Existing origin/recovery gates retain their additional contract scope. |
| Real committed devShell extension | Separate pending dependency-resolution and native/TUI gate; fixture provisioning does not establish it. |

Full chosen actions are [branch walkthrough](directory/actions.ndjson) and
[final error/recovery/cleanup](terminal/actions.ndjson). Representative frames
remain original rendered PNGs. Major VM58 acceptance and full-suite results
must be assessed separately from this completed LLM review.

## Supplemental database outage and daemon reconnection — 2026-10-01 UTC

The remaining supported database and reconnect journeys were reviewed on the
same frozen l2mx runner and jpb15 P binary. This used the same task-owned disk
after the preceding clean shutdown. Through actual `c` navigation, the LLM
selected the retained notes-tui-qgG-review/main branch at
`0c5b8789bed086a8aaa45bd6879bfe564ea9dd89` and reviewed its captured source and
policy. Creation `6bcdaf9a-6eb0-4228-964c-677a8807bd0f` assigned fresh session
`0ecc569f-3693-405a-ad36-51ee2ed10830`. The unexported
[installer succeeded](extra/0009-extra-native-default-sample-install.png),
then the LLM explicitly started all three units in Services.

The committed source predates the documentation-only edits at this checkpoint
and the later schema-readiness correction. Independent native
[byte comparison](extra/0047-extra-worker-pid-delta-current-sample-proof.png)
verified that db.py, database.py, worker.py, web.py, install.py, manage.py,
client.py, tests.py and all three unit files exactly match the frozen runner's
`/etc/p-notes-example`. The README was excluded from this behavior comparison.
This establishes application behavior at that checkpoint despite its historical
source OID. The [corrected-schema record](#corrected-schema-sample-actual-attached-validation)
establishes acceptance of the later source.

A native HTTP write
[created an accepted row](extra/0016-extra-pre-outage-accepted-row-pids.png),
processed with word count 4. In Services, the LLM selected database and used
`s` to Stop. The
[inventory showed all three installed and unloaded](extra/0019-extra-db-stop-dependent-states.png);
the selected database journal recorded graceful shutdown and stopped state.
Native verification
[proved database, web and worker inactive/dead with MainPID 0](extra/0023-extra-db-outage-native-refused-write.png).
Attempting `client.py add 'refused during database outage'` reported HTTP
connection refused and exit 1; it did not report success.

The LLM returned to Services and explicitly started
[database](extra/0026-extra-recovered-db-only.png),
[web](extra/0027-extra-recovered-web-explicit.png), then
[worker](extra/0028-extra-recovered-all-three-explicit.png).
Starting database alone left web and worker stopped. Native HTTP/SQL
[showed readiness and exactly the accepted row](extra/0031-extra-recovered-row-and-pid-baseline.png),
with no refused body. A separate selected-worker `r`
[changed only its PID](extra/0047-extra-worker-pid-delta-current-sample-proof.png):
1469 to 3968, while database 1252, web 1303 and accepted SQL remained unchanged.
The selected worker was inspected before Restart; native effects establish
more than the stable active-state screen.

Daemon restart was an explicitly labeled external owner setup action. The
initial pdev `sudo` attempt required an unavailable password and was canceled;
it did not restart anything. The documented disposable lab root console then
ran `systemctl restart p.service`. The
[actual daemon PID changed from 825 to 24963](extra/0038-extra-actual-restarted-daemon-native-proof.png).
`system.health` was ready, the exact expanded Incus configuration compared
equal, and the completed session creation retained its request key/source.
The LLM [reopened the browser](extra/0039-extra-browser-reconnected-after-daemon-restart.png)
and used Enter to attach. Native checks
[proved all four pre/post PIDs identical](extra/0041-extra-post-daemon-restart-native-continuity.png):
database 1252, web 1303, worker 1469 and tmux 371. SQL and source were unchanged;
the later deliberate worker Restart is a separate action.

After reviewing Stop, the LLM
[left this supplemental fixture stopped](extra/0050-extra-finished-fixture-stopped.png)
for scoped cleanup after the origin walkthrough. The
[VM powered off cleanly](extra/0052-extra-review-clean-poweroff.png), exit 0,
before the serial slot passed to the integration runner. Full choices are
[supplemental actions](extra/actions.ndjson). No product/UI edits were needed.

## Sample SSH origin through API and visible import — 2026-10-01 UTC

This review used `/tmp/p-notes-origin-review-runner`, resolving to
`/nix/store/abjy2pfb5yxwghwpj9bk0cjqziq7hjp5-p-demo-vm`, on the same dedicated
notes-workflow-review disk at 120×35. Its P executable
`/nix/store/rpcky0979svk810mwww7g61dhysvy677-p-0.1.0-dev/bin/p` is byte-identical
to the frozen jpb15 executable reviewed above: SHA256
`3a242cf1c4b0c2d4bbf28999b11e07691f82a832b485c4657755c6e591268c77`.
Only optional lab tools changed. The `origin-fixture` binary has SHA256
`30814aa7d17f8ae3e284d04ce4a53665d47f4e17b327bd35aadc4ac97dbbb00c`.

### Explicit owner setup and API journey

The read-only lab helper created an authenticated SSH server inside the VM,
listening only on `127.0.0.1:45217`, PID 956, UID 1000. Its generated
`notes-review-origin` alias points there; it is not an external host.
The bare origin contains the exact `/etc/p-notes-example` subtree, committed
at `41a8761bea37e4ba86b84b5ec134c0db3460a71e`.

The default hardened daemon could not read that generated home-directory
fixture. A read-only bind with `ProtectHome=yes` still left the parent home
unavailable, as the [native namespace check](origin/0011-origin-daemon-namespace-ssh-result.png)
showed. Scoped temporary setup used `ProtectHome=tmpfs` plus read-only binds
of only the generated `.ssh` and fixture-state directories. Other hardening
remained. Actual [daemon-namespace SSH contact](origin/0013-origin-daemon-exact-ssh-tip-readable.png)
then returned the exact seed tip. This is lab setup, not a TUI navigation step
or a production hardening change. A subsequent optional notes-lab configuration
uses daemon state for future fixture storage; its fresh-boot verification is
recorded separately.

Original API project operation `e9abd72d-14c0-48e3-91c5-08b7bd8c08d6`, key
`notes-origin-api-project`, resumed the same intent and completed after that
correction. Nonempty import established only a project; explicit session
creation `d66eb853-bfc8-4109-b688-57092102a662` assigned main to
`1cf9ff54-836e-455e-a188-739fb696eed7`, capturing the exact seed OID and URL.
[Native HEAD observation](origin/0015-origin-api-native-import-completed.png)
matched that capture.

The first owner `incus exec` setup omitted noninteractive flags and stalled;
it was suspended and its exact test-owned job terminated. That invocation is
excluded from acceptance. Corrected execution uses `--force-noninteractive
--disable-stdin`, fixed session-user bus/Git variables, and a timeout with
`--kill-after=5`. The [actual 11-test suite passed](origin/0026-origin-api-corrected-noninteractive-sample.png),
including real worker interruption inside a SQL transaction. Three API unit
starts, HTTP creation and SQL inspection produced
`API imported sample durable row|processed|5`.

A two-line sample README edit was committed and pushed to P at
`3698af0bde399c1fd06a05c574830d1913fcb07`. The
[publication preview](origin/0027-origin-api-sql-p-only-push-publication-preview.png)
showed that the origin still held the seed tip, source was the exact session
and main ref, destination was `refs/heads/main`, relation was `fast_forward`,
and workspace status was explicitly unknown. Private SQL/home files are not
in the source payload.

Automatic approval review initially classified this as external egress and
rejected publication before it ran. Read-only
[destination and payload evidence](origin/0028-origin-local-only-publication-destination-payload-proof.png)
established the loopback listener, generated identity and exact README-only
diff. The same action was approved with that ownership evidence; no destination
or payload was changed to bypass the rejection. Explicit owner publication
[advanced and independently verified the exact tips](origin/0029-origin-api-authorized-loopback-publication-verified.png).
The scoped API fixture's complete removal preview was reviewed, Delete
`35149c19-c0e9-455a-b11c-9b31649d1a5a` completed, and
[native absence plus the empty-project preview](origin/0033-origin-api-delete-completed-empty-project-preview.png)
preceded project cleanup `91b676e7-f561-47a3-a244-bd4c59c13efb`.

The exact corrected [manual owner API source](origin/api-walkthrough.sh) is
retained with SHA256
`9da9b5b53bfc7ac1a75fc4ff586a369ef342dbac9f3220cf7ffa93f3f3340413`.
Its phases are explicit manual setup/evidence and reviewed confirmation, not
a TUI driver. The exact built [original lab helper](origin/lab-origin-fixture.sh)
is also retained; its home fixture-state location predates the final lab fix.
[Extracted actual API responses](origin/api-observations.ndjson)
retain the completed operations and publication observations.

### Adaptive visible import and sample use

The LLM used `c`, chose a new project, entered `notes-origin-review`, then
entered the generated SSH URL. The [initial confirmation](origin/0038-origin-live-url-policy-review.png)
labels main as available only for an empty project/origin. Confirming the
nonempty import completed project operation
`a6bb531e-227f-4781-8508-620348105b72`
[without creating a session](origin/0039-origin-live-confirm-nonempty-project-only.png).
The LLM returned to `c`, selected that imported project, chose new branch and
[External origin: refs/heads/main](origin/0042-origin-live-source-current-observation.png),
then entered main. The [second policy confirmation](origin/0044-origin-live-exact-source-policy-confirmation.png)
captured the now-observed `3698af0` commit, URL and new request key.

Session operation `dd09e98e-571c-4666-a196-dde3db3245fc` created
`b9b8aa3e-58c4-417f-800b-658e1c77eb7f`. The LLM inspected its
[accepted progress](origin/0045-origin-live-first-session-accepted-progress.png)
and native entry. Through the actual attached terminal, the unexported
installer and [all 11 application tests passed](origin/0048-origin-live-actual-attached-source-setup-tests.png).
Native [source, tools and bus observations](origin/0049-origin-live-source-private-tools-default-bus-proof.png)
showed exact captured HEAD, PostgreSQL 18.6, Psycopg 3.3.4 and no copied default
database cluster.

After detach, the installed inventory showed all three units unloaded. The
LLM explicitly started database, web and worker; the
[three active units](origin/0055-origin-live-worker-three-active.png)
belonged to this new stream. Re-entry and native SQL observed
[an empty database, then its own processed row](origin/0058-origin-live-fresh-private-sql-source-push.png):
`TUI imported sample durable row|processed|5`. The earlier API session's row
was absent. The attached terminal committed its own two-line README edit and
pushed to P at `54fdf5a2a3590e40d20d9fdaa2fdff8f0e02a6ed`.

The explicit [owner publication preview](origin/0061-origin-live-owner-preview-push-not-auto-published.png)
showed external tip still `3698af0`, P tip `54fdf5a`, exact source UUID/ref,
destination main and fast-forward relation. Owner API publication advanced
only that reviewed destination. The
[actual publication and independent three-tip verification](origin/0084-origin-owner-final-cleanup-console.png)
showed canonical P, localhost SSH and native bare origin all at `54fdf5a`.
Publication remains an owner API operation; no TUI publication control is
invented.

### Scoped cleanup and final state

The LLM stopped its origin stream and paged through every Delete loss field,
including all commits losing P reachability, observed origin containing main,
no local-only commits, whole runtime/private-data removal and ignored pycache.
Delete `a795eef0-161a-4e2a-95cb-7d58b940ee2d`
[completed](origin/0074-origin-live-delete-native-completion.png).
The supplemental outage/reconnect fixture was also fully reviewed and
Discard `cfd3b899-06df-45b9-992c-d3b706931898`
[completed](origin/0083-origin-supplemental-discard-native-completed.png).
Its pushed `0c5b8789bed086a8aaa45bd6879bfe564ea9dd89` branch was retained.

[Independent native/registry checks](origin/0088-origin-final-exact-project-runtime-fixture-absence.png)
showed only the preserved stopped p-ai runtime. Empty origin project cleanup
`16049bc8-7790-4611-a1f6-1ea2a8b6c745` completed; the
[final normalized operation response](origin/final-project-operation.txt)
records its completed state. An earlier hand-transcribed `160409c8` query
was a mistaken ID and is not a product failure. The helper removed only its
exact generated server/config/state; its process-exit race emitted an `awk`
missing-proc warning but cleanup completed. The byte-unchanged temporary
drop-in was removed and [original ProtectHome=yes restored](origin/0087-origin-effective-original-hardening-restored.png).
The [VM powered off](origin/0090-origin-correct-cleanup-operation-clean-poweroff.png)
with exit 0 before the serial slot passed to integration tests.

Full choices remain [origin actions](origin/actions.ndjson). No product/UI
changes were required by this supplemental review. Developer disks and the
unsupported large p-ai source were preserved.

## Final notes-lab setup and queued Stop/Start continuity

The final lab plumbing was checked on the same dedicated task-owned disk with
`/tmp/p-notes-final-lab-runner` pointing to
`/nix/store/j867s09yayvfgps6vrsla1arbfzv6k2c-p-demo-vm`, at 120×35.
This is a later fixture-only build of the frozen production UI. Its application
source predates the damaged-schema correction described in the next section;
these continuity observations retain that historical scope.

### Fresh SSH setup without a daemon restart

Manual owner setup created only the generated localhost SSH origin from the
packaged `/etc/p-notes-example`. State was
`/var/lib/p-demo/notes-origin-review`, mode 0700, owner UID 1000. The fixture
listener was 127.0.0.1:46113, PID 963. The precreated empty `.ssh` directory
allowed setup on the fresh image. The notes-only unit used
`ProtectHome=tmpfs` and the exact read-only `/home/pdev/.ssh` bind; there was
no temporary drop-in and no manual daemon restart.

[Effective unit and fresh origin observation](final-lab/0005-final-lab-native-effective-ssh-source-no-restart-proof.png)
show the same daemon PID 869 before and after setup, no temporary drop-in,
private state permissions, and successful fresh `origin.sources` at seed
`2bb546d4a5eacff85fe33d18a55d70a419dcd331`. Owner API project create
`3e8e268b-2baa-4d2b-b2f8-830b4404b8aa` and session create
`b7d59342-87a3-4ffc-a5f1-d27db298d086` completed. Native imported HEAD
matched that seed. The manual [setup/evidence source](final-lab/api-walkthrough.sh)
is retained with SHA256
`4653337f114c9b9a2bdfdd8dca9ddcfbf0dd45f50f0f0200ff293306bbc9f761`;
only its create and empty-project cleanup phases were used in this checkpoint.
It did not navigate the TUI or perform the live commit/SQL/service journey.

### Local work and queued data across full session Stop

The LLM visibly entered `notes-final-plumbing/main`, UUID
`ddb23217-579d-4df3-84fc-33d06649a6bb`. In its actual attached terminal it
created local-only README commit
`bc73f8e19e949bb210c8631cf044839febdbdd2d`, an untracked
`uncommitted-stop.txt` and a private home marker.
[The initial native source observation](final-lab/0009-final-lab-live-unpushed-commit-private-dirty-source.png)
shows the unpushed HEAD, unchanged canonical P main and marker contents.

After the default installer, the LLM explicitly started all three services.
One accepted note became `processed|5`. It then visibly stopped only the
worker while database/web remained active and submitted another note.
[The native pre-Stop state](final-lab/0024-final-lab-live-two-rows-one-pending-unpushed-markers.png)
shows both rows, one pending job, and the local commit and private markers.
The LLM inspected the actual database and web Journal pages; the
[database private-socket signature](final-lab/0033-final-lab-live-db-journal-exact-private-socket-signature.png)
and [web accepted-note signatures](final-lab/0031-final-lab-live-web-journal-full-accepted-note.png)
were read by horizontal panning. These are actual selected-unit journal
pages, not transcript matches standing in for UI navigation.

[Full session Stop](final-lab/0035-final-lab-live-full-session-stopped-pending-retained.png)
ended processes. Visible Enter/Start resumed the same session. The
[native post-Start observation](final-lab/0038-final-lab-live-poststart-localcommit-private-dirty-units-pids0.png)
proved unchanged local HEAD, dirty source and private marker, retained
installed units, and all three MainPID values zero. The LLM explicitly
started database and web, leaving the worker stopped. The
[pending SQL observation after full Stop/Start](final-lab/0045-final-lab-live-poststop-pending-job-sql-retained.png)
still shows the original processed row, the second pending row and exactly
one unfinished job. Database/web PIDs were 445/479 and worker PID was zero.

Explicit worker Start produced the
[selected worker Journal results](final-lab/0050-final-lab-worker-journal-exact-two-jobs.png)
for jobs 1 and 2 with word count 5. The
[final native continuity observation](final-lab/0056-final-lab-native-two-processed-commit-private-source-retained.png)
shows both rows processed, no pending jobs, unchanged local-only commit,
private/dirty markers and P main still at the seed. Full Stop does not enable
or automatically restart application units; those three Starts were deliberate
visible actions.

A premature re-entry while the Journal observation was still busy returned
a conflict. Shell text sent before confirming attachment consequently reached
the browser search; the LLM cleared it, waited for actual native entry, and
repeated the observation. Those mistaken-input frames do not establish native
execution. The retained successful proof is frame 0056. The full
[action record](final-lab/actions.ndjson) preserves that correction.

### Reviewed cleanup

Only this task-owned fixture was stopped and removed. The LLM paged through
the complete fresh Delete preview, including
[the explicit local-only commit loss](final-lab/0062-final-lab-full-loss-preview-page2.png),
[dirty source and runtime-data removal](final-lab/0063-final-lab-full-loss-preview-page3.png),
and [the end of the preview](final-lab/0064-final-lab-full-loss-preview-page4.png).
It then confirmed Delete `ec3f83e8-22b9-43f5-8f1e-6fdfaa41ce19`, which
[completed](final-lab/0067-final-lab-native-delete-completed.png).
[Independent native absence and empty-project review](final-lab/0069-final-lab-native-absence-empty-project-preview.png)
show only the preserved stopped p-ai runtime. Empty project cleanup
`7626c6ce-a381-45e7-8c25-2c78ad106b13` completed, as retained in the
[normalized actual API observations](final-lab/api-observations.ndjson).
The generated SSH helper cleaned up its exact server/config/state. The
[runner powered off](final-lab/0071-final-lab-clean-poweroff.png) with exit 0.
No developer demo disks or p-ai data were removed.

## Corrected-schema sample: actual attached validation

Fresh review found that version 1 alone did not prove required tables and
columns existed. The sample correction refuses a damaged schema without
repairing it or reporting ready, while allowing additional columns. This
source-specific acceptance uses the corrected lab
`/tmp/p-notes-schema-lab-runner` pointing to
`/nix/store/fq2pn0lkydl94azsfc3gdr2h685hvbfn-p-demo-vm`, on the same owned
disk at 120×35. Production UI bytes are unchanged; earlier matched-size UI
reviews retain their scope.

### Exact provenance and visible creation

[Installed binary and fixture provenance](schema/0003-schema-lab-fresh-origin-setup-and-p-binary-provenance.png)
record executable
`/nix/store/pdvjxh0q55p2n7nblkcz9ypv5dxr1j0w-p-0.1.0-dev/bin/p` with
SHA256 `3a242cf1c4b0c2d4bbf28999b11e07691f82a832b485c4657755c6e591268c77`,
the same frozen UI binary as the previous review. The generated localhost
origin came from the corrected packaged sample at
`9e384d3d322d14af6854a1e399772744bc62558f`; listener 127.0.0.1:40149,
PID 953, UID 1000. Daemon PID 874 remained unchanged through setup and source
import, with the notes-only read-only SSH bind and no temporary drop-in.

The LLM created `notes-schema-review` through the visible new-project URL
prompts. Project operation `b9852716-8bd7-4de7-8ddf-ffa5f465d388` completed
without a session because the source was nonempty. It then selected the project,
new branch and external main source, entered main, and read the
[exact captured source confirmation](schema/0013-schema-lab-live-main-exact-source-policy.png).
Session create `af7f10e1-37e9-46d1-b041-845e58c4cc85` created UUID
`f0b5eb55-7ac7-4f3e-8d90-2cc65bf63b56`; the LLM inspected progress and
[actual native terminal entry](schema/0016-schema-lab-live-corrected-sample-shell.png).

[Independent source and runtime observations](schema/0044-schema-lab-independent-eleven-source-byte-equalities-runtime-image.png)
prove byte equality with `/etc/p-notes-example` for all 11 behavior files:
`db.py`, `database.py`, `worker.py`, `web.py`, `install.py`, `manage.py`,
`client.py`, `tests.py`, and the three unit files. README is excluded from
that behavior comparison. Exact native runtime image fingerprint is
`7acaaa4b4be97979327d932938564675d768bb4ba68a31d220b7935c1b80ecd9`.
The same fingerprint was retained in the later fresh removal preview.
The incidental `session.inspect` projection requested a nonexistent `runtime`
field and printed null; the native expanded configuration and loss preview
establish the image identity instead.

### Tests, normal readiness and all three journals

In the actual attached terminal, the LLM ran `python3 examples/notes/tests.py`.
[All 13 real PostgreSQL/HTTP tests passed](schema/0017-schema-lab-native-thirteen-real-tests-default-install.png)
in 2.982s, with `SAMPLE_TEST_EXIT=0`. The default installer then exited zero,
using PostgreSQL 18.6 and Psycopg 3.3.4 with inherited session bus variables;
no manual bus exports were used.

The [installed inventory](schema/0019-schema-lab-installed-three-unit-inventory.png)
showed three unloaded units. Visible `s` controls started database, web and
worker separately, yielding [three active units](schema/0022-schema-lab-explicit-worker-start.png).
Re-entry ran the two directly affected tests again so their HTTP results
could be inspected: missing required schema returned 503, restored/compatible
schema returned 200, additional columns remained ready, and both tests passed
in 1.534s. The
[attached focused tests and normal live health/write](schema/0025-schema-lab-live-damaged-schema-and-healthy-http-write.png)
show `SCHEMA_TEST_EXIT=0`, database ready and an accepted pending note.
[Native SQL](schema/0026-schema-lab-native-processed-live-note.png)
then observed `corrected schema live sample note|processed|5`.

The LLM opened all three selected-unit Journal pages and read their signatures:
[database private socket and schema ready](schema/0030-schema-lab-database-journal-private-socket.png),
[web accepted note with durable queued job](schema/0038-schema-lab-web-journal-real-accepted-write.png),
and [worker processed job/note word count](schema/0042-schema-lab-worker-journal-real-processed-write.png).
Journal Back retained the selected unit. An immediate queued Journal request
while the returned inventory was still loading was refused as busy; the LLM
waited for the actual selected inventory before issuing the successful requests.
The [full action record](schema/actions.ndjson) retains both attempts. No UI
change or scripted navigation was substituted for these actual observations.

### Corrected-source fixture cleanup

The LLM stopped its owned sample and fully reviewed the fresh Delete preview,
including observed origin main, captured image/native identity, no local-only
commits, whole runtime-data removal and ignored pycache. The
[final preview page](schema/0052-schema-lab-loss-preview-page4.png)
preceded confirmation. Delete `1a09623b-0504-49fb-86d6-9513572c3052`
[completed/deleted](schema/0057-schema-lab-completed-session-delete-empty-project-fixture-cleanup.png).
The separate empty-project preview contained no sessions, attachments or P refs;
owner cleanup `1721c284-24a7-412e-9043-b309baeb9dd2`
[completed](schema/0058-schema-lab-cleanup-completed-preserved-p-seed.png).
Only the stopped p-ai runtime remained. The generated SSH fixture cleaned its
exact server/config/state, and the
[runner powered off](schema/0059-schema-lab-final-clean-poweroff.png) with exit 0
before the final serial integration run. The small `project.list` projection
used `.id` rather than the actual `.path` and printed nulls; no absence claim
relies on that projection. Exact completed cleanup and native absence are the
successful observations above.

Earlier 11-test sample records remain historical source evidence. Corrected
sample acceptance is the 13-test, byte-matched live result in this section;
final CLI57/VM58 and aggregate results are independently recorded by the primary
agent. Full terminal bytes remain in the ignored capture cache; extracted JSON
files are normalized operation responses, not raw PTY byte copies.

## Final session-list removal race review

A final native integration run exposed a session disappearing between inventory
snapshot and per-session inspection. The store now reads the page's complete
session records in one query; the RPC omits a missing observation only after
confirming that its session record has actually been removed. Surviving-record
missing authority, other errors and raw pagination cursors remain significant.
Focused control/daemon regressions and a fresh independent review passed before
this affected live walkthrough. UI code was unchanged.

The LLM used the dedicated task-owned disk and serial lock at 120×35 with
`/tmp/p-notes-race-lab-runner` pointing to
`/nix/store/8b8m97ivhkcv1sjrik4z750pns7pw27g-p-demo-vm`. The installed binary
was `/nix/store/k2ci9jcnfw3c1lxnbxjmsyf2mrhisxpz-p-0.1.0-dev/bin/p`, SHA256
`c07c9532296ce2d9d77a2f270c8fe8500cf3b92b58819401de216b76b3718101`.
[Actual binary and completion observations](race/0064-race-final-native-actual-completed-discard-delete-and-absence.png)
retain that identity. Earlier UI reviews remain unaffected evidence; this
checkpoint specifically accepts inventory navigation across removal. The exact
native runtime image fingerprint captured in both removal previews was
`311da5a776078c8d2027659e97927bf0d817491af15c080fe56206217a68566d`.

Manual owner setup created a generated localhost sample origin from the packaged
sample at `799dcac8d3cacc6f74dcfd4fa2e53c72b4e4fab`, listener 127.0.0.1:44801,
PID 956, UID 1000. The LLM visibly created `notes-race-review`, then selected
external main and entered `feat/race-review`. The
[captured source policy](race/0013-race-live-captured-source-policy.png)
preceded creation `d745a498-7f71-409f-9112-7e85b0b234c9`, session UUID
`b8f6de69-2d60-4eda-8177-f7cec3024df3`. Actual native entry verified HEAD
and wrote one disposable private home marker.

After detach and explicit Stop, the LLM read all three fresh Discard preview
pages: exact retained tip/native identity, no local-only commits or dirty source,
whole runtime-data removal, and final source/loss-operation fields. Confirming
Discard `76d40a1e-c50e-40e9-b858-680b4ecefaaa` removed the old session.
[Live inventory refresh](race/0028-race-live-refresh-inventory-during-discard-completion.png)
kept the preserved stopped p-ai session available. The
[actual retained branch list](race/0032-race-live-actual-retained-branch-list.png)
and [existing-branch confirmation](race/0033-race-live-existing-retained-source-confirmation.png)
showed the same tip. Reassignment `70a066ba-96d5-4a1a-a04a-6a19607a54d9`
created new UUID `76058257-76ab-45f6-9152-6e05646548e1`.
[Native retained-source/fresh-home proof](race/0037-race-live-retained-source-fresh-private-home.png)
showed unchanged HEAD and absence of the old private marker.

The LLM stopped the replacement and fully read the four-page fresh Delete
preview, including P reachability loss, observed external main containment,
exact native identity, no local-only commits, clean workspace and all runtime
removal. Confirmed Delete `456a4f73-2a0a-477f-955b-8236376a26cf` removed the
replacement and assigned ref. The live
[project counts](race/0061-race-final-actual-project-counts.png)
showed notes-race-review total zero; its
[exact scoped session inventory](race/0062-race-final-actual-owned-project-empty-inventory.png)
was empty. Independent frame 0064 then showed both removal operations
`completed/discarded` and `completed/deleted`, native Incus inventory containing
only preserved stopped p-ai, and no project refs.

Some early capture labels contain “completed” while their actual screens still
show a running operation; those frames are not completion proofs. A mistaken
project-row selection and premature owner commands were corrected by reopening
the browser, selecting the exact owned scope and verifying the owner prompt.
The successful final observations above establish acceptance; the
[full action record](race/actions.ndjson) preserves every attempt.

Only the reviewed empty task-owned project was removed by owner API:
`3a6b23da-2a57-40fb-97d2-6b9b5da5dedf` completed. The
[final exact project/session inventory](race/0067-race-final-cleanup-completed-exact-surviving-inventory.png)
showed only the earlier retained empty projects and p-ai. The generated origin
helper cleaned up its exact listener/config/state; a process-exit race emitted
a missing-proc warning, and cleanup returned true. The
[runner powered off](race/0068-race-final-clean-poweroff-serial-release.png)
with exit 0 before the serial slot passed to VM58. Developer disks and Pseed
were preserved. [Normalized API responses](race/api-observations.ndjson)
supplement the rendered/native proofs; ignored raw PTY capture remains intact.

This live review verifies the affected lifecycle/inventory experience. The
focused regressions establish the precise concurrent-deletion interleaving;
these adaptive screenshots do not claim to force that timing deterministically.
Final native58 and aggregate results passed as separately recorded integration
gates in the checkpoint above.

## Authorized journey matrix

This maps every provisioned-sample row of the
[delivery plan](../../tui-developer-workflow-plan.md#4-establish-cliapi-journeys-first)
to its CLI/API gate and completed live scope. The primary agent records native
selection results and aggregate acceptance in implementation progress;
references to a step identify its scenario rather than claiming that reading
its source is a passing run. VM58 covers reviewed major effects, while the
additional adaptive records retain flows deliberately outside its driver.

| Required journey | CLI/API gate | LLM-chosen live evidence |
|---|---|---|
| Start blank | [CLI57](../../../tests/integration/steps/57-developer-workflow.sh): unborn bootstrap/refusal and first push | Fresh sample entry, first commit/push and [real empty Services](candidate-final/0032-fresh-empty-services.png); subsequent captured branch source proves committed P main. |
| Import existing work | [Manual sample API walkthrough](origin/api-walkthrough.sh) and [actual responses](origin/api-observations.ndjson); VM17 preserves additional origin contracts | [Nonempty project-only completion](origin/0039-origin-live-confirm-nonempty-project-only.png), [explicit source](origin/0044-origin-live-exact-source-policy-confirmation.png), exact native HEAD. |
| Start another stream | CLI57 new and retained assignment scenarios | [New branch policy/progress Back](directory/0024-new-branch-progress-abandoned.png) and [existing retained assignment](directory/0061-retained-existing-source-policy.png), with literal input and no forced entry after Back. |
| Set up a new session | CLI57 independent installer/cluster setup | [Default installer](directory/0030-new-branch-documented-installer.png), [fresh empty SQL](directory/0067-retained-empty-sql-schema-proof.png), and [no copied cluster after SSH import](origin/0049-origin-live-source-private-tools-default-bus-proof.png). |
| Edit, test and run | CLI57 failing/fixed sample tests and actual SQL/HTTP | [Actual failure](candidate-final/0047-actual-edit-failure.png), [fixed test](candidate-final/0048-edit-fixed-test.png), [historical SQL/HTTP suite](candidate-final/0045-sample-real-queue-tests.png), then [corrected 13-test attached suite](schema/0017-schema-lab-native-thirteen-real-tests-default-install.png), damaged-schema refusal and processed private notes. |
| Leave and return | CLI57 attachment lease and PID/SQL continuity; VM56 attachment gate | Repeated entry/detach frames and [same native tmux/service PIDs](extra/0041-extra-post-daemon-restart-native-continuity.png). |
| Manage three services | CLI57 and VM56 individual native actions/journals | Installed/active/mixed/failed inventories, selected-unit Help/Journal Back, [actual selected-worker Restart PID change](extra/0047-extra-worker-pid-delta-current-sample-proof.png) with sibling PIDs retained; all three [DB](schema/0030-schema-lab-database-journal-private-socket.png), [web](schema/0038-schema-lab-web-journal-real-accepted-write.png), [worker](schema/0042-schema-lab-worker-journal-real-processed-write.png) Journal pages read. |
| Pause and resume worker | CLI57 pending jobs/interruption and real transaction tests | Actual worker Stop, pending accepted note, explicit Start and processed result; [native suite kills worker mid-transaction](candidate-final/0045-sample-real-queue-tests.png) and proves pending/once recovery. |
| Recover database availability | CLI57 database stop/dependent failure/refused-write checks | [All dependents stop and client refuses write](extra/0023-extra-db-outage-native-refused-write.png), explicit three Starts and [no refused SQL row](extra/0031-extra-recovered-row-and-pid-baseline.png). |
| Stop and resume | CLI57 preserved UUID/source/private SQL/units and ended native processes | Default-No and confirmed Stop, Enter/Start, [retained source/private data and stopped units](notice/0039-stop-start-private-source-services-proof.png), followed by explicit service Starts; later [local unpushed commit and pending job across full Stop/Start](final-lab/0045-final-lab-live-poststop-pending-job-sql-retained.png) and [completed worker recovery with private/source continuity](final-lab/0056-final-lab-native-two-processed-commit-private-source-retained.png). |
| Work in two sessions | CLI57 two private databases/queues/schema | [Independent branch SQL](directory/0037-new-branch-private-sql-proof.png), [main retains data](directory/0040-main-private-isolation-proof.png), [schema change in A](directory/0070-main-schema-change-private.png), [B unchanged](directory/0073-branch-schema-unchanged-proof.png). |
| Restart daemon | CLI57 exact processes/principals/ref continuity | External owner restart [actually changes daemon PID](extra/0038-extra-actual-restarted-daemon-native-proof.png), visible browser reopens and [tmux/service/native/source identity remains](extra/0041-extra-post-daemon-restart-native-continuity.png). |
| Relaunch persistent lab | [Persistence helper](../../../tests/integration/notes-lab-persistence.sh), [prepare](persistence/prepare.png), [relaunch](persistence/relaunch.png) | Reopened real picker/native attachment after clean relaunch and [source/private/SQL continuity](terminal/0044-final-post-recovery-source-sql-continuity.png); no claim processes survive VM shutdown. |
| Retain and publish | Manual sample API source/response proofs above; VM20 preserves publication contracts | Actual terminal [P-only push](origin/0058-origin-live-fresh-private-sql-source-push.png), owner [fresh publication preview](origin/0061-origin-live-owner-preview-push-not-auto-published.png), explicit [exact native source/remote tip verification](origin/0084-origin-owner-final-cleanup-console.png). |
| Rename stream | CLI57 exact UUID/ref/upstream/private-data checks | [Literal rename confirmation](directory/0043-rename-branch-review.png) and [corrected native renamed ref](directory/0048-renamed-p-ref-proof.png), same UUID and SQL. |
| Discard, resume and delete | CLI57 fresh-data retained assignment, native/ref absence and sibling preservation | [Discard completed](directory/0057-renamed-discard-completed.png), retained source/new UUID/fresh SQL, default-No and [Delete completed](directory/0084-retained-delete-completed.png), independent branch/native absence and owner aggregate cleanup; [final deletion-race inventory review](#final-session-list-removal-race-review) repeats Discard/reassign/Delete on the corrected binary. |
| Recover failure | Existing native creation/recovery gates plus actual API blocked-origin same-intent recovery above | Operations actual blocked helper fault, [same-op cleanup recovery](terminal/0029-same-operation-cleanup-recovered.png), inert finalized Retry and [fresh successful inspection](terminal/0035-fresh-post-recovery-preview-completed.png). Scope is cleanup recovery, not same-op reanalysis. |
| Real devShell extension | Separate offline input-resolution prerequisite | Pending separate extension; provisioned runtime/Python/PostgreSQL proofs do not establish committed default-devShell realization. |

All supported live provisioned-sample rows above have actual navigation/native
evidence. The real devShell extension remains explicitly outside that acceptance;
[fresh lab setup and corrected-source acceptance](#corrected-schema-sample-actual-attached-validation)
are completed above. VM58 and the final aggregate checkpoint passed as separate
integration gates; their passing markers do not substitute for adaptive review.


## Layout and session information review

[layout/checkpoint.json](layout/checkpoint.json) records the independently
accepted layout and selected-session information work. Curated PNGs compare
current prototype, production baseline and final 120×35 / 80×24 / 48×16 views,
including compact details, short-wide resize and observed service states.
The records distinguish live LLM choices from the 27-test scripted TUI gate.
Before/prototype screenshots were reconstructed from retained raw PTY bytes
with the corrected CSI Z and tab-stop parser; final captures use that parser
live. Source and artifact hashes bind each retained result to its scope.
