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

- Where: tests/integration/steps (the builder scenarios 21–26 and 53 build offline; scenario
  37, the only VM with the public bridge, creates no builder); internal/runtimeincus
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
- Fix when: before the next public-egress acceptance claim, or when step 1 moves the build
  into the runtime driver.
- Left by: the agent, by rule (behavior left untested: the step's brief forbids new checks
  that depend on external services).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "give builders public internet access only" gave builders public egress
    with unit tests only.
