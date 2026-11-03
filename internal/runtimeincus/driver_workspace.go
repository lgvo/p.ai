package runtimeincus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// guestWorkspace is the session's Git clone in the guest: where a commit
// image's builder cloned the commit.
const guestWorkspace = builderWorkspace

// maxWorkspaceWrite bounds the bytes of one workspace write.
const maxWorkspaceWrite = 1 << 20

// ReadWorkspace reads one path of the stopped session's workspace, or "."
// for the workspace root: its type, owner and mode, and a regular file's
// bytes when it holds at most limit of them. A path that is missing, or
// under a missing directory, is absent.
func (d *Driver) ReadWorkspace(ctx context.Context, t runtimedriver.Target, p string, limit int64) (runtimedriver.File, error) {
	if limit < 0 {
		return runtimedriver.File{}, errors.New("a read needs a limit of zero or more bytes")
	}
	if p != "." {
		if err := checkWorkspacePath(p); err != nil {
			return runtimedriver.File{}, err
		}
	}
	w, err := d.backend.stoppedWorkspace(ctx, t)
	if err != nil {
		return runtimedriver.File{}, err
	}
	e, err := w.lstat(ctx, p)
	if err != nil {
		return runtimedriver.File{}, err
	}
	if !e.exists {
		return runtimedriver.File{Type: runtimedriver.FileAbsent}, nil
	}
	return w.read(ctx, e, limit)
}

