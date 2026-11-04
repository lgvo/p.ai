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

// The bounds of a package: its files, the manifest and the whole package.
// They admit a native executable as an entry.
const (
	maxPackageFiles  = 128
	maxFileBytes     = 32 << 20
	maxPackageBytes  = 64 << 20
	maxManifestBytes = 64 << 10
)

// agentAdapterAssets are the files an agent-adapter package holds, each
// named by its role.
var agentAdapterAssets = []string{"p-codex-adapter"}

// Conformance validates package data and returns its content digest. It never
// starts package code.
func Conformance(path string) (Package, error) {
	pkg, _, err := packageSnapshot(context.Background(), path, nil)
	return pkg, err
}

// packageSnapshot hashes the same opened bytes it optionally retains for an
// invocation. Callers must compare the returned digest with trusted selection
// before using retained bytes.
func packageSnapshot(ctx context.Context, path string, capture map[string]bool) (Package, map[string][]byte, error) {
	var result Package
	release, err := LeasePackage(path)
	if err != nil {
		return result, nil, err
	}
	defer release()
	return snapshotWithoutLease(ctx, path, capture)
}

func snapshotWithoutLease(ctx context.Context, path string, capture map[string]bool) (Package, map[string][]byte, error) {
	if !filepath.IsAbs(path) {
		return Package{}, nil, errors.New("package path must be absolute")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Package{}, nil, err
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	return snapshotOpen(ctx, path, dir, capture, nil)
}

// snapshotOpen hashes the package at path, whose directory dir is open,
// and retains the bytes of the files capture names, or of every file for
// "*". Each file is opened from dir without following a symbolic link;
// opened, when set, sees each before it is read and may refuse it.
func snapshotOpen(ctx context.Context, path string, dir *os.File, capture map[string]bool, opened func(name string, f *os.File) error) (Package, map[string][]byte, error) {
	var result Package
	retained := map[string][]byte{}
	entries, err := dir.ReadDir(maxPackageFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, nil, err
	}
	if len(entries) > maxPackageFiles {
		return result, nil, fmt.Errorf("package has more than %d files", maxPackageFiles)
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
	var totalSize int64
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, nil, err
		}
		file, err := openPackageFile(int(dir.Fd()), name)
		if err != nil {
			return result, nil, err
		}
		if opened != nil {
			if err := opened(name, file); err != nil {
				file.Close()
				return result, nil, err
			}
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
		if info.Size() > maxFileBytes || totalSize > maxPackageBytes {
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
			if info.Size() > maxManifestBytes {
				file.Close()
				return result, nil, errors.New("manifest exceeds 64 KiB")
			}
			manifestData, err = readBoundedContext(ctx, file, info.Size())
			if capture["*"] {
				retained[name] = manifestData
			}
			if err == nil && int64(len(manifestData)) != info.Size() {
				err = errors.New("manifest changed during read")
			}
			if err == nil {
				_, err = h.Write(manifestData)
			}
		} else if capture[name] || capture["*"] {
			data, readErr := readBoundedContext(ctx, file, info.Size())
			if readErr != nil || int64(len(data)) != info.Size() {
				file.Close()
				return result, nil, fmt.Errorf("package file changed during read: %s", name)
			}
			retained[name] = data
			_, err = h.Write(data)
		} else {
			_, err = copyNContext(ctx, h, file, info.Size())
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
	manifest, err := decodeManifest(manifestData)
	if err != nil {
		return result, nil, err
	}
	if err := validateManifest(manifest, names); err != nil {
		return result, nil, err
	}
	result = Package{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Manifest: manifest}
	return result, retained, nil
}

// decodeManifest reads a manifest strictly, after its schema: a manifest of
// another schema is refused as such, not by its fields.
func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	var schema struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return manifest, fmt.Errorf("manifest: %w", err)
	}
	if schema.Schema != ManifestSchema {
		return manifest, fmt.Errorf("unsupported manifest schema %q", schema.Schema)
	}
	if err := strictJSON(data, &manifest); err != nil {
		return manifest, fmt.Errorf("manifest: %w", err)
	}
	return manifest, nil
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

func validateManifest(m Manifest, names []string) error {
	if !idRE.MatchString(m.ID) {
		return errors.New("invalid plugin id")
	}
	if !versionRE.MatchString(m.Version) {
		return errors.New("version must be major.minor.patch")
	}
	if m.Description == "" || len(m.Description) > 512 {
		return errors.New("description must be 1–512 characters")
	}
	declared := map[string]bool{"plugin.json": true}
	switch m.Capability {
	case CapabilityEventHandler:
		if len(m.Assets) != 0 {
			return fmt.Errorf("%s names an entry, not assets", m.Capability)
		}
		if m.Entry == "" {
			return fmt.Errorf("%s requires an entry", m.Capability)
		}
		if err := safeRelative(m.Entry); err != nil {
			return fmt.Errorf("entry: %w", err)
		}
		if m.Entry == "plugin.json" || !slices.Contains(names, m.Entry) {
			return fmt.Errorf("missing entry %q", m.Entry)
		}
		declared[m.Entry] = true
	case CapabilityAgentAdapter:
		if m.Entry != "" {
			return fmt.Errorf("%s holds files and names no entry", m.Capability)
		}
		for _, asset := range m.Assets {
			if err := safeRelative(asset); err != nil {
				return fmt.Errorf("asset: %w", err)
			}
			if declared[asset] {
				return fmt.Errorf("duplicate asset %q", asset)
			}
			if !slices.Contains(names, asset) {
				return fmt.Errorf("missing asset %q", asset)
			}
			declared[asset] = true
		}
		if !slices.Equal(m.Assets, agentAdapterAssets) {
			return fmt.Errorf("%s must hold exactly the assets %q", m.Capability, agentAdapterAssets)
		}
	default:
		return fmt.Errorf("unsupported capability %q", m.Capability)
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

// Discover lists conforming and nonconforming packages; it selects none.
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
