package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func (l *lifecycle) RenameSession(ctx context.Context, req control.RenameRequest) (api.Operation, error) {
	if !control.ValidRenameRequest(req) {
		return api.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	if prior, replayed, err := replayRequest(l, ctx, req.Key, control.KindSessionRename, req); err != nil || replayed {
		return prior, err
	}
	if _, err := control.CheckNames(ctx, l.git.driver,
		control.NameField{Field: "new_branch", Name: sourcedriver.Name{Kind: sourcedriver.NameRenameBranch, Value: req.NewBranch}},
		control.NameField{Field: "expected_old_tip", Name: sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: req.ExpectedOldTip}}); err != nil {
		return api.Operation{}, err
	}
	if err := l.admitSessionWork(ctx, req.UUID, ""); err != nil {
		return api.Operation{}, err
	}
	s, err := l.store.GetSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
	}
	if s.Registry != "established" {
		return api.Operation{}, control.Conflict(api.ReasonSessionNotEstablished, api.TargetSession(req.UUID))
	}
	if s.Branch == req.NewBranch {
		return api.Operation{}, control.Conflict(api.ReasonBranchExists, api.TargetBranch(s.Project, req.NewBranch))
	}
	spec, err := l.sessionSpec(ctx, s)
	if err != nil {
		return api.Operation{}, err
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return api.Operation{}, control.Failed(api.ReasonRuntime, err)
	}
	if err := renameSourceRefusal(observed, req.UUID); err != nil {
		return api.Operation{}, err
	}
	ev := control.RenameEvidence{Project: s.Project, OldBranch: s.Branch, NewBranch: req.NewBranch,
		OldTip: req.ExpectedOldTip, PolicySHA256: s.PolicySHA256, InstanceUUID: l.instanceID,
		ImageFingerprint: string(spec.Image), BaseFingerprint: l.cfg.BaseImageFingerprint,
		Incarnation: string(observed.Incarnation)}
	verify := func(ctx context.Context) error {
		return control.Failed(api.ReasonGit, l.verifyRenamePRefs(ctx, ev, false))
	}
	op, err := l.store.BeginRename(ctx, req, ev, verify)
	if err != nil {
		return api.Operation{}, err
	}
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

// renameSourceRefusal refuses to rename session whose runtime was observed
// as o: its container is missing, or it is not stopped.
func renameSourceRefusal(o runtimedriver.Observation, session string) error {
	switch {
	case o.State == runtimedriver.StateAbsent:
		return control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(session))
	case !renameSourceStopped(o):
		return control.Conflict(api.ReasonSessionNotStopped, api.TargetSession(session))
	}
	return nil
}

// renameSourceStopped reports whether rename may change the workspace of the
// observed runtime: P changes a workspace only while its session is stopped.
func renameSourceStopped(o runtimedriver.Observation) bool {
	return o.State == runtimedriver.StateStopped && o.Incarnation != ""
}

// renameSourceExact reports whether o is the stopped runtime the rename
// recorded.
func renameSourceExact(o runtimedriver.Observation, ev control.RenameEvidence) bool {
	return renameSourceStopped(o) && string(o.Incarnation) == ev.Incarnation
}

// renameUID is the session user, who owns the clone.
const renameUID = 1000

// maxRenameReads bounds the rounds of reads one answer of the source driver
// asks for: a driver that keeps asking fails the rename instead of holding
// it.
const maxRenameReads = 16

// renameClone is the clone of the session a rename changes, at the
// incarnation the rename recorded. The source driver decides the rename's
// Git edits from the clone's files; core reads and writes them through the
// runtime driver, whose workspace operations act only on a stopped session
// at that incarnation.
type renameClone struct {
	runtime runtimedriver.Driver
	source  sourcedriver.Driver
	target  runtimedriver.Target
	// rename names the rename, its branches and the session user; each call
	// sets its goal and plan.
	rename sourcedriver.WorkspaceRename
}

// renameClone is the clone that rename op changes.
func (l *lifecycle) renameClone(spec runtimedriver.Session, ev control.RenameEvidence, op string) renameClone {
	return renameClone{runtime: l.driver, source: l.git.driver,
		target: runtimedriver.Target{Session: spec, At: runtimedriver.Incarnation(ev.Incarnation)},
		rename: sourcedriver.WorkspaceRename{ID: op, Old: ev.OldBranch, New: ev.NewBranch, User: renameUID}}
}

