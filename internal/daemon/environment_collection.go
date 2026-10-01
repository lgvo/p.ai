package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

const collectionPreviewLifetime = 2 * time.Minute
const collectionWarning = "Existing instances keep their private roots, but deleting this source image may prevent exact recreation if an instance is later lost and its branch environment has changed."

type collectionPreviewState struct {
	Claim     control.EnvironmentCollectionClaim
	ExpiresAt time.Time
}

func collectionToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func collectionTokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (l *lifecycle) observeCollection(ctx context.Context, entry control.EnvironmentImage) (control.EnvironmentCollectionClaim, int, error) {
	claim := control.EnvironmentCollectionClaim{Image: entry}
	info, found, err := l.runtime.ObserveOwnedBuilderImage(ctx, imageClaimFromCache(entry))
	if err != nil {
		return claim, 0, err
	}
	claim.ImagePresent = found
	claim.ObservedSize = entry.LogicalSize
	if found {
		claim.ObservedSize = info.Size
	}
	var count int
	claim.RelatedDigest, count, err = l.store.RelatedEnvironmentDigest(ctx, entry.Project, entry.Key, entry.Fingerprint)
	return claim, count, err
}

func collectionItem(claim control.EnvironmentCollectionClaim, count int) control.EnvironmentCacheItem {
	e := claim.Image
	status := "missing"
	if claim.ImagePresent {
		status = "present"
	}
	return control.EnvironmentCacheItem{
		Project: e.Project, Key: e.Key, Fingerprint: e.Fingerprint, BaseFingerprint: e.BaseFingerprint,
		CreatedAt: e.CreatedAt, LastUsedAt: e.LastUsedAt, LogicalSize: claim.ObservedSize,
		ImageStatus: status, RelatedCount: count,
	}
}

func (l *lifecycle) ListEnvironmentCache(ctx context.Context, project, after string, limit int) ([]control.EnvironmentCacheItem, string, error) {
	entries, next, err := l.store.ListEnvironmentImages(ctx, project, after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]control.EnvironmentCacheItem, 0, len(entries))
	for _, entry := range entries {
		claim, count, err := l.observeCollection(ctx, entry)
		if err != nil {
			return nil, "", err
		}
		items = append(items, collectionItem(claim, count))
	}
	return items, next, nil
}

func (l *lifecycle) PreviewEnvironmentCollection(ctx context.Context, project, key, token, after string, limit int) (control.EnvironmentCollectionPreview, error) {
	var state collectionPreviewState
	if limit < 1 || limit > 20 {
		return control.EnvironmentCollectionPreview{}, control.ErrInvalid
	}
	if token != "" {
		l.mu.Lock()
		state = l.collectionPreviews[token]
		l.mu.Unlock()
		if state.ExpiresAt.IsZero() || time.Now().After(state.ExpiresAt) ||
			state.Claim.Image.Project != project || state.Claim.Image.Key != key {
			return control.EnvironmentCollectionPreview{}, control.ErrConflict
		}
	} else {
		entry, found, err := l.store.GetEnvironmentImage(ctx, project, key)
		if err != nil {
			return control.EnvironmentCollectionPreview{}, err
		}
		if !found {
			return control.EnvironmentCollectionPreview{}, control.ErrNotFound
		}
		claim, _, err := l.observeCollection(ctx, entry)
		if err != nil {
			return control.EnvironmentCollectionPreview{}, err
		}
		token, err = collectionToken()
		if err != nil {
			return control.EnvironmentCollectionPreview{}, err
		}
		claim.ConfirmationDigest = collectionTokenDigest(token)
		state = collectionPreviewState{Claim: claim, ExpiresAt: time.Now().Add(collectionPreviewLifetime)}
		state.Claim.ConfirmationExpiresAt = state.ExpiresAt.UTC().Format(time.RFC3339Nano)
		l.mu.Lock()
		if l.collectionPreviews == nil {
			l.collectionPreviews = map[string]collectionPreviewState{}
		}
		for id, old := range l.collectionPreviews {
			if time.Now().After(old.ExpiresAt) {
				delete(l.collectionPreviews, id)
			}
		}
		if len(l.collectionPreviews) >= 64 {
			l.mu.Unlock()
			return control.EnvironmentCollectionPreview{}, control.ErrConflict
		}
		l.collectionPreviews[token] = state
		l.mu.Unlock()
	}
	current, found, err := l.store.GetEnvironmentImage(ctx, project, key)
	if err != nil || !found || !control.SameEnvironmentImage(current, state.Claim.Image) {
		return control.EnvironmentCollectionPreview{}, errors.Join(err, control.ErrConflict)
	}
	observed, count, err := l.observeCollection(ctx, current)
	if err != nil || observed.ImagePresent != state.Claim.ImagePresent || observed.ObservedSize != state.Claim.ObservedSize || observed.RelatedDigest != state.Claim.RelatedDigest {
		return control.EnvironmentCollectionPreview{}, errors.Join(err, control.ErrConflict)
	}
	related, next, err := l.store.ListRelatedEnvironmentSessions(ctx, project, key, current.Fingerprint, after, limit)
	if err != nil {
		return control.EnvironmentCollectionPreview{}, err
	}
	return control.EnvironmentCollectionPreview{
		Token: token, ExpiresAt: state.ExpiresAt.UTC().Format(time.RFC3339Nano), Item: collectionItem(state.Claim, count),
		RelatedSessions: related, RelatedNext: next, Warning: collectionWarning,
	}, nil
}

