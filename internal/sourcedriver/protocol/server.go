package protocol

import (
	"context"
	"encoding/json"
	"net"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Open makes the driver a hello configures.
type Open func(ctx context.Context, h Hello) (sourcedriver.Driver, error)

// Serve answers the hello on uc with the driver open makes, then serves
// that driver until the connection is lost or ctx ends. Calls in flight
// then have their contexts cancelled. Serve owns uc. It returns nil once
// core closed the connection or ctx ended, and otherwise why the connection
// ended, such as a refused hello.
func Serve(ctx context.Context, uc *net.UnixConn, open Open) error {
	s := &server{}
	return driverwire.Serve(ctx, uc, wire, func(ctx context.Context, config json.RawMessage, core Core) (driverwire.Handler, wireDescription, error) {
		d, err := open(ctx, Hello{Config: config, Core: core})
		if err != nil {
			return nil, wireDescription{}, err
		}
		s.driver = d
		return s.dispatch, wireDescription(d.Describe()), nil
	})
}

// server is the driver's side of one connection.
type server struct {
	// driver is set by the hello, in the read loop, before any call is
	// dispatched.
	driver sourcedriver.Driver
}

// dispatch runs call c on the driver and returns its result.
func (s *server) dispatch(ctx context.Context, c driverwire.Call) (any, error) {
	d, raw, gate := s.driver, c.Params, c.Gate
	switch c.Method {
	case methodCheck:
		if _, err := driverwire.Params[noParams](raw); err != nil {
			return nil, err
		}
		return nil, d.Check(ctx)
	case methodCheckNames:
		p, err := driverwire.Params[checkNamesParams](raw)
		if err != nil {
			return nil, err
		}
		names, err := d.CheckNames(ctx, fromNames(p.Names))
		return toNames(names), err
	case methodInspectRepository, methodCreateRepository:
		p, err := driverwire.Params[projectParams](raw)
		if err != nil {
			return nil, err
		}
		call := map[string]func(context.Context, string) (sourcedriver.Repository, error){
			methodInspectRepository: d.InspectRepository, methodCreateRepository: d.CreateRepository,
		}[c.Method]
		r, err := call(ctx, p.Project)
		return wireRepository(r), err
	case methodDeleteRepository:
		p, err := driverwire.Params[deleteRepositoryParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.DeleteRepository(ctx, sourcedriver.Repository(p.Repository), gate(p.Gate))
	case methodBranches:
		p, err := driverwire.Params[projectParams](raw)
		if err != nil {
			return nil, err
		}
		heads, err := d.Branches(ctx, p.Project)
		return toHeads(heads), err
	case methodInspectBranch:
		p, err := driverwire.Params[inspectBranchParams](raw)
		if err != nil {
			return nil, err
		}
		tip, found, err := d.InspectBranch(ctx, p.Project, p.Name)
		return inspectBranchResult{Tip: tip, Found: found}, err
	case methodCreateBranch:
		p, err := driverwire.Params[createBranchParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.CreateBranch(ctx, p.Project, p.Name, p.At, gate(p.Gate))
	case methodDeleteBranch:
		p, err := driverwire.Params[deleteBranchParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.DeleteBranch(ctx, p.Project, p.Name, p.Tip, gate(p.Gate))
	case methodInspectCommit:
		p, err := driverwire.Params[inspectCommitParams](raw)
		if err != nil {
			return nil, err
		}
		commit, err := d.InspectCommit(ctx, p.Project, p.ID)
		return wireCommit(commit), err
	case methodLosingCommits:
		p, err := driverwire.Params[losingCommitsParams](raw)
		if err != nil {
			return nil, err
		}
		loss, err := d.LosingCommits(ctx, p.Project, p.Removed)
		return toLoss(loss), err
	case methodExport:
		p, err := driverwire.Params[exportParams](raw)
		if err != nil {
			return nil, err
		}
		e, err := d.Export(ctx, p.Project, p.ID, p.Dir)
		return wireExport(e), err
	case methodRenameWorkspace:
		p, err := driverwire.Params[renameWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		edits, err := d.RenameWorkspace(ctx, sourcedriver.WorkspaceRename(p.Rename), fromWorkspaceFiles(p.Files))
		return toEdits(edits), err
	case methodServeGit:
		return nil, s.serveGit(ctx, c)
	case methodObserveOrigin:
		p, err := driverwire.Params[observeOriginParams](raw)
		if err != nil {
			return nil, err
		}
		refs, err := d.ObserveOrigin(ctx, p.URL)
		return toOriginRefs(refs), err
	case methodFetchOrigin:
		p, err := driverwire.Params[fetchOriginParams](raw)
		if err != nil {
			return nil, err
		}
		id, err := d.FetchOrigin(ctx, p.Project, p.URL, sourcedriver.OriginRef(p.Ref))
		return oidResult{OID: id}, err
	case methodComparePublication:
		p, err := driverwire.Params[comparePublicationParams](raw)
		if err != nil {
			return nil, err
		}
		var dest *sourcedriver.OriginRef
		if p.Dest != nil {
			ref := sourcedriver.OriginRef(*p.Dest)
			dest = &ref
		}
		relation, err := d.ComparePublication(ctx, p.Project, p.URL, p.Source, dest)
		return relationResult{Relation: relation}, err
	case methodPublish:
		p, err := driverwire.Params[publishParams](raw)
		if err != nil {
			return nil, err
		}
		status, err := d.Publish(ctx, p.Project, sourcedriver.Publication(p.Publication), gate(p.Gate))
		return publishResult{Status: status}, err
	case methodSettle:
		p, err := driverwire.Params[settleParams](raw)
		if err != nil {
			return nil, err
		}
		settled, err := d.Settle(ctx, sourcedriver.Effect(p.Effect))
		if err != nil {
			return nil, err
		}
		return toSettlement(settled)
	}
	return nil, driverwire.NoMethod(c.Method)
}
