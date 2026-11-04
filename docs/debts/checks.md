# Checks

## No committed VM check builds through a public-egress builder

- Where: tests/integration/steps (the builder scenarios 21, 22, 24–26 and 53 build offline;
  scenario 37, the only VM with the public bridge, creates no builder);
  internal/runtimedriver/incus builder.go `prepareBuilderNetwork`, builder_image.go `scrubBuilderResolver`;
  runtime/image.nix `trusted-substituters`
- Problem: A public builder's NIC, guest network, online Nix with the public cache and the
  image's restored resolver link are checked only by unit tests against a fake Incus. A VM
  check would depend on third parties (the DNS-over-HTTPS upstreams, GitHub and
  cache.nixos.org), which the step's brief rules out for new checks.
- Evidence: on 2026-11-02 an exploratory, uncommitted variant of scenario 37 built a `none`
  session's devShell from a locked nixpkgs input (`pkgs.hello`) through a public builder at
  10.233.0.10: from inside the builder 1.1.1.1:443 and cache.nixos.org answered, while the
  bridge gateway, the VM host, 192.168/16, 172.16/12, CGNAT, the metadata address, QEMU's
  host gateway and port 22 did not; the session ran `hello`. No committed check repeats it.
- Risk: a regression in a public builder's network, the image's trusted substituter or the
  resolver scrub goes unnoticed until a real devShell fails to build.
- Fix: run a public builder in the public-egress VM against VM-local fixtures on
  public-looking addresses (as scenario 37's DNAT target does) for the flake input, the
  binary cache and the DNS-over-HTTPS upstreams.
- Fix when: before the next public-egress acceptance claim.
- Left by: the agent, by rule (behavior left untested: the step's brief forbids new checks
  that depend on external services).
- Added 2026-11-02, updated 2026-11-03, 4 cases:
  - 2026-11-02: "give builders public internet access only" gave builders public egress
    with unit tests only.
  - 2026-11-02: "fold the environment plugin into the image build" deleted scenario 23 and
    moved scenarios 24 and 25 to the native build; they still build offline.
  - 2026-11-02: "one image per source commit" moved the build into the runtime driver
    (`runtimeincus.Driver` `BuildImage`), which met the old "Fix when", without adding
    the check: it still needs VM-local fixtures for the flake input, the binary cache and
    the DNS-over-HTTPS upstreams, so "Fix when" now names only the next public-egress
    acceptance claim.
  - 2026-11-03: "core holds only the runtime driver" moved the driver to
    internal/runtimedriver/incus and made its builder policy, the builder's network
    included, part of `Describe`; the check is still missing.

## A runtime driver test records a systemd poll its client abandoned

- Where: internal/runtimedriver/incus graceful_stop_test.go, `TestStopIsSentWhenGuestSystemdNeverComesUp`
  and its `fakeGuest`
- Problem: The test asserts that the fake guest saw "systemd not up", then the graceful stop,
  then the forced stop, in that order. When `awaitGuestSystemd`'s 30-second wait ends during
  a poll, the client abandons that request, but the fake guest's handler can still record
  "systemd not up" after the stop was sent; the order the test asserts then fails.
- Evidence: 2026-11-03, the package build inside `./dev/test-vm` for scenarios 56 and 58 failed
  once with "graceful stop not sent after the wait, then forced: 304 events, last [stop
  --timeout 30 systemd not up stop --force]" while another lane's VM gate ran; the same tree
  passed `just unit-tests`, two `nix build .#default` runs and the VM gate's builds. The
  mechanism is read from the code, not reproduced.
- Risk: the package build, and with it any VM run, fails at random under load, and reruns can
  hide a real failure of the graceful stop.
- Fix: record a poll only when its client still waits for it, or assert the stop's order
  without the abandoned poll.
- Fix when: the next change to the graceful stop or its tests, or the next time it fails a gate.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "TUI T5 and T6" saw it fail once while building the VM for scenarios 56 and 58.

## The conformance suite never runs against real Incus

- Where: internal/runtimedriver/conformance (its `Fixture` controls);
  tests/integration/steps/06-runtime-incus.sh; internal/runtimedriver/incus conformance_test.go
- Problem: rework/runtime-driver.md ("How it will be checked") expects scenario 06 to run
  the runtime driver conformance suite against real Incus. The suite runs only against the
  in-memory driver, the Incus driver over a stubbed Incus, and the in-memory driver over
  the driver protocol. Scenario 06 still drives the Incus driver through its own fixture
  commands.
- Evidence: 2026-11-03, `grep -rln runtimedriver/conformance tests dev` finds nothing; the
  suite's controls (`Restart`, held and delayed effects, renamed runtimes, foreign mounts)
  exist only for the in-memory world and the stubbed Incus.
- Risk: a difference between real Incus and the stub, in creation, settlement after a
  driver restart, stopped storage or terminals, passes the suite and shows only in the
  scenarios that happen to cover it.
- Fix: a real-Incus fixture for the conformance controls, run from scenario 06 in the VM
  (the controls that real Incus cannot provide skip their cases there).
- Fix when: the next change to the conformance suite or the Incus driver's effects.
- Left by: the agent, by rule (a check the plan expected and the step did not add).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "the runtime driver as its own executable" (step 8, M7) finished the
    runtime driver without it.

## Two TUI mock tests read the screen a fixed time after a resize

- Where: tests/integration/tui-mock-test.py, `test_01_navigation_reports_policy_branches_operations_search_resize`
  (its resize loop reads "X delete", "D details" and the page size "1–N of 120" 100 ms after
  the resize and 40 ms after `gg`) and `test_03_attachment_stop_start_rename_and_cancel`
  (reads the tmux pane's size 150 ms after a resize)
- Problem: Each reads the screen, or the pane, once, a fixed time after the resize, so when the
  TUI takes longer to redraw it reads the previous size: the resize loop then expects a page
  of the old size and fails.
- Evidence: 2026-11-03, read from the code. Under 16 busy loops pinned to the tests' two CPUs,
  the same kind of read a fixed time after a key failed in test_01's movement and resize
  loops (now waits); these reads after a resize did not fail in those runs.
- Risk: `just tui-tests` fails at random on a heavily loaded machine.
- Fix: after a resize, wait with a deadline for a frame drawn at the new size (for example
  the panel border at the new width) before reading it.
- Fix when: the next change to these tests, or the next time one fails a check.
- Left by: the agent, by rule (a problem outside the change, suspected only).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "wait for the sessions to load before the TUI mock tests act" found it while
    proving that fix under load.
