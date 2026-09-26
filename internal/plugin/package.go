package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

var (
	idRE      = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

var placements = map[string]string{
	"runtime": "host", "interactive-host": "internal-session",
	"source-git": "hybrid", "environment": "host",
	"event-handler": "host", "agent-adapter": "internal-session",
}

var grants = map[string]string{
	"runtime.incus": "runtime", "session.asset.install": "interactive-host",
	"git.project": "source-git", "environment.nix": "environment",
	"event.file.append": "event-handler", "agent.status.report": "agent-adapter",
}

// Conformance validates package data and returns its content digest. It never
// starts package code or grants any requested operation.
func Conformance(path string) (Package, error) {
	pkg, _, err := packageSnapshot(context.Background(), path, nil)
	return pkg, err
}

// packageSnapshot hashes the same opened bytes it optionally retains for an
// invocation. Callers must compare the returned digest with trusted selection
// before using retained bytes.
func packageSnapshot(ctx context.Context, path string, capture map[string]bool) (Package, map[string][]byte, error) {
	var result Package
	retained := map[string][]byte{}
	if !filepath.IsAbs(path) {
		return result, nil, errors.New("package path must be absolute")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return result, nil, err
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	entries, err := dir.ReadDir(129)
	if err != nil {
		return result, nil, err
	}
	if len(entries) > 128 {
		return result, nil, errors.New("package has more than 128 files")
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			return result, nil, fmt.Errorf("symlink in package: %s", name)
		}
		if !entry.Type().IsRegular() {
			return result, nil, fmt.Errorf("non-regular package file: %s", name)
		}
		names = append(names, name)
	}
	if !slices.Contains(names, "plugin.json") {
		return result, nil, errors.New("missing plugin.json")
	}
	slices.Sort(names)
	h := sha256.New()
	var manifestData []byte
	headers := map[string][4]byte{}
	var totalSize int64
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, nil, err
		}
		file, err := openPackageFile(int(dir.Fd()), name)
		if err != nil {
			return result, nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return result, nil, err
		}
		if !info.Mode().IsRegular() {
			file.Close()
			return result, nil, fmt.Errorf("changed package file: %s", name)
		}
		totalSize += info.Size()
		if info.Size() > 8<<20 || totalSize > 16<<20 {
			file.Close()
			return result, nil, fmt.Errorf("package size limit exceeded: %s", name)
		}
		if _, err = io.WriteString(h, name); err != nil {
			file.Close()
			return result, nil, err
		}
		if _, err = h.Write([]byte{0}); err != nil {
			file.Close()
			return result, nil, err
		}
		if err = binary.Write(h, binary.BigEndian, uint64(info.Size())); err != nil {
			file.Close()
			return result, nil, err
		}
		if name == "plugin.json" {
			if info.Size() > 64<<10 {
				file.Close()
				return result, nil, errors.New("manifest exceeds 64 KiB")
			}
			manifestData, err = readBoundedContext(ctx, file, info.Size())
			if err == nil && int64(len(manifestData)) != info.Size() {
				err = errors.New("manifest changed during read")
			}
			if err == nil {
				_, err = h.Write(manifestData)
			}
		} else if capture[name] {
			if info.Size() > 8<<20 {
				file.Close()
				return result, nil, fmt.Errorf("captured package file too large: %s", name)
			}
			data, readErr := readBoundedContext(ctx, file, info.Size())
			if readErr != nil || int64(len(data)) != info.Size() {
				file.Close()
				return result, nil, fmt.Errorf("package file changed during read: %s", name)
			}
			var header [4]byte
			copy(header[:], data)
			headers[name] = header
			retained[name] = data
			_, err = h.Write(data)
		} else {
			var header [4]byte
			_, _ = io.ReadFull(file, header[:])
			headers[name] = header
			if _, err = file.Seek(0, io.SeekStart); err == nil {
				_, err = copyNContext(ctx, h, file, info.Size())
			}
		}
		if err == nil {
			finalInfo, statErr := file.Stat()
			if statErr != nil {
				err = statErr
			} else if finalInfo.Size() != info.Size() {
				err = fmt.Errorf("package file changed during read: %s", name)
			}
		}
		file.Close()
		if err != nil {
			return result, nil, err
		}
	}
	var manifest Manifest
	if err := strictJSON(manifestData, &manifest); err != nil {
		return result, nil, fmt.Errorf("manifest: %w", err)
	}
	if err := validateManifest(manifest, names, headers); err != nil {
		return result, nil, err
	}
	result = Package{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Manifest: manifest}
	return result, retained, nil
}

func readBoundedContext(ctx context.Context, source io.Reader, size int64) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(int(size))
	_, err := copyNContext(ctx, &buf, source, size)
	return buf.Bytes(), err
}

