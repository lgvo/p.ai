package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// CommitImageKey names the one image P keeps for sessions started from a
// source commit: an upgrade of the base image or of the image builder names a
// different image even for the same commit.
type CommitImageKey struct {
	Project        string `json:"project"`
	Commit         string `json:"commit_oid"`
	BaseImage      string `json:"base_image"`
	System         string `json:"system"`
	BuilderVersion string `json:"builder_version"`
}

// Labels are the labels core gives the image of this key built by build.
func (k CommitImageKey) Labels(build string) map[string]string {
	return map[string]string{
		runtimedriver.LabelProject:        k.Project,
		runtimedriver.LabelSourceCommit:   k.Commit,
		runtimedriver.LabelBaseImage:      k.BaseImage,
		runtimedriver.LabelSystem:         k.System,
		runtimedriver.LabelBuilderVersion: k.BuilderVersion,
		runtimedriver.LabelBuild:          build,
	}
}

const commitImageKeyWhere = `project_path=? AND commit_oid=? AND base_image=? AND system=? AND builder_version=?`

func (k CommitImageKey) args() []any {
	return []any{k.Project, k.Commit, k.BaseImage, k.System, k.BuilderVersion}
}

// CommitImage is one row of the commit-image index. The index maps a key to
// the driver's image; it is never authority for the image itself, which a
// caller inspects before using or deleting it.
type CommitImage struct {
	Key            CommitImageKey    `json:"key"`
	Ref            string            `json:"ref"`
	Build          string            `json:"build"`
	Labels         map[string]string `json:"labels"`
	Selection      string            `json:"selection"`
	MaterialSHA256 string            `json:"material_sha256,omitempty"`
	Size           int64             `json:"size"`
	State          string            `json:"state"` // ready, or releasing once its delete may have started
	CreatedAt      string            `json:"created_at"`
}

func (c CommitImage) valid() bool {
	k := c.Key
	return validProject(k.Project) && validOID(k.Commit) && validFingerprint(k.BaseImage) && validSystem(k.System) &&
		validImageBuilder(k.BuilderVersion) && validImageRef(c.Ref) && validUUID(c.Build) &&
		maps.Equal(c.Labels, k.Labels(c.Build)) && validImageSelection(c.Selection, c.MaterialSHA256) &&
		c.Size >= 0 && c.Size <= 1<<50 && (c.State == "ready" || c.State == "releasing")
}

func validSystem(system string) bool {
	return system == "x86_64-linux" || system == "aarch64-linux"
}

// validImageBuilder bounds the driver's image-builder version, which names
// images and is stored as a label.
func validImageBuilder(version string) bool {
	if version == "" || len(version) > 128 {
		return false
	}
	for i := 0; i < len(version); i++ {
		if version[i] < 0x20 || version[i] > 0x7e {
			return false
		}
	}
	return true
}

// validImageRef is the one check of a driver's image reference. The Incus
// driver's references are image fingerprints.
func validImageRef(ref string) bool { return validFingerprint(ref) }

// validImageSelection requires the activation material exactly for an image
// that activates a devShell.
func validImageSelection(selection, material string) bool {
	switch runtimedriver.Selection(selection) {
	case runtimedriver.SelectionNoFlake, runtimedriver.SelectionNoDefault:
		return material == ""
	case runtimedriver.SelectionDevShell:
		return validFingerprint(material)
	}
	return false
}

// PutCommitImage indexes a built image. The row starts ready, created now,
// whatever img.State and img.CreatedAt say. A key or reference that already
// has a row is a conflict: a key's row changes only by being forgotten.
func (s *Store) PutCommitImage(ctx context.Context, img CommitImage) error {
	img.State = "ready"
	if !img.valid() {
		return ErrInvalid
	}
	labels, _ := json.Marshal(img.Labels)
	k := img.Key
	_, err := s.db.ExecContext(ctx, `INSERT INTO commit_images(project_path,commit_oid,base_image,system,builder_version,image_ref,build_ref,labels_json,selection,material_sha256,logical_size,state,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?,'ready',?)`,
		k.Project, k.Commit, k.BaseImage, k.System, k.BuilderVersion, img.Ref, img.Build, string(labels), img.Selection, img.MaterialSHA256, img.Size,
		time.Now().UTC().Format(time.RFC3339Nano))
	return classifyWrite(err)
}

const commitImageColumns = `project_path,commit_oid,base_image,system,builder_version,image_ref,build_ref,labels_json,selection,material_sha256,logical_size,state,created_at`