// WriteWorkspace writes a regular file or a directory with its exact owner,
// mode and bytes where its parent directory exists. It never replaces a
// symlink nor a path of another type, and never changes an existing
// directory, which Incus's file API would leave as it is.
func (d *Driver) WriteWorkspace(ctx context.Context, t runtimedriver.Target, p string, f runtimedriver.File, g runtimedriver.Gate) error {
	if g == nil {
		return errors.New("workspace write gate required")
	}
	want, err := workspaceWrite(f)
	if err != nil {
		return err
	}
	if err := checkWorkspacePath(p); err != nil {
		return err
	}
	w, err := d.backend.stoppedWorkspace(ctx, t)
	if err != nil {
		return err
	}
	e, err := w.lstat(ctx, p)
	if err != nil {
		return err
	}
	if !e.parent {
		return fmt.Errorf("the parent of %s is not a directory", p)
	}
	if e.exists {
		switch {
		case e.file.typ == "symlink":
			return fmt.Errorf("%s is a symlink, which a write never replaces", p)
		case e.file.typ != want.typ:
			return fmt.Errorf("%s is a %s, not a %s", p, workspaceType(e.file.typ), f.Type)
		case want.typ == "directory" && (e.file.uid != want.uid || e.file.gid != want.gid || e.file.mode != want.mode):
			return fmt.Errorf("directory %s has another owner or mode, which Incus does not change", p)
		}
	}
	if err := g(); err != nil {
		return err
	}
	// An existing directory is already what the write asks.
	if !e.exists || want.typ == "file" {
		if err := w.api.put(ctx, e.name, want); err != nil {
			return errors.Join(err, runtimedriver.ErrUnknownOutcome)
		}
	}
	if held, err := w.holds(ctx, p, f); err != nil || !held {
		return errors.Join(err, fmt.Errorf("%s is not as written", p), runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// RemoveWorkspace removes a file, a symlink or an empty directory. A path
// already absent needs no removal and no gate.
func (d *Driver) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, p string, g runtimedriver.Gate) error {
	if g == nil {
		return errors.New("workspace remove gate required")
	}
	if err := checkWorkspacePath(p); err != nil {
		return err
	}
	w, err := d.backend.stoppedWorkspace(ctx, t)
	if err != nil {
		return err
	}
	e, err := w.lstat(ctx, p)
	if err != nil || !e.exists {
		return err
	}
	directory := e.file.typ == "directory"
	if directory {
		empty, err := w.api.emptyDirectory(ctx, e.name)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("directory %s is not empty", p)
		}
	}
	if err := g(); err != nil {
		return err
	}
	if err := w.api.remove(ctx, e.name, directory); err != nil {
		return errors.Join(err, runtimedriver.ErrUnknownOutcome)
	}
	if after, err := w.lstat(ctx, p); err != nil || after.exists {
		return errors.Join(err, fmt.Errorf("%s remains after its removal", p), runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// stoppedWorkspace is the workspace of a session's stopped runtime at one
// incarnation, reached only through Incus's file API of that instance.
//
// This is how the Incus driver keeps to its stopped storage: Incus serves a
// stopped container's files from the instance's storage volume alone. It
// mounts no disk device into it, so no grant, endpoint directory, host path
// or other runtime's storage can be reached through it, at, below or above
// any path, and a symlink inside it resolves inside it. lstat refuses a
// symlink above a path anyway, and no operation follows one at the path.
type stoppedWorkspace struct{ api *unixFileAPI }

// stoppedWorkspace checks a workspace operation's target: the session's
// runtime, exactly as specified, stopped, at the incarnation t.At, in a
// confined project.
func (b *Backend) stoppedWorkspace(ctx context.Context, t runtimedriver.Target) (*stoppedWorkspace, error) {
	if t.At == "" {
		return nil, errors.New("workspace operations need the runtime's incarnation")
	}
	s, err := b.NativeSession(t.Session)
	if err != nil {
		return nil, err
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return nil, err
	}
	o, err := b.Inspect(ctx, s)
	if err != nil {
		return nil, err
	}
	if !o.Exists {
		return nil, errors.New("the session has no runtime")
	}
	if err := sameIncarnation(o, t.At); err != nil {
		return nil, err
	}
	if o.Status != "Stopped" {
		return nil, errors.New("the workspace is reached only while the runtime is stopped")
	}
	return &stoppedWorkspace{api: &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}}, nil
}

func checkWorkspacePath(p string) error {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
		return fmt.Errorf("workspace path %q is not relative and clean", p)
	}
	return nil
}

// workspaceEntry is one workspace path as lstat found it.
type workspaceEntry struct {
	name   string    // its guest path
	parent bool      // its parent is a directory, not a symlink
	exists bool      // it exists; file is what it is
	file   guestFile // with its full mode
}

// lstat reads the workspace path p, or "." for the root, one component at a
// time with SFTP LSTAT, so that no symlink is followed: a symlink above p is
// refused, and a missing component or a file above p makes p absent.
func (w *stoppedWorkspace) lstat(ctx context.Context, p string) (workspaceEntry, error) {
	name := path.Join(guestWorkspace, p)
	root, exists, err := w.api.lstatOptional(ctx, guestWorkspace)
	if err != nil || !exists {
		return workspaceEntry{name: name}, err
	}
	if root.typ != "directory" {
		return workspaceEntry{}, fmt.Errorf("the workspace is a %s, not a directory", workspaceType(root.typ))
	}
	if p == "." {
		return workspaceEntry{name: name, parent: true, exists: true, file: root}, nil
	}
	parts := strings.Split(p, "/")
	current := guestWorkspace
	for _, part := range parts[:len(parts)-1] {
		current += "/" + part
		f, exists, err := w.api.lstatOptional(ctx, current)
		switch {
		case err != nil:
			return workspaceEntry{}, err
		case !exists:
			return workspaceEntry{name: name}, nil
		case f.typ == "symlink":
			return workspaceEntry{}, fmt.Errorf("%s is a symlink above %s", strings.TrimPrefix(current, guestWorkspace+"/"), p)
		case f.typ != "directory":
			return workspaceEntry{name: name}, nil
		}
	}
	f, exists, err := w.api.lstatOptional(ctx, name)
	if err != nil {
		return workspaceEntry{}, err
	}
	return workspaceEntry{name: name, parent: true, exists: exists, file: f}, nil
}

// read is the file e found, with a regular file's bytes, at most limit of
// them. Its parents are directories, so Incus's HTTP GET reads e itself.
func (w *stoppedWorkspace) read(ctx context.Context, e workspaceEntry, limit int64) (runtimedriver.File, error) {
	f := runtimedriver.File{Type: workspaceType(e.file.typ), UID: e.file.uid, GID: e.file.gid, Mode: uint32(e.file.mode)}
	if e.file.typ != "file" {
		return f, nil
	}
	got, exists, err := w.api.getBounded(ctx, e.name, limit)
	if err != nil {
		return runtimedriver.File{}, err
	}
	// The HTTP file API shows only the permission bits.
	if !exists || got.typ != "file" || got.uid != e.file.uid || got.gid != e.file.gid || got.mode != e.file.mode&0777 {
		return runtimedriver.File{}, errors.New("workspace file changed during read")
	}
	f.Data = got.data
	return f, nil
}

// holds says whether the workspace path p is exactly want: its type, owner
// and mode, and a regular file's bytes.
func (w *stoppedWorkspace) holds(ctx context.Context, p string, want runtimedriver.File) (bool, error) {
	e, err := w.lstat(ctx, p)
	if err != nil || !e.exists || workspaceType(e.file.typ) != want.Type {
		return false, err
	}
	got, err := w.read(ctx, e, int64(len(want.Data)))
	if err != nil {
		return false, err
	}
	return got.UID == want.UID && got.GID == want.GID && got.Mode == want.Mode && bytes.Equal(got.Data, want.Data), nil
}

// workspaceWrite is the guest file a write of f puts: a regular file or a
// directory, with permission bits only, since Incus's file API sets no
// others exactly.
func workspaceWrite(f runtimedriver.File) (guestFile, error) {
	typ := map[runtimedriver.FileType]string{runtimedriver.FileRegular: "file", runtimedriver.FileDirectory: "directory"}[f.Type]
	switch {
	case typ == "":
		return guestFile{}, errors.New("only a regular file or a directory is written")
	case f.Mode&^0o777 != 0:
		return guestFile{}, fmt.Errorf("mode %#o has bits beyond the permissions", f.Mode)
	case f.UID < 0 || f.GID < 0 || f.UID > 1<<31-1 || f.GID > 1<<31-1:
		return guestFile{}, errors.New("owner out of range")
	case typ == "directory" && len(f.Data) != 0:
		return guestFile{}, errors.New("a directory has no bytes")
	case len(f.Data) > maxWorkspaceWrite:
		return guestFile{}, fmt.Errorf("a write holds at most %d bytes", maxWorkspaceWrite)
	}
	return guestFile{typ: typ, uid: f.UID, gid: f.GID, mode: int(f.Mode), data: bytes.Clone(f.Data)}, nil
}

func workspaceType(typ string) runtimedriver.FileType {
	switch typ {
	case "file":
		return runtimedriver.FileRegular
	case "directory":
		return runtimedriver.FileDirectory
	case "symlink":
		return runtimedriver.FileSymlink
	}
	return runtimedriver.FileOther
}

// remove removes one guest path with SFTP REMOVE, or RMDIR for a directory,
// which fails unless it is empty. Neither follows a symlink at the path.
func (a *unixFileAPI) remove(ctx context.Context, name string, directory bool) error {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return err
	}
	defer conn.Close()
	op := byte(13) // SSH_FXP_REMOVE
	if directory {
		op = 15 // SSH_FXP_RMDIR
	}
	if err := writeSFTPPathRequest(conn, op, name); err != nil {
		return err
	}
	status, err := readSFTPPacket(stream)
	if err != nil {
		return err
	}
	return sftpStatusError(status, "remove")
}

// emptyDirectory says whether a directory holds no entry, from the first
// page of its SFTP listing. The HTTP file API would list the whole
// directory on the server before any bound applied.
func (a *unixFileAPI) emptyDirectory(ctx context.Context, name string) (bool, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := writeSFTPPathRequest(conn, 11, name); err != nil { // SSH_FXP_OPENDIR
		return false, err
	}
	reply, err := readSFTPPacket(stream)
	if err != nil {
		return false, err
	}
	if len(reply) < 9 || reply[0] != 102 || binary.BigEndian.Uint32(reply[1:5]) != 1 { // SSH_FXP_HANDLE
		return false, errors.New("Incus SFTP opendir refused")
	}
	handle, rest, ok := sftpField(reply[5:])
	if !ok || len(handle) == 0 || len(handle) > 256 || len(rest) != 0 {
		return false, errors.New("Incus SFTP directory handle malformed")
	}
	// The dot entries do not count; a page may hold only them.
	for id := uint32(2); id < 5; id++ {
		if err := writeSFTPRequest(conn, 12, id, string(handle)); err != nil { // SSH_FXP_READDIR
			return false, err
		}
		page, err := readSFTPPacketBounded(stream, 128<<10)
		if err != nil {
			return false, err
		}
		if len(page) >= 9 && page[0] == 101 && binary.BigEndian.Uint32(page[1:5]) == id && binary.BigEndian.Uint32(page[5:9]) == 1 { // SSH_FX_EOF
			return true, nil
		}
		names, err := parseSFTPNames(page, id)
		if err != nil {
			return false, err
		}
		for _, n := range names {
			if n != "." && n != ".." {
				return false, nil
			}
		}
	}
	return false, errors.New("Incus SFTP listing did not end")
}

// parseSFTPNames reads the file names of an SSH_FXP_NAME reply to request
// id, checking the whole encoding.
func parseSFTPNames(reply []byte, id uint32) ([]string, error) {
	if len(reply) < 9 || reply[0] != 104 || binary.BigEndian.Uint32(reply[1:5]) != id {
		return nil, errors.New("Incus SFTP listing refused")
	}
	count := binary.BigEndian.Uint32(reply[5:9])
	rest := reply[9:]
	if count == 0 || count > 1024 {
		return nil, errors.New("Incus SFTP listing count outside bound")
	}
	names := make([]string, 0, count)
	for range count {
		name, tail, ok := sftpField(rest)
		if !ok {
			return nil, errors.New("Incus SFTP listing name malformed")
		}
		if _, tail, ok = sftpField(tail); !ok || len(tail) < 4 { // the long name
			return nil, errors.New("Incus SFTP listing long name malformed")
		}
		flags := binary.BigEndian.Uint32(tail[:4])
		tail = tail[4:]
		if flags&^uint32(0xf) != 0 {
			return nil, errors.New("Incus SFTP listing attributes unsupported")
		}
		for _, field := range []struct {
			flag  uint32
			bytes int
		}{{1, 8}, {2, 8}, {4, 4}, {8, 8}} { // size, owner, permissions, times
			if flags&field.flag != 0 {
				if len(tail) < field.bytes {
					return nil, errors.New("Incus SFTP listing attributes truncated")
				}
				tail = tail[field.bytes:]
			}
		}
		names = append(names, string(name))
		rest = tail
	}
	if len(rest) != 0 {
		return nil, errors.New("Incus SFTP listing has trailing bytes")
	}
	return names, nil
}

// sftpField splits a length-prefixed SFTP string off raw.
func sftpField(raw []byte) ([]byte, []byte, bool) {
	if len(raw) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(raw[:4])
	if n > uint32(len(raw)-4) {
		return nil, nil, false
	}
	return raw[4 : 4+n], raw[4+n:], true
}
