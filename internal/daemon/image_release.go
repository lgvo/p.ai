package daemon

import (
	"context"
	"errors"
	"log"
	"runtime/debug"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// startRelease releases the project's unused commit images in the
// background, never as part of an operation. One run per project goes at a
// time; a request during a run asks for one more run after it.
func (l *lifecycle) startRelease(project string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.releases == nil {
		l.releases = map[string]bool{}
	}
	if _, running := l.releases[project]; running {
		l.releases[project] = true
		return
	}
	l.releases[project] = false
	go func() {
		for {
			l.runRelease(project)
			again := false
			l.withLock(func() {
				again = l.releases[project] && l.ctx.Err() == nil
				if again {
					l.releases[project] = false
				} else {
					delete(l.releases, project)
				}
			})
			if !again {
				return
			}
		}
	}()
}

// runRelease runs one release of the project's images and logs its failure.
// A panic in it is recovered and logged too, so the daemon keeps serving;
// what it left is released by a later run.
func (l *lifecycle) runRelease(project string) {
	defer func() {
		if value := recover(); value != nil {
			log.Printf("image release for %s panicked: %v\n%s", project, value, debug.Stack())
		}
	}()
	if err := l.releaseImages(l.ctx, project); err != nil {
		log.Printf("image release for %s: %v", project, err)
	}
}

// releaseImages deletes the project's commit images that no session uses: an
// image is kept while some session started from its commit exists, in any
// registry state. It skips a commit whose lock a creation holds and a project
// being deleted. A driver failure leaves everything for the next run.
func (l *lifecycle) releaseImages(ctx context.Context, project string) error {
	active, err := l.store.HasActiveProject(ctx, project)
	if err != nil || !active {
		return err
	}
	var errs []error
	indexed := false
	var after control.CommitImageKey
	for {
		rows, more, err := l.store.ListCommitImages(ctx, project, after, 100)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, row := range rows {
			indexed = true
			errs = append(errs, l.releaseIndexedImage(ctx, row.Key))
		}
		if !more {
			break
		}
		after = rows[len(rows)-1].Key
	}
	if l.cfg.Environment == nil && !indexed {
		return nil
	}
	images, err := l.images.Images(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, img := range images {
		if img.Labels[runtimedriver.LabelProject] == project {
			errs = append(errs, l.releaseUnindexedImage(ctx, img))
		}
	}
	return errors.Join(errs...)
}

// releaseIndexedImage deletes the image a row indexes once no session uses
// its commit, then forgets the row. The row turns releasing right before the
// delete, so no creation reuses an image whose delete may have started.
func (l *lifecycle) releaseIndexedImage(ctx context.Context, key control.CommitImageKey) error {
	release, locked := l.tryLockCommit(key.Project, key.Commit)
	if !locked {
		return nil
	}
	defer release()
	row, found, err := l.store.GetCommitImage(ctx, key)
	if err != nil || !found {
		return err
	}
	intent := noIntent
	if row.State == "ready" {
		sessions, _, err := l.store.CommitUse(ctx, key.Project, key.Commit)
		if err != nil || sessions != 0 {
			return err
		}
		intent = func() error { return l.store.MarkCommitImageReleasing(ctx, key, row.Ref) }
	}
	if err = l.images.DeleteImage(ctx, indexedImage(row), intent); err != nil {
		return err
	}
	return l.store.ForgetCommitImage(ctx, key, row.Ref)
}

// releaseUnindexedImage deletes an image of the project that the index does
// not name, such as a build that finished after its creation gave up on it,
// unless a creation from its commit may still accept it.
func (l *lifecycle) releaseUnindexedImage(ctx context.Context, img runtimedriver.Image) error {
	key := control.CommitImageKey{Project: img.Labels[runtimedriver.LabelProject], Commit: img.Labels[runtimedriver.LabelSourceCommit],
		BaseImage: img.Labels[runtimedriver.LabelBaseImage], System: img.Labels[runtimedriver.LabelSystem], BuilderVersion: img.Labels[runtimedriver.LabelBuilderVersion]}
	release, locked := l.tryLockCommit(key.Project, key.Commit)
	if !locked {
		return nil
	}
	defer release()
	row, found, err := l.store.GetCommitImage(ctx, key)
	if err != nil || found && row.Ref == string(img.Ref) {
		return err
	}
	_, creating, err := l.store.CommitUse(ctx, key.Project, key.Commit)
	if err != nil || creating != 0 {
		return err
	}
	return l.images.DeleteImage(ctx, img, noIntent)
}
