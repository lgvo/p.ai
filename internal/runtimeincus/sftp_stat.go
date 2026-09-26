package runtimeincus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// storeDirectoryMode asks Incus's confined instance SFTP endpoint for the
// complete POSIX mode. Its HTTP file endpoint exposes only Mode().Perm().
// Only LSTAT of this fixed ancestor is supported; this is not a general SFTP
// client or an execution channel into the guest.
func (a attachmentFileAPI) storeDirectoryMode(ctx context.Context) (guestFile, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return guestFile{}, err
	}
	defer conn.Close()
	return sftpLstat(conn, stream, "/nix/store")
}

// fullDirectoryMode is used by stopped builder image scrubbing where sticky
// bits matter. The HTTP file metadata contains only FileMode.Perm().
func (a *unixFileAPI) fullDirectoryMode(ctx context.Context, path string) (guestFile, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return guestFile{}, err
	}
	defer conn.Close()
	return sftpLstat(conn, stream, path)
}

// lstat observes an exact fixed image path without Incus HTTP RealPath
// following a symlink. Assembly uses it while the instance is stopped.
func (a *unixFileAPI) lstat(ctx context.Context, path string) (guestFile, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return guestFile{}, err
	}
	defer conn.Close()
	return sftpLstatAny(conn, stream, path)
}

func (a *unixFileAPI) lstatOptional(ctx context.Context, path string) (guestFile, bool, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return guestFile{}, false, err
	}
	defer conn.Close()
	if err := writeSFTPPathRequest(conn, 7, path); err != nil {
		return guestFile{}, false, err
	}
	response, err := readSFTPPacket(stream)
	if err != nil {
		return guestFile{}, false, err
	}
	if len(response) >= 9 && response[0] == 101 && binary.BigEndian.Uint32(response[1:5]) == 1 && binary.BigEndian.Uint32(response[5:9]) == 2 {
		// No-such-file is the only status accepted as absence. Validate the
		// remaining SSH_FXP_STATUS strings rather than trusting a prefix.
		rest := response[9:]
		for i := 0; i < 2; i++ {
			if len(rest) < 4 {
				return guestFile{}, false, errors.New("Incus SFTP missing-path status malformed")
			}
			n := binary.BigEndian.Uint32(rest[:4])
			rest = rest[4:]
			if n > uint32(len(rest)) {
				return guestFile{}, false, errors.New("Incus SFTP missing-path status malformed")
			}
			rest = rest[n:]
		}
		if len(rest) != 0 {
			return guestFile{}, false, errors.New("Incus SFTP missing-path status malformed")
		}
		return guestFile{}, false, nil
	}
	f, err := parseSFTPLstat(response)
	return f, err == nil, err
}

// openIncusSFTP opens only the confined instance endpoint. The caller closes
// conn after one bounded request; stream includes any buffered upgrade bytes.
func openIncusSFTP(ctx context.Context, socket, project, instance string) (net.Conn, io.Reader, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		if closeOnError {
			stopCancellation()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	u := url.URL{Scheme: "http", Host: "incus", Path: "/1.0/instances/" + url.PathEscape(instance) + "/sftp"}
	q := u.Query()
	q.Set("project", project)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "sftp")
	if err = req.Write(conn); err != nil {
		return nil, nil, err
	}
	// ReadResponse has no caller-supplied header limit. Bound the upgrade
	// handshake separately, then drain any buffered protocol bytes before
	// reading directly from the connection.
	reader := bufio.NewReader(io.LimitReader(conn, 8<<10))
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "sftp" {
		resp.Body.Close()
		return nil, nil, errors.New("Incus SFTP upgrade refused")
	}
	buffered := make([]byte, reader.Buffered())
	if _, err := io.ReadFull(reader, buffered); err != nil {
		return nil, nil, err
	}
	stream := io.MultiReader(bytes.NewReader(buffered), conn)
	var init [9]byte
	binary.BigEndian.PutUint32(init[:4], 5)
	init[4] = 1 // SSH_FXP_INIT
	binary.BigEndian.PutUint32(init[5:], 3)
	if err = writePacket(conn, init[:]); err != nil {
		return nil, nil, err
	}
	version, err := readSFTPPacket(stream)
	if err != nil || len(version) < 5 || version[0] != 2 || binary.BigEndian.Uint32(version[1:5]) != 3 || !validSFTPExtensions(version[5:]) {
		return nil, nil, errors.New("Incus SFTP version refused")
	}
	closeOnError = false
	return &cancellableSFTPConn{Conn: conn, stop: stopCancellation}, stream, nil
}

