package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func projectDeleteAbsent(r control.ProjectDeleteResource) bool {
	return r.Status == "deleted" || r.Status == "already_absent"
}

// projectDeleteRuntimeDecision decides what a pass does with a session's
// runtime. An issued DELETE not yet recorded as done is settled before
// anything else, even when the runtime already looks absent; absence then
// still needs the full authoritative proof. A present issued target never
// grants redispatch. A running runtime is stopped first; its stop is
// recorded before it is sent, and since stopping is idempotent a pass that
// finds it still running after a recorded stop sends it again.
func projectDeleteRuntimeDecision(resource control.ProjectDeleteResource, planned api.RemovalRuntime, spec runtimedriver.Session, actual runtimedriver.Observation) string {
	if actual.State == runtimedriver.StateAbsent {
		if resource.DeleteIssued && !projectDeleteAbsent(resource) {
			return "issued-unresolved"
		}
		return "prove-absence"
	}
	at := recordedIncarnation(planned.IncusUUID, planned.Generation)
	if projectDeleteAbsent(resource) || planned.Condition != "present" || !removableState(actual.State) || at == "" || actual.Incarnation != at ||
		string(spec.Image) != planned.ImageFingerprint {
		return "identity-conflict"
	}
	switch {
	case resource.DeleteIssued:
		return "issued-unresolved"
	case actual.State == runtimedriver.StateRunning:
		return "stop-exact"
	}
	return "delete-exact"
}

