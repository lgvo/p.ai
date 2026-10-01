package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var grantNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// FilesystemGrant is a trusted host selection. SourceIdentity is written only
// into P's immutable effective policy, never accepted from host configuration
// or a session request. The destination is always /mnt/p/<name>.
type FilesystemGrant struct {
	Name           string          `json:"name"`
	Source         string          `json:"source"`
	Type           string          `json:"type"`
	Access         string          `json:"access"`
	Executable     bool            `json:"executable"`
	SourceIdentity *SourceIdentity `json:"source_identity,omitempty"`
}

type SourceIdentity struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	OwnerUID uint32 `json:"owner_uid"`
	OwnerGID uint32 `json:"owner_gid"`
}

// GrantBoundary holds only host-owned isolation facts. Incus's active project
// restriction is checked separately against these exact configured ceilings.
type GrantBoundary struct {
	Ceilings       []string
	StateDir       string
	EndpointPrefix string
	IncusSocket    string
}

func grantWithin(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

func validateGrantShapes(grants []FilesystemGrant) error {
	names := map[string]bool{}
	for _, grant := range grants {
		if !grantNamePattern.MatchString(grant.Name) || names[grant.Name] ||
			!filepath.IsAbs(grant.Source) || filepath.Clean(grant.Source) != grant.Source || len(grant.Source) > 4096 ||
			(grant.Type != "file" && grant.Type != "directory") ||
			(grant.Access != "read-only" && grant.Access != "read-write") {
			return errors.New("filesystem grant has invalid name, source, type or access")
		}
		names[grant.Name] = true
	}
	for i := range grants {
		for j := 0; j < i; j++ {
			if grantWithin(grants[i].Source, grants[j].Source) || grantWithin(grants[j].Source, grants[i].Source) {
				return errors.New("filesystem grant sources overlap")
			}
		}
	}
	return nil
}

func validateGrantBoundary(grant FilesystemGrant, boundary GrantBoundary) error {
	allowed := false
	for _, ceiling := range boundary.Ceilings {
		// The configured endpoint prefix may be a private child of a wider
		// Incus ceiling. None of that ceiling is usable as a grant source:
		// sibling P instances keep sockets and credentials there too.
		if boundary.EndpointPrefix != "" && grantWithin(boundary.EndpointPrefix, ceiling) && grantWithin(grant.Source, ceiling) {
			return errors.New("filesystem grant overlaps endpoint ceiling")
		}
		if ceiling != "" && grant.Source != ceiling && grantWithin(grant.Source, ceiling) {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("filesystem grant exceeds confined disk-source ceiling")
	}
	for _, protected := range []string{"/", "/etc", "/home", "/root", "/var/lib/incus", "/var/lib/p", "/nix", "/run", "/proc", "/sys", "/dev", "/tmp", "/var/tmp", boundary.StateDir, boundary.EndpointPrefix, boundary.IncusSocket} {
		if protected == "" {
			continue
		}
		if grantWithin(grant.Source, protected) || grantWithin(protected, grant.Source) {
			return errors.New("filesystem grant overlaps protected host path")
		}
	}
	return nil
}

// ValidateConfiguredGrants checks closed host policy shape, exact ceilings and
// protected trees, without requiring a source to exist yet. Source existence
// and identity are checked at creation and again before Start.
func ValidateConfiguredGrants(policy ProjectPolicy, boundary GrantBoundary) error {
	if err := validateGrantShapes(policy.FilesystemMounts); err != nil {
		return err
	}
	for _, grant := range policy.FilesystemMounts {
		if grant.SourceIdentity != nil {
			return errors.New("host policy cannot supply a source identity")
		}
		if err := validateGrantBoundary(grant, boundary); err != nil {
			return err
		}
		if err := rejectExistingGrantSymlinks(grant.Source); err != nil {
			return err
		}
	}
	return nil
}

func rejectExistingGrantSymlinks(source string) error {
	for path := source; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("filesystem grant path contains a symlink")
		}
		if path == "/" {
			return nil
		}
	}
}

func inspectGrantSource(grant FilesystemGrant) (SourceIdentity, error) {
	return inspectGrantSourceWith(grant, os.Lstat)
}

func inspectGrantSourceWith(grant FilesystemGrant, lstat func(string) (os.FileInfo, error)) (SourceIdentity, error) {
	// Reject symlinks in every path component. The source parent must be owned
	// by root or the daemon and not writable by other host users; only the
	// leaf may be writable as an explicit read-write grant.
	var identity SourceIdentity
	for path := grant.Source; ; path = filepath.Dir(path) {
		info, err := lstat(path)
		if err != nil {
			return SourceIdentity{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return SourceIdentity{}, errors.New("filesystem grant path contains a symlink")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return SourceIdentity{}, errors.New("filesystem grant identity unavailable")
		}
		if path != grant.Source && (!info.IsDir() || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0) {
			return SourceIdentity{}, fmt.Errorf("filesystem grant parent %s is unsafe", path)
		}
		if path == grant.Source {
			if grant.Type == "file" && !info.Mode().IsRegular() || grant.Type == "directory" && !info.IsDir() {
				return SourceIdentity{}, errors.New("filesystem grant source type changed")
			}
			if stat.Ino == 0 {
				return SourceIdentity{}, errors.New("filesystem grant inode unavailable")
			}
			if path == "/" {
				return SourceIdentity{}, errors.New("host root cannot be granted")
			}
			identity = SourceIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, OwnerUID: stat.Uid, OwnerGID: stat.Gid}
		}
		if path == "/" {
			return identity, nil
		}
	}
}

// CaptureProjectPolicy records exact host source generations with the typed
// effective policy. This is read-only; no grant path is created or followed.
func CaptureProjectPolicy(policy ProjectPolicy, boundary GrantBoundary) (ProjectPolicy, error) {
	if err := validProjectPolicy(policy); err != nil {
		return ProjectPolicy{}, err
	}
	if err := ValidateConfiguredGrants(policy, boundary); err != nil {
		return ProjectPolicy{}, err
	}
	captured := policy
	captured.FilesystemMounts = append([]FilesystemGrant(nil), policy.FilesystemMounts...)
	for i := range captured.FilesystemMounts {
		id, err := inspectGrantSource(captured.FilesystemMounts[i])
		if err != nil {
			return ProjectPolicy{}, err
		}
		captured.FilesystemMounts[i].SourceIdentity = &id
	}
	return captured, nil
}

// RevalidateProjectPolicy refuses an absent, replaced or newly out-of-ceiling
// source. It never repairs a host path or mutates external grant contents.
func RevalidateProjectPolicy(policy ProjectPolicy, boundary GrantBoundary) error {
	if err := validProjectPolicy(policy); err != nil {
		return err
	}
	for _, grant := range policy.FilesystemMounts {
		if grant.SourceIdentity == nil || grant.SourceIdentity.Inode == 0 {
			return errors.New("filesystem grant identity missing")
		}
		if err := validateGrantBoundary(grant, boundary); err != nil {
			return err
		}
		current, err := inspectGrantSource(grant)
		if err != nil {
			return err
		}
		if current != *grant.SourceIdentity {
			return errors.New("filesystem grant source identity changed")
		}
	}
	return nil
}