type cancellableSFTPConn struct {
	net.Conn
	stop func() bool
}

func (c *cancellableSFTPConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

func sftpLstat(conn net.Conn, stream io.Reader, name string) (guestFile, error) {
	got, err := sftpLstatAny(conn, stream, name)
	if err != nil || got.typ != "directory" {
		return guestFile{}, errors.Join(err, errors.New("Incus SFTP directory metadata invalid"))
	}
	return got, nil
}

func sftpLstatAny(conn net.Conn, stream io.Reader, name string) (guestFile, error) {
	var zero guestFile
	if err := writeSFTPPathRequest(conn, 7, name); err != nil { // SSH_FXP_LSTAT
		return zero, err
	}
	attrs, err := readSFTPPacket(stream)
	if err != nil {
		return zero, err
	}
	return parseSFTPLstat(attrs)
}

func parseSFTPLstat(attrs []byte) (guestFile, error) {
	var zero guestFile
	if len(attrs) < 9 || attrs[0] != 105 || binary.BigEndian.Uint32(attrs[1:5]) != 1 {
		return zero, errors.New("Incus SFTP LSTAT refused")
	}
	flags := binary.BigEndian.Uint32(attrs[5:9])
	if flags&^uint32(0x0000000f) != 0 || flags&0x00000006 != 0x00000006 {
		return zero, errors.New("Incus SFTP attributes incomplete")
	}
	offset := 9
	if flags&1 != 0 { // size
		offset += 8
	}
	want := offset + 12
	if flags&8 != 0 { // atime and mtime
		want += 8
	}
	if len(attrs) != want {
		return zero, errors.New("Incus SFTP attributes truncated")
	}
	uid := binary.BigEndian.Uint32(attrs[offset : offset+4])
	gid := binary.BigEndian.Uint32(attrs[offset+4 : offset+8])
	mode := binary.BigEndian.Uint32(attrs[offset+8 : offset+12])
	if uid > 1<<31-1 || gid > 1<<31-1 || mode&^uint32(0177777) != 0 {
		return zero, errors.New("Incus SFTP metadata invalid")
	}
	typ := ""
	switch mode & 0170000 {
	case 0040000:
		typ = "directory"
	case 0100000:
		typ = "file"
	case 0120000:
		typ = "symlink"
	default:
		return zero, errors.New("Incus SFTP path type invalid")
	}
	return guestFile{typ: typ, uid: int(uid), gid: int(gid), mode: int(mode & 07777)}, nil
}

func writeSFTPPathRequest(conn net.Conn, op byte, name string) error {
	request := make([]byte, 13+len(name))
	binary.BigEndian.PutUint32(request[:4], uint32(len(request)-4))
	request[4] = op
	binary.BigEndian.PutUint32(request[5:9], 1)
	binary.BigEndian.PutUint32(request[9:13], uint32(len(name)))
	copy(request[13:], name)
	return writePacket(conn, request)
}

// VERSION may advertise extension name/data pairs. We use no extensions, but
// still require their wire encoding to be complete before trusting LSTAT.
func validSFTPExtensions(data []byte) bool {
	for len(data) != 0 {
		for field := 0; field < 2; field++ {
			if len(data) < 4 {
				return false
			}
			n := binary.BigEndian.Uint32(data[:4])
			data = data[4:]
			if (field == 0 && n == 0) || n > uint32(len(data)) {
				return false
			}
			data = data[n:]
		}
	}
	return true
}

func writePacket(w io.Writer, packet []byte) error {
	for len(packet) != 0 {
		n, err := w.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}

func readSFTPPacket(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < 5 || n > 16<<10 {
		return nil, errors.New("Incus SFTP packet outside bound")
	}
	packet := make([]byte, n)
	_, err := io.ReadFull(r, packet)
	return packet, err
}
