# Behavior that differs from its document

## Stop does not force a guest that ignores the graceful shutdown

- Where: internal/runtimeincus backend.go `transition`; docs/session-lifecycle.md "Stop"
- Problem: The Stop section says Stop "uses its documented forced termination after the
  timeout", but `transition` sends only `incus stop <name> --timeout 30`, which Incus runs
  as a graceful shutdown with no forced fallback. A guest that has not stopped after 30 s
  leaves Stop failed and the session running.
- Evidence: Incus 7.4.0 `cmd/incusd/instance_state.go` calls only `inst.Shutdown(timeout)`
  for a stop without `--force`; the stop-window experiments of 2026-11-02 saw Incus exit 1
  with `Failed shutting down instance, status is "Running"` and the instance still running.
- Risk: a user who reads the document expects Stop to end a hung session; it fails instead
  and needs a retry, or a discard or delete, which force-stop.
- Fix: decide which holds: force-stop after the graceful timeout in `transition`, or a
  document that says a stop that times out fails and leaves the session running.
- Fix when: the runtime driver implements Stop (rework/runtime-driver.md: "`Stop` is
  graceful and bounded"), or the next change to how P stops a session.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "wait for the guest's systemd before a graceful stop" (step 0j) found it
    while documenting the wait.
