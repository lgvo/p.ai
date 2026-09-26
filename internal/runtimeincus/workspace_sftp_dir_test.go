package runtimeincus

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func sftpDirectoryName(id uint32, names ...string) []byte {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, uint32(len(names)))
	for _, name := range names {
		body = append(body, mountinfoString([]byte(name))...)
		body = append(body, mountinfoString([]byte("display value ignored"))...)
		attrs := make([]byte, 16)
		binary.BigEndian.PutUint32(attrs[:4], 6) // UID/GID and permissions
		binary.BigEndian.PutUint32(attrs[4:8], 1000)
		binary.BigEndian.PutUint32(attrs[8:12], 1000)
		binary.BigEndian.PutUint32(attrs[12:16], 0100644)
		body = append(body, attrs...)
	}
	return mountinfoPacket(104, id, body)
}

func TestBoundedSFTPDirectoryRequiresExactReadOnlySequence(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	serverResult := make(chan error, 1)
	go func() {
		open, err := readSFTPPacket(server)
		if err != nil {
			serverResult <- err
			return
		}
		if !bytes.Equal(open, sftpDirectoryRequest(11, 1, []byte("/workspace"))[4:]) {
			serverResult <- fmt.Errorf("OPENDIR path changed: %x", open)
			return
		}
		if err := writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h")))); err != nil {
			serverResult <- err
			return
		}
		for id, packet := range [][]byte{sftpDirectoryName(2, ".git", "tracked"), mountinfoStatus(3, 1)} {
			request, err := readSFTPPacket(server)
			if err != nil {
				serverResult <- err
				return
			}
			if !bytes.Equal(request, sftpDirectoryRequest(12, uint32(id+2), []byte("h"))[4:]) {
				serverResult <- fmt.Errorf("READDIR changed: %x", request)
				return
			}
			if err := writePacket(server, packet); err != nil {
				serverResult <- err
				return
			}
		}
		closeRequest, err := readSFTPPacket(server)
		if err != nil {
			serverResult <- err
			return
		}
		if !bytes.Equal(closeRequest, sftpDirectoryRequest(4, 4, []byte("h"))[4:]) {
			serverResult <- fmt.Errorf("CLOSE changed: %x", closeRequest)
			return
		}
		serverResult <- writePacket(server, mountinfoStatus(4, 0))
	}()
	got, err := readBoundedSFTPDirectory(client, client, "/workspace", 2)
	if err != nil || len(got) != 2 || got[0] != ".git" || got[1] != "tracked" {
		t.Fatalf("bounded directory result %q: %v", got, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestBoundedSFTPDirectoryRejectsPartialOrUnsafePages(t *testing.T) {
	unknownAttrs := sftpDirectoryName(2, "a")
	binary.BigEndian.PutUint32(unknownAttrs[len(unknownAttrs)-16:len(unknownAttrs)-12], 0x10)
	for _, scenario := range []struct {
		name    string
		packet  []byte
		maximum int
	}{
		{"more than allowance", sftpDirectoryName(2, "a", "b"), 1},
		{"duplicate names", sftpDirectoryName(2, "a", "a"), 2},
		{"escaped component", sftpDirectoryName(2, "../outside"), 2},
		{"empty page", sftpDirectoryName(2), 2},
		{"wrong id", sftpDirectoryName(3, "a"), 2},
		{"unknown attrs", unknownAttrs, 2},
		{"non-EOF status", mountinfoStatus(2, 4), 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			go func() {
				_, _ = readSFTPPacket(server)
				_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
				_, _ = readSFTPPacket(server)
				_ = writePacket(server, scenario.packet)
			}()
			if _, err := readBoundedSFTPDirectory(client, client, "/workspace", scenario.maximum); err == nil {
				t.Fatal("partial or unsafe directory page accepted")
			}
		})
	}
}

func TestBoundedSFTPDirectoryRequiresEOFAndClose(t *testing.T) {
	for _, closeCode := range []uint32{0, 4} {
		t.Run(fmt.Sprint(closeCode), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			go func() {
				_, _ = readSFTPPacket(server)
				_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
				_, _ = readSFTPPacket(server)
				_ = writePacket(server, sftpDirectoryName(2, "a"))
				_, _ = readSFTPPacket(server)
				_ = writePacket(server, mountinfoStatus(3, 1))
				_, _ = readSFTPPacket(server)
				if closeCode != 0 {
					_ = writePacket(server, mountinfoStatus(4, closeCode))
				}
			}()
			_, err := readBoundedSFTPDirectory(client, client, "/workspace", 1)
			if closeCode == 0 && err == nil || closeCode != 0 && (err == nil || !strings.Contains(err.Error(), "CLOSE")) {
				t.Fatalf("missing/refused CLOSE accepted: %v", err)
			}
		})
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	_ = server.SetDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_, _ = readSFTPPacket(server)
		_ = writePacket(server, mountinfoPacket(102, 1, mountinfoString([]byte("h"))))
		_, _ = readSFTPPacket(server)
		_ = writePacket(server, sftpDirectoryName(2, "a"))
		_ = server.Close() // No EOF: a one-entry page is never a complete scan.
	}()
	if _, err := readBoundedSFTPDirectory(client, client, "/workspace", 1); err == nil {
		t.Fatalf("missing EOF accepted: %v", err)
	}
}
