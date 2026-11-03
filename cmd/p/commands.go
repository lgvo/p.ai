package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/attachment"
	"github.com/lgvo/p.ai/internal/errtext"
	"github.com/lgvo/p.ai/internal/plugin"
)

// commands are the client task commands, in the order p help lists them.
var commands = []*command{
	{noun: "status", summary: "Show the instance, its protocol, configured capabilities and record counts", run: runStatus},

	{noun: "project", verb: "list", summary: "List the projects", run: runProjectList},
	{noun: "project", verb: "create", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runProjectCreate,
		summary: "Create a project, blank or from an origin, and wait for it",
		flags: func(fs *flag.FlagSet, in *invocation) {
			stringFlag(fs, &in.origin, "origin", "the SSH `URL` of the project's origin")
			fs.BoolVar(&in.attach, "attach", false, "enter the project's first session once created, when creation makes one")
			keyFlags(fs, in)
		}},
	{noun: "project", verb: "delete", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runProjectDelete, flags: askFlags,
		summary: "Delete a project with its sessions and branches, after showing the plan and asking"},

	{noun: "branch", verb: "list", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runBranchList,
		summary: "List the project's P branches, or with --retained those no session holds",
		flags: func(fs *flag.FlagSet, in *invocation) {
			fs.BoolVar(&in.retained, "retained", false, "list only the branches no session holds")
		}},
	{noun: "branch", verb: "rename", args: "PROJECT:BRANCH NEW", minArgs: 2, maxArgs: 2, run: runBranchRename, flags: keyFlags,
		summary: "Rename a branch no session holds, and wait for it"},
	{noun: "branch", verb: "delete", args: "PROJECT:BRANCH", minArgs: 1, maxArgs: 1, run: runBranchDelete, flags: askFlags,
		summary: "Delete a branch no session holds, after showing the plan and asking"},

	{noun: "origin", verb: "show", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runOriginShow,
		summary: "Show the project's origin and its last observation",
		flags: func(fs *flag.FlagSet, in *invocation) {
			fs.BoolVar(&in.refresh, "refresh", false, "observe the origin again first")
		}},
	{noun: "origin", verb: "refs", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runOriginRefs,
		summary: "Observe the origin and list its branches and tags"},
	{noun: "origin", verb: "set", args: "PROJECT URL", minArgs: 2, maxArgs: 2, run: runOriginSet, flags: keyFlags,
		summary: "Set or replace the project's origin, and wait for it"},
	{noun: "origin", verb: "remove", args: "PROJECT", minArgs: 1, maxArgs: 1, run: runOriginRemove, flags: keyFlags,
		summary: "Remove the project's origin, and wait for it"},
	{noun: "origin", verb: "publish", args: "PROJECT:BRANCH", minArgs: 1, maxArgs: 1, run: runOriginPublish,
		summary: "Push a P branch to the origin, after showing the plan and asking",
		flags: func(fs *flag.FlagSet, in *invocation) {
			stringFlag(fs, &in.to, "to", "publish to the origin's branch `NAME`; the same name by default")
			askFlags(fs, in)
		}},

	{noun: "session", verb: "list", run: runSessionList,
		summary: "List the sessions",
		flags: func(fs *flag.FlagSet, in *invocation) {
			stringFlag(fs, &in.project, "project", "list only the sessions of `PROJECT`")
		}},
	{noun: "session", verb: "show", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionShow,
		summary: "Show a session's conditions, attachments, latest agent report, policy and diagnostic"},
	{noun: "session", verb: "create", args: "PROJECT:BRANCH", minArgs: 1, maxArgs: 1, run: runSessionCreate,
		summary: "Create a session on a kept branch, or a new one with --from or --from-origin; --replace replaces a blocked creation, after asking",
		flags: func(fs *flag.FlagSet, in *invocation) {
			stringFlag(fs, &in.from, "from", "start a new branch from a P branch or commit `REF`")
			stringFlag(fs, &in.fromOrigin, "from-origin", "start a new branch from the origin's branch, or full ref such as refs/tags/v1, `REF`")
			fs.BoolVar(&in.attach, "attach", false, "enter the session once created")
			stringFlag(fs, &in.replace, "replace", "replace the blocked creation of `SESSION` with this one, after showing the plan and asking")
			keyFlags(fs, in)
			runFlags(fs, in)
		}},
	{noun: "session", verb: "start", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionStart, flags: noWaitFlag,
		summary: "Start a session and wait until it is ready"},
	{noun: "session", verb: "stop", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionStop, flags: noWaitFlag,
		summary: "Stop a session, keeping its files, and wait until it is stopped"},
	{noun: "session", verb: "rename", args: "SESSION NEW", minArgs: 2, maxArgs: 2, run: runSessionRename, flags: keyFlags,
		summary: "Rename a stopped session's branch, and wait for it"},
	{noun: "session", verb: "discard", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionDiscard, flags: askFlags,
		summary: "Remove a session and keep its branch, after showing the plan and asking"},
	{noun: "session", verb: "delete", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionDelete, flags: askFlags,
		summary: "Remove a session and its branch, after showing the plan and asking"},
	{noun: "session", verb: "cleanup", args: "SESSION", minArgs: 1, maxArgs: 1, run: runSessionCleanup, flags: askFlags,
		summary: "End a blocked creation and keep its branch, after showing the plan and asking"},

	{noun: "attach", args: "[SOCKET] SESSION", minArgs: 1, maxArgs: 2, socketArg: true, run: runAttach,
		summary: "Enter a session's terminal, starting the session first when it is not ready"},

	{noun: "service", verb: "list", args: "SESSION", minArgs: 1, maxArgs: 1, run: runServiceList,
		summary: "List a session's project services"},
	{noun: "service", verb: "start", args: "SESSION NAME", minArgs: 2, maxArgs: 2, run: runServiceAction,
		summary: "Queue a start of a project service"},
	{noun: "service", verb: "stop", args: "SESSION NAME", minArgs: 2, maxArgs: 2, run: runServiceAction,
		summary: "Queue a stop of a project service"},
	{noun: "service", verb: "restart", args: "SESSION NAME", minArgs: 2, maxArgs: 2, run: runServiceAction,
		summary: "Queue a restart of a project service"},
	{noun: "service", verb: "logs", args: "SESSION NAME", minArgs: 2, maxArgs: 2, run: runServiceLogs,
		summary: "Show a project service's recent log"},

	{noun: "operation", verb: "list", run: runOperationList,
		summary: "List the operations, each with its key"},
	{noun: "operation", verb: "show", args: "ID", minArgs: 1, maxArgs: 1, run: runOperationShow,
		summary: "Show an operation"},
	{noun: "operation", verb: "wait", args: "ID", minArgs: 1, maxArgs: 1, run: runOperationWait,
		summary: "Wait until an operation is no longer running"},
	{noun: "operation", verb: "retry", args: "ID", minArgs: 1, maxArgs: 1, run: runOperationRetry, flags: noWaitFlag,
		summary: "Retry a blocked operation, and wait for it"},
}

