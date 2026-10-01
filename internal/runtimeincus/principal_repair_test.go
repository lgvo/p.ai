package runtimeincus

import (
	"context"
	"errors"
	"testing"
)

type principalFilesFixture struct {
	files  map[string]guestFile
	puts   int
	putErr error
}

func (f *principalFilesFixture) lstat(_ context.Context, path string) (guestFile, error) {
	v, ok := f.files[path]
	if !ok {
		return guestFile{}, errors.New("missing directory")
	}
	return v, nil
}
func (f *principalFilesFixture) lstatOptional(_ context.Context, path string) (guestFile, bool, error) {
	v, ok := f.files[path]
	return v, ok, nil
}
func (f *principalFilesFixture) getBounded(_ context.Context, path string, _ int64) (guestFile, bool, error) {
	v, ok := f.files[path]
	return v, ok, nil
}
func (f *principalFilesFixture) put(_ context.Context, path string, file guestFile) error {
	f.puts++
	f.files[path] = file
	return f.putErr
}
func principalFiles() *principalFilesFixture {
	f := &principalFilesFixture{files: map[string]guestFile{}}
	for _, path := range []string{"/", "/etc", "/etc/p", "/etc/p/git"} {
		f.files[path] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	}
	f.files[sessionIdentityPath] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0400, data: []byte("old")}
	return f
}

func TestStoppedPrincipalRepairChecksClosedPathAndSingleAttempt(t *testing.T) {
	ctx := context.Background()
	key := []byte("new-private-key")
	for _, bad := range []struct {
		name string
		edit func(*principalFilesFixture)
	}{
		{"symlink ancestor", func(f *principalFilesFixture) {
			f.files["/etc/p/git"] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0755}
		}},
		{"writable parent", func(f *principalFilesFixture) {
			f.files["/etc/p/git"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0777}
		}},
		{"symlink identity", func(f *principalFilesFixture) {
			f.files[sessionIdentityPath] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0400}
		}},
		{"wrong owner", func(f *principalFilesFixture) {
			f.files[sessionIdentityPath] = guestFile{typ: "file", uid: 0, gid: 0, mode: 0400}
		}},
	} {
		f := principalFiles()
		bad.edit(f)
		called := 0
		if err := installStoppedSessionIdentityFiles(ctx, f, key, func() error { called++; return nil }); err == nil || called != 0 || f.puts != 0 {
			t.Fatalf("%s reached write: calls=%d puts=%d err=%v", bad.name, called, f.puts, err)
		}
	}
	f := principalFiles()
	issued := false
	f.putErr = errors.New("native result unknown after write")
	if err := installStoppedSessionIdentityFiles(ctx, f, key, func() error { issued = true; return nil }); err == nil || !issued || f.puts != 1 {
		t.Fatalf("uncertain write not durably preceded by marker: issued=%v puts=%d err=%v", issued, f.puts, err)
	}
	if err := verifiedSessionIdentity(ctx, f, key); err != nil {
		t.Fatalf("exact positive readback could not reconcile issued write: %v", err)
	}
	if err := verifiedSessionIdentity(ctx, f, []byte("other")); err == nil {
		t.Fatal("wrong replacement identity accepted")
	}
}

func TestStoppedPrincipalRepairRefusesSameNamedReplacement(t *testing.T) {
	name := "p-550e8400-e29b-41d4-a716-446655440000"
	image := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uuid := "11111111-1111-4111-8111-111111111111"
	generation := "22222222-2222-4222-8222-222222222222"
	exact := Observation{Exists: true, Name: name, Status: "Stopped", IncusUUID: uuid,
		Generation: generation, Fingerprint: image, EndpointMounted: true}
	if !stoppedSessionIdentityMatches(exact, name, image, uuid, generation) {
		t.Fatal("exact stopped runtime refused")
	}
	for _, change := range []func(*Observation){
		func(o *Observation) { o.IncusUUID = "33333333-3333-4333-8333-333333333333" },
		func(o *Observation) { o.Generation = "44444444-4444-4444-8444-444444444444" },
		func(o *Observation) { o.Status = "Running" },
		func(o *Observation) { o.EndpointMounted = false },
		func(o *Observation) {
			o.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
	} {
		got := exact
		change(&got)
		if stoppedSessionIdentityMatches(got, name, image, uuid, generation) {
			t.Fatalf("changed runtime accepted: %+v", got)
		}
	}
}
