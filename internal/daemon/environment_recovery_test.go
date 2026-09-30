package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type retainedEnvironmentRuntime struct {
	native       runtimeincus.Session
	exists       bool
	present      bool
	inspectError error
	inspected    int
}

func (*retainedEnvironmentRuntime) ReconcileBuilderPublication(context.Context, runtimeincus.BuilderImageClaim) (runtimeincus.BuilderImageInfo, error) {
	return runtimeincus.BuilderImageInfo{}, errors.New("unexpected publication")
}
func (r *retainedEnvironmentRuntime) VerifyBuilderImage(context.Context, runtimeincus.BuilderImageClaim) (runtimeincus.BuilderImageInfo, bool, error) {
	return runtimeincus.BuilderImageInfo{}, r.present, nil
}
func (r *retainedEnvironmentRuntime) InspectCreated(_ context.Context, s runtimeincus.Session) (runtimeincus.Observation, error) {
	r.inspected++
	if !reflect.DeepEqual(s, r.native) {
		return runtimeincus.Observation{}, errors.New("retained native identity/devices differ from captured request")
	}
	return runtimeincus.Observation{Exists: r.exists, Status: "Stopped"}, r.inspectError
}

// This drives the production recovery branch with a recorded native identity,
// not a native crash run. Backend device validation has separate fixture coverage.
func TestEnvironmentRecoveryRetainsCapturedRuntimeDevices(t *testing.T) {
	for _, public := range []bool{false, true} {
		name := "filesystem"
		if public {
			name = "filesystem-and-public-nic"
		}
		t.Run(name, func(t *testing.T) {
			l, op, ev, native, cache := environmentRecoveryFixture(t, public)
			// Project configuration can change while the creating session retains its
			// immutable snapshot. A new grant is never adopted during exact recovery.
			changed := control.ProjectPolicy{Network: "none", Command: []string{"/bin/sh"}, FilesystemMounts: []control.FilesystemGrant{{Name: "other", Source: "/var/lib/p-grants/other", Type: "directory", Access: "read-write", SourceIdentity: &control.SourceIdentity{Device: 2, Inode: 3}}}}
			raw, _, err := control.ProjectPolicySnapshot(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err = l.store.SetProjectPolicy(l.ctx, op.Project, raw); err != nil {
				t.Fatal(err)
			}
			l.cfg.ProjectPolicies = map[string]control.ProjectPolicy{op.Project: changed}
			retained := &retainedEnvironmentRuntime{native: native, exists: true}
			got, err := l.ensureEnvironmentWithRuntime(l.ctx, &op, ev, retained, func(context.Context, *control.Operation, *control.CreationEvidence) (string, error) {
				t.Fatal("retained instance rebuilt")
				return "", nil
			})
			if err != nil || got != native.ImageFingerprint || retained.inspected != 1 {
				t.Fatalf("captured runtime refused: image=%s inspect=%d err=%v", got, retained.inspected, err)
			}
			if _, exists, err := l.store.GetEnvironmentImage(l.ctx, cache.Project, cache.Key); err != nil || !exists {
				t.Fatalf("retained instance evicted cache identity: %v %v", exists, err)
			}
		})
	}
}
func TestEnvironmentRecoveryMissingRuntimeRebuildsExactSource(t *testing.T) {
	l, op, ev, native, cache := environmentRecoveryFixture(t, true)
	absent := &retainedEnvironmentRuntime{native: native}
	calls := 0
	got, err := l.ensureEnvironmentWithRuntime(l.ctx, &op, ev, absent, func(ctx context.Context, saved *control.Operation, input *control.CreationEvidence) (string, error) {
		calls++
		if input.CapturedOID != native.InitialOID || input.ImageFingerprint != native.ImageFingerprint || saved.ID != op.ID {
			t.Fatal("rebuild changed exact creation input")
		}
		if _, exists, err := l.store.GetEnvironmentImage(ctx, cache.Project, cache.Key); err != nil || exists {
			t.Fatalf("verified missing image was not forgotten: %v %v", exists, err)
		}
		return "fixture-rebuilt", nil
	})
	if err != nil || got != "fixture-rebuilt" || calls != 1 || absent.inspected != 1 {
		t.Fatalf("absence did not rebuild: %s %d %v", got, calls, err)
	}
}
func TestEnvironmentRecoveryRefusesChangedIdentity(t *testing.T) {
	for _, change := range []string{"native-ownership", "image", "source", "policy-digest", "egress-substrate", "established"} {
		t.Run(change, func(t *testing.T) {
			l, op, ev, native, cache := environmentRecoveryFixture(t, true)
			retained := &retainedEnvironmentRuntime{native: native, exists: true}
			switch change {
			case "native-ownership":
				retained.inspectError = errors.New("Incus instance identity mismatch")
			case "image":
				ev.ImageFingerprint = strings.Repeat("9", 64)
				ev.EnvironmentState.Fingerprint = ev.ImageFingerprint
			case "source":
				ev.CapturedOID = strings.Repeat("9", 40)
			case "policy-digest":
				ev.PolicySHA256 = strings.Repeat("9", 64)
			case "egress-substrate":
				l.cfg.PublicEgress.ACL = "different"
			case "established":
				if err := l.store.CompleteCreation(l.ctx, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := l.ensureEnvironmentWithRuntime(l.ctx, &op, ev, retained, func(context.Context, *control.Operation, *control.CreationEvidence) (string, error) {
				t.Fatal("conflicting identity rebuilt")
				return "", nil
			})
			if err == nil {
				t.Fatal("changed identity accepted")
			}
			if _, exists, err := l.store.GetEnvironmentImage(l.ctx, cache.Project, cache.Key); err != nil || !exists {
				t.Fatalf("uncertain identity evicted cache: %v %v", exists, err)
			}
		})
	}
}
func environmentRecoveryFixture(t *testing.T, public bool) (*lifecycle, control.Operation, control.CreationEvidence, runtimeincus.Session, control.EnvironmentImage) {
	t.Helper()
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	instance, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	module := &plugin.Active{Config: json.RawMessage(`{}`), Package: plugin.Package{SHA256: strings.Repeat("e", 64), Manifest: plugin.Manifest{ID: "environment"}}}
	l := &lifecycle{ctx: ctx, store: store, instanceID: instance, environmentPlugin: module, cfg: control.RuntimeConfig{BaseImageFingerprint: strings.Repeat("a", 64), EndpointPrefix: "/var/lib/p-endpoints", Environment: &control.EnvironmentConfig{System: "x86_64-linux", BuilderStoragePool: "builders"}}}
	grant := control.FilesystemGrant{Name: "notice", Source: "/var/lib/p-grants/notice", Type: "file", Access: "read-only", SourceIdentity: &control.SourceIdentity{Device: 1, Inode: 2, OwnerUID: 1000, OwnerGID: 1000}}
	policy := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{grant}, Command: []string{"/bin/sh"}}
	if public {
		l.cfg.PublicEgress = &control.PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/example/bin/nft", BridgeProofBinary: "/nix/store/example/bin/p-public-network-proof"}
		policy.Network = "public-egress"
		policy.PublicEgressSHA256 = l.cfg.PublicEgress.SHA256()
	}
	raw, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", raw); err != nil {
		t.Fatal(err)
	}
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("1", 64), HostID: "host", HostSHA256: strings.Repeat("2", 64), SourceID: "source", SourceSHA256: strings.Repeat("3", 64)}
	op, s, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "recover", Project: "app", Branch: "work", Choice: "existing"}, l.cfg.BaseImageFingerprint, selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("4", 40), true, nil
	}, l.environmentIntent())
	if err != nil {
		t.Fatal(err)
	}
	ev, err := control.Evidence(op)
	if err != nil {
		t.Fatal(err)
	}
	cache := control.EnvironmentImage{Project: "app", Key: strings.Repeat("b", 64), Fingerprint: strings.Repeat("c", 64), BaseFingerprint: l.cfg.BaseImageFingerprint, System: "x86_64-linux", MaterialDigest: strings.Repeat("d", 64), CaptureStorePath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env", BuilderRequest: op.ID}
	cache.Properties = map[string]string{"p.contract": "p.incus-system-image/v2", "p.compression": "none", "p.instance": instance, "p.incus_project": "user-1000", "p.project_path": cache.Project, "p.environment_key": cache.Key, "p.base_image": cache.BaseFingerprint, "p.material": cache.MaterialDigest, "p.capture_store_path": cache.CaptureStorePath, "p.builder_request": cache.BuilderRequest, "p.system": cache.System}
	if err = store.PutEnvironmentImage(ctx, cache); err != nil {
		t.Fatal(err)
	}
	ev.ImageFingerprint = cache.Fingerprint
	ev.RuntimeInitState = "attempted"
	ev.EnvironmentState = &control.EnvironmentState{Key: cache.Key, Fingerprint: cache.Fingerprint, MaterialDigest: cache.MaterialDigest, CaptureStorePath: cache.CaptureStorePath, BuilderRequest: op.ID, Properties: cache.Properties}
	evidence, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, evidence, ""); err != nil {
		t.Fatal(err)
	}
	op, err = store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	native := runtimeincus.Session{InstanceUUID: instance, SessionUUID: s.UUID, ProjectPath: s.Project, AssignedBranch: s.Branch, InitialOID: strings.Repeat("4", 40), ContractVersion: "1", ImageFingerprint: cache.Fingerprint, EndpointSource: filepath.Join(l.cfg.EndpointPrefix, s.UUID), Grants: []runtimeincus.FilesystemGrant{{Name: "notice", Source: grant.Source, Type: "file", Access: "read-only", Device: 1, Inode: 2, OwnerUID: 1000, OwnerGID: 1000}}}
	if public {
		native.PublicIPv4 = "10.233.0.10/24"
	}
	return l, op, ev, native, cache
}
