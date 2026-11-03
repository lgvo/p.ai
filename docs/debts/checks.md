# Checks

## No committed VM check builds through a public-egress builder

- Where: tests/integration/steps (the builder scenarios 21, 22, 24–26 and 53 build offline;
  scenario 37, the only VM with the public bridge, creates no builder); internal/runtimeincus
  builder.go `prepareBuilderNetwork`, builder_image.go `scrubBuilderResolver`;
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
- Added 2026-11-02, updated 2026-11-02, 3 cases:
  - 2026-11-02: "give builders public internet access only" gave builders public egress
    with unit tests only.
  - 2026-11-02: "fold the environment plugin into the image build" deleted scenario 23 and
    moved scenarios 24 and 25 to the native build; they still build offline.
  - 2026-11-02: "one image per source commit" moved the build into the runtime driver
    (`runtimeincus.Driver` `BuildImage`), which met the old "Fix when", without adding
    the check: it still needs VM-local fixtures for the flake input, the binary cache and
    the DNS-over-HTTPS upstreams, so "Fix when" now names only the next public-egress
    acceptance claim.

## A runtime driver test records a systemd poll its client abandoned

- Where: internal/runtimeincus graceful_stop_test.go, `TestStopIsSentWhenGuestSystemdNeverComesUp`
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