func (in *invocation) table(header string, rows func(w io.Writer)) {
	w := tabwriter.NewWriter(in.stdout, 0, 4, 2, ' ', 0)
	if header != "" {
		fmt.Fprintln(w, header)
	}
	rows(w)
	w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runStatus(in *invocation) error {
	hello, err := call(in, api.SystemHello, api.HelloRequest{Protocol: api.Protocol})
	if err != nil {
		return err
	}
	health, err := call(in, api.SystemHealth, api.HealthRequest{})
	if err != nil {
		return err
	}
	records, err := call(in, api.SystemInspect, api.SystemInspectRequest{})
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(struct {
			api.HelloResult
			api.HealthResult
			api.SystemInspectResult
		}{hello, health, records})
		return nil
	}
	capabilities := make([]string, len(hello.Capabilities))
	for i, c := range hello.Capabilities {
		capabilities[i] = string(c)
	}
	in.table("", func(w io.Writer) {
		fmt.Fprintf(w, "Instance\t%s\n", hello.InstanceID)
		fmt.Fprintf(w, "Build\t%s\n", hello.BuildVersion)
		fmt.Fprintf(w, "Protocol\t%s\n", hello.Protocol)
		fmt.Fprintf(w, "Capabilities\t%s\n", orDash(strings.Join(capabilities, ", ")))
		fmt.Fprintf(w, "Control state\t%s\n", health.ControlState)
		fmt.Fprintf(w, "Records\t%d projects, %d sessions, %d operations\n", records.Projects, records.Sessions, records.Operations)
	})
	return nil
}