// Every pass attempts independent exact ensure-absent targets. A native issued
// but unresolved DELETE stays recorded; it cannot be replayed against a name.
func (l *lifecycle) processProjectDelete(observed api.Operation) {
	if observed.Status != "running" && observed.Status != "blocked" {
		return
	}
	release, err := l.lockProjectDelete(l.ctx, observed.Project)
	if err != nil {
		return
	}
	defer release()
	op, err := l.store.GetOperation(l.ctx, observed.ID)
	if err != nil || op.Status != "running" && op.Status != "blocked" {
		return
	}
	var ev control.ProjectDeleteEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.IncusProject != l.incus.Project || ev.BaseFingerprint != l.cfg.BaseImageFingerprint || l.store.ValidateProjectDeleteAuthority(l.ctx, op, ev) != nil {
		_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, true, op.Evidence, api.ReasonChanged, "confirmed project deletion exact identity/authority unavailable")
		return
	}
	ctx := l.ctx
	persist := func(status, reason, diagnostic string) error {
		if e := l.store.UpdateProjectDelete(ctx, op, ev, status, reason, diagnostic); e != nil {
			return e
		}
		raw, _ := json.Marshal(ev)
		op.Evidence = raw
		op.Status = status
		op.Reason, op.Diagnostic = reason, diagnostic
		return nil
	}
	if op.Status == "blocked" {
		if err = persist("running", "", ""); err != nil {
			return
		}
	}
	// failure is the reason of the first resource that could not be removed;
	// the operation blocks with it when resources remain.
	failure := ""
	fail := func(index int, cause error) bool {
		if ctx.Err() != nil {
			return false
		}
		r := &ev.Resources[index]
		if !projectDeleteAbsent(*r) {
			r.Status = "unreachable"
		}
		r.Diagnostic = control.ResourceDiagnostic(cause.Error())
		if failure == "" {
			failure = control.OperationReason(cause)
		}
		return persist("running", "", "") == nil
	}
	markAbsent := func(index int) bool {
		r := &ev.Resources[index]
		if !projectDeleteAbsent(*r) {
			if r.DeleteIssued {
				r.Status = "deleted"
			} else {
				r.Status = "already_absent"
			}
		}
		r.Diagnostic = ""
		return persist("running", "", "") == nil
	}
	verifiedRuntimes := make([]bool, len(ev.Sessions))
	for i, ps := range ev.Sessions {
		ri, li := 2*i, 2*i+1
		session, err := l.store.GetSession(ctx, ps.Session.UUID)
		if err != nil {
			if !fail(ri, err) {
				return
			}
			continue
		}
		spec, err := l.sessionSpec(ctx, session)
		if err != nil {
			if !fail(ri, err) {
				return
			}
			continue
		}
		prove := func(call context.Context) error { return proveRuntimeAbsent(call, l.driver, spec) }
		actual, err := l.driver.Inspect(ctx, spec)
		if err != nil {
			if !fail(ri, err) {
				return
			}
			continue
		}
		decision := projectDeleteRuntimeDecision(ev.Resources[ri], ps.Runtime, spec, actual)
		if decision == "prove-absence" {
			if err = prove(ctx); err != nil {
				if !fail(ri, err) {
					return
				}
				continue
			}
			if !markAbsent(ri) {
				return
			}
		} else {
			r := &ev.Resources[ri]
			if decision == "identity-conflict" {
				if !fail(ri, &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonRuntimePresent, Cause: fmt.Errorf("runtime identity changed at %s", ps.Runtime.InstanceName)}) {
					return
				}
				continue
			}
			at := recordedIncarnation(ps.Runtime.IncusUUID, ps.Runtime.Generation)
			if decision == "issued-unresolved" {
				err = l.settleRuntimeDelete(ctx, spec, at, fmt.Errorf("native delete outcome unresolved for %s UUID %s generation %s", ps.Runtime.InstanceName, ps.Runtime.IncusUUID, ps.Runtime.Generation))
			} else {
				if decision == "stop-exact" {
					err = l.stopReviewedRuntime(ctx, spec, at, func() error {
						if r.StopIssued {
							return nil
						}
						r.StopIssued = true
						return persist("running", "", "")
					})
				}
				if err == nil {
					err = l.deleteRuntime(ctx, spec, at, func() error { r.DeleteIssued = true; return persist("running", "", "") })
				}
			}
			if err != nil {
				if !fail(ri, err) {
					return
				}
				continue
			}
			if err = prove(ctx); err != nil {
				if !fail(ri, err) {
					return
				}
				continue
			}
			if !markAbsent(ri) {
				return
			}
		}
		verifiedRuntimes[i] = true
		if !projectDeleteAbsent(ev.Resources[li]) {
			ev.Resources[li].DeleteIssued = true
			if err = persist("running", "", ""); err != nil {
				return
			}
		}
		if err = cleanupReplacementLocal(ctx, l.store.StateDir(), l.endpoints, &ps.Local, prove); err != nil {
			if !fail(li, err) {
				return
			}
			continue
		}
		if err = l.createCleanupLocalAbsent(ps.Session.UUID); err != nil {
			if !fail(li, err) {
				return
			}
			continue
		}
		// The local removal primitive permits exact missing files on subsequent
		// passes; its independent absence receipt never rewinds.
		if !projectDeleteAbsent(ev.Resources[li]) {
			ev.Resources[li].DeleteIssued = true
		}
		if !markAbsent(li) {
			return
		}
	}
	runtimesAbsent := true
	for i := range ev.Sessions {
		if !verifiedRuntimes[i] {
			runtimesAbsent = false
		}
	}
	for i, c := range ev.Images {
		index := 2*len(ev.Sessions) + i
		if !runtimesAbsent {
			continue
		}
		if e := l.deleteProjectImage(ctx, c, &ev.Resources[index], func() error { return persist("running", "", "") }); e != nil {
			if !fail(index, e) {
				return
			}
			continue
		}
		if !markAbsent(index) {
			return
		}
	}
	verifyResources := func(call context.Context) error {
		if e := proveProjectReviewed(call, l.driver, op.Project, nil, nil); e != nil {
			return e
		}
		for _, ps := range ev.Sessions {
			session, e := l.store.GetSession(call, ps.Session.UUID)
			if e != nil {
				return e
			}
			spec, e := l.sessionSpec(call, session)
			if e != nil {
				return e
			}
			if e = proveRuntimeAbsent(call, l.driver, spec); e != nil {
				return e
			}
			if e = l.createCleanupLocalAbsent(ps.Session.UUID); e != nil {
				return e
			}
		}
		for _, c := range ev.Images {
			_, found, e := l.driver.InspectImage(call, runtimedriver.ImageRef(c.Image.Ref))
			if e != nil {
				return imageFailure(e)
			}
			if found {
				return control.Failed(api.ReasonRuntime, fmt.Errorf("project image %s remains", c.Image.Ref))
			}
			_, found, e = l.store.GetCommitImage(call, c.Image.Key)
			if e != nil || found {
				return errors.Join(e, errors.New("project image index remains"))
			}
		}
		return nil
	}
	last := len(ev.Resources) - 1
	allAbsent := true
	for _, r := range ev.Resources[:last] {
		if !projectDeleteAbsent(r) {
			allAbsent = false
		}
	}
	if allAbsent {
		if err = verifyResources(ctx); err != nil {
			if !fail(last, err) {
				return
			}
		} else {
			repo, e := l.git.backend.RepositoryPath(op.Project)
			if e == nil {
				e = matchReplacementLocal(filepath.Dir(repo), ev.RepositoryParent, false)
			}
			if e == nil {
				e = matchReplacementLocal(repo, ev.Repository, true)
			}
			if e == nil {
				if _, statErr := os.Lstat(repo); statErr == nil {
					if projectDeleteAbsent(ev.Resources[last]) {
						e = errors.New("repository reappeared after confirmed absence")
					} else {
						ev.Resources[last].DeleteIssued = true
						if e = persist("running", "", ""); e == nil {
							e = os.RemoveAll(repo)
						}
					}
				} else if !errors.Is(statErr, os.ErrNotExist) {
					e = statErr
				}
			}
			if e == nil {
				e = l.projectDeleteRepositoryAbsent(op.Project, ev.RepositoryParent)
			}
			if e != nil {
				if !fail(last, e) {
					return
				}
			} else if !markAbsent(last) {
				return
			}
		}
	}
	allAbsent = true
	for _, r := range ev.Resources {
		if !projectDeleteAbsent(r) {
			allAbsent = false
		}
	}
	if allAbsent {
		err = l.store.CompleteProjectDelete(ctx, op, func(call context.Context) error {
			if e := verifyResources(call); e != nil {
				return e
			}
			return l.projectDeleteRepositoryAbsent(op.Project, ev.RepositoryParent)
		})
		if err == nil {
			l.forgetProject(op.Project)
			func() {
				l.eventMu.Lock()
				defer l.eventMu.Unlock()
				for _, ps := range ev.Sessions {
					delete(l.observed, ps.Session.UUID)
					delete(l.eventContexts, ps.Session.UUID)
				}
			}()
			return
		}
	}
	reason, diagnostic := failure, "project deletion incomplete"
	if err != nil {
		reason, diagnostic = control.OperationReason(err), fmt.Sprintf("project deletion completion blocked: %v", err)
	}
	if reason == "" {
		reason = api.ReasonInternal
	}
	_ = persist("blocked", reason, diagnostic)
}

