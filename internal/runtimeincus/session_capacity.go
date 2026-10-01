package runtimeincus

import (
	"context"
	"errors"
	"strconv"
)

// SessionCapacity is a fresh, project-scoped physical inventory. Every listed
// container consumes one slot, regardless of its owner. BuilderRequest is
// populated only for an exact P builder label pair; the control store also
// checks its deterministic name against the creation operation.
type SessionCapacity struct {
	Limit     int
	Instances []SessionCapacityInstance
}

type SessionCapacityInstance struct {
	Name           string
	BuilderRequest string
	BuilderProject string
}

func (b *Backend) SessionCapacity(ctx context.Context) (SessionCapacity, error) {
	if err := b.CheckConfinement(ctx); err != nil {
		return SessionCapacity{}, err
	}
	raw, err := b.command(ctx, "project", "list", "--format", "json")
	if err != nil {
		return SessionCapacity{}, err
	}
	var projects []projectJSON
	if err := decode(raw, &projects); err != nil {
		return SessionCapacity{}, err
	}
	limit := 0
	for _, project := range projects {
		if project.Name != b.config.Project {
			continue
		}
		if limit != 0 {
			return SessionCapacity{}, errors.New("Incus project capacity ambiguous")
		}
		limit, err = strconv.Atoi(project.Config["limits.containers"])
		if err != nil || limit < 2 || limit > 64 {
			return SessionCapacity{}, errors.New("Incus project capacity unavailable")
		}
	}
	if limit == 0 {
		return SessionCapacity{}, errors.New("Incus project capacity unavailable")
	}
	raw, err = b.command(ctx, "list", "--format", "json")
	if err != nil {
		return SessionCapacity{}, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) > 64 {
		return SessionCapacity{}, errors.Join(err, errors.New("Incus container inventory unavailable"))
	}
	out := SessionCapacity{Limit: limit, Instances: make([]SessionCapacityInstance, 0, len(instances))}
	seen := make(map[string]bool, len(instances))
	for _, instance := range instances {
		if instance.Name == "" || seen[instance.Name] || instance.Type != "container" {
			return SessionCapacity{}, errors.New("Incus container inventory ambiguous")
		}
		seen[instance.Name] = true
		item := SessionCapacityInstance{Name: instance.Name}
		if instance.Config["user.p.builder_request_uuid"] != "" &&
			instance.Config["user.p.builder_request_uuid"] == instance.ExpandedConfig["user.p.builder_request_uuid"] &&
			instance.Config["user.p.builder_project_path"] != "" &&
			instance.Config["user.p.builder_project_path"] == instance.ExpandedConfig["user.p.builder_project_path"] {
			item.BuilderRequest = instance.Config["user.p.builder_request_uuid"]
			item.BuilderProject = instance.Config["user.p.builder_project_path"]
		}
		out.Instances = append(out.Instances, item)
	}
	return out, nil
}
