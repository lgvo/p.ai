package supervise

import (
	"context"
	"slices"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The Driver operations go to the running driver.

// Describe is what the driver described at its first start; a restarted
// driver must describe the same.
func (s *Supervisor) Describe() runtimedriver.Capabilities {
	c := s.capabilities
	c.Networks = slices.Clone(c.Networks)
	return c
}

func (s *Supervisor) Check(ctx context.Context) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.Check(ctx)
}

func (s *Supervisor) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Inventory{}, err
	}
	return d.Inventory(ctx)
}

func (s *Supervisor) Inspect(ctx context.Context, sess runtimedriver.Session) (runtimedriver.Observation, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.Inspect(ctx, sess)
}

func (s *Supervisor) Create(ctx context.Context, sess runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.Create(ctx, sess, at, g)
}

func (s *Supervisor) Start(ctx context.Context, sess runtimedriver.Session) (runtimedriver.Observation, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.Start(ctx, sess)
}

func (s *Supervisor) Stop(ctx context.Context, sess runtimedriver.Session) (runtimedriver.Observation, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.Stop(ctx, sess)
}

func (s *Supervisor) HostLog(ctx context.Context, t runtimedriver.Target) (string, error) {
	d, err := s.driver()
	if err != nil {
		return "", err
	}
	return d.HostLog(ctx, t)
}

func (s *Supervisor) OpenTerminal(ctx context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.OpenTerminal(ctx, t, size)
}

func (s *Supervisor) ReadWorkspace(ctx context.Context, t runtimedriver.Target, path string, limit int64) (runtimedriver.File, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.File{}, err
	}
	return d.ReadWorkspace(ctx, t, path, limit)
}

func (s *Supervisor) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.WriteWorkspace(ctx, t, path, f, g)
}

func (s *Supervisor) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, path string, g runtimedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.RemoveWorkspace(ctx, t, path, g)
}

func (s *Supervisor) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.Services(ctx, t)
}

func (s *Supervisor) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.ServiceAction(ctx, t, name, a)
}

func (s *Supervisor) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Logs{}, err
	}
	return d.ServiceLogs(ctx, t, name)
}

func (s *Supervisor) BuildImage(ctx context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Image{}, err
	}
	return d.BuildImage(ctx, b, g)
}

func (s *Supervisor) Images(ctx context.Context) ([]runtimedriver.Image, error) {
	d, err := s.driver()
	if err != nil {
		return nil, err
	}
	return d.Images(ctx)
}

func (s *Supervisor) InspectImage(ctx context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Image{}, false, err
	}
	return d.InspectImage(ctx, ref)
}

func (s *Supervisor) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.DeleteImage(ctx, img, g)
}

func (s *Supervisor) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	d, err := s.driver()
	if err != nil {
		return runtimedriver.Unknown, err
	}
	return d.Settle(ctx, e)
}

func (s *Supervisor) Delete(ctx context.Context, r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	d, err := s.driver()
	if err != nil {
		return err
	}
	return d.Delete(ctx, r, at, g)
}
