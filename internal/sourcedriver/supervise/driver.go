package supervise

import (
	"context"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// The Driver operations go to the running driver.

func (s *Supervisor) Check(ctx context.Context) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.Check(ctx)
}

func (s *Supervisor) CheckNames(ctx context.Context, names []sourcedriver.Name) ([]sourcedriver.Name, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.CheckNames(ctx, names)
}

func (s *Supervisor) InspectRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	return d.InspectRepository(ctx, project)
}

func (s *Supervisor) CreateRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	return d.CreateRepository(ctx, project)
}

func (s *Supervisor) DeleteRepository(ctx context.Context, r sourcedriver.Repository, g sourcedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.DeleteRepository(ctx, r, g)
}

func (s *Supervisor) Branches(ctx context.Context, project string) ([]sourcedriver.Head, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.Branches(ctx, project)
}

func (s *Supervisor) InspectBranch(ctx context.Context, project, name string) (sourcedriver.OID, bool, error) {
	d, err := s.driver()
	if err != nil {
		return "", false, err
	}
	return d.InspectBranch(ctx, project, name)
}

func (s *Supervisor) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.CreateBranch(ctx, project, name, at, g)
}

func (s *Supervisor) DeleteBranch(ctx context.Context, project, name string, tip sourcedriver.OID, g sourcedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.DeleteBranch(ctx, project, name, tip, g)
}

func (s *Supervisor) InspectCommit(ctx context.Context, project string, id sourcedriver.OID) (sourcedriver.Commit, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Commit{}, err
	}
	return d.InspectCommit(ctx, project, id)
}

func (s *Supervisor) LosingCommits(ctx context.Context, project string, removed []string) (sourcedriver.Loss, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Loss{}, err
	}
	return d.LosingCommits(ctx, project, removed)
}

func (s *Supervisor) Export(ctx context.Context, project string, id sourcedriver.OID, dir string) (sourcedriver.Export, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Export{}, err
	}
	return d.Export(ctx, project, id, dir)
}

func (s *Supervisor) RenameWorkspace(ctx context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.WorkspaceEdits{}, err
	}
	return d.RenameWorkspace(ctx, r, files)
}

func (s *Supervisor) ServeGit(ctx context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.ServeGit(ctx, command, stream, open)
}

func (s *Supervisor) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.ObserveOrigin(ctx, url)
}

func (s *Supervisor) FetchOrigin(ctx context.Context, project, url string, ref sourcedriver.OriginRef) (sourcedriver.OID, error) {
	d, err := s.driver()
	if err != nil {
		return "", err
	}
	return d.FetchOrigin(ctx, project, url, ref)
}

func (s *Supervisor) ComparePublication(ctx context.Context, project, url string, source sourcedriver.OID, dest *sourcedriver.OriginRef) (sourcedriver.Relation, error) {
	d, err := s.driver()
	if err != nil {
		return "", err
	}
	return d.ComparePublication(ctx, project, url, source, dest)
}

func (s *Supervisor) Publish(ctx context.Context, project string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	d, err := s.driver()
	if err != nil {
		return "", err
	}
	return d.Publish(ctx, project, p, g)
}

func (s *Supervisor) Settle(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	d, err := s.driver()
	if err != nil {
		return sourcedriver.Unknown, err
	}
	return d.Settle(ctx, e)
}
