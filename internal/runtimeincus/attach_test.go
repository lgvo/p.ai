package runtimeincus

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
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
	assets := []runtimedriver.Asset{testAsset("p-interactive.service", 0644, []byte("unit")), testAsset("p-attach", 0555, []byte("attach"))}
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
			err := verifyAttachAssets(context.Background(), &memoryFiles{files: files}, assets)
			if (err == nil) != (scenario == "valid" || scenario == "sticky Nix store") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