// ask is the source driver's answer toward goal for the clone as it is now,
// with the files it read: core reads each path the driver asks for within
// the bound it gives, and calls again with them, until the driver answers
// with edits.
func (c renameClone) ask(ctx context.Context, goal sourcedriver.Goal, plan string) (sourcedriver.WorkspaceEdits, map[string]runtimedriver.File, error) {
	r := c.rename
	r.Goal, r.Plan = goal, plan
	read := map[string]runtimedriver.File{}
	var files []sourcedriver.WorkspaceFile
	for range maxRenameReads {
		answer, err := c.source.RenameWorkspace(ctx, r, files)
		if err != nil || len(answer.Read) == 0 {
			return answer, read, err
		}
		for _, p := range slices.Sorted(maps.Keys(answer.Read)) {
			f, err := c.runtime.ReadWorkspace(ctx, c.target, p, answer.Read[p])
			if err != nil {
				return sourcedriver.WorkspaceEdits{}, nil, err
			}
			read[p] = f
			files = append(files, sourcedriver.WorkspaceFile{Path: p, File: sourceFile(f)})
		}
	}
	return sourcedriver.WorkspaceEdits{}, nil, errors.New("rename workspace reads exceed their bound")
}

// holds proves the clone at goal: the source driver asks for no edit and
// returns plan.
func (c renameClone) holds(ctx context.Context, goal sourcedriver.Goal, plan string) error {
	answer, _, err := c.ask(ctx, goal, plan)
	if err != nil {
		return err
	}
	if len(answer.Edits) != 0 || answer.Plan != plan {
		return fmt.Errorf("rename workspace is not %s as planned", goal)
	}
	return nil
}

// reach takes the clone to goal and returns the rename's plan: it makes the
// source driver's edits in order, each after issued records it with the
// plan, then proves the goal holds. plan is the plan recorded, or empty
// before an answer returned one.
func (c renameClone) reach(ctx context.Context, goal sourcedriver.Goal, plan string, issued func(control.RenameEdit, string) error) (string, error) {
	answer, _, err := c.ask(ctx, goal, plan)
	if err != nil {
		return "", err
	}
	if plan != "" && answer.Plan != plan {
		return "", fmt.Errorf("rename workspace plan changed toward %s", goal)
	}
	for _, e := range answer.Edits {
		recorded := control.RenameEdit{Goal: goal, Path: e.Path, Remove: e.File.Type == sourcedriver.FileAbsent}
		if err := c.edit(ctx, e, func() error { return issued(recorded, answer.Plan) }); err != nil {
			return "", err
		}
	}
	return answer.Plan, c.holds(ctx, goal, answer.Plan)
}

// edit makes one of the source driver's edits after gate: a regular file
// written, or a path removed. An edit whose outcome was lost is settled at
// once, by what it was to leave, and never sent again: only one the runtime
// driver settles applied lets the rename go on.
func (c renameClone) edit(ctx context.Context, e sourcedriver.WorkspaceFile, gate runtimedriver.Gate) error {
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectRemove, Target: c.target, Path: e.Path}
	sent := false
	var err error
	switch e.File.Type {
	case sourcedriver.FileAbsent:
		err = c.runtime.RemoveWorkspace(ctx, c.target, e.Path, tracked(gate, &sent))
	case sourcedriver.FileRegular:
		effect.Kind = runtimedriver.EffectWrite
		effect.Content = runtimedriver.File{Type: runtimedriver.FileRegular, UID: e.File.UID, GID: e.File.GID, Mode: e.File.Mode, Data: e.File.Data}
		err = c.runtime.WriteWorkspace(ctx, c.target, e.Path, effect.Content, tracked(gate, &sent))
	default:
		return fmt.Errorf("rename edit of %s is neither a file nor a removal", e.Path)
	}
	if err == nil || !sent {
		return err
	}
	return c.settle(ctx, effect, fmt.Errorf("%s %s outcome unresolved: %w", e.Path, effect.Kind, err))
}

// settle succeeds only when the runtime driver settles e applied; otherwise
// it returns unresolved, with what the driver said.
func (c renameClone) settle(ctx context.Context, e runtimedriver.Effect, unresolved error) error {
	settled, err := c.runtime.Settle(ctx, e)
	if err == nil && settled == runtimedriver.Applied {
		return nil
	}
	return errors.Join(unresolved, err)
}

