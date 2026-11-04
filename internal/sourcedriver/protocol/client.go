package protocol

import (
	"context"
	"net"
	"slices"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Client is a source driver at the other end of a protocol connection.
type Client struct {
	c           *driverwire.Client
	description sourcedriver.Description
}

var _ sourcedriver.Driver = (*Client)(nil)

// Connect agrees the protocol with the driver at the other end of uc,
// giving it h, and returns a client for it. The client owns uc; on an error
// uc is closed.
func Connect(ctx context.Context, uc *net.UnixConn, h Hello) (*Client, error) {
	c, description, err := driverwire.Connect[Core, wireDescription](ctx, uc, wire, h.Config, h.Core)
	if err != nil {
		return nil, err
	}
	return &Client{c: c, description: sourcedriver.Description(description)}, nil
}

// Close ends the connection. Calls in flight end as if it were lost.
func (cl *Client) Close() error { return cl.c.Close() }

// Done closes once the connection is lost.
func (cl *Client) Done() <-chan struct{} { return cl.c.Done() }

// Describe is what the driver answered the hello with.
func (cl *Client) Describe() sourcedriver.Description {
	d := cl.description
	d.Commands = slices.Clone(d.Commands)
	return d
}

func (cl *Client) Check(ctx context.Context) error {
	return cl.c.Call(ctx, methodCheck, noParams{}, nil, nil, nil)
}

func (cl *Client) CheckNames(ctx context.Context, names []sourcedriver.Name) ([]sourcedriver.Name, error) {
	var res []wireName
	if err := cl.c.Call(ctx, methodCheckNames, checkNamesParams{Names: toNames(names)}, nil, nil, &res); err != nil {
		return nil, err
	}
	return fromNames(res), nil
}

func (cl *Client) repository(ctx context.Context, method, project string) (sourcedriver.Repository, error) {
	var res wireRepository
	if err := cl.c.Call(ctx, method, projectParams{Project: project}, nil, nil, &res); err != nil {
		return sourcedriver.Repository{}, err
	}
	return sourcedriver.Repository(res), nil
}

func (cl *Client) InspectRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	return cl.repository(ctx, methodInspectRepository, project)
}

func (cl *Client) CreateRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	return cl.repository(ctx, methodCreateRepository, project)
}

func (cl *Client) DeleteRepository(ctx context.Context, r sourcedriver.Repository, g sourcedriver.Gate) error {
	return cl.c.Call(ctx, methodDeleteRepository, deleteRepositoryParams{Repository: wireRepository(r), Gate: g != nil}, g, nil, nil)
}

func (cl *Client) Branches(ctx context.Context, project string) ([]sourcedriver.Head, error) {
	var res []wireHead
	if err := cl.c.Call(ctx, methodBranches, projectParams{Project: project}, nil, nil, &res); err != nil {
		return nil, err
	}
	return fromHeads(res), nil
}

func (cl *Client) InspectBranch(ctx context.Context, project, name string) (sourcedriver.OID, bool, error) {
	var res inspectBranchResult
	if err := cl.c.Call(ctx, methodInspectBranch, inspectBranchParams{Project: project, Name: name}, nil, nil, &res); err != nil {
		return "", false, err
	}
	return res.Tip, res.Found, nil
}

func (cl *Client) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	return cl.c.Call(ctx, methodCreateBranch, createBranchParams{Project: project, Name: name, At: at, Gate: g != nil}, g, nil, nil)
}

func (cl *Client) DeleteBranch(ctx context.Context, project, name string, tip sourcedriver.OID, g sourcedriver.Gate) error {
	return cl.c.Call(ctx, methodDeleteBranch, deleteBranchParams{Project: project, Name: name, Tip: tip, Gate: g != nil}, g, nil, nil)
}

func (cl *Client) InspectCommit(ctx context.Context, project string, id sourcedriver.OID) (sourcedriver.Commit, error) {
	var res wireCommit
	if err := cl.c.Call(ctx, methodInspectCommit, inspectCommitParams{Project: project, ID: id}, nil, nil, &res); err != nil {
		return sourcedriver.Commit{}, err
	}
	return sourcedriver.Commit(res), nil
}

func (cl *Client) LosingCommits(ctx context.Context, project string, removed []string) (sourcedriver.Loss, error) {
	var res wireLoss
	if err := cl.c.Call(ctx, methodLosingCommits, losingCommitsParams{Project: project, Removed: removed}, nil, nil, &res); err != nil {
		return sourcedriver.Loss{}, err
	}
	return res.loss(), nil
}

func (cl *Client) Export(ctx context.Context, project string, id sourcedriver.OID, dir string) (sourcedriver.Export, error) {
	var res wireExport
	if err := cl.c.Call(ctx, methodExport, exportParams{Project: project, ID: id, Dir: dir}, nil, nil, &res); err != nil {
		return sourcedriver.Export{}, err
	}
	return sourcedriver.Export(res), nil
}

func (cl *Client) RenameWorkspace(ctx context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	var res wireEdits
	if err := cl.c.Call(ctx, methodRenameWorkspace, renameWorkspaceParams{Rename: wireRename(r), Files: toWorkspaceFiles(files)}, nil, nil, &res); err != nil {
		return sourcedriver.WorkspaceEdits{}, err
	}
	return res.edits(), nil
}

func (cl *Client) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	var res []wireOriginRef
	if err := cl.c.Call(ctx, methodObserveOrigin, observeOriginParams{URL: url}, nil, nil, &res); err != nil {
		return nil, err
	}
	return fromOriginRefs(res), nil
}

func (cl *Client) FetchOrigin(ctx context.Context, project, url string, ref sourcedriver.OriginRef) (sourcedriver.OID, error) {
	var res oidResult
	if err := cl.c.Call(ctx, methodFetchOrigin, fetchOriginParams{Project: project, URL: url, Ref: wireOriginRef(ref)}, nil, nil, &res); err != nil {
		return "", err
	}
	return res.OID, nil
}

func (cl *Client) ComparePublication(ctx context.Context, project, url string, source sourcedriver.OID, dest *sourcedriver.OriginRef) (sourcedriver.Relation, error) {
	var d *wireOriginRef
	if dest != nil {
		w := wireOriginRef(*dest)
		d = &w
	}
	var res relationResult
	if err := cl.c.Call(ctx, methodComparePublication, comparePublicationParams{Project: project, URL: url, Source: source, Dest: d}, nil, nil, &res); err != nil {
		return "", err
	}
	return res.Relation, nil
}

func (cl *Client) Publish(ctx context.Context, project string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	var res publishResult
	if err := cl.c.Call(ctx, methodPublish, publishParams{Project: project, Publication: wirePublication(p), Gate: g != nil}, g, nil, &res); err != nil {
		return "", err
	}
	return res.Status, nil
}

func (cl *Client) Settle(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	var res settleResult
	if err := cl.c.Call(ctx, methodSettle, settleParams{Effect: wireEffect(e)}, nil, nil, &res); err != nil {
		return sourcedriver.Unknown, err
	}
	return res.settlement()
}