func scanCommitImage(row interface{ Scan(...any) error }) (CommitImage, error) {
	var c CommitImage
	var labels string
	k := &c.Key
	if err := row.Scan(&k.Project, &k.Commit, &k.BaseImage, &k.System, &k.BuilderVersion, &c.Ref, &c.Build, &labels, &c.Selection, &c.MaterialSHA256, &c.Size, &c.State, &c.CreatedAt); err != nil {
		return CommitImage{}, err
	}
	if len(labels) > 4096 || json.Unmarshal([]byte(labels), &c.Labels) != nil || !c.valid() {
		return CommitImage{}, fmt.Errorf("invalid persisted commit image for %s", k.Project)
	}
	return c, nil
}

// GetCommitImage reads the row of key, and reports false when there is none.
func (s *Store) GetCommitImage(ctx context.Context, key CommitImageKey) (CommitImage, bool, error) {
	c, err := scanCommitImage(s.db.QueryRowContext(ctx, `SELECT `+commitImageColumns+` FROM commit_images WHERE `+commitImageKeyWhere, key.args()...))
	if errors.Is(err, sql.ErrNoRows) {
		return CommitImage{}, false, nil
	}
	return c, err == nil, err
}

// MarkCommitImageReleasing records, before the image's delete may start, that
// the row of key indexing ref is no longer reused. Marking a releasing row
// again succeeds. A missing row, another image, and a commit some session was
// started from are conflicts: an image is kept while such a session exists.
func (s *Store) MarkCommitImageReleasing(ctx context.Context, key CommitImageKey, ref string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE commit_images SET state='releasing' WHERE `+commitImageKeyWhere+` AND image_ref=?
	 AND NOT EXISTS(SELECT 1 FROM sessions s WHERE `+commitSessionsWhere+`)`, append(key.args(), ref, key.Project, key.Commit)...)
	if err != nil {
		return classifyWrite(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// ForgetCommitImage removes the row of key only while it indexes ref, so a
// caller never forgets an image it did not observe. It never deletes the image.
func (s *Store) ForgetCommitImage(ctx context.Context, key CommitImageKey, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM commit_images WHERE `+commitImageKeyWhere+` AND image_ref=?`, append(key.args(), ref)...)
	return err
}

// MaxProjectDeleteImages bounds the commit images one project deletion
// reviews.
const MaxProjectDeleteImages = 20

// ListCommitImages lists up to limit rows of project in key order, after the
// key after (the zero key starts at the first row), and reports whether more
// rows follow.
func (s *Store) ListCommitImages(ctx context.Context, project string, after CommitImageKey, limit int) ([]CommitImage, bool, error) {
	if limit < 1 || limit > 100 {
		return nil, false, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+commitImageColumns+` FROM commit_images WHERE project_path=?
	 AND (commit_oid,base_image,system,builder_version)>(?,?,?,?)
	 ORDER BY commit_oid,base_image,system,builder_version LIMIT ?`,
		project, after.Commit, after.BaseImage, after.System, after.BuilderVersion, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var images []CommitImage
	for rows.Next() {
		c, err := scanCommitImage(rows)
		if err != nil {
			return nil, false, err
		}
		images = append(images, c)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	if len(images) > limit {
		return images[:limit], true, nil
	}
	return images, false, nil
}

// commitSessionsWhere selects, with the arguments project and commit, the
// sessions of a project whose creation captured a commit, in every registry
// state: a creation waiting for Retry and a session being removed still use
// the commit's image.
const commitSessionsWhere = `s.project_path=? AND EXISTS(SELECT 1 FROM operations o WHERE o.session_uuid=s.uuid
 AND o.kind='session.create' AND json_extract(o.evidence_json,'$.captured_oid')=?)`

// CommitUse counts the sessions of project started from commit, and how many
// of them are still creating.
func (s *Store) CommitUse(ctx context.Context, project, commit string) (sessions, creating int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(s.registry_state='creating'),0) FROM sessions s WHERE `+commitSessionsWhere, project, commit).Scan(&sessions, &creating)
	return sessions, creating, err
}

// maxCommitSessions bounds the sessions one digest reads.
const maxCommitSessions = 1024

// CommitSessionsDigest binds the sessions of project started from commit:
// their UUIDs, branches and registry states.
func (s *Store) CommitSessionsDigest(ctx context.Context, project, commit string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.uuid,s.branch,s.registry_state FROM sessions s WHERE `+commitSessionsWhere+`
	 ORDER BY s.uuid COLLATE BINARY LIMIT ?`, project, commit, maxCommitSessions+1)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	n := 0
	for rows.Next() {
		var v struct {
			UUID     string `json:"uuid"`
			Branch   string `json:"branch"`
			Registry string `json:"registry_state"`
		}
		if err = rows.Scan(&v.UUID, &v.Branch, &v.Registry); err != nil {
			return "", err
		}
		if n++; n > maxCommitSessions {
			return "", fmt.Errorf("commit %s of project %s has more than %d sessions", commit, project, maxCommitSessions)
		}
		encoded, _ := json.Marshal(v)
		_, _ = h.Write(encoded)
		_, _ = h.Write([]byte{0})
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
