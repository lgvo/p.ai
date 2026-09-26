package runtimeincus

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

const frozenMountinfoPath = "/proc/1/mountinfo"
const frozenMountinfoMax = 256 << 10
const frozenMountinfoChunk = 8 << 10

// Procfs files report stat size zero even when they have content. Incus's
// HTTP file response uses that size as Content-Length, so this one fixed path
// is read through the same project-qualified SFTP endpoint as LSTAT. It does
// not expose an arbitrary-path read or guest execution channel.
func (a *unixFileAPI) readFrozenMountinfoSFTP(ctx context.Context) ([]byte, error) {
	conn, stream, err := openIncusSFTP(ctx, a.socket, a.project, a.instance)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	return readFixedMountinfoSFTP(conn, stream)
}

func readFixedMountinfoSFTP(conn net.Conn, stream io.Reader) ([]byte, error) {
	name := frozenMountinfoPath
	open := make([]byte, 21+len(name))
	binary.BigEndian.PutUint32(open[:4], uint32(len(open)-4))
	open[4] = 3 // SSH_FXP_OPEN
	binary.BigEndian.PutUint32(open[5:9], 1)
	binary.BigEndian.PutUint32(open[9:13], uint32(len(name)))
	copy(open[13:], name)
	offset := 13 + len(name)
	binary.BigEndian.PutUint32(open[offset:offset+4], 1) // SSH_FXF_READ only
	// Zero SSH_FILEXFER_ATTR flags follow; no write/create rights are sent.
	if err := writePacket(conn, open); err != nil {
		return nil, err
	}
	reply, err := readSFTPPacket(stream)
	if err != nil {
		return nil, err
	}
	handle, err := parseMountinfoHandle(reply)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, 16<<10)
	for requestID := uint32(2); requestID < 2+frozenMountinfoMax/frozenMountinfoChunk+2; requestID++ {
		read := make([]byte, 25+len(handle))
		binary.BigEndian.PutUint32(read[:4], uint32(len(read)-4))
		read[4] = 5 // SSH_FXP_READ
		binary.BigEndian.PutUint32(read[5:9], requestID)
		binary.BigEndian.PutUint32(read[9:13], uint32(len(handle)))
		copy(read[13:], handle)
		at := 13 + len(handle)
		binary.BigEndian.PutUint64(read[at:at+8], uint64(len(result)))
		binary.BigEndian.PutUint32(read[at+8:at+12], frozenMountinfoChunk)
		if err := writePacket(conn, read); err != nil {
			return nil, err
		}
		reply, err := readSFTPPacket(stream)
		if err != nil {
			return nil, err
		}
		data, eof, err := parseMountinfoRead(reply, requestID)
		if err != nil {
			return nil, err
		}
		if eof {
			closeRequest := make([]byte, 13+len(handle))
			binary.BigEndian.PutUint32(closeRequest[:4], uint32(len(closeRequest)-4))
			closeRequest[4] = 4 // SSH_FXP_CLOSE
			binary.BigEndian.PutUint32(closeRequest[5:9], requestID+1)
			binary.BigEndian.PutUint32(closeRequest[9:13], uint32(len(handle)))
			copy(closeRequest[13:], handle)
			if err := writePacket(conn, closeRequest); err != nil {
				return nil, err
			}
			closed, err := readSFTPPacket(stream)
			if err != nil || parseMountinfoStatus(closed, requestID+1, 0) != nil {
				return nil, errors.Join(err, errors.New("frozen mountinfo SFTP close refused"))
			}
			return result, nil
		}
		if len(result)+len(data) > frozenMountinfoMax {
			return nil, errors.New("frozen mountinfo SFTP content exceeds bound")
		}
		result = append(result, data...)
	}
	return nil, errors.New("frozen mountinfo SFTP did not reach EOF within bound")
}

func parseMountinfoHandle(reply []byte) ([]byte, error) {
	if len(reply) < 10 || reply[0] != 102 || binary.BigEndian.Uint32(reply[1:5]) != 1 {
		return nil, errors.New("frozen mountinfo SFTP OPEN refused")
	}
	length := binary.BigEndian.Uint32(reply[5:9])
	if length < 1 || length > 256 || len(reply) != 9+int(length) {
		return nil, errors.New("frozen mountinfo SFTP handle malformed")
	}
	return append([]byte(nil), reply[9:]...), nil
}

func parseMountinfoRead(reply []byte, id uint32) ([]byte, bool, error) {
	if len(reply) < 9 || binary.BigEndian.Uint32(reply[1:5]) != id {
		return nil, false, errors.New("frozen mountinfo SFTP READ response malformed")
	}
	if reply[0] == 101 { // SSH_FXP_STATUS
		if err := parseMountinfoStatus(reply, id, 1); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if reply[0] != 103 { // SSH_FXP_DATA
		return nil, false, errors.New("frozen mountinfo SFTP READ type invalid")
	}
	length := binary.BigEndian.Uint32(reply[5:9])
	if length < 1 || length > frozenMountinfoChunk || len(reply) != 9+int(length) {
		return nil, false, errors.New("frozen mountinfo SFTP DATA malformed")
	}
	return reply[9:], false, nil
}

func parseMountinfoStatus(reply []byte, id, wanted uint32) error {
	if len(reply) < 17 || reply[0] != 101 || binary.BigEndian.Uint32(reply[1:5]) != id || binary.BigEndian.Uint32(reply[5:9]) != wanted {
		return errors.New("frozen mountinfo SFTP status refused")
	}
	rest := reply[9:]
	for i := 0; i < 2; i++ {
		if len(rest) < 4 {
			return errors.New("frozen mountinfo SFTP status truncated")
		}
		n := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if n > uint32(len(rest)) {
			return errors.New("frozen mountinfo SFTP status malformed")
		}
		rest = rest[n:]
	}
	if len(rest) != 0 {
		return errors.New("frozen mountinfo SFTP status has trailing data")
	}
	return nil
}