// deleteProjectImage deletes one reviewed image of a project deletion and
// forgets its index row. The delete is recorded as issued right before it
// starts. An image present again after its reviewed or confirmed absence is
// never deleted: it is not the reviewed one.
func (l *lifecycle) deleteProjectImage(ctx context.Context, c control.CommitImageClaim, r *control.ProjectDeleteResource, persist func() error) error {
	current, found, err := l.store.GetCommitImage(ctx, c.Image.Key)
	if err != nil {
		return err
	}
	if found && !reflect.DeepEqual(current, c.Image) {
		return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, fmt.Errorf("index row of image %s changed", c.Image.Ref))
	}
	_, present, err := l.driver.InspectImage(ctx, runtimedriver.ImageRef(c.Image.Ref))
	if err != nil {
		return imageFailure(err)
	}
	if present {
		if projectDeleteAbsent(*r) || !c.ImagePresent {
			return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, fmt.Errorf("image %s present after its reviewed or confirmed absence", c.Image.Ref))
		}
		var gateErr error
		err = l.driver.DeleteImage(ctx, indexedImage(c.Image), func() error { r.DeleteIssued = true; gateErr = persist(); return gateErr })
		if gateErr != nil {
			return err
		}
		if err != nil {
			return imageFailure(err)
		}
	}
	return l.store.ForgetCommitImage(ctx, c.Image.Key, c.Image.Ref)
}
