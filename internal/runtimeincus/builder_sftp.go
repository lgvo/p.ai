package runtimeincus

import (
	"context"
	"encoding/binary"
	"errors"
)

// Incus's HTTP file POST does not change an existing directory's mode, and
// its GET follows symlinks. These narrow SFTP requests use the same confined
// instance socket as the file transfer and never execute code in the guest.
func (a *unixFileAPI) chmodDirectory(ctx context.Context, path string, mode int) error {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return err
	}
	defer conn.Close()
	request := make([]byte, 21+len(path))
	binary.BigEndian.PutUint32(request[:4], uint32(len(request)-4))
	request[4] = 9 // SSH_FXP_SETSTAT
	binary.BigEndian.PutUint32(request[5:9], 1)
	binary.BigEndian.PutUint32(request[9:13], uint32(len(path)))
	copy(request[13:], path)
	offset := 13 + len(path)
	binary.BigEndian.PutUint32(request[offset:offset+4], 4) // SSH_FILEXFER_ATTR_PERMISSIONS
	binary.BigEndian.PutUint32(request[offset+4:], uint32(mode))
	if err := writePacket(conn, request); err != nil {
		return err
	}
	status, err := readSFTPPacket(stream)
	if err != nil {
		return err
	}
	return sftpStatusError(status, "directory chmod")
}

func (a *unixFileAPI) readlinkBounded(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes > 16<<10 {
		return nil, errors.New("invalid symlink target bound")
	}
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := writeSFTPPathRequest(conn, 19, path); err != nil { // SSH_FXP_READLINK
		return nil, err
	}
	response, err := readSFTPPacket(stream)
	if err != nil {
		return nil, err
	}
	return parseSFTPReadlink(response, maxBytes)
}

func parseSFTPReadlink(response []byte, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || len(response) < 17 || response[0] != 104 || binary.BigEndian.Uint32(response[1:5]) != 1 || binary.BigEndian.Uint32(response[5:9]) != 1 {
		return nil, errors.New("Incus SFTP readlink refused")
	}
	length := binary.BigEndian.Uint32(response[9:13])
	if int64(length) > maxBytes || int(length) > len(response)-13 {
		return nil, errors.New("Incus SFTP symlink target exceeds bound")
	}
	target := response[13 : 13+length]
	// NAME also carries a long name and attrs. Require complete encoding.
	remainder := response[13+length:]
	if len(remainder) < 8 {
		return nil, errors.New("Incus SFTP readlink response truncated")
	}
	longNameSize := binary.BigEndian.Uint32(remainder[:4])
	if int(longNameSize) > len(remainder)-8 || len(remainder) != 8+int(longNameSize) {
		return nil, errors.New("Incus SFTP readlink response malformed")
	}
	if binary.BigEndian.Uint32(remainder[4+longNameSize:]) != 0 {
		return nil, errors.New("Incus SFTP readlink attrs unexpected")
	}
	return append([]byte(nil), target...), nil
}

func sftpStatusError(response []byte, operation string) error {
	if len(response) < 9 || response[0] != 101 || binary.BigEndian.Uint32(response[1:5]) != 1 {
		return errors.New("Incus SFTP " + operation + " status invalid")
	}
	if code := binary.BigEndian.Uint32(response[5:9]); code != 0 {
		return errors.New("Incus SFTP " + operation + " refused")
	}
	return nil
}
