package runtimeincus

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttachmentStoreModeUsesNativeIncusSFTP(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		mode         uint32
		uid          uint32
		flags        uint32
		versionExt   bool
		versionExtra bool
		timestamps   bool
		wrongID      bool
		wrongType    bool
		trailing     bool
		ok           bool
	}{
		{name: "sticky store", mode: 0041775, flags: 6, ok: true},
		{name: "version extensions", mode: 0041775, flags: 6, versionExt: true, ok: true},
		{name: "no sticky bit", mode: 0040775, flags: 6, ok: true},
		{name: "owner mismatch", mode: 0041775, uid: 1000, flags: 6},
		{name: "complete timestamps", mode: 0041775, flags: 14, timestamps: true, ok: true},
		{name: "missing timestamp fields", mode: 0041775, flags: 14},
		{name: "unknown attribute flags", mode: 0041775, flags: 0x10 | 6},
		{name: "trailing attribute bytes", mode: 0041775, flags: 6, trailing: true},
		{name: "malformed version", mode: 0041775, flags: 6, versionExtra: true},
		{name: "wrong response ID", mode: 0041775, flags: 6, wrongID: true},
		{name: "wrong response type", mode: 0041775, flags: 6, wrongType: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "incus.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				endpoint := "files"
				if r.Header.Get("Upgrade") == "sftp" {
					endpoint = "sftp"
				}
				if r.URL.Query().Get("project") != "test" || r.URL.Path != "/1.0/instances/p-test/"+endpoint {
					t.Error("wrong Incus scope")
				}
				if r.Header.Get("Upgrade") != "sftp" {
					if r.URL.Query().Get("path") != "/nix/store" || r.Method != http.MethodHead {
						t.Error("unexpected HTTP file request")
					}
					w.Header().Set("X-Incus-type", "directory")
					w.Header().Set("X-Incus-uid", "0")
					w.Header().Set("X-Incus-gid", "30000")
					w.Header().Set("X-Incus-mode", "0775")
					return
				}
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("upgrade unavailable")
					return
				}
				conn, reader, err := h.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: sftp\r\n\r\n")
				init, err := readSFTPPacket(reader)
				if err != nil || len(init) != 5 || init[0] != 1 {
					t.Error("invalid SFTP init")
					return
				}
				version := []byte{0, 0, 0, 5, 2, 0, 0, 0, 3}
				if scenario.versionExt {
					for _, field := range []string{"posix-rename@openssh.com", "1", "statvfs@openssh.com", "2"} {
						var size [4]byte
						binary.BigEndian.PutUint32(size[:], uint32(len(field)))
						version = append(version, size[:]...)
						version = append(version, field...)
					}
					binary.BigEndian.PutUint32(version[:4], uint32(len(version)-4))
				}
				if scenario.versionExtra {
					version = append(version, 0)
					version[3]++
				}
				if err := writePacket(conn, version); err != nil {
					t.Error(err)
					return
				}
				if scenario.versionExtra {
					return
				}
				request, err := readSFTPPacket(reader)
				if err != nil || len(request) < 9 || request[0] != 7 || string(request[9:]) != "/nix/store" {
					t.Error("unexpected SFTP request")
					return
				}
				attrs := make([]byte, 4+1+4+4+4+4+4)
				binary.BigEndian.PutUint32(attrs[:4], uint32(len(attrs)-4))
				attrs[4] = 105
				if scenario.wrongType {
					attrs[4] = 101
				}
				id := uint32(1)
				if scenario.wrongID {
					id = 2
				}
				binary.BigEndian.PutUint32(attrs[5:9], id)
				binary.BigEndian.PutUint32(attrs[9:13], scenario.flags)
				binary.BigEndian.PutUint32(attrs[13:17], scenario.uid)
				binary.BigEndian.PutUint32(attrs[17:21], 30000)
				binary.BigEndian.PutUint32(attrs[21:25], scenario.mode)
				if scenario.timestamps {
					attrs = append(attrs, make([]byte, 8)...)
					binary.BigEndian.PutUint32(attrs[:4], uint32(len(attrs)-4))
				}
				if scenario.trailing {
					attrs = append(attrs, 0)
					binary.BigEndian.PutUint32(attrs[:4], uint32(len(attrs)-4))
				}
				if err := writePacket(conn, attrs); err != nil {
					t.Error(err)
				}
			})}
			go server.Serve(listener)
			defer server.Close()
			f, exists, err := (attachmentFileAPI{&unixFileAPI{socket: socket, project: "test", instance: "p-test"}}).get(context.Background(), "/nix/store")
			if (err == nil && exists) != scenario.ok {
				t.Fatalf("metadata accepted=%v, err=%v", exists, err)
			}
			if scenario.ok && f.mode != int(scenario.mode&07777) {
				t.Fatalf("full mode lost: %04o", f.mode)
			}
		})
	}
}

func TestSFTPPacketBound(t *testing.T) {
	for _, packet := range [][]byte{{0, 0, 0, 4}, {0, 1, 0, 0}} {
		if _, err := readSFTPPacket(bytes.NewReader(packet)); err == nil {
			t.Fatal("accepted out-of-bound SFTP packet")
		}
	}
}

func TestSFTPUpgradeHeaderBound(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nX-Fill: " + strings.Repeat("x", 8<<10)))
	}()
	api := attachmentFileAPI{&unixFileAPI{socket: socket, project: "test", instance: "p-test"}}
	start := time.Now()
	if _, err := api.storeDirectoryMode(context.Background()); err == nil || time.Since(start) > time.Second {
		t.Fatalf("oversized upgrade header was not promptly refused: %v", err)
	}
}

func TestSFTPCancellationClosesEstablishedSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		var b [1]byte
		for {
			if _, e = conn.Read(b[:]); e != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	api := attachmentFileAPI{&unixFileAPI{socket: socket, project: "test", instance: "p-test"}}
	go func() { _, e := api.storeDirectoryMode(ctx); done <- e }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SFTP socket was not established")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("canceled SFTP request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled SFTP request did not close its socket")
	}
}