func runProjectList(in *invocation) error {
	projects, err := list(in, api.ProjectList, func(after string) api.ProjectListRequest {
		return api.ProjectListRequest{Limit: 100, After: after}
	}, func(r api.ProjectListResult) ([]api.ProjectSummary, string) { return r.Projects, r.Next })
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(projects)
		return nil
	}
	in.table("PROJECT\tSTATE", func(w io.Writer) {
		for _, p := range projects {
			fmt.Fprintf(w, "%s\t%s\n", p.Path, p.Registry)
		}
	})
	return nil
}

// refuseAttachWith refuses --attach beside a flag that leaves no terminal to
// enter or no wait to enter after.
func refuseAttachWith(in *invocation) error {
	if in.attach && (in.noWait || in.json || in.dryRun) {
		return usageError("--attach enters a terminal once the change is done: it cannot be combined with --no-wait, --json or --dry-run")
	}
	return nil
}

func runProjectCreate(in *invocation) error {
	project := in.args[0]
	if err := refuseAttachWith(in); err != nil {
		return err
	}
	op, err := keyedChange(in, api.ProjectCreate,
		func(i api.OperationIntent) bool { return i.Project == project && i.URL == in.origin },
		func(key string) (api.ProjectCreateRequest, error) {
			return api.ProjectCreateRequest{Key: key, Project: project, URL: in.origin}, nil
		},
		func(op api.Operation) string {
			if op.SessionUUID == "" {
				return "Created project " + project + "."
			}
			return fmt.Sprintf("Created project %s and its first session %s.", project, op.SessionUUID)
		})
	if err != nil || !in.attach {
		return err
	}
	if op.SessionUUID == "" {
		fmt.Fprintf(in.stderr, "Creation made no session to enter; create one with: p session create %s:BRANCH\n", project)
		return nil
	}
	return in.enter(op.SessionUUID)
}

func runBranchList(in *invocation) error {
	project := in.args[0]
	type branch struct{ name, commit string }
	var rows []branch
	if in.retained {
		branches, err := list(in, api.ProjectRetainedBranches, func(after string) api.ProjectRetainedBranchesRequest {
			return api.ProjectRetainedBranchesRequest{Project: project, Limit: 8, After: after}
		}, func(r api.RetainedBranchesResult) ([]api.RetainedBranch, string) { return r.Branches, r.Next })
		if err != nil {
			return err
		}
		if in.json {
			in.printJSON(branches)
			return nil
		}
		for _, b := range branches {
			rows = append(rows, branch{b.Branch, b.OID})
		}
	} else {
		refs, err := list(in, api.ProjectBranches, func(after string) api.ProjectBranchesRequest {
			return api.ProjectBranchesRequest{Project: project, Limit: 8, After: after}
		}, func(r api.ProjectBranchesResult) ([]plugin.GitRef, string) { return r.Refs, r.Next })
		if err != nil {
			return err
		}
		if in.json {
			in.printJSON(refs)
			return nil
		}
		for _, r := range refs {
			rows = append(rows, branch{strings.TrimPrefix(r.Ref, "refs/heads/"), r.OID})
		}
	}
	in.table("BRANCH\tCOMMIT", func(w io.Writer) {
		for _, b := range rows {
			fmt.Fprintf(w, "%s\t%s\n", b.name, b.commit)
		}
	})
	return nil
}

func runBranchRename(in *invocation) error {
	b, err := parseBranchName(in.args[0])
	if err != nil {
		return err
	}
	name := in.args[1]
	_, err = keyedChange(in, api.ProjectRetainedRename,
		func(i api.OperationIntent) bool {
			return i.Project == b.project && i.Branch == b.branch && i.NewBranch == name
		},
		func(key string) (api.ProjectRetainedRenameRequest, error) {
			tip, err := in.branchTip(b)
			return api.ProjectRetainedRenameRequest{Key: key, Project: b.project, OldBranch: b.branch, NewBranch: name, ExpectedOldTip: tip}, err
		},
		func(api.Operation) string { return fmt.Sprintf("Renamed branch %s to %s.", b, name) })
	return err
}

