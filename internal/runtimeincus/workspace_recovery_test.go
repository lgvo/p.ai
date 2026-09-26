package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceHelperBootTimeoutRetainsLastReadinessPredicate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitWorkspaceHelperBoot(ctx, func(context.Context) error {
		return errors.New("helper systemd target link unsafe")
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "helper systemd target link unsafe") ||
		!strings.Contains(err.Error(), "workspace helper boot readiness unresolved") {
		t.Fatalf("bounded helper readiness lost the actual failing predicate: %v", err)
	}
}

func TestWorkspaceInertTargetRequiresPinnedTwoLinkStoreChain(t *testing.T) {
	item := "/nix/store/" + strings.Repeat("a", 32) + "-p-host-unit-links"
	first := "/etc/systemd/system/p-session.target"
	second := item + "/etc/systemd/system/p-session.target"
	base := func() *fakeWorkspaceReader {
		f := &fakeWorkspaceReader{files: map[string]guestFile{}, links: map[string]string{}}
		f.files["/nix"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		f.files["/nix/store"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 01775}
		for _, dir := range []string{item, item + "/etc", item + "/etc/systemd", item + "/etc/systemd/system"} {
			f.files[dir] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0555}
		}
		f.files[first] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777}
		f.files[second] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777}
		f.links[first] = second
		f.links[second] = "/etc/p/assets/p-session.target"
		return f
	}
	if err := verifyInertWorkspaceTargetLinks(context.Background(), base()); err != nil {
		t.Fatalf("pinned immutable link chain refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*fakeWorkspaceReader)
	}{
		{"direct unpinned link", func(f *fakeWorkspaceReader) { f.links[first] = "/etc/p/assets/p-session.target" }},
		{"link loop", func(f *fakeWorkspaceReader) { f.links[second] = first }},
		{"foreign final target", func(f *fakeWorkspaceReader) { f.links[second] = "/etc/p/assets/p-interactive.service" }},
		{"writable store ancestor", func(f *fakeWorkspaceReader) {
			f.files[item+"/etc/systemd"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		}},
		{"store root symlink", func(f *fakeWorkspaceReader) {
			f.files["/nix/store"] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777}
		}},
		{"user-owned store link", func(f *fakeWorkspaceReader) {
			f.files[second] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.edit(f)
			if err := verifyInertWorkspaceTargetLinks(context.Background(), f); err == nil {
				t.Fatal("unsafe inert target link chain accepted")
			}
		})
	}
}

func TestWorkspaceHelperInitPinsAndRechecksEffectiveLimits(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	helper := WorkspaceHelper(testSession().InstanceUUID, "22222222-2222-4222-8222-222222222222", testUUID, "/source/repo.git", testImage)
	created := false
	changed := ""
	var initArgs []string
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		args := argv[3:]
		if len(args) > 0 && args[0] == "init" {
			initArgs = slices.Clone(args)
			created = true
			return nil, nil
		}
		if len(args) > 0 && args[0] == "list" {
			if !created {
				return []byte(`[]`), nil
			}
			config := map[string]string{
				"user.p.instance_uuid": helper.InstanceUUID, "user.p.session_uuid": helper.SessionUUID,
				"user.p.workspace_owner": helper.WorkspaceOwner, "user.p.project_path": helper.ProjectPath,
				"user.p.contract_version": helper.ContractVersion, "user.p.image_fingerprint": helper.ImageFingerprint,
				"volatile.base_image": helper.ImageFingerprint, "security.idmap.isolated": "true",
				"limits.cpu": workspaceHelperCPU, "limits.memory": workspaceHelperMemory, "limits.processes": workspaceHelperProcesses,
			}
			expanded := map[string]string{}
			for k, v := range config {
				expanded[k] = v
			}
			if changed == "local" {
				config["limits.memory"] = "unlimited"
			}
			if changed == "expanded" {
				expanded["limits.processes"] = "unlimited"
			}
			return json.Marshal([]instanceJSON{{Name: b.name(helper), Type: "container", Status: "Stopped",
				Config: config, ExpandedConfig: expanded, Devices: map[string]map[string]string{},
				ExpandedDevices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}, Profiles: []string{"default"}}})
		}
		return f.run(ctx, binary, argv, env)
	}
	marked := false
	if _, err := b.create(context.Background(), helper, func() error { marked = true; return nil }); err != nil || !marked {
		t.Fatalf("bounded helper creation refused: marked=%t err=%v", marked, err)
	}
	for _, required := range []string{"limits.cpu=" + workspaceHelperCPU, "limits.memory=" + workspaceHelperMemory, "limits.processes=" + workspaceHelperProcesses} {
		if !slices.Contains(initArgs, required) {
			t.Fatalf("helper init omitted %s: %v", required, initArgs)
		}
	}
	if _, err := b.InspectWorkspaceHelper(context.Background(), helper); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"local", "expanded"} {
		changed = side
		if _, err := b.InspectWorkspaceHelper(context.Background(), helper); err == nil {
			t.Fatalf("changed %s resource limit accepted", side)
		}
	}
}

