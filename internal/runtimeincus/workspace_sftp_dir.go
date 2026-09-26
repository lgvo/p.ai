package runtimeincus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"time"
)

// listDirectory asks the confined Incus SFTP endpoint for bounded pages.
// Incus's HTTP directory GET materializes the entire server-side ReadDir
// result before our response limit can apply. No directory is opened until
// its caller has LSTATed it while the source is stopped or frozen.
func (a *unixFileAPI) listDirectory(ctx context.Context, dir string, maximum int) ([]string, error) {
	if maximum < 0 || maximum > workspaceMaxEntries || dir == "" || dir[0] != '/' || path.Clean(dir) != dir {
		return nil, errors.New("workspace directory request outside bound")
	}
	prior, err := a.lstat(ctx, dir)
	if err != nil || prior.typ != "directory" {
		return nil, errors.Join(err, errors.New("directory LSTAT refused"))
	}
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(20 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	names, err := readBoundedSFTPDirectory(conn, stream, dir, maximum)
	if err != nil {
		return nil, err
	}
	after, err := a.lstat(ctx, dir)
	if err != nil || after.typ != prior.typ || after.uid != prior.uid || after.gid != prior.gid || after.mode != prior.mode {
		return nil, errors.Join(err, errors.New("directory changed during enumeration"))
	}
	return names, nil
}

func readBoundedSFTPDirectory(conn net.Conn, stream io.Reader, dir string, maximum int) ([]string, error) {
	open := sftpDirectoryRequest(11, 1, []byte(dir)) // SSH_FXP_OPENDIR
	if err := writePacket(conn, open); err != nil {
		return nil, err
	}
	reply, err := readSFTPPacket(stream)
	if err != nil {
		return nil, errors.Join(err, errors.New("workspace SFTP OPENDIR response unavailable"))
	}
	handle, err := parseDirectoryHandle(reply)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	seen := make(map[string]bool)
	for id := uint32(2); id <= uint32(maximum)+3; id++ {
		if err := writePacket(conn, sftpDirectoryRequest(12, id, handle)); err != nil { // SSH_FXP_READDIR
			return nil, err
		}
		reply, err := readSFTPPacket(stream)
		if err != nil {
			return nil, errors.Join(err, fmt.Errorf("workspace SFTP READDIR id=%d response unavailable", id))
		}
		if len(reply) >= 5 && reply[0] == 101 { // SSH_FXP_STATUS
			if err := parseMountinfoStatus(reply, id, 1); err != nil {
				return nil, errors.Join(err, errors.New("workspace SFTP READDIR refused"))
			}
			if err := writePacket(conn, sftpDirectoryRequest(4, id+1, handle)); err != nil { // SSH_FXP_CLOSE
				return nil, err
			}
			closed, err := readSFTPPacket(stream)
			if err != nil {
				return nil, errors.Join(err, errors.New("workspace SFTP CLOSE response unavailable"))
			}
			if err := parseMountinfoStatus(closed, id+1, 0); err != nil {
				return nil, errors.Join(err, errors.New("workspace SFTP CLOSE refused"))
			}
			return names, nil
		}
		page, err := parseSFTPDirectoryNames(reply, id, maximum-len(names))
		if err != nil || len(page) == 0 {
			return nil, errors.Join(err, errors.New("workspace SFTP READDIR page invalid"))
		}
		for _, name := range page {
			if !validWorkspaceComponent(name) || seen[name] {
				return nil, errors.New("workspace directory contains unsupported or duplicate name")
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return nil, errors.New("workspace SFTP READDIR did not reach EOF within bound")
}

func sftpDirectoryRequest(op byte, id uint32, value []byte) []byte {
	request := make([]byte, 13+len(value))
	binary.BigEndian.PutUint32(request[:4], uint32(len(request)-4))
	request[4] = op
	binary.BigEndian.PutUint32(request[5:9], id)
	binary.BigEndian.PutUint32(request[9:13], uint32(len(value)))
	copy(request[13:], value)
	return request
}

func parseDirectoryHandle(reply []byte) ([]byte, error) {
	if len(reply) < 10 || reply[0] != 102 || binary.BigEndian.Uint32(reply[1:5]) != 1 {
		return nil, errors.New("workspace SFTP OPENDIR refused")
	}
	n := binary.BigEndian.Uint32(reply[5:9])
	if n < 1 || n > 256 || len(reply) != 9+int(n) {
		return nil, errors.New("workspace SFTP directory handle malformed")
	}
	return append([]byte(nil), reply[9:]...), nil
}

func parseSFTPDirectoryNames(reply []byte, id uint32, remaining int) ([]string, error) {
	if len(reply) < 9 || reply[0] != 104 || binary.BigEndian.Uint32(reply[1:5]) != id { // SSH_FXP_NAME
		return nil, errors.New("workspace SFTP NAME response malformed")
	}
	count := binary.BigEndian.Uint32(reply[5:9])
	if count == 0 || count > uint32(remaining) || count > 128 {
		return nil, errors.New("workspace SFTP NAME count exceeds bound")
	}
	rest := reply[9:]
	names := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		filename, tail, ok := boundedSFTPString(rest)
		if !ok || len(filename) > 255 {
			return nil, errors.New("workspace SFTP filename malformed")
		}
		_, tail, ok = boundedSFTPString(tail) // longname is never trusted
		if !ok || len(tail) < 4 {
			return nil, errors.New("workspace SFTP longname malformed")
		}
		flags := binary.BigEndian.Uint32(tail[:4])
		tail = tail[4:]
		if flags&^uint32(0xf) != 0 {
			return nil, errors.New("workspace SFTP NAME attributes unsupported")
		}
		for _, field := range []struct {
			mask  uint32
			bytes int
		}{{1, 8}, {2, 8}, {4, 4}, {8, 8}} {
			if flags&field.mask != 0 {
				if len(tail) < field.bytes {
					return nil, errors.New("workspace SFTP NAME attributes truncated")
				}
				tail = tail[field.bytes:]
			}
		}
		names = append(names, string(filename))
		rest = tail
	}
	if len(rest) != 0 {
		return nil, errors.New("workspace SFTP NAME trailing bytes")
	}
	return names, nil
}

func boundedSFTPString(raw []byte) ([]byte, []byte, bool) {
	if len(raw) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(raw[:4])
	if n > uint32(len(raw)-4) {
		return nil, nil, false
	}
	return raw[4 : 4+n], raw[4+n:], true
}