func runOriginShow(in *invocation) error {
	project := in.args[0]
	var r api.OriginResult
	var err error
	if in.refresh {
		r, err = call(in, api.OriginRefresh, api.OriginRefreshRequest{Project: project})
	} else {
		r, err = call(in, api.OriginInspect, api.OriginInspectRequest{Project: project})
	}
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(r)
		return nil
	}
	o := r.Origin
	if o.Status == "local-only" {
		fmt.Fprintf(in.stdout, "Project %s has no origin (local-only).\n", project)
		return nil
	}
	in.table("", func(w io.Writer) {
		fmt.Fprintf(w, "Project\t%s\n", o.Project)
		fmt.Fprintf(w, "URL\t%s\n", o.URL)
		fmt.Fprintf(w, "Status\t%s\n", o.Status)
		fmt.Fprintf(w, "Observed at\t%s\n", orDash(o.ObservedAt))
		fmt.Fprintf(w, "Refs\t%d\n", o.RefCount)
		if o.Diagnostic != "" {
			fmt.Fprintf(w, "Diagnostic\t%s\n", o.Diagnostic)
		}
	})
	return nil
}

func runOriginRefs(in *invocation) error {
	origin, err := in.observeOrigin(in.args[0])
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(origin.refs)
		return nil
	}
	fmt.Fprintf(in.stderr, "Refs of %s, observed now.\n", origin.url)
	in.table("REF\tCOMMIT", func(w io.Writer) {
		for _, r := range origin.refs {
			fmt.Fprintf(w, "%s\t%s\n", r.Ref, r.CommitOID)
		}
	})
	return nil
}

func runOriginSet(in *invocation) error {
	project, url := in.args[0], in.args[1]
	return changeOrigin(in, project, "set", url, fmt.Sprintf("Set the origin of %s to %s.", project, url))
}

func runOriginRemove(in *invocation) error {
	project := in.args[0]
	return changeOrigin(in, project, "remove", "", fmt.Sprintf("Removed the origin of %s.", project))
}

// changeOrigin sets or removes the origin of project, expecting the URL it
// reads now.
func changeOrigin(in *invocation, project, kind, url, done string) error {
	_, err := keyedChange(in, api.OriginChange,
		func(i api.OperationIntent) bool { return i.Project == project && i.URL == url },
		func(key string) (api.OriginChangeRequest, error) {
			current, err := call(in, api.OriginInspect, api.OriginInspectRequest{Project: project})
			return api.OriginChangeRequest{Key: key, Project: project, Kind: kind, ExpectedURL: current.Origin.URL, URL: url}, err
		},
		func(api.Operation) string { return done })
	return err
}

func runSessionList(in *invocation) error {
	sessions, err := list(in, api.SessionList, func(after string) api.SessionListRequest {
		return api.SessionListRequest{Limit: 8, After: after}
	}, func(r api.SessionListResult) ([]api.SessionView, string) { return r.Sessions, r.Next })
	if err != nil {
		return err
	}
	if in.project != "" {
		kept := sessions[:0]
		for _, s := range sessions {
			if s.Project == in.project {
				kept = append(kept, s)
			}
		}
		sessions = kept
	}
	if in.json {
		in.printJSON(sessions)
		return nil
	}
	in.table("SESSION\tPROJECT:BRANCH\tCONDITION\tATTACHED\tREPORT", func(w io.Writer) {
		for _, s := range sessions {
			report := "-"
			if s.LatestUnattendedCondition != nil {
				report = s.LatestUnattendedCondition.Condition
			}
			fmt.Fprintf(w, "%s\t%s:%s\t%s\t%d\t%s\n", s.UUID, s.Project, s.Branch, s.Condition, s.AttachedCount, report)
		}
	})
	return nil
}