func copyNContext(ctx context.Context, dst io.Writer, src io.Reader, count int64) (int64, error) {
	var total int64
	buffer := make([]byte, 32<<10)
	for total < count {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		chunk := int64(len(buffer))
		if count-total < chunk {
			chunk = count - total
		}
		n, err := io.ReadFull(src, buffer[:chunk])
		if err != nil {
			return total, err
		}
		written, err := dst.Write(buffer[:n])
		total += int64(written)
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func validateManifest(m Manifest, names []string, headers map[string][4]byte) error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	if !idRE.MatchString(m.ID) {
		return errors.New("invalid plugin id")
	}
	if !versionRE.MatchString(m.Version) {
		return errors.New("version must be major.minor.patch")
	}
	if m.API != APIVersion {
		return fmt.Errorf("unsupported plugin API %q", m.API)
	}
	placement, ok := placements[m.Capability]
	if !ok {
		return fmt.Errorf("unsupported capability %q", m.Capability)
	}
	if m.Placement != placement {
		return fmt.Errorf("%s requires %s placement", m.Capability, placement)
	}
	if m.Description == "" || len(m.Description) > 512 {
		return errors.New("description must be 1–512 characters")
	}
	if len(m.Requests) == 0 {
		return errors.New("plugin requests no typed grants")
	}
	seen := map[string]bool{}
	for _, req := range m.Requests {
		if grants[req] != m.Capability {
			return fmt.Errorf("grant %q is not available to %s", req, m.Capability)
		}
		if seen[req] {
			return fmt.Errorf("duplicate grant %q", req)
		}
		seen[req] = true
	}
	assets := map[string]bool{}
	for _, asset := range m.Assets {
		if err := safeRelative(asset); err != nil {
			return fmt.Errorf("asset: %w", err)
		}
		if !slices.Contains(names, asset) {
			return fmt.Errorf("missing asset %q", asset)
		}
		if assets[asset] {
			return fmt.Errorf("duplicate asset %q", asset)
		}
		assets[asset] = true
	}
	if m.Runtime.Kind == "assets" {
		var required []string
		switch m.Capability {
		case "interactive-host":
			required = []string{"p-session.target", "p-interactive.service", "p-attach"}
		case "agent-adapter":
			required = []string{"p-codex-adapter"}
		default:
			return errors.New("asset capability is not implemented")
		}
		if len(m.Assets) != len(required) {
			return errors.New("asset package must contain exact required roles")
		}
		for _, name := range required {
			if !assets[name] {
				return fmt.Errorf("missing asset role %q", name)
			}
		}
	} else if m.Capability == "source-git" && m.Runtime.Kind == "wasi-command" {
		if len(m.Assets) > 1 || len(m.Assets) == 1 && m.Assets[0] != "p-git-ssh" {
			return errors.New("source-git permits only the p-git-ssh session asset")
		}
	} else if len(m.Assets) != 0 {
		return errors.New("assets require asset runtime")
	}
	switch m.Runtime.Kind {
	case "wasi-command":
		if err := safeRelative(m.Runtime.Entry); err != nil {
			return fmt.Errorf("entry: %w", err)
		}
		if !slices.Contains(names, m.Runtime.Entry) {
			return fmt.Errorf("missing entry %q", m.Runtime.Entry)
		}
		if headers[m.Runtime.Entry] != [4]byte{0, 97, 115, 109} {
			return errors.New("entry is not a WebAssembly module")
		}
	case "declarative":
		if m.Capability != "event-handler" || m.Runtime.Entry != "event.file.append" || len(m.Assets) != 0 {
			return errors.New("unsupported declarative operation")
		}
	case "assets":
		if m.Runtime.Entry != "" || len(m.Assets) == 0 || m.Placement == "host" {
			return errors.New("asset runtime requires internal-session or hybrid placement and assets")
		}
	default:
		return fmt.Errorf("unsupported runtime kind %q", m.Runtime.Kind)
	}
	declared := map[string]bool{"plugin.json": true}
	if m.Runtime.Kind == "wasi-command" {
		declared[m.Runtime.Entry] = true
	}
	for _, name := range m.Assets {
		declared[name] = true
	}
	for _, name := range names {
		if !declared[name] {
			return fmt.Errorf("undeclared package file %q", name)
		}
	}
	return nil
}

func openPackageFile(dirFD int, name string) (*os.File, error) {
	fd, err := syscall.Openat(dirFD, name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func safeRelative(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\") || filepath.IsAbs(name) || name == "." || filepath.Clean(name) != name || name == ".." {
		return fmt.Errorf("unsafe relative path %q", name)
	}
	return nil
}

func strictJSON(data []byte, dst any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON object key")
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

// Discover lists conforming and nonconforming packages without activation.
func Discover(root string) ([]Package, map[string]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	var packages []Package
	rejected := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := Conformance(filepath.Join(root, entry.Name()))
		if err != nil {
			rejected[entry.Name()] = err.Error()
			continue
		}
		packages = append(packages, p)
	}
	return packages, rejected, nil
}