// settleEdit settles e, the edit toward goal that a gate recorded and whose
// outcome was lost. The rename goes on only when the source driver asks for
// no edit toward goal and the runtime driver settles e applied for the file
// now at its path; e is never sent again.
func (c renameClone) settleEdit(ctx context.Context, e control.RenameEdit, goal sourcedriver.Goal, plan string) error {
	if e.Goal != goal {
		return fmt.Errorf("rename workspace edit toward %s unrecorded", goal)
	}
	answer, read, err := c.ask(ctx, goal, plan)
	if err != nil {
		return err
	}
	now, found := read[e.Path]
	if len(answer.Edits) != 0 || answer.Plan != plan || !found {
		return fmt.Errorf("%s edit outcome unresolved", e.Path)
	}
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectWrite, Target: c.target, Path: e.Path, Content: now}
	if e.Remove {
		effect = runtimedriver.Effect{Kind: runtimedriver.EffectRemove, Target: c.target, Path: e.Path}
	}
	return c.settle(ctx, effect, fmt.Errorf("%s edit outcome unresolved", e.Path))
}

// sourceFileTypes are the runtime driver's file types as the source driver
// names them.
var sourceFileTypes = map[runtimedriver.FileType]sourcedriver.FileType{
	runtimedriver.FileAbsent:    sourcedriver.FileAbsent,
	runtimedriver.FileRegular:   sourcedriver.FileRegular,
	runtimedriver.FileDirectory: sourcedriver.FileDirectory,
	runtimedriver.FileSymlink:   sourcedriver.FileSymlink,
	runtimedriver.FileOther:     sourcedriver.FileOther,
}

// sourceFile is a file of the clone the runtime driver read, as the source
// driver takes it.
func sourceFile(f runtimedriver.File) sourcedriver.File {
	return sourcedriver.File{Type: sourceFileTypes[f.Type], UID: f.UID, GID: f.GID, Mode: f.Mode, Data: f.Data}
}

// The caller either holds gitAuthority or is only making a preflight
// observation. No Store method is called from this callback. Before the
// rename (oldAbsent false), an old ref that no longer holds the expected tip
// is a changed precondition and a taken new name a conflict; after it, a
// mismatch breaks the rename's own invariant.
func (l *lifecycle) verifyRenamePRefs(ctx context.Context, ev control.RenameEvidence, oldAbsent bool) error {
	old, exists, err := l.inspectBranch(ctx, ev.Project, ev.OldBranch)
	if err != nil {
		return err
	}
	if oldAbsent {
		if exists || old != "" {
			return control.ErrConflict
		}
	} else if !exists || old != ev.OldTip {
		return control.Stale(api.ReasonChanged)
	}
	newTip, exists, err := l.inspectBranch(ctx, ev.Project, ev.NewBranch)
	if errors.Is(err, control.ErrConflict) && !oldAbsent {
		// A symbolic ref occupies the new name.
		return control.Conflict(api.ReasonBranchExists, api.TargetBranch(ev.Project, ev.NewBranch))
	}
	if err != nil {
		return err
	}
	if oldAbsent {
		if !exists || newTip != ev.OldTip {
			return control.ErrConflict
		}
	} else if exists || newTip != "" {
		return control.Conflict(api.ReasonBranchExists, api.TargetBranch(ev.Project, ev.NewBranch))
	}
	return nil
}

func (l *lifecycle) renameNewRefExact(ctx context.Context, ev control.RenameEvidence) error {
	old, exists, err := l.inspectBranch(ctx, ev.Project, ev.OldBranch)
	if err != nil || !exists || old != ev.OldTip {
		return errors.Join(err, control.ErrConflict)
	}
	newTip, exists, err := l.inspectBranch(ctx, ev.Project, ev.NewBranch)
	if err != nil || !exists || newTip != ev.OldTip {
		return errors.Join(err, control.ErrConflict)
	}
	return nil
}

func (l *lifecycle) blockRename(op api.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("rename blocked: %v", cause)
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), message)
}

func (l *lifecycle) advanceRename(op *api.Operation, ev control.RenameEvidence, phase string, committed bool) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 2048 {
		return control.ErrInvalid
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed = phase, raw, committed
	return nil
}