func runSessionShow(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	s, err := in.session(name)
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(api.SessionResult{Session: s})
		return nil
	}
	in.table("", func(w io.Writer) {
		fmt.Fprintf(w, "Session\t%s\n", s.UUID)
		fmt.Fprintf(w, "Branch\t%s:%s\n", s.Project, s.Branch)
		fmt.Fprintf(w, "Condition\t%s\n", s.Condition)
		fmt.Fprintf(w, "Registry\t%s\n", s.Registry)
		fmt.Fprintf(w, "Attached\t%d\n", s.AttachedCount)
		report := "none"
		if r := s.LatestUnattendedCondition; r != nil {
			report = r.Condition + " at " + r.ReceivedAt
			if r.Adapter != "" {
				report += " from " + r.Adapter + " " + r.AdapterVersion
			}
			if r.Reason != "" {
				report += ": " + r.Reason
			}
		}
		fmt.Fprintf(w, "Agent report\t%s\n", report)
		fmt.Fprintf(w, "Policy\t%s (%s)\n", s.PolicyCondition, s.PolicySHA256)
		if i := s.Image; i != nil {
			fmt.Fprintf(w, "Image\t%s, %s, commit %s, %s\n", i.State, i.Selection, orDash(i.CommitOID), i.System)
		}
		if s.Diagnostic != "" {
			fmt.Fprintf(w, "Diagnostic\t%s\n", s.Diagnostic)
		}
	})
	return nil
}

func runSessionCreate(in *invocation) error {
	b, err := parseBranchName(in.args[0])
	if err != nil {
		return err
	}
	switch {
	case in.from != "" && in.fromOrigin != "":
		return usageError("--from and --from-origin exclude each other")
	case in.replace != "" && in.fromOrigin != "":
		return usageError("a replacement cannot start from the origin: --replace and --from-origin exclude each other")
	case in.replace == "" && (in.force || in.dryRun || in.ifMatch != ""):
		return usageError("only a replacement asks first: --force, --dry-run and --if-match need --replace")
	}
	if err := refuseAttachWith(in); err != nil {
		return err
	}
	choice, source, ref := "existing", "", ""
	if in.from != "" {
		choice, source = "new", sourceRef(in.from)
	}
	if in.fromOrigin != "" {
		choice, ref = "new", originRef(in.fromOrigin)
	}
	if in.replace != "" {
		op, err := replaceCreation(in, b, choice, source)
		if err != nil || !in.attach {
			return err
		}
		return in.enter(op.SessionUUID)
	}
	op, err := keyedChange(in, api.SessionCreate,
		func(i api.OperationIntent) bool {
			return i.Project == b.project && i.Branch == b.branch && i.Choice == choice && i.Source == source && i.OriginRef == ref &&
				i.ReplacesSession == "" && i.ReplacesBranch == ""
		},
		func(key string) (api.SessionCreateRequest, error) {
			req := api.SessionCreateRequest{Key: key, Project: b.project, Branch: b.branch, Choice: choice, Source: source}
			if ref == "" {
				return req, nil
			}
			origin, err := in.observeOrigin(b.project)
			if err != nil {
				return req, err
			}
			for _, r := range origin.refs {
				if r.Ref == ref {
					fmt.Fprintf(in.stderr, "Creating from %s of %s at commit %s.\n", ref, origin.url, r.CommitOID)
					req.OriginRef, req.ExpectedCommitOID, req.ExpectedOriginURL = ref, r.CommitOID, origin.url
					return req, nil
				}
			}
			return req, &exitError{code: exitFailed, text: fmt.Sprintf("The origin of %s has no ref %s; p origin refs %s lists them.", b.project, ref, b.project)}
		},
		func(op api.Operation) string { return fmt.Sprintf("Created session %s (%s).", op.SessionUUID, b) })
	if err != nil || !in.attach {
		return err
	}
	return in.enter(op.SessionUUID)
}

func runSessionStart(in *invocation) error {
	return startOrStop(in, api.SessionStart, func(id string) api.SessionStartRequest { return api.SessionStartRequest{UUID: id} }, "Started", "running")
}

func runSessionStop(in *invocation) error {
	return startOrStop(in, api.SessionStop, func(id string) api.SessionStopRequest { return api.SessionStopRequest{UUID: id} }, "Stopped", "stopped")
}

