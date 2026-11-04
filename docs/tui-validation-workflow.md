# TUI change and validation workflow

Apply this workflow to changes in TUI layout, navigation, controls, feedback,
or terminal handoff, and when assessing the implemented experience. An LLM
must navigate and review the actual rendered product before its experience is
accepted or its workflow is encoded in the integration driver. Passing driver
checks alone does not satisfy this review.

1. **Establish the intended experience.** Read the
   [reviewed prototype decisions](../.prototype/tui-options/DECISIONS.md), the
   TUI's current scope in [control protocol and clients](architecture/control-protocol-and-clients.md#the-tui),
   and the keys and limitations that the archived
   [user guide](archive/user-guide.md#use-the-terminal-browser) recorded at 471a3eb.
   Run the current prototype using its [launch instructions](../.prototype/tui-options/README.md#run-the-current-browser)
   and inspect the affected interaction. Use its selected browser as the
   comparison baseline; the older gallery captures are historical evidence.
   Identify the developer's task, expected sequence and visible outcome.
   Record prototype features outside implemented scope separately from
   mismatches in supported behavior; lifecycle and authority contracts still
   govern production behavior.
2. **Observe the current product before changing it.** For presentation and
   navigation, use `just tui-mock` to run the production `p tui` against its
   [mock socket](archive/development-validations.md#mock-socket-tui-exploration-and-fast-integration). For backend
   behavior, use a real daemon and Incus sessions in the
   [P lab](../dev/vm/README.md#explore-p) or an equivalent disposable instance.
   The LLM chooses actions from the
   current screen, reads the resulting screen, and continues through the
   task. Capture the states and transitions that explain any mismatch with
   the prototype, including errors or unavailable actions. API observations
   may confirm effects, but navigation must use the visible product controls.
3. **Implement and review the changed experience.** Run relevant component
   tests, then have the LLM repeat the task on the changed product, inspecting
   screens between actions. Compare layout, spacing, hierarchy, selection,
   command placement, readable feedback and Back/cancel behavior with the
   prototype. For layout changes, compare at matched dimensions, including
   120×35 and 80×24; inspect 48×16 when changing compact presentation. Exercise
   affected loading/error/empty states and resizing. For terminal handoff,
   inspect entry, command execution, detach and repeated entry, including
   intermediate frames that could expose the previous console. Fix discovered
   mismatches and repeat the affected path before accepting the experience.
4. **Validate integration at the affected boundary.** After the LLM experience
   review passes, run `just tui-tests` for socket-backed navigation, action
   wiring, cancellation, and local terminal transport. Add or update its action
   coverage for the reviewed workflow. If production backend behavior or its
   boundary changes, also run the smallest relevant serial VM selection and
   update major workflow coverage where needed. Use actual backend effects
   to establish completion: creation and branch assignment, native terminal
   execution, Git updates, attachment teardown, service state/journals, and
   reviewed cleanup with native absence. Screen recognition may synchronize
   actions; keep detailed navigation invariants in focused component tests.
   A changed heading or key sequence in the driver cannot resolve a product
   experience mismatch. Existing driver checks may help reproduce a failure
   during investigation; they do not replace the LLM review.
5. **Record both kinds of evidence.** Record the tested revision/build,
   instance setup and fresh/persistent state, terminal dimensions, actions,
   representative before/after screens, findings and their resolution in the
   [interactive LLM review evidence](llm-interactive-review/tui/README.md).
   Identify the LLM experience-review result separately from component and VM
   results. Store its screenshots and action records in that directory,
   separately from automated integration-test implementations.
   After any further product change, repeat the affected experience review
   and relevant integration checks. Preserve unaffected evidence with its scope.

Use terminal screenshots or faithfully rendered terminal frames that retain
cell positions, styling and cursor/selection state. Read the reconstructed
screen; raw ANSI output, text matches and post-run transcripts alone cannot
establish presentation or navigation quality. A PTY or capture helper can
provide input/output, but a complete scripted run followed by an LLM reading
its pass marker is not an interactive experience review. Include enough
transition evidence to assess loading and terminal handoff, beyond final
screens. Keep credentials out of captures.

The review passes when the affected developer task completes through visible
controls, its screens and transitions match the reviewed direction within
documented product scope, and discovered experience defects are resolved.
Major integration checks must also pass before claiming the implemented
workflow works end to end. If actual terminal access or an integration run is
unavailable, record the missing evidence and leave that acceptance pending;
do not substitute fixture snapshots or a scripted pass marker for the live
review. Interactive navigation of the production TUI against the mock socket
can establish presentation and navigation evidence. Record that scope; actual
Incus, Git, systemd, isolation, and durable-storage acceptance requires native
integration evidence.