func (l *lifecycle) CollectEnvironmentCache(ctx context.Context, key, token string) (control.Operation, error) {
	if key == "" || len(key) > 128 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	prior, err := l.store.GetOperationByKey(ctx, key)
	if err == nil {
		var accepted control.EnvironmentCollectionClaim
		if prior.Kind != "environment.collect" || json.Unmarshal(prior.Request, &accepted) != nil ||
			accepted.ConfirmationDigest != collectionTokenDigest(token) {
			return control.Operation{}, control.ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, control.ErrNotFound) {
		return control.Operation{}, err
	}
	l.mu.Lock()
	state := l.collectionPreviews[token]
	l.mu.Unlock()
	if state.ExpiresAt.IsZero() || time.Now().After(state.ExpiresAt) || state.Claim.ConfirmationDigest != collectionTokenDigest(token) {
		return control.Operation{}, control.ErrConflict
	}
	op, err := confirmCollectionPreflight(ctx, key, state,
		l.store.GetEnvironmentImage, l.observeCollection, l.store.BeginEnvironmentCollection)
	if err != nil {
		return control.Operation{}, err
	}
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func confirmCollectionPreflight(ctx context.Context, key string, state collectionPreviewState,
	load func(context.Context, string, string) (control.EnvironmentImage, bool, error),
	observe func(context.Context, control.EnvironmentImage) (control.EnvironmentCollectionClaim, int, error),
	begin func(context.Context, string, control.EnvironmentCollectionClaim) (control.Operation, error)) (control.Operation, error) {
	current, found, err := load(ctx, state.Claim.Image.Project, state.Claim.Image.Key)
	if err != nil || !found || !control.SameEnvironmentImage(current, state.Claim.Image) {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	observed, _, err := observe(ctx, current)
	if err != nil || observed.ImagePresent != state.Claim.ImagePresent || observed.ObservedSize != state.Claim.ObservedSize || observed.RelatedDigest != state.Claim.RelatedDigest {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	return begin(ctx, key, state.Claim)
}

func (l *lifecycle) processEnvironmentCollection(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var claim control.EnvironmentCollectionClaim
	if json.Unmarshal(op.Request, &claim) != nil || !claim.Valid() || claim.Image.Project != op.Project {
		l.blockEnvironmentCollection(op, errors.New("durable collection claim invalid"))
		return
	}
	ctx := l.ctx
	current, found, err := l.store.GetEnvironmentImage(ctx, claim.Image.Project, claim.Image.Key)
	if err != nil || found && !control.SameEnvironmentImage(current, claim.Image) {
		l.blockEnvironmentCollection(op, errors.Join(err, control.ErrConflict))
		return
	}
	owned := imageClaimFromCache(claim.Image)
	if claim.ImagePresent {
		if err := l.runtime.DeleteOwnedBuilderImage(ctx, owned); err != nil {
			l.blockEnvironmentCollection(op, err)
			return
		}
	} else {
		_, present, err := l.runtime.ObserveOwnedBuilderImage(ctx, owned)
		if err != nil {
			l.blockEnvironmentCollection(op, err)
			return
		}
		if present {
			_ = l.store.AdvanceOperation(ctx, op.ID, "superseded", "stale", true, nil, "image appeared after missing-image confirmation; new preview required")
			return
		}
	}
	if err := l.store.AdvanceOperation(ctx, op.ID, "running", "image-absent", true, nil, ""); err != nil {
		l.blockEnvironmentCollection(op, err)
		return
	}
	op.Phase = "image-absent"
	if err := l.store.DeleteEnvironmentImageExact(ctx, claim.Image); err != nil {
		l.blockEnvironmentCollection(op, err)
		return
	}
	if err := l.store.AdvanceOperation(ctx, op.ID, "completed", "index-absent", true, nil, ""); err != nil {
		l.blockEnvironmentCollection(op, err)
		return
	}
}

func (l *lifecycle) blockEnvironmentCollection(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	diagnostic := fmt.Sprintf("exact environment cache collection blocked: %v", cause)
	if len(diagnostic) > 900 {
		diagnostic = diagnostic[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, true, nil, diagnostic)
}
