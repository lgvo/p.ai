"""Major notes integration effects, added only after adaptive live review.

Fixtures supply source and controlled failures; visible controls perform the
developer actions. Native SQL/process/Git observations establish completion.
"""
import json
import os
import re
from pathlib import Path


def run_notes(driver):
    send = driver["send"]
    expect = driver["expect"]
    driver_await_state = driver["await_state"]
    driver_rpc = driver["rpc"]
    observe_process = driver["observe_process"]
    socket = driver["socket"]
    uuid_file = Path(driver["uuid_file"])
    owned_file = uuid_file.with_name("owned-uuids")
    project = "notes58"
    observation_depth = 0
    read_methods = {"session.list", "session.inspect", "session.services", "session.service.journal",
                    "project.branches", "project.retained_branches"}

    def await_state(test, label):
        def observation():
            nonlocal observation_depth
            observation_depth += 1
            try:
                return test()
            finally:
                observation_depth -= 1
        return driver_await_state(observation, label)

    def rpc(method, **params):
        assert method in read_methods, "notes observation cannot retry a mutation: " + method
        if observation_depth:
            # The enclosing wait owns the sole deadline and explicit busy
            # retry. Never start an inner wait for an observation predicate.
            return driver_rpc(method, **params)
        # Direct reads get the same bounded explicit-BusyObservation handling.
        # A tuple keeps even an empty successful result truthy; its contents
        # are asserted by the caller without retrying a negative/default-No
        # outcome until it becomes positive.
        return await_state(lambda: (driver_rpc(method, **params),), "notes read " + method)[0]

    def page_ready(title, *content, selected_state=None):
        # A heading renders before an asynchronous inventory completes.
        # Require the actual expected options/review controls before input.
        blocked = ("Waiting for daemon", "busy:", "invalid_params:", "unavailable:",
                   "internal:", "conflict:", "not_found:", "STALE ·",
                   "Origin observation unavailable", "Origin observation is stale")
        def ready():
            screen = driver["plain"]()
            row_ready = selected_state is None or any(
                selected_state[0] in line and selected_state[1] in line for line in screen.splitlines())
            return (all(token in screen for token in (title, *content))
                    and not any(token in screen for token in blocked) and row_ready)
        await_state(ready, "ready " + title + " with " + ", ".join(content))

    def fixture(uuid, mode, value=None):
        argv = ["python3", os.environ["P_TEST_SOURCE"] + "/tests/integration/notes-tui-fixture.py",
                "--socket", socket, "--uuid", uuid, mode]
        if value is not None:
            argv.append(value)
        result = observe_process(argv, timeout=120, text=True)
        assert result.returncode == 0, (mode, result.stdout[-2000:], result.stderr[-2000:])
        return result.stdout.strip()

    def snapshot(uuid):
        observed = json.loads(fixture(uuid, "snapshot"))
        # Keep the fixture's HTTP response wrapper intact. Only its inner
        # notes list represents application rows, not the wrapper's key count.
        assert observed["uuid"] == uuid, observed
        assert isinstance(observed["notes"], dict) and isinstance(observed["notes"]["notes"], list), observed
        assert len(observed["pids"]) == 3 and all(pid.isdigit() for pid in observed["pids"]), observed
        assert observed["tmux_pid"].isdigit() and int(observed["tmux_pid"]) > 0, observed
        assert isinstance(observed["pending"], int) and isinstance(observed["jobs"], int), observed
        return observed

    def guest(uuid, *argv):
        result = observe_process(["incus", "--force-local", "--project", "user-1000", "exec", "p-" + uuid,
                                  "--force-noninteractive", "--disable-stdin", "--user", "1000", "--group", "1000",
                                  "--cwd", "/workspace", "--env", "HOME=/home/p",
                                  "--env", "XDG_RUNTIME_DIR=/run/user/1000",
                                  "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
                                  "--", *argv], timeout=40, text=True)
        assert result.returncode == 0, (argv, result.stdout[-2000:], result.stderr[-2000:])
        return result.stdout.strip()

    def sessions():
        return [item for item in rpc("session.list", limit=8)["sessions"] if item["project"] == project]

    def marker(uuid, name):
        return guest(uuid, "bash", "-c", 'if test -f "$1"; then cat -- "$1"; fi', "--", name)

    def attach(uuid):
        await_state(lambda: rpc("session.inspect", uuid=uuid)["session"]["attached_count"] == 1,
                    "confirmed notes attachment")

    def detach(uuid):
        send(b"\x02d")
        await_state(lambda: rpc("session.inspect", uuid=uuid)["session"]["attached_count"] == 0,
                    "notes attachment lease ended")
        expect("All project sessions")

    def service_state(uuid, unit, state):
        return any(item["unit"] == "p-project-notes-" + unit + ".service" and item["active_state"] == state
                   for item in rpc("session.services", uuid=uuid)["services"])

    service_states = {"active": ("active (running)",), "failed": ("failed (failed)",),
                      "inactive": ("installed", "inactive (dead)")}

    def service_ui_ready(unit, states):
        name = "p-project-notes-" + unit + ".service"
        def ready():
            screen = driver["plain"]()
            if "Project services" not in screen or any(message in screen for message in
                    ("Waiting for daemon", "Service observation unavailable", "controls disabled", "busy:", "STALE ·")):
                return False
            selected = next((line for line in screen.splitlines() if "› " + name in line), "")
            return (any(state in selected for state in states)
                    and "Enter/J journal" in screen
                    and ("s stop" if "active (running)" in selected else "s start") in screen)
        await_state(ready, "usable selected " + unit + " UI state " + "/".join(states))

    def wait_service(uuid, unit, state):
        # Finish the one-shot visible action before native observation can
        # contend with its authority lock. Other active rows prove nothing
        # about the selected unit's accepted action.
        service_ui_ready(unit, service_states[state])
        if state == "inactive":
            await_state(lambda: guest(uuid, "systemctl", "--user", "show", "p-project-notes-" + unit + ".service",
                                      "-p", "ActiveState", "--value") == "inactive",
                        unit + " native manager inactive")
        else:
            await_state(lambda: service_state(uuid, unit, state), unit + " native API state " + state)

    def service_action(uuid, unit, state, previous="inactive"):
        service_ui_ready(unit, service_states[previous])
        send("s")
        wait_service(uuid, unit, state)

    def service_journal(uuid, unit, content):
        name = "p-project-notes-" + unit + ".service"
        service_ui_ready(unit, service_states["active"] + service_states["failed"])
        send("\r"); expect(name + " · journal tail")

        def loaded():
            screen = driver["plain"]()
            heading = re.search(re.escape(name) + r" · journal tail · (\d+)–(\d+)/(\d+)", screen)
            if not heading or "Waiting for daemon" in screen:
                return False
            start, end, count = map(int, heading.groups())
            # An error leaves the new page at 1–0/0. Empty journal strings
            # may render one blank numbered row, which is also insufficient.
            return (0 < start <= end <= count
                    and re.search(r"(?m)^[ \t]*\d+[ \t]+\S", screen) is not None)

        await_state(loaded, "selected " + unit + " journal UI loaded visible rows")
        # The visible row can be clipped, and the read-only API observation
        # may briefly share authority with the UI's journal request.
        await_state(lambda: content in rpc("session.service.journal", uuid=uuid,
                                         unit=name).get("journal", ""),
                    "native " + unit + " journal contains " + content)

    # Actual blank project creation and Back editing precede first source.
    send("c"); page_ready("Create · project", "› Create a new project")
    send("\r"); expect("Project path>")
    send(project + "\r"); expect("SSH origin URL")
    send("\r"); page_ready("Create · policy review", "Enter create project")
    send("q"); expect("SSH origin URL")
    assert not sessions(), "Back from creation review accepted a session"
    send("\r"); page_ready("Create · policy review", "Enter create project")
    send("\r")
    created = await_state(lambda: next((item for item in sessions() if item["branch"] == "main" and item["attached_count"] == 1), None),
                          "notes bootstrap and confirmed terminal attachment")
    uuid = created["uuid"]
    uuid_file.write_text(uuid, encoding="ascii")
    with owned_file.open("a", encoding="ascii") as stream:
        stream.write(uuid + "\n")
    fixture(uuid, "seed")
    send("printf first-entry > /workspace/notes58-entry; git add examples/notes && git -c user.name=P -c user.email=p@example.invalid commit -qm notes58 && git push origin HEAD:main\r")
    await_state(lambda: marker(uuid, "notes58-entry") == "first-entry", "native first terminal command")
    await_state(lambda: any(ref["ref"] == "refs/heads/main" for ref in rpc("project.branches", project=project, limit=8)["refs"]),
                "interactive first Git push")
    source_oid = guest(uuid, "git", "rev-parse", "HEAD")
    assert any(ref["ref"] == "refs/heads/main" and ref["oid"] == source_oid
               for ref in rpc("project.branches", project=project, limit=8)["refs"])
    detach(uuid)

    # No installed units is an available empty inventory, not an API outage.
    fixture(uuid, "assert-empty")
    send("S"); expect("Project services")
    expect("No project units observed")
    send("q"); expect("All project sessions")
    send("\r"); attach(uuid)
    send("python3 examples/notes/install.py > /workspace/notes58-install.log 2>&1; printf '%s' \"$?\" > /workspace/notes58-install.status\r")
    await_state(lambda: marker(uuid, "notes58-install.status") == "0", "unexported real installer user-bus reload")
    detach(uuid)
    send("S"); expect("Project services")
    expect("p-project-notes-worker.service")
    service_action(uuid, "db", "active")
    send("j"); service_action(uuid, "web", "active")
    send("j"); service_action(uuid, "worker", "active")
    assert json.loads(guest(uuid, "python3", "examples/notes/client.py", "health"))["database"] == "ready"
    fixture(uuid, "add", "five words processed by worker")
    fixture(uuid, "wait-jobs")
    assert fixture(uuid, "sql", "SELECT word_count FROM notes WHERE id=1") == "5"

    # The plan requires real journal pages for all three selected units.
    # Native signatures verify content even when long rows are clipped.
    send("kk")
    service_journal(uuid, "db", "starting PostgreSQL: private socket=")
    send("q"); expect("Project services")
    send("j")
    service_journal(uuid, "web", "accepted note=1 with durable queued job")
    send("q"); expect("Project services")
    send("j")

    # Help and journal Back retain the selected worker. Subsequent Stop must
    # affect that unit, while database/web remain active and HTTP accepts jobs.
    before = snapshot(uuid)
    send("?"); expect("P · keys")
    send("q"); expect("Project services")
    service_journal(uuid, "worker", "processed job=")
    send("q"); expect("Project services")
    service_action(uuid, "worker", "inactive", previous="active")
    assert guest(uuid, "systemctl", "--user", "show", "p-project-notes-worker.service", "-p", "MainPID", "--value") == "0"
    assert service_state(uuid, "db", "active") and service_state(uuid, "web", "active")
    fixture(uuid, "add", "queued while worker is stopped")
    assert fixture(uuid, "sql", "SELECT count(*) FROM jobs WHERE completed_at IS NULL") == "1"
    service_action(uuid, "worker", "active")
    fixture(uuid, "wait-jobs")
    assert fixture(uuid, "sql", "SELECT word_count FROM notes WHERE id=2") == "5"
    assert guest(uuid, "systemctl", "--user", "show", "p-project-notes-db.service", "-p", "MainPID", "--value") == before["pids"][0]
    assert guest(uuid, "systemctl", "--user", "show", "p-project-notes-web.service", "-p", "MainPID", "--value") == before["pids"][1]

    fixture(uuid, "fail-worker")
    wait_service(uuid, "worker", "failed")
    service_journal(uuid, "worker", "signal")
    assert guest(uuid, "systemctl", "--user", "show", "p-project-notes-worker.service",
                 "-p", "Result", "--value") == "signal"
    send("q"); expect("Project services")
    service_action(uuid, "worker", "active", previous="failed")
    fixture(uuid, "wait-jobs")
    after = snapshot(uuid)
    assert after["notes"]["notes"] == [
        {"id": 1, "body": "five words processed by worker", "status": "processed", "word_count": 5},
        {"id": 2, "body": "queued while worker is stopped", "status": "processed", "word_count": 5},
    ] and after["pending"] == 0 and after["jobs"] == 2, after
    send("q"); expect("All project sessions")
    send("\r"); attach(uuid)
    send("printf repeated-entry > /workspace/notes58-entry\r")
    await_state(lambda: marker(uuid, "notes58-entry") == "repeated-entry", "native repeated terminal command")
    detach(uuid)
    repeated = snapshot(uuid)
    assert repeated["tmux_pid"] == after["tmux_pid"] and repeated["pids"] == after["pids"], (after, repeated)
    assert repeated["source"] == source_oid and repeated["notes"] == after["notes"], repeated

    # Stop is default-No, then explicit confirmation ends processes while
    # Start/Enter retains the same source, private files and application data.
    send("\r"); attach(uuid)
    send("printf private-retained > /home/p/notes58-private; printf dirty-retained > /workspace/notes58-dirty\r")
    await_state(lambda: marker(uuid, "notes58-dirty") == "dirty-retained", "native dirty file before Stop")
    detach(uuid)
    send("s"); page_ready("Confirm action", "UUID: " + uuid, "Confirm [y/N]")
    send("\r"); expect("All project sessions")
    assert rpc("session.inspect", uuid=uuid)["session"]["session_condition"] == "ready"
    unchanged = snapshot(uuid)
    assert unchanged["pids"] == repeated["pids"] and unchanged["tmux_pid"] == repeated["tmux_pid"], (repeated, unchanged)
    send("s"); page_ready("Confirm action", "UUID: " + uuid, "Confirm [y/N]")
    send("y")
    await_state(lambda: rpc("session.inspect", uuid=uuid)["session"]["session_condition"] == "stopped",
                "confirmed Stop ended notes runtime processes")
    expect("Stopped. Files and branch retained.")
    send("q")
    page_ready("All project sessions", selected_state=("› notes58 / main", "stopped"))
    send("\r"); attach(uuid)
    assert guest(uuid, "git", "rev-parse", "HEAD") == source_oid
    assert guest(uuid, "cat", "/home/p/notes58-private") == "private-retained"
    assert marker(uuid, "notes58-dirty") == "dirty-retained"
    assert guest(uuid, "systemctl", "--user", "show", "p-project-notes-db.service",
                 "p-project-notes-web.service", "p-project-notes-worker.service", "-p", "MainPID", "--value").split() == ["0", "0", "0"]
    detach(uuid)
    send("S"); expect("Project services")
    service_action(uuid, "db", "active")
    send("j"); service_action(uuid, "web", "active")
    send("j"); service_action(uuid, "worker", "active")
    resumed = snapshot(uuid)
    assert resumed["notes"] == repeated["notes"] and resumed["pending"] == 0 and resumed["jobs"] == 2, resumed
    send("q"); expect("All project sessions")

    # A second branch uses the pushed source, excluding private/dirty state.
    # Leaving accepted progress prevents automatic entry when it finishes.
    branch = "feat/very-long-notes-qgG-review-branch-name"
    send("c"); page_ready("Create · project", "› Create a new project", project)
    send("j"); page_ready("Create · project", "› " + project)
    send("\r"); page_ready("Create · branch", "› Create new branch")
    send("\r"); page_ready("Create · source", "› P · refs/heads/main")
    send("\r"); expect("New branch name>")
    send(branch + "\r"); page_ready("Create · policy review", source_oid, "Enter create, boot and enter")
    send("\r"); expect("Operation / readiness")
    send("q"); expect("All project sessions")
    branch_session = await_state(lambda: next((item for item in sessions()
                                              if item["branch"] == branch and item["session_condition"] == "ready"), None),
                                 "new branch ready after leaving accepted progress")
    branch_uuid = branch_session["uuid"]
    with owned_file.open("a", encoding="ascii") as stream:
        stream.write(branch_uuid + "\n")
    assert branch_uuid != uuid and branch_session["attached_count"] == 0
    assert guest(branch_uuid, "git", "rev-parse", "HEAD") == source_oid
    guest(branch_uuid, "test", "!", "-e", "notes58-dirty")
    guest(branch_uuid, "test", "!", "-e", "/home/p/notes58-private")
    fixture(branch_uuid, "assert-empty")
    page_ready("All project sessions", "feat/very-long-notes")
    send("j"); page_ready("All project sessions", "› notes58 / feat/very-long")
    send("\r"); attach(branch_uuid)
    send("python3 examples/notes/install.py > /workspace/notes58-install.log 2>&1; printf '%s' \"$?\" > /workspace/notes58-install.status\r")
    await_state(lambda: marker(branch_uuid, "notes58-install.status") == "0", "new branch real installer")
    send("printf branch-private > /home/p/notes58-branch-private\r")
    await_state(lambda: marker(branch_uuid, "/home/p/notes58-branch-private") == "branch-private", "branch private marker")
    detach(branch_uuid)
    send("S"); expect("Project services")
    service_action(branch_uuid, "db", "active")
    send("j"); service_action(branch_uuid, "web", "active")
    send("j"); service_action(branch_uuid, "worker", "active")
    assert fixture(branch_uuid, "sql", "SELECT count(*) FROM notes") == "0"
    fixture(branch_uuid, "add", "only branch notes")
    fixture(branch_uuid, "wait-jobs")
    assert fixture(branch_uuid, "sql", "SELECT word_count FROM notes WHERE id=1") == "3"
    assert fixture(uuid, "sql", "SELECT count(*) FROM notes") == "2"
    fixture(uuid, "sql", "ALTER TABLE notes ADD COLUMN review_extra text")
    schema_query = "SELECT count(*) FROM information_schema.columns WHERE table_name='notes' AND column_name='review_extra'"
    assert fixture(uuid, "sql", schema_query) == "1"
    assert fixture(branch_uuid, "sql", schema_query) == "0"
    send("q"); expect("All project sessions")

    renamed = "feat/renamed-notes-qgG"
    send("R"); expect("New assigned branch name>")
    send(renamed + "\r"); page_ready("Confirm action", branch_uuid, "Confirm [y/N]")
    send("y")
    expect("Operation completed.")
    await_state(lambda: any(item["uuid"] == branch_uuid and item["branch"] == renamed for item in sessions()),
                "rename preserved the native session UUID")
    send("q"); expect("All project sessions")
    assert guest(branch_uuid, "git", "branch", "--show-current") == renamed
    assert guest(branch_uuid, "git", "config", "branch." + renamed + ".merge") == "refs/heads/" + renamed
    assert guest(branch_uuid, "cat", "/home/p/notes58-branch-private") == "branch-private"
    assert fixture(branch_uuid, "sql", "SELECT body FROM notes") == "only branch notes"
    refs = rpc("project.branches", project=project, limit=8)["refs"]
    assert any(ref["ref"] == "refs/heads/" + renamed and ref["oid"] == source_oid for ref in refs)
    assert all(ref["ref"] != "refs/heads/" + branch for ref in refs)

    def stop_selected(selected_uuid):
        assigned = rpc("session.inspect", uuid=selected_uuid)["session"]["branch"]
        page_ready("All project sessions", "› " + project + " / " + assigned[:18])
        send("s"); page_ready("Confirm action", "UUID: " + selected_uuid, "Confirm [y/N]")
        send("y")
        await_state(lambda: rpc("session.inspect", uuid=selected_uuid)["session"]["session_condition"] == "stopped",
                    "selected notes session stopped before removal")
        expect("Stopped. Files and branch retained.")
        send("q"); page_ready("All project sessions", selected_state=("› " + project + " / " + assigned[:18], "stopped"))

    def remove_selected(selected_uuid, key, *, default_no=False):
        send(key); page_ready("Removal loss preview", "y authorize", "[y/N] Default No")
        if default_no:
            send("\r")
            driver["read"](0.2)
            assert rpc("session.inspect", uuid=selected_uuid)["session"]["session_condition"] == "stopped"
            page_ready("Removal loss preview", "y authorize", "[y/N] Default No")
        send("y")
        # Wait for the reviewed visible completion before reading inventory;
        # the removal operation is still changing registry/runtime ownership.
        expect("Operation completed.")
        await_state(lambda: all(item["uuid"] != selected_uuid for item in sessions()),
                    "reviewed native removal of " + selected_uuid)
        send("q"); expect("All project sessions")
        result = observe_process(["incus", "--force-local", "--project", "user-1000", "list",
                                  "^p-" + selected_uuid + "$", "--format", "json"], timeout=40, text=True)
        assert result.returncode == 0 and json.loads(result.stdout) == [], result.stdout[-2000:]

    stop_selected(branch_uuid)
    remove_selected(branch_uuid, "d")
    retained = rpc("project.retained_branches", project=project, limit=8)["branches"]
    assert any(item["branch"] == renamed and item["oid"] == source_oid for item in retained)
    assert fixture(uuid, "sql", "SELECT count(*) FROM notes") == "2"
    send("c"); page_ready("Create · project", "› Create a new project", project)
    send("j"); page_ready("Create · project", "› " + project)
    send("\r"); page_ready("Create · branch", "› Create new branch", renamed)
    send("j"); page_ready("Create · branch", "› " + renamed)
    send("\r"); page_ready("Create · policy review", "Existing retained P branch: " + renamed, source_oid, "Enter create, boot and enter")
    send("\r")
    reassigned = await_state(lambda: next((item for item in sessions()
                                          if item["branch"] == renamed and item["attached_count"] == 1), None),
                             "retained branch reassigned and confirmed native attachment")
    fresh_uuid = reassigned["uuid"]
    with owned_file.open("a", encoding="ascii") as stream:
        stream.write(fresh_uuid + "\n")
    assert fresh_uuid not in (uuid, branch_uuid)
    assert guest(fresh_uuid, "git", "rev-parse", "HEAD") == source_oid
    guest(fresh_uuid, "test", "!", "-e", "/home/p/notes58-branch-private")
    fixture(fresh_uuid, "assert-empty")
    send("python3 examples/notes/install.py > /workspace/notes58-install.log 2>&1; printf '%s' \"$?\" > /workspace/notes58-install.status\r")
    await_state(lambda: marker(fresh_uuid, "notes58-install.status") == "0", "retained branch real installer")
    detach(fresh_uuid)
    send("S"); expect("Project services")
    service_action(fresh_uuid, "db", "active")
    send("j"); service_action(fresh_uuid, "web", "active")
    send("j"); service_action(fresh_uuid, "worker", "active")
    assert fixture(fresh_uuid, "sql", "SELECT count(*) FROM notes") == "0"
    assert fixture(fresh_uuid, "sql", "SELECT count(*) FROM jobs") == "0"
    assert fixture(fresh_uuid, "sql", schema_query) == "0"
    assert fixture(uuid, "sql", schema_query) == "1"
    send("q"); expect("All project sessions")
    stop_selected(fresh_uuid)
    remove_selected(fresh_uuid, "X", default_no=True)
    assert all(ref["ref"] != "refs/heads/" + renamed
               for ref in rpc("project.branches", project=project, limit=8)["refs"])
    assert fixture(uuid, "sql", "SELECT count(*) FROM notes") == "2"

    # Finish the surviving main through the same reviewed Stop/Delete path;
    # whole-project deletion belongs to the wrapper's explicit owner API flow.
    stop_selected(uuid)
    remove_selected(uuid, "X", default_no=True)
    assert not sessions()
    send("q")
