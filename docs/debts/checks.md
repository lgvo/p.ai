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

## Notes TUI scenario expects the old session-row format

- Where: tests/integration/tui_notes_driver.py `run_notes` (its `page_ready(...,
  selected_state=...)` calls) and `page_ready`'s timeout label
- Problem: The driver waits for a selected row containing `› notes58 / main`, but the browser
  draws aligned tree columns (`› notes58   └─ main   …   stopped`) since 0dfc4ad, so
  `58-notes-tui.sh` always times out after Stop. Its timeout label omits the expected row, so
  the failure reads as a missing title.
- Evidence: scenario 58 alone fails at tui_notes_driver.py line 279 on unchanged code
  (a9fce20); the full diagnostic screen shows the title and the tree-style selected row. A
  five-second mock reproduction is in the scratchpad (rootcause58-work/repro.py). The fast
  mock suite never asserts session-row text, so no fast gate caught it.
- Risk: the native TUI walkthrough of the three-service workflow is not exercised.
- Fix: match the selected row by project and branch parts (a regex over the tree columns) at
  the four calls that use the old format, and include the expected row in the timeout label.
- Fix when: before the next native TUI acceptance claim.
- Left by: the agent, by rule (a problem outside the change, traced with find-root-cause).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" found scenario 58 failing at baseline.
