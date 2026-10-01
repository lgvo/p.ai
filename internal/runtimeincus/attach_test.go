package runtimeincus

import (
	"context"
	"encoding/json"
	"github.com/lgvo/p.ai/internal/plugin"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func TestFixedAttachmentAssetsResolveTrustedImageLinks(t *testing.T) {
	base := map[string]guestFile{}
	for _, p := range []string{"/", "/etc", "/etc/p", "/etc/p/assets", "/etc/systemd", "/usr", "/usr/libexec", "/usr/libexec/p", "/nix", "/nix/store", "/nix/store/unit-tree"} {
		base[p] = guestFile{typ: "directory", mode: 0755}
	}
	base["/etc/systemd/system"] = guestFile{typ: "symlink", data: []byte("/nix/store/unit-tree")}
	base["/nix/store/unit-tree/p-interactive.service"] = guestFile{typ: "symlink", data: []byte("/etc/p/assets/p-interactive.service")}
	base["/usr/libexec/p/attach"] = guestFile{typ: "symlink", data: []byte("/etc/p/assets/p-attach")}
	base["/etc/p/assets/p-interactive.service"] = guestFile{typ: "file", mode: 0644, data: []byte("unit")}
	base["/etc/p/assets/p-attach"] = guestFile{typ: "file", mode: 0555, data: []byte("attach")}
	plan := plugin.AssetPlan{Files: []plugin.AssetFile{{Role: "p-interactive.service", Destination: "/etc/systemd/system/p-interactive.service", Mode: 0644, Data: []byte("unit")}, {Role: "p-attach", Destination: "/usr/libexec/p/attach", Mode: 0555, Data: []byte("attach")}}}
	for _, scenario := range []string{"valid", "sticky Nix store", "writable Nix store", "changed bytes", "writable parent", "foreign owner", "redirect", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			files := map[string]guestFile{}
			for p, v := range base {
				files[p] = v
			}
			switch scenario {
			case "sticky Nix store":
				files["/nix/store"] = guestFile{typ: "directory", mode: 01775}
			case "writable Nix store":
				files["/nix/store"] = guestFile{typ: "directory", mode: 0775}
			case "changed bytes":
				f := files["/etc/p/assets/p-attach"]
				f.data = []byte("other")
				files["/etc/p/assets/p-attach"] = f
			case "writable parent":
				files["/etc/p"] = guestFile{typ: "directory", mode: 0777}
			case "foreign owner":
				f := files["/usr/libexec/p/attach"]
				f.uid = 1000
				files["/usr/libexec/p/attach"] = f
			case "redirect":
				files["/usr/libexec/p/attach"] = guestFile{typ: "symlink", data: []byte("/etc/p/assets/p-interactive.service")}
			case "cycle":
				files["/usr/libexec/p/attach"] = guestFile{typ: "symlink", data: []byte("/usr/libexec/p/attach")}
			}
			err := verifyAttachAssets(context.Background(), &memoryFiles{files: files}, plan)
			if (err == nil) != (scenario == "valid" || scenario == "sticky Nix store") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}

func TestAttachmentPromotionRequiresRunningFixedNativeOperation(t *testing.T) {
	for _, scenario := range []string{"valid", "ended", "other runtime", "other project", "arbitrary command", "noninteractive", "wrong class"} {
		t.Run(scenario, func(t *testing.T) {
			code := 103
			instance := "/1.0/instances/p-" + testUUID + "?project=test"
			command := "/usr/libexec/p/attach"
			interactive := true
			class := "websocket"
			switch scenario {
			case "ended":
				code = 200
			case "other runtime":
				instance = "/1.0/instances/other?project=test"
			case "other project":
				instance = "/1.0/instances/p-" + testUUID + "?project=other"
			case "arbitrary command":
				command = "/bin/sh"
			case "noninteractive":
				interactive = false
			case "wrong class":
				class = "task"
			}
			socket := filepath.Join(t.TempDir(), "incus.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("project") != "test" {
					t.Error("project scope missing")
				}
				json.NewEncoder(w).Encode(map[string]any{"type": "sync", "metadata": map[string]any{"class": class, "status_code": code, "resources": map[string][]string{"instances": {instance}}, "metadata": map[string]any{"command": []string{command}, "interactive": interactive}}})
			})}
			go server.Serve(listener)
			defer server.Close()
			b := &Backend{config: Config{UserSocket: socket, Project: "test"}}
			err = b.ValidateAttachmentOperation(context.Background(), testSession(), "/1.0/operations/11111111-1111-1111-1111-111111111111")
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
