package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
)

// BuilderImageClaim is core-owned durable metadata for one project-scoped
// cache entry. It contains no command, source path, alias, or mutable image
// locator. The Incus fingerprint is accepted only after fresh verification.
type BuilderImageClaim struct {
	Fingerprint      string
	ProjectPath      string
	Key              string
	BaseFingerprint  string
	System           string
	MaterialDigest   string
	CaptureStorePath string
	BuilderRequest   string
	Properties       map[string]string
}

func (b *Backend) VerifyBuilderImage(ctx context.Context, claim BuilderImageClaim) (BuilderImageInfo, bool, error) {
	var zero BuilderImageInfo
	if !fingerprintPattern.MatchString(claim.Fingerprint) || !validBuilderProject(claim.ProjectPath) ||
		!fingerprintPattern.MatchString(claim.Key) || !fingerprintPattern.MatchString(claim.BaseFingerprint) ||
		!fingerprintPattern.MatchString(claim.MaterialDigest) || !validBuilderSystem(claim.System) ||
		!builderStorePathPattern.MatchString(claim.CaptureStorePath) || !strings.HasSuffix(claim.CaptureStorePath, "-env") ||
		!uuidPattern.MatchString(claim.BuilderRequest) || !uuidPattern.MatchString(b.config.PInstanceID) {
		return zero, false, errors.New("invalid environment image claim")
	}
	labels := map[string]string{
		"p.contract": imageContract, "p.compression": "none",
		"p.instance": b.config.PInstanceID, "p.incus_project": b.config.Project,
		"p.project_path": claim.ProjectPath, "p.builder_request": claim.BuilderRequest,
		"p.base_image": claim.BaseFingerprint, "p.environment_key": claim.Key,
		"p.material": claim.MaterialDigest, "p.capture_store_path": claim.CaptureStorePath,
		"p.system": claim.System,
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, false, err
	}
	images, err := b.imageList(ctx)
	if err != nil {
		return zero, false, err
	}
	expected, err := expectedBuilderImageProperties(images, claim.BaseFingerprint, labels)
	if err != nil {
		return zero, false, err
	}
	if !maps.Equal(expected, claim.Properties) {
		return zero, false, errors.New("cached environment image properties changed from pinned base")
	}
	var count int
	for _, image := range images {
		if image.Fingerprint != claim.Fingerprint {
			continue
		}
		count++
		if err := checkPublishedBuilderImage(image, expected, claim.System, b.config.Project); err != nil {
			return zero, false, err
		}
		zero = BuilderImageInfo{Fingerprint: claim.Fingerprint, Project: b.config.Project, Size: image.Size, Properties: maps.Clone(expected)}
	}
	if count > 1 {
		return BuilderImageInfo{}, false, errors.New("ambiguous environment image fingerprint")
	}
	return zero, count == 1, nil
}

// ReconcileBuilderPublication is used only after a durable publish-attempt
// marker. It accepts one exact private image with the pinned P instance,
// project, builder request, key, material, capture and inherited base metadata.
// If the image is absent, the outcome stays unresolved: the canceled Incus
// client may have left a daemon operation that will finish later. The caller
// must not issue a second publish from this result.
func (b *Backend) ReconcileBuilderPublication(ctx context.Context, claim BuilderImageClaim) (BuilderImageInfo, error) {
	var zero BuilderImageInfo
	if claim.Fingerprint != "" || !validBuilderProject(claim.ProjectPath) ||
		!fingerprintPattern.MatchString(claim.Key) || !fingerprintPattern.MatchString(claim.BaseFingerprint) ||
		!fingerprintPattern.MatchString(claim.MaterialDigest) || !validBuilderSystem(claim.System) ||
		!builderStorePathPattern.MatchString(claim.CaptureStorePath) || !strings.HasSuffix(claim.CaptureStorePath, "-env") ||
		!uuidPattern.MatchString(claim.BuilderRequest) || !uuidPattern.MatchString(b.config.PInstanceID) {
		return zero, errors.New("invalid publication reconciliation claim")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, err
	}
	pending, err := b.pendingImagePublications(ctx)
	if err != nil {
		return zero, err
	}
	images, err := b.imageList(ctx)
	if err != nil {
		return zero, err
	}
	labels := map[string]string{
		"p.contract": imageContract, "p.compression": "none",
		"p.instance": b.config.PInstanceID, "p.incus_project": b.config.Project,
		"p.project_path": claim.ProjectPath, "p.builder_request": claim.BuilderRequest,
		"p.base_image": claim.BaseFingerprint, "p.environment_key": claim.Key,
		"p.material": claim.MaterialDigest, "p.capture_store_path": claim.CaptureStorePath,
		"p.system": claim.System,
	}
	expected, err := expectedBuilderImageProperties(images, claim.BaseFingerprint, labels)
	if err != nil {
		return zero, err
	}
	if !maps.Equal(expected, claim.Properties) {
		return zero, errors.New("publication claim properties changed from pinned base")
	}
	var count int
	for _, image := range images {
		if image.Properties["p.builder_request"] != claim.BuilderRequest ||
			image.Properties["p.instance"] != b.config.PInstanceID ||
			image.Properties["p.environment_key"] != claim.Key {
			continue
		}
		count++
		if !fingerprintPattern.MatchString(image.Fingerprint) {
			return zero, errors.New("reconciled image fingerprint invalid")
		}
		if err := checkPublishedBuilderImage(image, expected, claim.System, b.config.Project); err != nil {
			return zero, err
		}
		zero = BuilderImageInfo{Fingerprint: image.Fingerprint, Project: b.config.Project, Size: image.Size, Properties: maps.Clone(expected)}
	}
	if count != 1 {
		// Even a present but ambiguous operation is never guessed or deleted.
		return BuilderImageInfo{}, fmt.Errorf("image publication outcome unresolved; no unique exact P image (%d active image operations)", pending)
	}
	return zero, nil
}

func (b *Backend) pendingImagePublications(ctx context.Context) (int, error) {
	raw, err := b.command(ctx, "operation", "list", "--format", "json")
	if err != nil {
		return 0, err
	}
	var operations []struct {
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	if err := decode(raw, &operations); err != nil {
		return 0, err
	}
	var pending int
	for _, op := range operations {
		if op.Description == "Downloading image" && (op.Status == "Running" || op.Status == "Pending") {
			pending++
		}
	}
	return pending, nil
}
