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

## An observer test waits a fixed tenth of a second for a redraw

- Where: tests/integration/tui-observe-test.py, `test_real_pty_expansion_renders_tabs_in_detail_column`
- Problem: The test resizes a real PTY and reads the frame after a fixed 0.1-second wait; the
  child redraws only when its SIGWINCH handler runs, so on a loaded machine the frame can still
  be blank.
- Evidence: 2026-11-03, `just tui-tests` failed once in three runs ("'      ' != 'DETAIL'") while
  two VM gates ran on the machine; the other runs passed. It tests the observation tool, not the
  product.
- Risk: `just tui-tests` fails at random under load and can hide a real failure behind reruns.
- Fix: wait until the frame shows the redraw, with a deadline, instead of a fixed delay.
- Fix when: the next change to tui-observe.py or its tests, or the next time it fails a gate.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "waiting" saw it fail once while running the TUI tests for its third commit.
