package runtimekit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lgvo/p.ai/internal/nixenv"
)

const (
	devShellDir        = "/etc/p/devshell"
	devShellActivation = devShellDir + "/activate.sh"
	devShellManifest   = devShellDir + "/material.json"
)

// VerifyDevShellMaterial checks the root-owned captured material immediately
// before a UID-1000 shell can source it. It never evaluates Nix or executes
// the manifest; the later shell hook remains unprivileged.
func VerifyDevShellMaterial(digest string) error {
	if !validSHA256(digest) {
		return errors.New("invalid activation material digest")
	}
	dir, err := os.Lstat(devShellDir)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 || owner(dir) != 0 || dir.Mode().Perm() != 0755 {
		return errors.New("activation material directory is unsafe")
	}
	return verifyDevShellFiles(digest, readRootMaterial)
}

func verifyDevShellFiles(digest string, read func(string, int) ([]byte, error)) error {
	raw, err := read(devShellManifest, nixenv.MaxScript+nixenv.MaxJSON)
	if err != nil {
		return err
	}
	material, err := nixenv.ParseMaterial(raw)
	if err != nil {
		return err
	}
	got, err := material.Digest()
	if err != nil || got != digest {
		return errors.Join(err, errors.New("activation material digest changed"))
	}
	for _, item := range []struct {
		path string
		want string
		max  int
	}{
		{devShellActivation, material.Script, nixenv.MaxScript},
		{devShellDir + "/.attrs.sh", material.AttrsSH, nixenv.MaxJSON},
		{devShellDir + "/.attrs.json", material.AttrsJSON, nixenv.MaxJSON},
	} {
		data, err := read(item.path, item.max)
		if err != nil || !bytes.Equal(data, []byte(item.want)) {
			return errors.Join(err, fmt.Errorf("activation asset %s changed", filepath.Base(item.path)))
		}
	}
	return nil
}

func readRootMaterial(path string, max int) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || meta.Uid != 0 || meta.Gid != 0 || meta.Nlink != 1 || st.Mode().Perm() != 0444 || st.Size() > int64(max) {
		return nil, errors.New("root-owned activation asset is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(data) > max {
		return nil, errors.Join(err, errors.New("activation asset exceeds bound"))
	}
	return data, nil
}
