package protocol

import (
	"context"
	"net"
	"slices"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Client is a runtime driver at the other end of a protocol connection.
type Client struct {
	c            *driverwire.Client
	capabilities runtimedriver.Capabilities
}

var _ runtimedriver.Driver = (*Client)(nil)

// Connect agrees the protocol with the driver at the other end of uc,
// giving it h, and returns a client for it. The client owns uc; on an error
// uc is closed.
func Connect(ctx context.Context, uc *net.UnixConn, h Hello) (*Client, error) {
	c, capabilities, err := driverwire.Connect[Core, wireCapabilities](ctx, uc, wire, h.Config, h.Core)
	if err != nil {
		return nil, err
	}
	return &Client{c: c, capabilities: runtimedriver.Capabilities(capabilities)}, nil
}

// Close ends the connection. Calls in flight end as if it were lost.
func (cl *Client) Close() error { return cl.c.Close() }

// Done closes once the connection is lost.
func (cl *Client) Done() <-chan struct{} { return cl.c.Done() }

func (cl *Client) Describe() runtimedriver.Capabilities {
	c := cl.capabilities
	c.Networks = slices.Clone(c.Networks)
	return c
}

func (cl *Client) Check(ctx context.Context) error {
	return cl.c.Call(ctx, methodCheck, noParams{}, nil, nil, nil)
}

func (cl *Client) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	var res wireInventory
	if err := cl.c.Call(ctx, methodInventory, noParams{}, nil, nil, &res); err != nil {
		return runtimedriver.Inventory{}, err
	}
	return res.inventory(), nil
}

func (cl *Client) observe(ctx context.Context, method string, params any, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	var res wireObservation
	if err := cl.c.Call(ctx, method, params, g, nil, &res); err != nil {
		return runtimedriver.Observation{}, err
	}
	return res.observation(), nil
}

func (cl *Client) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodInspect, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodCreate, createParams{Session: toSession(s), At: at, Gate: g != nil}, g)
}

func (cl *Client) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodStart, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) Stop(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodStop, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) HostLog(ctx context.Context, t runtimedriver.Target) (string, error) {
	var res hostLogResult
	err := cl.c.Call(ctx, methodHostLog, targetParams{Target: toTarget(t)}, nil, nil, &res)
	return res.Text, err
}

func (cl *Client) ReadWorkspace(ctx context.Context, t runtimedriver.Target, path string, limit int64) (runtimedriver.File, error) {
	var res wireFile
	if err := cl.c.Call(ctx, methodReadWorkspace, readWorkspaceParams{Target: toTarget(t), Path: path, Limit: limit}, nil, nil, &res); err != nil {
		return runtimedriver.File{}, err
	}
	return runtimedriver.File(res), nil
}

func (cl *Client) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	return cl.c.Call(ctx, methodWriteWorkspace, writeWorkspaceParams{Target: toTarget(t), Path: path, File: wireFile(f), Gate: g != nil}, g, nil, nil)
}

func (cl *Client) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, path string, g runtimedriver.Gate) error {
	return cl.c.Call(ctx, methodRemoveWorkspace, removeWorkspaceParams{Target: toTarget(t), Path: path, Gate: g != nil}, g, nil, nil)
}

func (cl *Client) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	var res []wireService
	if err := cl.c.Call(ctx, methodServices, targetParams{Target: toTarget(t)}, nil, nil, &res); err != nil {
		return nil, err
	}
	return each(res, func(s wireService) runtimedriver.Service { return runtimedriver.Service(s) }), nil
}

func (cl *Client) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	return cl.c.Call(ctx, methodServiceAction, serviceActionParams{Target: toTarget(t), Name: name, Action: a}, nil, nil, nil)
}

func (cl *Client) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	var res wireLogs
	if err := cl.c.Call(ctx, methodServiceLogs, serviceParams{Target: toTarget(t), Name: name}, nil, nil, &res); err != nil {
		return runtimedriver.Logs{}, err
	}
	return runtimedriver.Logs(res), nil
}

func (cl *Client) BuildImage(ctx context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	var res wireImage
	if err := cl.c.Call(ctx, methodBuildImage, buildImageParams{Build: toBuild(b), Gate: g != nil}, g, nil, &res); err != nil {
		return runtimedriver.Image{}, err
	}
	return res.image(), nil
}

func (cl *Client) Images(ctx context.Context) ([]runtimedriver.Image, error) {
	var res []wireImage
	if err := cl.c.Call(ctx, methodImages, noParams{}, nil, nil, &res); err != nil {
		return nil, err
	}
	return each(res, wireImage.image), nil
}

func (cl *Client) InspectImage(ctx context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	var res inspectImageResult
	if err := cl.c.Call(ctx, methodInspectImage, inspectImageParams{Ref: ref}, nil, nil, &res); err != nil {
		return runtimedriver.Image{}, false, err
	}
	return res.Image.image(), res.Found, nil
}

func (cl *Client) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	return cl.c.Call(ctx, methodDeleteImage, deleteImageParams{Image: toImage(img), Gate: g != nil}, g, nil, nil)
}

func (cl *Client) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	var res wireSettlement
	if err := cl.c.Call(ctx, methodSettle, settleParams{Effect: toEffect(e)}, nil, nil, &res); err != nil {
		return runtimedriver.Unknown, err
	}
	return res.settlement()
}

func (cl *Client) Delete(ctx context.Context, r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	return cl.c.Call(ctx, methodDelete, deleteParams{Ref: wireRef(r), At: at, Gate: g != nil}, g, nil, nil)
}
