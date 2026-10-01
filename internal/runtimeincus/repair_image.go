package runtimeincus

import (
	"context"
	"errors"
)

// ImagePresent observes only the exact recorded image in the configured
// project. Absence is distinct from an unreachable or malformed inventory.
func (b *Backend) ImagePresent(ctx context.Context, fingerprint string) (bool, error) {
	if !fingerprintPattern.MatchString(fingerprint) {
		return false, errors.New("recorded image fingerprint invalid")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return false, err
	}
	raw, err := b.command(ctx, "image", "list", "--format", "json")
	if err != nil {
		return false, err
	}
	var images []struct {
		Fingerprint string `json:"fingerprint"`
		Type        string `json:"type"`
	}
	if err := decode(raw, &images); err != nil || len(images) > 4096 {
		return false, errors.Join(err, errors.New("image inventory unavailable"))
	}
	found := false
	for _, image := range images {
		if image.Fingerprint != fingerprint {
			continue
		}
		if found || image.Type != "container" {
			return false, errors.New("recorded image identity ambiguous")
		}
		found = true
	}
	return found, nil
}
