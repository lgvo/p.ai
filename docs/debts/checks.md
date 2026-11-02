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