// startOrStop starts or stops the session the argument names. A session
// already as asked changes nothing; an operation, new or unfinished, is
// waited for.
func startOrStop[Req api.Request](in *invocation, m api.Method[Req, api.SessionChangeResult], request func(id string) Req, done, already string) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	id, err := in.sessionID(name)
	if err != nil {
		return err
	}
	r, err := sendStartOrStop(in, m, request(id))
	if err != nil {
		return err
	}
	if r.Operation == nil {
		if in.json {
			in.printJSON(r)
		} else {
			fmt.Fprintf(in.stdout, "Session %s is already %s.\n", id, already)
		}
		return nil
	}
	op := *r.Operation
	if in.noWait {
		return in.reportStartOrStop(r, op, nil)
	}
	op, err = in.follow(op)
	if answered(err) {
		return err
	}
	if err == nil {
		err = in.ended(op, func(api.Operation) string { return done + " session " + id + "." })
	}
	return in.reportStartOrStop(r, op, err)
}

// sendStartOrStop sends a start or a stop. Neither takes a key: a repeat
// converges, so a lost reply only says to repeat the command.
func sendStartOrStop[Req api.Request](in *invocation, m api.Method[Req, api.SessionChangeResult], req Req) (api.SessionChangeResult, error) {
	r, err := call(in, m, req)
	var lost *lostReplyError
	if errors.As(err, &lost) {
		return r, &exitError{code: exitFailed, text: lost.Error() + " Repeating the command is safe: one already done or under way changes nothing."}
	}
	return r, err
}

// reportStartOrStop prints the reply with --json, with the operation as it
// is now, and returns end.
func (in *invocation) reportStartOrStop(r api.SessionChangeResult, op api.Operation, end error) error {
	switch {
	case in.json:
		r.Operation = &op
		in.printJSON(r)
	case in.noWait:
		fmt.Fprintln(in.stdout, op.ID)
		fmt.Fprintf(in.stderr, "Accepted. Wait for it with: p operation wait %s\n", op.ID)
	}
	return end
}

func runSessionRename(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	branch := in.args[1]
	_, err = keyedChange(in, api.SessionRename,
		func(i api.OperationIntent) bool { return name.named(i) && i.NewBranch == branch },
		func(key string) (api.SessionRenameRequest, error) {
			s, err := in.session(name)
			if err != nil {
				return api.SessionRenameRequest{}, err
			}
			tip, err := in.branchTip(branchName{s.Project, s.Branch})
			return api.SessionRenameRequest{Key: key, UUID: s.UUID, NewBranch: branch, ExpectedOldTip: tip}, err
		},
		func(op api.Operation) string {
			return fmt.Sprintf("Renamed the branch of session %s to %s.", op.SessionUUID, branch)
		})
	return err
}

func runAttach(in *invocation) error {
	if in.json {
		return usageError("p attach has no --json output")
	}
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	id, err := in.sessionID(name)
	if err != nil {
		return err
	}
	return in.enter(id)
}

// enter attaches the terminal to session id. session.attach accepts only a
// ready session, so it starts the session first: a ready one is entered at
// once, otherwise once its start completes.
func (in *invocation) enter(id string) error {
	r, err := sendStartOrStop(in, api.SessionStart, api.SessionStartRequest{UUID: id})
	if err != nil {
		return err
	}
	if r.Operation != nil {
		op, err := in.follow(*r.Operation)
		if err != nil {
			return err
		}
		if op.Status != "completed" {
			return in.ended(op, nil)
		}
	}
	pending, err := call(in, api.SessionAttach, api.SessionAttachRequest{UUID: id})
	if err != nil {
		return err
	}
	return attachment.Client(in.ctx, in.socket, pending)
}

func runServiceList(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	id, err := in.sessionID(name)
	if err != nil {
		return err
	}
	r, err := call(in, api.SessionServices, api.SessionServicesRequest{UUID: id})
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(r)
		return nil
	}
	in.table("NAME\tSTATE\tDETAIL\tDESCRIPTION", func(w io.Writer) {
		for _, s := range r.Services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.State, orDash(s.Detail), s.Description)
		}
	})
	return nil
}

// runServiceAction queues a service action. It records no operation and has
// no key, so it is never sent twice: after a lost reply or the daemon's
// deadline the action may have been queued, and the CLI says so.
func runServiceAction(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	id, err := in.sessionID(name)
	if err != nil {
		return err
	}
	action, service := in.cmd.verb, in.args[1]
	r, err := callOnce(in, api.SessionServiceAction, api.SessionServiceActionRequest{UUID: id, Name: service, Action: action})
	var lost *lostReplyError
	var answer *api.Error
	if errors.As(err, &lost) || errors.As(err, &answer) && answer.Reason() == api.ReasonDeadline {
		return explained(err, fmt.Sprintf("%s The %s of %s may have been queued; p service list %s shows its state.", in.sentence(err), action, service, in.args[0]))
	}
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(r)
		return nil
	}
	fmt.Fprintf(in.stdout, "Queued %s of %s; p service list %s shows its progress.\n", action, service, in.args[0])
	return nil
}