// editIssued is the gate of a clone edit: it records the edit and the
// rename's plan in ev and moves op to phase.
func (l *lifecycle) editIssued(op *api.Operation, ev *control.RenameEvidence, phase string, committed bool) func(control.RenameEdit, string) error {
	return func(e control.RenameEdit, plan string) error {
		next := *ev
		next.Edit, next.Plan = e, plan
		if err := l.advanceRename(op, next, phase, committed); err != nil {
			return err
		}
		*ev = next
		return nil
	}
}

func (l *lifecycle) rollbackRenamePrecommit(op api.Operation, ev control.RenameEvidence, clone renameClone, cause error) {
	ctx := l.ctx
	if op.Phase == "prepare-issued" || op.Phase == "new-ref-create-issued" || op.Committed {
		l.blockRename(op, cause)
		return
	}
	if op.Phase == "workspace-prepared" {
		if _, err := clone.reach(ctx, sourcedriver.GoalDone, ev.Plan, l.editIssued(&op, &ev, op.Phase, op.Committed)); err != nil {
			l.blockRename(op, errors.Join(cause, err))
			return
		}
	}
	// A replaced or started source does not prevent releasing these two ref
	// guards after a definite no-effect precommit refusal.
	reason, diagnostic := api.ReasonChanged, "rename precommit input changed"
	var mismatch *sourcedriver.WorkspaceMismatch
	if errors.As(cause, &mismatch) {
		reason, diagnostic = api.ReasonWorkspaceMismatch, mismatch.Error()
	}
	if err := l.store.FailRenamePrecommit(ctx, op.ID, reason, diagnostic); err != nil {
		l.blockRename(op, errors.Join(cause, err))
	}
}

