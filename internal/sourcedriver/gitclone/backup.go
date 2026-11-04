package gitclone

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// backup is what the clone held before the rename, and the modes the
// rename writes HEAD and the config with: the ones they had. The clone
// keeps it, private to the session user, until the rename is done, so a
// rename picked up again knows what it changes without core storing the
// clone's config.
type backup struct {
	Schema     string `json:"schema"`
	Old        string `json:"old"`
	New        string `json:"new"`
	Head       []byte `json:"head"`
	HeadMode   uint32 `json:"head_mode"`
	Ref        []byte `json:"ref"`
	Config     []byte `json:"config"`
	ConfigMode uint32 `json:"config_mode"`
	HasLog     bool   `json:"has_log"`
	Log        []byte `json:"log,omitempty"`
}

const backupSchema = "p.workspace-rename/v1"

// backupOf is the backup of the clone's branch old as the clone holds it
// now.
func (c *clone) backupOf() (backup, error) {
	head, headMode, err := c.safeFile(headPath)
	if err != nil {
		return backup{}, err
	}
	if err := checkHead(head, c.r.Old); err != nil {
		return backup{}, err
	}
	ref, _, err := c.safeFile(refPath(c.r.Old))
	if err != nil {
		return backup{}, err
	}
	config, configMode, err := c.safeFile(configPath)
	if err != nil {
		return backup{}, err
	}
	b := backup{Schema: backupSchema, Old: c.r.Old, New: c.r.New, Head: head, HeadMode: headMode, Ref: ref,
		Config: config, ConfigMode: configMode}
	oldLog, exists, err := c.present(logPath(c.r.Old))
	if err != nil {
		return backup{}, err
	}
	if exists {
		if !c.owned(oldLog, sourcedriver.FileRegular) {
			return backup{}, fmt.Errorf("rename Git file metadata unavailable: %s", logPath(c.r.Old))
		}
		b.Log, b.HasLog = oldLog.Data, true
	}
	return b, nil
}

// readBackup is the rename's backup in the clone, with the plan it fixes
// and the config the renamed clone holds.
func (c *clone) readBackup() (backup, plan, []byte, error) {
	raw, mode, err := c.safeFile(c.backupPath())
	if err != nil || mode != 0o600 {
		return backup{}, plan{}, nil, errors.Join(err, errors.New("rename backup unavailable"))
	}
	var b backup
	if err := json.Unmarshal(raw, &b); err != nil {
		return backup{}, plan{}, nil, errors.New("rename backup malformed")
	}
	p, after, err := b.details(c.r.Old, c.r.New)
	return b, p, after, err
}

// details checks that b is a backup of the rename of old to next, and
// returns the plan it fixes and the config the renamed clone holds.
func (b backup) details(old, next string) (plan, []byte, error) {
	if err := checkHead(b.Head, old); err != nil {
		return plan{}, nil, err
	}
	head := strings.TrimSuffix(string(b.Ref), "\n")
	if b.Schema != backupSchema || b.Old != old || b.New != next || len(b.Ref) < 41 || b.Ref[len(b.Ref)-1] != '\n' ||
		!gitnames.OID(head) || b.HeadMode&^0o755 != 0 || b.ConfigMode&^0o755 != 0 {
		return plan{}, nil, errors.New("rename backup identity unavailable")
	}
	after, err := renameConfig(b.Config, old, next)
	if err != nil {
		return plan{}, nil, err
	}
	sum := sha256.Sum256(after)
	return plan{head: head, configSHA256: hex.EncodeToString(sum[:])}, after, nil
}

// checkHead refuses a HEAD that is not on branch old, as a mismatch that
// names only a branch of the closed layout, a commit ID or a fixed marker.
func checkHead(head []byte, old string) error {
	if bytes.Equal(head, []byte("ref: refs/heads/"+old+"\n")) {
		return nil
	}
	actual := "<unavailable>"
	value := strings.TrimSuffix(string(head), "\n")
	if name, ok := strings.CutPrefix(value, "ref: refs/heads/"); ok {
		if closedLayoutName(name) {
			actual = "refs/heads/" + name
		}
	} else if gitnames.OID(value) {
		actual = "<detached> " + value
	}
	return &sourcedriver.WorkspaceMismatch{Field: "branch", Expected: "refs/heads/" + old, Actual: actual}
}
