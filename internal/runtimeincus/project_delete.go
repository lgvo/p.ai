package runtimeincus

import (
	"context"
	"errors"
	"fmt"
)

// CheckProjectDeletionInventory refuses unreviewed builders, helpers, renamed
// runtimes or cache images. It grants no cleanup authority for unknown objects.
func (b *Backend) CheckProjectDeletionInventory(ctx context.Context, project string, names, images map[string]bool) error {
	if !validBuilderProject(project) {
		return errors.New("invalid project deletion inventory")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return err
	}
	var instances []instanceJSON
	if decode(raw, &instances) != nil || len(instances) > 4 {
		return errors.New("project deletion native inventory unavailable")
	}
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] || in.Type != "container" || in.Config == nil || in.ExpandedConfig == nil {
			return errors.New("project deletion inventory ambiguous")
		}
		seen[in.Name] = true
		matches := false
		for _, c := range []map[string]string{in.Config, in.ExpandedConfig} {
			if c["user.p.project_path"] == project || c["user.p.builder_project_path"] == project {
				matches = true
			}
		}
		if matches && !names[in.Name] {
			return fmt.Errorf("unreviewed project native resource %s; manual exact-identity investigation required", in.Name)
		}
	}
	all, err := b.imageList(ctx)
	if err != nil {
		return err
	}
	if len(all) > 1024 {
		return errors.New("project image inventory exceeds bound")
	}
	for _, im := range all {
		if im.Properties["p.project_path"] == project && !images[im.Fingerprint] {
			return fmt.Errorf("unreviewed project image %s; exact owned cache investigation/collection required", im.Fingerprint)
		}
	}
	return nil
}