func TestWorkspaceFreezeAttemptRemainsUnknownUntilExactFrozenState(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	marked := false
	pauseAttempted := false
	busyOperation := true
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "operation" {
			if busyOperation {
				return []byte(`[{"status_code":103,"resources":{"instances":["/1.0/instances/p-550e8400-e29b-41d4-a716-446655440000?project=user-1000"]}}]`), nil
			}
			return []byte(`[]`), nil // Incus may not have registered a delayed request yet.
		}
		if len(argv) > 3 && argv[3] == "pause" {
			if !marked {
				t.Fatal("pause sent before durable intent callback")
			}
			pauseAttempted = true
			return nil, errors.New("client connection dropped before daemon admission")
		}
		return f.run(ctx, binary, argv, env)
	}
	source := testSession()
	if err := b.FreezeWorkspaceSource(context.Background(), source, func() error { marked = true; return nil }); err == nil || marked || pauseAttempted {
		t.Fatalf("active exec did not prevent freeze before durable marker: marked=%t attempted=%t err=%v", marked, pauseAttempted, err)
	}
	busyOperation = false
	if err := b.FreezeWorkspaceSource(context.Background(), source, func() error { marked = true; return nil }); err == nil || !pauseAttempted {
		t.Fatalf("delayed pause not represented as uncertain: marked=%t attempted=%t err=%v", marked, pauseAttempted, err)
	}
	// Both the operation inventory and status can be unchanged while the
	// request waits before Incus OperationCreate. This must retain the guard.
	if err := b.ReconcileWorkspaceFreeze(context.Background(), source); err == nil {
		t.Fatal("unchanged Running state released uncertain freeze")
	}
	if slices.Contains(f.mutations, "resume") {
		t.Fatalf("source was resumed without proving a freeze: %v", f.mutations)
	}
	// Only an exact Frozen state after no active matching operation permits
	// recovery to continue with the same source identity.
	f.status = "Frozen"
	if err := b.ReconcileWorkspaceFreeze(context.Background(), source); err != nil {
		t.Fatalf("positive exact freeze was not accepted: %v", err)
	}
}

func TestWorkspaceExactThawRefusesReplacementBeforeResume(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Frozen"}
	b := fakeBackend(f)
	const oldUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const oldGeneration = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	currentUUID, currentGeneration := oldUUID, oldGeneration
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "list" {
			raw, err := f.run(ctx, binary, argv, env)
			if err != nil {
				return nil, err
			}
			var instances []instanceJSON
			if err := json.Unmarshal(raw, &instances); err != nil {
				return nil, err
			}
			for i := range instances {
				for _, c := range []map[string]string{instances[i].Config, instances[i].ExpandedConfig} {
					c["volatile.uuid"] = currentUUID
					c["volatile.uuid.generation"] = currentGeneration
				}
			}
			return json.Marshal(instances)
		}
		if len(argv) > 3 && argv[3] == "resume" {
			f.status = "Running"
			currentGeneration = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
			f.mutations = append(f.mutations, "resume")
			return nil, nil
		}
		return f.run(ctx, binary, argv, env)
	}
	currentUUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if err := b.ThawWorkspaceSourceExact(context.Background(), testSession(), oldUUID, oldGeneration); err == nil || slices.Contains(f.mutations, "resume") {
		t.Fatalf("replacement resumed: err=%v mutations=%v", err, f.mutations)
	}
	currentUUID = oldUUID
	if err := b.ThawWorkspaceSourceExact(context.Background(), testSession(), oldUUID, oldGeneration); err == nil || !slices.Contains(f.mutations, "resume") {
		t.Fatalf("generation changed during resume was accepted: err=%v mutations=%v", err, f.mutations)
	}
}

func TestWorkspaceHelperInitMissCannotAuthorizeDuplicateInit(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "operation" {
			return []byte(`[]`), nil
		}
		return f.run(ctx, binary, argv, env)
	}
	helper := WorkspaceHelper(testSession().InstanceUUID, "22222222-2222-4222-8222-222222222222", testUUID, "/source/repo.git", testImage)
	if _, err := b.ReconcileWorkspaceHelperInit(context.Background(), helper); err == nil {
		t.Fatal("two inventory misses treated as proof a delayed init cannot appear")
	}
	if len(f.mutations) != 0 {
		t.Fatalf("reconciliation issued a second mutation: %v", f.mutations)
	}
}

func TestWorkspaceInitMarkerIsAfterPreflightBeforeMutation(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	marked := false
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "init" {
			if !marked {
				t.Fatal("Incus init sent without durable intent")
			}
			return nil, errors.New("client connection dropped")
		}
		return f.run(ctx, binary, argv, env)
	}
	if _, err := b.create(context.Background(), testSession(), func() error { marked = true; return nil }); err == nil || !marked {
		t.Fatalf("attempted init not surfaced: marked=%t err=%v", marked, err)
	}
	marked = false
	if _, err := b.create(context.Background(), testSession(), func() error { marked = true; return errors.New("durable write refused") }); err == nil || marked == false {
		t.Fatalf("pre-command refusal not surfaced: marked=%t err=%v", marked, err)
	}
	if f.instance {
		t.Fatal("pre-command refusal created an instance")
	}
}
