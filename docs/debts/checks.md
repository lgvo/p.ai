# Checks

## Public-egress scenario depends on an external redirect service

- Where: tests/integration/steps/37-public-egress.sh (the redirect-denial probe)
- Problem: The scenario proves that a session cannot follow a public redirect to an internal
  address by asking an external redirect service to issue that redirect. The service now
  refuses with `403 Forbidden redirect target: redirect target is an internal address`, so
  the scenario stops with "redirect service failure is not denial evidence".
- Evidence: baseline run on unchanged code (a9fce20), 2026-11-01: every other public probe in
  the scenario passed, then this probe failed at line 414.
- Risk: the public-egress gate cannot pass, and a full VM pass never reaches scenarios 38–58.
- Fix: serve the redirect from a fixture the VM controls, so the denial check no longer
  depends on a third party's policy.
- Fix when: before the next public-egress acceptance claim.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" found it while taking the VM baseline.

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

## The pending manual Codex acceptance still inspects workspace loss

- Where: docs/implementation-progress.md, "Manual Codex acceptance — pending user
  validation", step 5 (the Discard and Delete flow)
- Problem: The procedure the user is to run calls `workspace.loss.inspect` and passes
  `loss_operation_id` to `session.removal.preview`. Groundwork step 0e removed the loss
  inspection, so step 5 fails at its first call, and the preview refuses the unknown field.
- Evidence: `api.Methods()` has no `workspace.loss.inspect`;
  `api.SessionRemovalPreviewRequest` has no `loss_operation_id`, and strict decoding refuses
  it (TestUnconfiguredAndUnknownMethodAnswers, TestRemovalPreviewRPCUsesClosedRequest).
- Risk: the user's manual acceptance stops at Discard/Delete, or is misread as a product
  failure.
- Fix: rewrite step 5 as preview, review and confirm, without the loss inspection.
- Fix when: before the user runs the manual Codex acceptance.
- Left by: the agent, by rule (a problem outside the change: the wire types step only removed
  `v` from the procedure's calls).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "wire types" found it while moving the procedure's calls off `"v":1`.
