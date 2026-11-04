# Archived design record

These are P's design, status, plan and interface-rework documents as they
stood at commit 471a3eb (2026-11-04), when the maintainer archived them. They
are kept for history and for the reasons behind past decisions. They are not
authority: the [architecture overview](../architecture/README.md) is the
current account of the system, and the code is the source of truth.

The documents are not maintained. Each keeps the content it had when it was
archived, with two kinds of edit: links fixed so they still resolve, and the
TUI workflow section of development-validations.md replaced by a pointer to
its live copy. Words such as "current", "today" or "authoritative" in them
refer to the time they were archived.

## Design, status and plan documents

| Document | What it covered |
|---|---|
| [communication-boundaries.md](communication-boundaries.md) | What crosses the Git, RPC, SSH, attachment, model-gateway, build and event-handler channels |
| [control-api.md](control-api.md) | The host control connection and private per-session RPC: framing, method schemas, error kinds and codes |
| [design-space.md](design-space.md) | Neighboring projects, P's positioning and validation wedges |
| [development-validations.md](development-validations.md) | Evidence gates for support claims; its TUI workflow section moved to the live [TUI change and validation workflow](../tui-validation-workflow.md) |
| [environment-building.md](environment-building.md) | One immutable image per source commit, holding its Nix environment and code, and when the image is deleted |
| [FAQ.md](FAQ.md) | Explanations and tradeoffs behind the design |
| [implementation-plan.md](implementation-plan.md) | The delivery plan for a testable MVP (2026-09-23) |
| [implementation-progress.md](implementation-progress.md) | The log of implementation checkpoints, VM evidence and live TUI reviews |
| [missing-pieces.md](missing-pieces.md) | A checklist of remaining implementation work |
| [model-gateway.md](model-gateway.md) | The retained post-MVP design for session model access through Bifrost |
| [mvp-status.md](mvp-status.md) | A snapshot of MVP scope and readiness (2026-10-01) |
| [nix-project-workflow-validation.md](nix-project-workflow-validation.md) | A manual experiment validating the Incus and Nix environment model against a real Nix repository |
| [plugin-contract.md](plugin-contract.md) | Plugin packaging, discovery, selection, launch checks, the event-handler protocol and the agent adapter's files |
| [PR.md](PR.md) | A working-backwards press release for the MVP |
| [project-lifecycle.md](project-lifecycle.md) | Project identity, creation, origins, retained branches and project deletion |
| [runtime-isolation.md](runtime-isolation.md) | Runtime placement, systemd hosting, grants, storage, networking, attachment execution and cleanup |
| [session-lifecycle.md](session-lifecycle.md) | Session identity, operations, loss analysis and recovery |
| [session-observability.md](session-observability.md) | Session conditions, attachment presence, status reduction and P events |
| [technology-stack.md](technology-stack.md) | Implementation choices, internal seams and dependency policy |
| [tui-developer-workflow-plan.md](tui-developer-workflow-plan.md) | The delivery plan for the TUI experience and the developer workflow (2026-09-30) |
| [user-guide.md](user-guide.md) | How to use P: projects, sessions, the terminal browser, publishing and cleanup |

## Interface rework

The interface rework of 2026-11-02 to 2026-11-04 made P's interfaces smaller.
[rework/REWORK-PLAN.md](rework/REWORK-PLAN.md) is its plan and status; the
other documents are the step designs and decisions it links.

| Document | What it covered |
|---|---|
| [rework/REWORK-PLAN.md](rework/REWORK-PLAN.md) | The plan, its steps and their status (done) |
| [rework/cli.md](rework/cli.md) | Task commands in the CLI |
| [rework/commit-images.md](rework/commit-images.md) | One image per source commit |
| [rework/errors.md](rework/errors.md) | Error kinds that name the next step |
| [rework/plan-shape.md](rework/plan-shape.md) | The shape of a dry run's plan and its loss list |
| [rework/plugins.md](rework/plugins.md) | Native plugins, and which plugin packages were folded into P |
| [rework/repairs.md](rework/repairs.md) | The decision to remove session repair |
| [rework/runtime-driver.md](rework/runtime-driver.md) | One runtime driver for every runtime effect |
| [rework/services-plugin.md](rework/services-plugin.md) | Project services in the runtime driver |
| [rework/source-driver.md](rework/source-driver.md) | One source driver for every Git effect |
| [rework/state-hash-confirmation.md](rework/state-hash-confirmation.md) | Confirming risky changes with change tags |
| [rework/waiting.md](rework/waiting.md) | Waiting for changes to finish |
| [rework/wire-types.md](rework/wire-types.md) | One request type and one result type per method |