func (l *lifecycle) processRename(op api.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RenameEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.Project != op.Project || ev.InstanceUUID != l.instanceID ||
		ev.BaseFingerprint != l.cfg.BaseImageFingerprint || ev.OldBranch == "" || ev.NewBranch == "" {
		l.blockRename(op, errors.New("rename durable identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Project != ev.Project || s.PolicySHA256 != ev.PolicySHA256 || s.Registry != "established" ||
		(s.Branch != ev.OldBranch && s.Branch != ev.NewBranch) {
		l.blockRename(op, errors.Join(err, errors.New("rename assignment changed")))
		return
	}
	source, err := l.sessionSpec(ctx, s)
	if err != nil || string(source.Image) != ev.ImageFingerprint {
		l.blockRename(op, errors.Join(err, errors.New("rename runtime selection changed")))
		return
	}
	clone := l.renameClone(source, ev, op.ID)
	// Rollback and completion remove the backup without leaving their
	// phase: a removal whose gate recorded it is settled before anything
	// else.
	if ev.Edit.Goal == sourcedriver.GoalDone {
		if err := clone.settleEdit(ctx, ev.Edit, sourcedriver.GoalDone, ev.Plan); err != nil {
			l.blockRename(op, err)
			return
		}
	}
	for step := 0; step < 18; step++ {
		switch op.Phase {
		case "reserved":
			if err := l.advanceRename(&op, ev, "guarded", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "guarded":
			if err := l.verifyRenamePRefs(ctx, ev, false); err != nil {
				l.rollbackRenamePrecommit(op, ev, clone, err)
				return
			}
			observed, err := l.driver.Inspect(ctx, source)
			if err != nil || !renameSourceExact(observed, ev) {
				l.rollbackRenamePrecommit(op, ev, clone, errors.Join(err, errors.New("rename source changed or started")))
				return
			}
			plan, err := clone.reach(ctx, sourcedriver.GoalPrepared, "", l.editIssued(&op, &ev, "prepare-issued", false))
			if err != nil {
				l.rollbackRenamePrecommit(op, ev, clone, err)
				return
			}
			ev.Plan, ev.Edit = plan, control.RenameEdit{}
			if err := l.advanceRename(&op, ev, "workspace-prepared", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "prepare-issued":
			// A write whose reply was lost can land after an immediate miss.
			// Only the backup in place, its write settled by the runtime
			// driver, authorizes forward motion; anything else remains
			// guarded for explicit resolution.
			if err := clone.settleEdit(ctx, ev.Edit, sourcedriver.GoalPrepared, ev.Plan); err != nil {
				l.blockRename(op, err)
				return
			}
			ev.Edit = control.RenameEdit{}
			if err := l.advanceRename(&op, ev, "workspace-prepared", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-prepared":
			if err := l.verifyRenamePRefs(ctx, ev, false); err != nil {
				l.rollbackRenamePrecommit(op, ev, clone, err)
				return
			}
			if err := clone.holds(ctx, sourcedriver.GoalPrepared, ev.Plan); err != nil {
				l.rollbackRenamePrecommit(op, ev, clone, errors.Join(err, errors.New("prepared workspace changed")))
				return
			}
			err := l.createBranch(ctx, ev.Project, ev.NewBranch, ev.OldTip, func() error {
				return l.advanceRename(&op, ev, "new-ref-create-issued", false)
			})
			if err == nil {
				err = l.renameNewRefExact(ctx, ev)
			}
			if err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "new-ref-created", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "new-ref-create-issued":
			// A creation may have been admitted after its reply was lost. Only
			// the source driver's settlement, and the new head then exact,
			// prove it.
			err := l.settleHead(ctx, creation(ev.Project, ev.NewBranch, ev.OldTip), nil)
			if err == nil {
				err = l.renameNewRefExact(ctx, ev)
			}
			if err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "new-ref-created", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "new-ref-created":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if _, err := clone.reach(ctx, sourcedriver.GoalRenamed, ev.Plan, l.editIssued(&op, &ev, "workspace-rename-issued", true)); err != nil {
				l.blockRename(op, errors.Join(err, errors.New("workspace rename postcondition unavailable")))
				return
			}
			ev.Edit = control.RenameEdit{}
			if err := l.advanceRename(&op, ev, "workspace-renamed", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-rename-issued":
			if err := clone.settleEdit(ctx, ev.Edit, sourcedriver.GoalRenamed, ev.Plan); err != nil {
				l.blockRename(op, errors.Join(err, errors.New("workspace rename effect ambiguous")))
				return
			}
			ev.Edit = control.RenameEdit{}
			if err := l.advanceRename(&op, ev, "workspace-renamed", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-renamed":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := clone.holds(ctx, sourcedriver.GoalRenamed, ev.Plan); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.store.UpdateRenameAssignment(ctx, op.ID, func(ctx context.Context, ev control.RenameEvidence) error { return l.renameNewRefExact(ctx, ev) }); err != nil {
				l.blockRename(op, err)
				return
			}
			op.Phase = "assignment-updated"
		case "assignment-updated":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.deleteBranch(ctx, ev.Project, ev.OldBranch, ev.OldTip, func() error {
				return l.advanceRename(&op, ev, "old-ref-delete-issued", true)
			}); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "old-ref-deleted", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "old-ref-delete-issued":
			err := l.settleHead(ctx, deletion(ev.Project, ev.OldBranch, ev.OldTip), nil)
			if err == nil {
				err = l.verifyRenamePRefs(ctx, ev, true)
			}
			if err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "old-ref-deleted", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "old-ref-deleted":
			if err := clone.holds(ctx, sourcedriver.GoalRenamed, ev.Plan); err != nil {
				l.blockRename(op, err)
				return
			}
			if _, err := clone.reach(ctx, sourcedriver.GoalDone, ev.Plan, l.editIssued(&op, &ev, op.Phase, op.Committed)); err != nil {
				l.blockRename(op, err)
				return
			}
			ev.Edit = control.RenameEdit{}
			if err := l.advanceRename(&op, ev, "backup-absent", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "backup-absent":
			if err := l.verifyRenamePRefs(ctx, ev, true); err != nil {
				l.blockRename(op, err)
				return
			}
			observed, err := l.driver.Inspect(ctx, source)
			if err != nil || !renameSourceExact(observed, ev) {
				l.blockRename(op, errors.Join(err, errors.New("rename source changed before completion")))
				return
			}
			if err := l.store.CompleteRename(ctx, op.ID, func(ctx context.Context, ev control.RenameEvidence) error { return l.verifyRenamePRefs(ctx, ev, true) }); err != nil {
				l.blockRename(op, err)
				return
			}
			return
		default:
			l.blockRename(op, errors.New("rename phase unsupported"))
			return
		}
	}
	l.blockRename(op, errors.New("rename phase sequence exceeded bound"))
}
