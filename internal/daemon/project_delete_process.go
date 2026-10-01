package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func projectDeleteAbsent(r control.ProjectDeleteResource) bool {
	return r.Status == "deleted" || r.Status == "already_absent"
}

// Absence observed after an issued DELETE can recover only through the full
// authoritative absence proof; a present issued target never grants redispatch.
func projectDeleteRuntimeDecision(resource control.ProjectDeleteResource, review control.RemovalRuntimePreview, actual runtimeincus.Observation) string {
	if !actual.Exists {
		return "prove-absence"
	}
	if projectDeleteAbsent(resource) || review.Condition != "present" || actual.Status != "Stopped" || actual.Name != review.InstanceName || actual.IncusUUID != review.IncusUUID || actual.Generation != review.Generation || actual.Fingerprint != review.ImageFingerprint {
		return "identity-conflict"
	}
	if resource.DeleteIssued {
		return "issued-unresolved"
	}
	return "delete-exact"
}

// Every pass attempts independent exact ensure-absent targets. A native issued
// but unresolved DELETE stays recorded; it cannot be replayed against a name.
func (l *lifecycle) processProjectDelete(observed control.Operation) {
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
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.IncusProject != l.cfg.IncusProject || ev.BaseFingerprint != l.cfg.BaseImageFingerprint || l.store.ValidateProjectDeleteAuthority(l.ctx, op, ev) != nil {
		_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, true, op.Evidence, "confirmed project deletion exact identity/authority unavailable")
		return
	}
	ctx := l.ctx
	persist := func(status string, diagnostic string) error {
		if e := l.store.UpdateProjectDelete(ctx, op, ev, status, diagnostic); e != nil {
			return e
		}
		raw, _ := json.Marshal(ev)
		op.Evidence = raw
		op.Status = status
		op.Diagnostic = diagnostic
		return nil
	}
	if op.Status == "blocked" {
		if err = persist("running", ""); err != nil {
			return
		}
	}
	fail := func(index int, cause error) bool {
		if ctx.Err() != nil {
			return false
		}
		r := &ev.Resources[index]
		if !projectDeleteAbsent(*r) {
			r.Status = "unreachable"
		}
		r.Diagnostic = cause.Error()
		if len(r.Diagnostic) > 512 {
			r.Diagnostic = r.Diagnostic[:512]
		}
		return persist("running", "") == nil
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
		return persist("running", "") == nil
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
		native, err := l.runtimeSession(ctx, session)
		if err != nil {
			if !fail(ri, err) {
				return
			}
			continue
		}
		prove := func(call context.Context) error { return l.runtime.ConfirmSessionRuntimeAbsent(call, native) }
		actual, err := l.runtime.Inspect(ctx, native)
		if err != nil {
			if !fail(ri, err) {
				return
			}
			continue
		}
		decision := projectDeleteRuntimeDecision(ev.Resources[ri], ps.Runtime, actual)
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
				if !fail(ri, fmt.Errorf("runtime identity changed at %s; preserve unfamiliar resource and investigate manually", ps.Runtime.InstanceName)) {
					return
				}
				continue
			}
			if decision == "issued-unresolved" {
				if !fail(ri, fmt.Errorf("native delete outcome unresolved for %s UUID %s generation %s; administrative reconciliation required, no name-based replay", ps.Runtime.InstanceName, ps.Runtime.IncusUUID, ps.Runtime.Generation)) {
					return
				}
				continue
			}
			err = l.runtime.DeleteForDiscardExact(ctx, native, ps.Runtime.IncusUUID, ps.Runtime.Generation, func() error { r.DeleteIssued = true; return persist("running", "") })
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
			if err = persist("running", ""); err != nil {
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
		current, found, e := l.store.GetEnvironmentImage(ctx, c.Image.Project, c.Image.Key)
		if e != nil || found && !control.SameEnvironmentImage(current, c.Image) {
			if !fail(index, errors.Join(e, errors.New("cache index identity changed"))) {
				return
			}
			continue
		}
		owned := imageClaimFromCache(c.Image)
		_, present, e := l.runtime.ObserveOwnedBuilderImage(ctx, owned)
		if e != nil {
			if !fail(index, e) {
				return
			}
			continue
		}
		if present {
			if projectDeleteAbsent(ev.Resources[index]) || !c.ImagePresent {
				if !fail(index, errors.New("cache image reappeared after reviewed/confirmed absence")) {
					return
				}
				continue
			}
			ev.Resources[index].DeleteIssued = true
			if e = persist("running", ""); e != nil {
				return
			}
			if e = l.runtime.DeleteOwnedBuilderImage(ctx, owned); e != nil {
				if !fail(index, e) {
					return
				}
				continue
			}
		}
		if e = l.store.DeleteEnvironmentImageExact(ctx, c.Image); e != nil {
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
		if e := l.runtime.CheckProjectDeletionInventory(call, op.Project, map[string]bool{}, map[string]bool{}); e != nil {
			return e
		}
		for _, ps := range ev.Sessions {
			session, e := l.store.GetSession(call, ps.Session.UUID)
			if e != nil {
				return e
			}
			native, e := l.runtimeSession(call, session)
			if e != nil {
				return e
			}
			if e = l.runtime.ConfirmSessionRuntimeAbsent(call, native); e != nil {
				return e
			}
			if e = l.createCleanupLocalAbsent(ps.Session.UUID); e != nil {
				return e
			}
		}
		for _, c := range ev.Images {
			_, found, e := l.runtime.ObserveOwnedBuilderImage(call, imageClaimFromCache(c.Image))
			if e != nil || found {
				return errors.Join(e, errors.New("project cache image absence unavailable"))
			}
			_, found, e = l.store.GetEnvironmentImage(call, c.Image.Project, c.Image.Key)
			if e != nil || found {
				return errors.Join(e, errors.New("project cache index remains"))
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
						if e = persist("running", ""); e == nil {
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
			l.recordProgress(op, "completed")
			l.forgetProjectDeletePreviews(op.Project)
			l.eventMu.Lock()
			for _, ps := range ev.Sessions {
				delete(l.observed, ps.Session.UUID)
				delete(l.eventContexts, ps.Session.UUID)
			}
			l.eventMu.Unlock()
			return
		}
	}
	diagnostic := "project deletion incomplete; Retry exact confirmed operation after authority returns, or investigate reported native identity"
	if err != nil {
		diagnostic = fmt.Sprintf("project deletion completion blocked: %v", err)
		if len(diagnostic) > 900 {
			diagnostic = diagnostic[:900]
		}
	}
	if persist("blocked", diagnostic) == nil {
		l.recordProgress(op, "blocked")
	}
}
