package runtimeincus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var grantName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func grantDeviceName(name string) string { return "p-grant-" + name }

func grantOptions(g FilesystemGrant) string {
	if g.Executable {
		return "exec,nosuid,nodev"
	}
	return "noexec,nosuid,nodev"
}

func grantDevice(g FilesystemGrant) map[string]string {
	readonly := "true"
	if g.Access == "read-write" {
		readonly = "false"
	}
	device := map[string]string{"type": "disk", "source": g.Source, "path": "/mnt/p/" + g.Name,
		"readonly": readonly, "propagation": "private", "shift": "false", "raw.mount.options": grantOptions(g)}
	// Incus 7.4 rejects recursive on file disks even when set to false.
	if g.Type == "directory" {
		device["recursive"] = "false"
	}
	return device
}

func grantDeviceAddArgs(instance string, g FilesystemGrant) []string {
	device := grantDevice(g)
	argv := []string{"config", "device", "add", instance, grantDeviceName(g.Name), "disk"}
	for _, key := range []string{"source", "path", "readonly", "propagation", "shift", "recursive", "raw.mount.options"} {
		if value, ok := device[key]; ok {
			argv = append(argv, key+"="+value)
		}
	}
	return argv
}

func validGrantDevice(actual map[string]string, g FilesystemGrant) bool {
	want := grantDevice(g)
	if len(actual) != len(want) {
		return false
	}
	for key, value := range want {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func validateRuntimeGrants(c Config, s Session, inspectSource bool) error {
	if len(s.Grants) > 8 || s.WorkspaceOwner != "" && len(s.Grants) != 0 {
		return errors.New("invalid runtime grant count")
	}
	seen := map[string]bool{}
	for i, g := range s.Grants {
		if !grantName.MatchString(g.Name) || seen[g.Name] || !filepath.IsAbs(g.Source) || filepath.Clean(g.Source) != g.Source ||
			(g.Type != "file" && g.Type != "directory") || (g.Access != "read-only" && g.Access != "read-write") || g.Inode == 0 {
			return errors.New("invalid runtime grant identity")
		}
		seen[g.Name] = true
		allowed := false
		for _, ceiling := range c.DiskSourceCeilings {
			if c.EndpointPrefix != "" && pathWithinCeiling(c.EndpointPrefix, ceiling) && pathWithinCeiling(g.Source, ceiling) {
				return errors.New("runtime grant overlaps endpoint ceiling")
			}
			if g.Source != ceiling && pathWithinCeiling(g.Source, ceiling) {
				allowed = true
			}
		}
		if !allowed || c.EndpointPrefix != "" && (pathWithinCeiling(g.Source, c.EndpointPrefix) || pathWithinCeiling(c.EndpointPrefix, g.Source)) {
			return errors.New("runtime grant exceeds trusted disk boundary")
		}
		for j := 0; j < i; j++ {
			if pathWithinCeiling(g.Source, s.Grants[j].Source) || pathWithinCeiling(s.Grants[j].Source, g.Source) {
				return errors.New("runtime grants overlap")
			}
		}
		if inspectSource {
			if err := inspectRuntimeGrantSource(g); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectRuntimeGrantSource(g FilesystemGrant) error {
	return inspectRuntimeGrantSourceWith(g, os.Lstat)
}

func inspectRuntimeGrantSourceWith(g FilesystemGrant, lstat func(string) (os.FileInfo, error)) error {
	for path := g.Source; ; path = filepath.Dir(path) {
		info, err := lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime grant path contains symlink")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("runtime grant source identity unavailable")
		}
		if path == g.Source {
			if (g.Type == "file" && !info.Mode().IsRegular()) || (g.Type == "directory" && !info.IsDir()) || uint64(stat.Dev) != g.Device || stat.Ino != g.Inode || stat.Uid != g.OwnerUID || stat.Gid != g.OwnerGID {
				return fmt.Errorf("runtime grant %s source identity changed", g.Name)
			}
		} else if !info.IsDir() || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
			return errors.New("runtime grant parent changed")
		}
		if path == "/" {
			break
		}
	}
	return nil
}
