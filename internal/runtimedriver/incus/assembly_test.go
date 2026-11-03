package incus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
)

func TestStoppedAssemblyRequiresExactPrivateActivationMaterial(t *testing.T) {
	_, result := imageResultFixture(t)
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		t.Fatal(err)
	}
	makeFiles := func() *memoryImageFiles {
		files := map[string]guestFile{}
		for _, path := range []string{"/", "/etc", "/etc/p", nixenv.AttrsDir} {
			files[path] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		}
		for path, data := range map[string][]byte{
			builderActivationPath:              []byte(result.Material.Script),
			nixenv.AttrsDir + "/.attrs.sh":     []byte(result.Material.AttrsSH),
			nixenv.AttrsDir + "/.attrs.json":   []byte(result.Material.AttrsJSON),
			nixenv.AttrsDir + "/material.json": manifest,
		} {
			files[path] = guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: data}
		}
		return &memoryImageFiles{files: files}
	}
	if err := verifyStoppedDevShellImage(context.Background(), makeFiles(), result.MaterialDigest); err != nil {
		t.Fatalf("valid image material refused: %v", err)
	}
	for _, changed := range []struct {
		name string
		edit func(*memoryImageFiles)
	}{
		{"missing manifest", func(f *memoryImageFiles) { delete(f.files, nixenv.AttrsDir+"/material.json") }},
		{"tampered script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.data = []byte("bad")
			f.files[builderActivationPath] = v
		}},
		{"symlink ancestor", func(f *memoryImageFiles) {
			v := f.files[nixenv.AttrsDir]
			v.typ = "symlink"
			f.files[nixenv.AttrsDir] = v
		}},
		{"symlink script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.typ = "symlink"
			f.files[builderActivationPath] = v
		}},
		{"writable script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.mode = 0666
			f.files[builderActivationPath] = v
		}},
	} {
		t.Run(changed.name, func(t *testing.T) {
			files := makeFiles()
			changed.edit(files)
			if err := verifyStoppedDevShellImage(context.Background(), files, result.MaterialDigest); err == nil {
				t.Fatal("unsafe image material accepted")
			}
		})
	}
}

type memoryFiles struct {
	files  map[string]guestFile
	fail   error
	writes int
}

func (m *memoryFiles) get(_ context.Context, path string) (guestFile, bool, error) {
	if m.fail != nil {
		return guestFile{}, false, m.fail
	}
	f, ok := m.files[path]
	return f, ok, nil
}
func (m *memoryFiles) put(_ context.Context, path string, f guestFile) error {
	m.writes++
	m.files[path] = f
	return nil
}

func TestGuestInstallDistinguishesMissingFromErrorsAndMutation(t *testing.T) {
	ctx := context.Background()
	want := guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("expected")}
	m := &memoryFiles{files: map[string]guestFile{}}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err != nil || m.writes != 1 {
		t.Fatalf("install: %v writes=%d", err, m.writes)
	}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err != nil || m.writes != 1 {
		t.Fatalf("idempotent: %v writes=%d", err, m.writes)
	}
	m.files["/etc/p/session.json"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("expected")}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err == nil || m.writes != 1 {
		t.Fatalf("owner mutation accepted: %v", err)
	}
	m.fail = errors.New("permission denied")
	delete(m.files, "/etc/p/session.json")
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err == nil || !strings.Contains(err.Error(), "permission denied") || m.writes != 1 {
		t.Fatalf("transport error became absence: %v", err)
	}
}

func TestGuestEtcBootstrapAndUnsafeExistingState(t *testing.T) {
	ctx := context.Background()
	root := guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	for _, tc := range []struct {
		name       string
		files      map[string]guestFile
		fail       error
		wantWrites int
		wantError  string
	}{
		{"fresh image", map[string]guestFile{"/": root}, nil, 1, ""},
		{"safe existing etc", map[string]guestFile{"/etc": {typ: "directory", uid: 0, gid: 0, mode: 0555}}, nil, 0, ""},
		{"missing image root", map[string]guestFile{}, nil, 0, "observed absent"},
		{"unsafe image root", map[string]guestFile{"/": {typ: "directory", uid: 0, gid: 0, mode: 0777}}, nil, 0, "mode=0777"},
		{"existing symlink", map[string]guestFile{"/etc": {typ: "symlink", uid: 0, gid: 0, mode: 0777}}, nil, 0, "type=symlink"},
		{"existing wrong owner", map[string]guestFile{"/etc": {typ: "directory", uid: 1000, gid: 0, mode: 0755}}, nil, 0, "uid=1000"},
		{"existing writable", map[string]guestFile{"/etc": {typ: "directory", uid: 0, gid: 0, mode: 0775}}, nil, 0, "mode=0775"},
		{"inspection failure", map[string]guestFile{"/": root}, errors.New("permission denied"), 0, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &memoryFiles{files: tc.files, fail: tc.fail}
			err := ensureGuestEtc(ctx, m)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("ensureGuestEtc() = %v, want error containing %q", err, tc.wantError)
			}
			if m.writes != tc.wantWrites {
				t.Fatalf("writes = %d, want %d", m.writes, tc.wantWrites)
			}
			if tc.wantWrites == 1 {
				got := m.files["/etc"]
				if got.typ != "directory" || got.uid != 0 || got.gid != 0 || got.mode != 0755 {
					t.Fatalf("created /etc = %#v", got)
				}
			}
		})
	}
}

func TestIncusFileAPIRequiresExplicit404ForAbsence(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0/instances/p-session/files" || r.URL.Query().Get("project") != "confined" || r.URL.Query().Get("path") != "/etc/p/session.json" {
			t.Errorf("unscoped file request: %s", r.URL)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(int(status.Load()))
	})}
	go server.Serve(listener)
	defer server.Close()
	api := &unixFileAPI{socket: socket, project: "confined", instance: "p-session"}
	_, exists, err := api.get(context.Background(), "/etc/p/session.json")
	if err != nil || exists {
		t.Fatalf("404: exists=%t err=%v", exists, err)
	}
	status.Store(http.StatusForbidden)
	_, exists, err = api.get(context.Background(), "/etc/p/session.json")
	if err == nil || exists {
		t.Fatalf("403 treated as absence: exists=%t err=%v", exists, err)
	}
}

func TestDanglingGuestSymlinkCannotBecomeMissingFile(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("X-Incus-type", "symlink")
			w.Header().Set("X-Incus-uid", "0")
			w.Header().Set("X-Incus-gid", "0")
			w.Header().Set("X-Incus-mode", "0777")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	api := &unixFileAPI{socket: socket, project: "confined", instance: "p-session"}
	err = ensureGuestFile(context.Background(), api, "/etc/p/session.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("trusted")})
	if err == nil || !strings.Contains(err.Error(), "differs from trusted assembly") || posts.Load() != 0 {
		t.Fatalf("dangling symlink overwritten: err=%v posts=%d", err, posts.Load())
	}
}
