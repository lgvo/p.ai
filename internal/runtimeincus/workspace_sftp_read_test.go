package runtimeincus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func mountinfoPacket(typ byte, id uint32, body []byte) []byte {
	packet := make([]byte, 4+1+4+len(body))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(packet)-4))
	packet[4] = typ
	binary.BigEndian.PutUint32(packet[5:9], id)
	copy(packet[9:], body)
	return packet
}

func mountinfoString(value []byte) []byte {
	result := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(result[:4], uint32(len(value)))
	copy(result[4:], value)
	return result
}

func mountinfoStatus(id, code uint32) []byte {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, code)
	body = append(body, mountinfoString(nil)...)
	body = append(body, mountinfoString(nil)...)
	return mountinfoPacket(101, id, body)
}

func TestFixedMountinfoSFTPReadsZeroStatSizeContentThroughEOF(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	want := []byte("25 1 0:22 / /proc rw - proc proc rw\n")
	serverResult := make(chan error, 1)
	go func() {
		open, err := readSFTPPacket(server)
		if err != nil {
			serverResult <- err
			return
		}
		name := frozenMountinfoPath
		if len(open) != 17+len(name) || open[0] != 3 || binary.BigEndian.Uint32(open[1:5]) != 1 || binary.BigEndian.Uint32(open[5:9]) != uint32(len(name)) || string(open[9:9+len(name)]) != name || binary.BigEndian.Uint32(open[9+len(name):13+len(name)]) != 1 || binary.BigEndian.Uint32(open[13+len(name):]) != 0 {
			serverResult <- fmt.Errorf("OPEN was not fixed read-only path: %x", open)
			return
		}
		handle := []byte("h")
		if err := writePacket(server, mountinfoPacket(102, 1, mountinfoString(handle))); err != nil {
			serverResult <- err
			return
		}
		for id, part := range [][]byte{want[:12], want[12:]} {
			read, err := readSFTPPacket(server)
			if err != nil {
				serverResult <- err
				return
			}
			if len(read) != 22 || read[0] != 5 || binary.BigEndian.Uint32(read[1:5]) != uint32(id+2) || binary.BigEndian.Uint32(read[5:9]) != 1 || read[9] != 'h' || binary.BigEndian.Uint64(read[10:18]) != uint64(len(want[:12])*id) || binary.BigEndian.Uint32(read[18:22]) != frozenMountinfoChunk {
				serverResult <- fmt.Errorf("READ offset or bound invalid: %x", read)
				return
			}
			if err := writePacket(server, mountinfoPacket(103, uint32(id+2), mountinfoString(part))); err != nil {
				serverResult <- err
				return
			}
		}
		read, err := readSFTPPacket(server)
		if err != nil {
			serverResult <- err
			return
		}
		if read[0] != 5 || binary.BigEndian.Uint32(read[1:5]) != 4 || binary.BigEndian.Uint64(read[10:18]) != uint64(len(want)) {
			serverResult <- errors.New("EOF read offset invalid")
			return
		}
		if err := writePacket(server, mountinfoStatus(4, 1)); err != nil {
			serverResult <- err
			return
		}
		closeReq, err := readSFTPPacket(server)
		if err != nil {
			serverResult <- err
			return
		}
		if !bytes.Equal(closeReq, mountinfoPacket(4, 5, mountinfoString(handle))[4:]) {
			serverResult <- fmt.Errorf("CLOSE request invalid: %x", closeReq)
			return
		}
		serverResult <- writePacket(server, mountinfoStatus(5, 0))
	}()
	got, err := readFixedMountinfoSFTP(client, client)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read fixed zero-stat-size procfs content: %q, %v", got, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestFixedMountinfoSFTPRejectsMalformedResponses(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		response []byte
	}{
		{"wrong read id", mountinfoPacket(103, 3, mountinfoString([]byte("x")))},
		{"zero data", mountinfoPacket(103, 2, mountinfoString(nil))},
		{"oversized data", mountinfoPacket(103, 2, mountinfoString(bytes.Repeat([]byte{'x'}, frozenMountinfoChunk+1)))},
		{"truncated data", mountinfoPacket(103, 2, append(mountinfoString([]byte("x")), 'z'))},
		{"wrong status", mountinfoStatus(2, 4)},
		{"malformed EOF", mountinfoPacket(101, 2, []byte{0, 0, 0, 1})},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			go func() {
				_, _ = readSFTPPacket(server) // OPEN
				_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
				_, _ = readSFTPPacket(server) // READ
				_ = writePacket(server, scenario.response)
			}()
			if _, err := readFixedMountinfoSFTP(client, client); err == nil {
				t.Fatal("malformed SFTP response accepted")
			}
		})
	}
}

func TestFixedMountinfoSFTPRequiresBoundedEOF(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	go func() {
		_, _ = readSFTPPacket(server) // OPEN
		_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
		for id := uint32(2); id < 2+frozenMountinfoMax/frozenMountinfoChunk; id++ {
			if _, err := readSFTPPacket(server); err != nil {
				return
			}
			_ = writePacket(server, mountinfoPacket(103, id, mountinfoString(bytes.Repeat([]byte{'x'}, frozenMountinfoChunk))))
		}
		// Exactly 256 KiB without a real EOF is not a completed read.
		_ = server.Close()
	}()
	if _, err := readFixedMountinfoSFTP(client, client); err == nil {
		t.Fatalf("full-limit stream without EOF accepted: %v", err)
	}
}

func TestFixedMountinfoSFTPRejectsOversizeAndRefusedClose(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		oversize bool
	}{
		{"oversize", true},
		{"close refused", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			go func() {
				_, _ = readSFTPPacket(server) // OPEN
				_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
				if scenario.oversize {
					for id := uint32(2); id <= 2+frozenMountinfoMax/frozenMountinfoChunk; id++ {
						if _, err := readSFTPPacket(server); err != nil {
							return
						}
						_ = writePacket(server, mountinfoPacket(103, id, mountinfoString(bytes.Repeat([]byte{'x'}, frozenMountinfoChunk))))
					}
					return
				}
				_, _ = readSFTPPacket(server) // READ
				_ = writePacket(server, mountinfoStatus(2, 1))
				_, _ = readSFTPPacket(server) // CLOSE
				_ = writePacket(server, mountinfoStatus(3, 4))
			}()
			if _, err := readFixedMountinfoSFTP(client, client); err == nil || (scenario.oversize && !strings.Contains(err.Error(), "bound")) {
				t.Fatalf("oversize or refused CLOSE accepted: %v", err)
			}
		})
	}
}
