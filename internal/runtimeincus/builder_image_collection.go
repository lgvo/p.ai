package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
)

// ObserveOwnedBuilderImage verifies an already accepted, exact image claim.
// Collection deliberately does not require the base image to remain present:
// its inherited properties are pinned in the durable accepted claim.
func (b *Backend) ObserveOwnedBuilderImage(ctx context.Context, claim BuilderImageClaim) (BuilderImageInfo, bool, error) {
	var zero BuilderImageInfo
	if !fingerprintPattern.MatchString(claim.Fingerprint) || !validBuilderProject(claim.ProjectPath) ||
		!fingerprintPattern.MatchString(claim.Key) || !fingerprintPattern.MatchString(claim.BaseFingerprint) ||
		!fingerprintPattern.MatchString(claim.MaterialDigest) || !validBuilderSystem(claim.System) ||
		!builderStorePathPattern.MatchString(claim.CaptureStorePath) || !strings.HasSuffix(claim.CaptureStorePath, "-env") ||
		!uuidPattern.MatchString(claim.BuilderRequest) || !uuidPattern.MatchString(b.config.PInstanceID) ||
		len(claim.Properties) < 10 || len(claim.Properties) > 64 {
		return zero, false, errors.New("invalid owned image claim")
	}
	labels := map[string]string{
		"p.contract": imageContract, "p.compression": "none",
		"p.instance": b.config.PInstanceID, "p.incus_project": b.config.Project,
		"p.project_path": claim.ProjectPath, "p.builder_request": claim.BuilderRequest,
		"p.base_image": claim.BaseFingerprint, "p.environment_key": claim.Key,
		"p.material": claim.MaterialDigest, "p.capture_store_path": claim.CaptureStorePath,
		"p.system": claim.System,
	}
	for key, want := range labels {
		if claim.Properties[key] != want {
			return zero, false, fmt.Errorf("owned image label %s differs from claim", key)
		}
	}
	for key, value := range claim.Properties {
		if key == "" || len(key) > 128 || len(value) > 1024 {
			return zero, false, errors.New("owned image property bound exceeded")
		}
		if strings.HasPrefix(key, "p.") {
			if _, ok := labels[key]; !ok {
				return zero, false, errors.New("owned image has unknown P label")
			}
		}
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, false, err
	}
	images, err := b.imageList(ctx)
	if err != nil {
		return zero, false, err
	}
	count := 0
	for _, image := range images {
		if image.Fingerprint != claim.Fingerprint {
			continue
		}
		count++
		if err := checkPublishedBuilderImage(image, claim.Properties, claim.System, b.config.Project); err != nil {
			return zero, false, err
		}
		zero = BuilderImageInfo{Fingerprint: image.Fingerprint, Project: b.config.Project, Size: image.Size, Properties: maps.Clone(claim.Properties)}
	}
	if count > 1 {
		return BuilderImageInfo{}, false, errors.New("ambiguous owned image fingerprint")
	}
	return zero, count == 1, nil
}

// DeleteOwnedBuilderImage is only for an accepted durable collection intent.
// It never resolves aliases or chooses a replacement cache generation.
func (b *Backend) DeleteOwnedBuilderImage(ctx context.Context, claim BuilderImageClaim) error {
	_, found, err := b.ObserveOwnedBuilderImage(ctx, claim)
	if err != nil || !found {
		return err
	}
	_, deleteErr := b.command(ctx, "image", "delete", claim.Fingerprint)
	_, stillPresent, inspectErr := b.ObserveOwnedBuilderImage(ctx, claim)
	if inspectErr != nil {
		return errors.Join(deleteErr, inspectErr, errors.New("owned image deletion outcome unresolved"))
	}
	if stillPresent {
		return errors.Join(deleteErr, errors.New("owned image deletion outcome unresolved; exact fingerprint remains"))
	}
	return nil
}
