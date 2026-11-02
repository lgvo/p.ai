package gitservice

import (
	"context"

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