func runServiceLogs(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	id, err := in.sessionID(name)
	if err != nil {
		return err
	}
	r, err := call(in, api.SessionServiceLogs, api.SessionServiceLogsRequest{UUID: id, Name: in.args[1]})
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(r)
		return nil
	}
	if r.LogsTruncated {
		fmt.Fprintln(in.stderr, "Older lines were cut; this is the recent tail.")
	}
	fmt.Fprint(in.stdout, r.Logs)
	if r.Logs != "" && !strings.HasSuffix(r.Logs, "\n") {
		fmt.Fprintln(in.stdout)
	}
	return nil
}

func runOperationList(in *invocation) error {
	ops, err := list(in, api.OperationList, func(after string) api.OperationListRequest {
		return api.OperationListRequest{Limit: 20, After: after}
	}, func(r api.OperationListResult) ([]api.OperationSummary, string) { return r.Operations, r.Next })
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(ops)
		return nil
	}
	in.table("OPERATION\tKIND\tSTATUS\tPHASE\tTARGET\tKEY", func(w io.Writer) {
		for _, op := range ops {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", op.ID, op.Kind, op.Status, op.Phase, intentTarget(op.Intent, op.SessionUUID), op.Key)
		}
	})
	return nil
}

// intentTarget names what an operation acts on: its project, and the branch
// or session the caller named.
func intentTarget(i api.OperationIntent, session string) string {
	switch {
	case i.Branch != "":
		return i.Project + ":" + i.Branch
	case session != "":
		return session
	}
	return i.Project
}

func runOperationShow(in *invocation) error {
	r, err := call(in, api.OperationInspect, api.OperationInspectRequest{ID: in.args[0]})
	if err != nil {
		return err
	}
	if in.json {
		in.printJSON(r)
		return nil
	}
	op := r.Operation
	in.table("", func(w io.Writer) {
		fmt.Fprintf(w, "Operation\t%s\n", op.ID)
		fmt.Fprintf(w, "Kind\t%s\n", op.Kind)
		fmt.Fprintf(w, "Status\t%s\n", op.Status)
		fmt.Fprintf(w, "Phase\t%s\n", op.Phase)
		fmt.Fprintf(w, "Target\t%s\n", intentTarget(op.Intent, op.SessionUUID))
		if op.SessionUUID != "" {
			fmt.Fprintf(w, "Session\t%s\n", op.SessionUUID)
		}
		fmt.Fprintf(w, "Key\t%s\n", op.Key)
		fmt.Fprintf(w, "Created\t%s\n", op.CreatedAt)
		fmt.Fprintf(w, "Updated\t%s\n", op.UpdatedAt)
		if op.Reason != "" {
			fmt.Fprintf(w, "Reason\t%s: %s\n", op.Reason, errtext.Operation(op.Status, op.Reason))
		}
		if op.Diagnostic != "" {
			fmt.Fprintf(w, "Diagnostic\t%s\n", op.Diagnostic)
		}
	})
	return nil
}

func runOperationWait(in *invocation) error {
	id := in.args[0]
	r, err := call(in, api.OperationInspect, api.OperationInspectRequest{ID: id})
	if err != nil {
		return err
	}
	op, err := in.follow(r.Operation)
	if in.json && !answered(err) {
		in.printJSON(api.OperationResult{Operation: op})
	}
	if err != nil {
		return err
	}
	return in.ended(op, func(api.Operation) string { return "Operation " + id + " completed." })
}

func runOperationRetry(in *invocation) error {
	id := in.args[0]
	r, err := callOnce(in, api.OperationRetry, api.OperationRetryRequest{ID: id})
	if err != nil {
		return err
	}
	_, err = in.report(r.Operation, func(api.Operation) string { return "Operation " + id + " completed." })
	return err
}
