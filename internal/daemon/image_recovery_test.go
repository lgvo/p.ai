package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type retainedRuntime struct {
	native       runtimeincus.Session
	exists       bool
	inspectError error
	inspected    int
}

func (r *retainedRuntime) InspectCreated(_ context.Context, s runtimeincus.Session) (runtimeincus.Observation, error) {
	r.inspected++
	if !reflect.DeepEqual(s, r.native) {
		return runtimeincus.Observation{}, errors.New("retained native identity/devices differ from captured request")
	}
	return runtimeincus.Observation{Exists: r.exists, Status: "Stopped"}, r.inspectError
}

// This drives the production recovery branch with a recorded native identity,
// not a native crash run. Backend device validation has separate fixture coverage.
func TestImageRecoveryRetainsCapturedRuntimeDevices(t *testing.T) {
	for _, public := range []bool{false, true} {
		name := "filesystem"
		if public {
			name = "filesystem-and-public-nic"
		}
		t.Run(name, func(t *testing.T) {
			l, op, ev, native, row := imageRecoveryFixture(t, public)
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
			retained := &retainedRuntime{native: native, exists: true}
			got, err := l.ensureImageWith(l.ctx, &op, ev, retained)
			if err != nil || got != native.ImageFingerprint || retained.inspected != 1 {
				t.Fatalf("captured runtime refused: image=%s inspect=%d err=%v", got, retained.inspected, err)
			}
			if _, exists, err := l.store.GetCommitImage(l.ctx, row.Key); err != nil || !exists {
				t.Fatalf("retained instance forgot its image: %v %v", exists, err)
			}
		})
	}
}

func TestImageRecoveryRefusesChangedIdentity(t *testing.T) {
	for _, change := range []string{"native-ownership", "image", "source", "policy-digest", "egress-substrate", "established"} {
		t.Run(change, func(t *testing.T) {
			l, op, ev, native, row := imageRecoveryFixture(t, true)
			retained := &retainedRuntime{native: native, exists: true}
			switch change {
			case "native-ownership":
				retained.inspectError = errors.New("Incus instance identity mismatch")
			case "image":
				ev.ImageFingerprint = strings.Repeat("9", 64)
				ev.Image.Ref = ev.ImageFingerprint
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
			if _, err := l.ensureImageWith(l.ctx, &op, ev, retained); err == nil {
				t.Fatal("changed identity accepted")
			}
			if _, exists, err := l.store.GetCommitImage(l.ctx, row.Key); err != nil || !exists {
				t.Fatalf("uncertain identity forgot the image: %v %v", exists, err)
			}
		})
	}
}

// imageRecoveryFixture is a creation past image-ready, before runtime-created,
// whose accepted image is gone from the driver. It has no P repository, so a
// rebuild cannot start.
func imageRecoveryFixture(t *testing.T, public bool) (*lifecycle, api.Operation, control.CreationEvidence, runtimeincus.Session, control.CommitImage) {
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
	l := &lifecycle{ctx: ctx, store: store, instanceID: instance, images: newFakeImages(), cfg: control.RuntimeConfig{BaseImageFingerprint: strings.Repeat("a", 64), EndpointPrefix: "/var/lib/p-endpoints", Environment: &control.EnvironmentConfig{System: "x86_64-linux", BuilderStoragePool: "builders"}}}
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
	op, s, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "recover", Project: "app", Branch: "work", Choice: "existing"}, l.cfg.BaseImageFingerprint, imageTestSelection(), func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("4", 40), true, nil
	}, l.environmentIntent())
	if err != nil {
		t.Fatal(err)
	}
	ev, err := control.Evidence(op)
	if err != nil {
		t.Fatal(err)
	}
	key := commitImageKey("app", ev)
	build := control.BuildRef(op.ID, 1)
	row := control.CommitImage{Key: key, Ref: strings.Repeat("c", 64), Build: build, Labels: key.Labels(build), Selection: "devshell", MaterialSHA256: strings.Repeat("d", 64), Size: 1 << 30}
	if err = store.PutCommitImage(ctx, row); err != nil {
		t.Fatal(err)
	}
	ev.ImageFingerprint = row.Ref
	ev.Image = &control.CreationImage{Ref: row.Ref, Built: true, Selection: row.Selection, MaterialSHA256: row.MaterialSHA256}
	ev.RuntimeInitState = "attempted"
	for _, state := range []string{"attempted", "absent"} {
		ev.EnvironmentBuilder = &control.CreationBuilderState{Cycle: 1, State: state}
		evidence, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, evidence, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	op, err = store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ = store.GetCommitImage(ctx, key)
	native := runtimeincus.Session{InstanceUUID: instance, SessionUUID: s.UUID, ProjectPath: s.Project, AssignedBranch: s.Branch, InitialOID: strings.Repeat("4", 40), ContractVersion: "1", ImageFingerprint: row.Ref, EndpointSource: filepath.Join(l.cfg.EndpointPrefix, s.UUID), Grants: []runtimeincus.FilesystemGrant{{Name: "notice", Source: grant.Source, Type: "file", Access: "read-only", Device: 1, Inode: 2, OwnerUID: 1000, OwnerGID: 1000}}}
	if public {
		native.PublicIPv4 = "10.233.0.10/24"
	}
	return l, op, ev, native, row
}

// With host public egress, a builder of a session without a public address
// holds a slot of its own until the session's image is ready.
func TestBuilderAddressSlotIsHeldOnlyUntilTheImageIsReady(t *testing.T) {
	l, op, ev, native, _ := imageRecoveryFixture(t, false)
	if address, err := l.reserveBuilderPublicIPv4(l.ctx, op.SessionUUID); err != nil || address != "" {
		t.Fatalf("builder got a NIC without host public egress: %q %v", address, err)
	}
	l.cfg.PublicEgress = &control.PublicEgressConfig{Network: "p-public-v1", ACL: "p-public-v1-acl", BridgeIPv4: "10.233.0.1/24", DNS: []string{"1.1.1.1", "9.9.9.9"}, SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/nix/store/example/bin/nft", BridgeProofBinary: "/nix/store/example/bin/p-public-network-proof"}
	if _, err := l.ensureImageWith(l.ctx, &op, ev, &retainedRuntime{native: native, exists: true}); err == nil {
		t.Fatal("creation recorded without public egress resumed with public builders")
	}
	ev.Environment = l.environmentIntent()
	for range 2 {
		if address, err := l.reserveBuilderPublicIPv4(l.ctx, op.SessionUUID); err != nil || address != "10.233.0.10/24" {
			t.Fatalf("builder address: %q %v", address, err)
		}
	}
	failing := &retainedRuntime{native: native, exists: true, inspectError: errors.New("Incus instance identity mismatch")}
	if _, err := l.ensureImageWith(l.ctx, &op, ev, failing); err == nil {
		t.Fatal("changed identity accepted")
	}
	if address, err := l.builderPublicIPv4(l.ctx, op.SessionUUID); err != nil || address != "10.233.0.10/24" {
		t.Fatalf("builder address released before the image was ready: %q %v", address, err)
	}
	if _, err := l.ensureImageWith(l.ctx, &op, ev, &retainedRuntime{native: native, exists: true}); err != nil {
		t.Fatal(err)
	}
	if address, err := l.builderPublicIPv4(l.ctx, op.SessionUUID); err != nil || address != "" {
		t.Fatalf("builder address kept after the image was ready: %q %v", address, err)
	}
}
