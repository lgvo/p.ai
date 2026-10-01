package gitservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
)

// PCommitPresent distinguishes a local tip that is already a P-owned commit
// object from one whose bytes exist only in the runtime. The latter requires
// a separately authorized, bounded transfer and is unavailable in this gate.
func (b *Backend) PCommitPresent(ctx context.Context, project, oid string) (bool, error) {
	if !validProject(project) || !validOID(oid) || allZero(oid) {
		return false, control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return false, err
	}
	repo, err := b.checkRepository(ctx, project)
	if err != nil {
		return false, err
	}
	types, err := b.lossPObjectTypes(ctx, repo, nil, []string{oid})
	if err != nil {
		return false, err
	}
	return types[oid] == "commit", nil
}

// CreateRestoredAssignedRefExact performs only the absent-ref zero-old CAS.
// The durable operation and existing session assignment authorize skipping
// normal reachability from another P head. The object must already exist in
// P's bare repository; runtime bytes are never parsed by host Git.
func (b *Backend) CreateRestoredAssignedRefExact(ctx context.Context, operationID, project, branch, tip string) error {
	if b.store == nil || !validProject(project) || !validBranch(branch) || !validOID(tip) || allZero(tip) {
		return control.ErrInvalid
	}
	op, err := b.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	var ev control.RefRepairEvidence
	if op.Kind != "session.ref.repair" || op.Status != "running" || op.Phase != "ref-create-issued" ||
		op.Project != project || op.SessionUUID == "" ||
		json.Unmarshal(op.Evidence, &ev) != nil || ev.Project != project || ev.Branch != branch || ev.Tip != tip {
		return control.ErrConflict
	}
	session, err := b.store.GetSession(ctx, op.SessionUUID)
	if err != nil || session.Project != project || session.Branch != branch || session.Registry != "established" ||
		session.PolicySHA256 != ev.PolicySHA256 {
		return errors.Join(err, control.ErrConflict)
	}
	present, err := b.PCommitPresent(ctx, project, tip)
	if err != nil || !present {
		return errors.Join(err, control.ErrConflict)
	}
	current, exists, err := b.InspectBranchRef(ctx, project, branch)
	if err != nil || exists || current != "" {
		return errors.Join(err, control.ErrConflict)
	}
	return b.createBranch(ctx, project, branch, tip, true)
}
