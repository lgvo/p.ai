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

## Notes TUI scenario's Stop can be refused while a services read holds the session lock

- Where: tests/integration/tui_notes_driver.py `stop_selected`; internal/daemon services.go
  `SessionServices` and lifecycle.go `StopSession` (both take `lockSession`, which never waits)
- Problem: Scenario 58 confirms Stop once and waits for `stopped`. Stop answers `busy` when
  the session lock is held, and `SessionServices`, which the TUI calls every 3 seconds for the
  selected session, holds that lock while it runs `runtime.services` in the container. A Stop
  that lands during that read is refused, and the driver does not retry it.
- Evidence: step 0h's VM gate, 2026-11-02: scenario 58 failed in `stop_selected` with the TUI
  showing "busy: lifecycle request conflicts with current authority" after Stop's
  confirmation; it passed on rerun and in the gates of steps 0b, 0c, 0d and 0f. The cause is
  inferred from the code, not reproduced.
- Risk: the VM gate fails now and then on scenario 58, and a TUI user can see Stop refused as
  busy right after selecting a session.
- Fix: let a mutation wait briefly for a read-only holder of the session lock, or let the
  services list run without the exclusive lock.
- Fix when: scenario 58 fails this way again, or step 4 makes Stop an operation.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "remove session repair" saw it once in its VM gate; the rerun passed.
