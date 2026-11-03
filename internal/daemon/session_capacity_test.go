package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestCapacityAssociationExactSessionAndPendingBuilders(t *testing.T) {
	const project = "team/capacity"
	ids := []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"}
	ops := []string{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	base := strings.Repeat("a", 64)
	selected := strings.Repeat("b", 64)
	first, _ := json.Marshal(control.BlankProjectEvidence{ImageFingerprint: base})
	// Each creation's attempted build is its second cycle.
	creator := func(oid string) json.RawMessage {
		ev := control.CreationEvidence{CapturedOID: oid, ImageFingerprint: selected,
			Environment: &control.EnvironmentIntent{BaseFingerprint: base}, EnvironmentBuilder: &control.CreationBuilderState{Cycle: 2, State: "attempted"}}
		data, _ := json.Marshal(ev)
		return data
	}
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	reservations := []control.CapacityReservation{
		{SessionUUID: ids[0], Project: project, Policy: policy, Kind: "project.create", Evidence: first},
		{SessionUUID: ids[1], Project: project, Policy: policy, Kind: "session.create", OperationID: ops[0], Evidence: creator(strings.Repeat("d", 40))},
		{SessionUUID: ids[2], Project: project, Policy: policy, Kind: "session.create", OperationID: ops[1], Evidence: creator(strings.Repeat("e", 40))},
	}
	physical := runtimeincus.SessionCapacity{Limit: 4, Instances: []runtimeincus.SessionCapacityInstance{
		{Name: "p-" + ids[0]}, {Name: "p-builder-" + control.BuildRef(ops[0], 2)}, {Name: "p-builder-" + control.BuildRef(ops[1], 2)},
	}}
	inspectSession := func(_ context.Context, session runtimeincus.Session) (runtimeincus.Observation, error) {
		if session.SessionUUID != ids[0] || session.EndpointSource != filepath.Join("/run/p-endpoints", ids[0]) || session.ImageFingerprint != base {
			t.Fatalf("established session inspected without exact endpoint/image: %+v", session)
		}
		return runtimeincus.Observation{Exists: true}, nil
	}
	// The first builder's session holds a public address slot; the second's none.
	builderAddress := func(_ context.Context, session string) (string, error) {
		if session == ids[1] {
			return "10.233.0.11/24", nil
		}
		return "", nil
	}
	inspectBuilder := func(_ context.Context, builder runtimeincus.Builder) (runtimeincus.BuilderObservation, error) {
		if builder.RequestUUID != control.BuildRef(ops[0], 2) && builder.RequestUUID != control.BuildRef(ops[1], 2) || builder.BaseImageFingerprint != base || builder.ProjectPath != project || len(builder.CommitOID) != 40 {
			t.Fatalf("builder inspection lacks durable identity: %+v", builder)
		}
		if (builder.RequestUUID == control.BuildRef(ops[0], 2)) != (builder.PublicIPv4 == "10.233.0.11/24") {
			t.Fatalf("builder inspected without its session's address: %+v", builder)
		}
		return runtimeincus.BuilderObservation{Exists: true}, nil
	}
	got, err := associateSessionCapacity(context.Background(), reservations, physical, ids[0], "/run/p-endpoints", inspectSession, inspectBuilder, builderAddress)
	if err != nil || got.PhysicalCount != 3 || len(got.Occupied) != 3 {
		t.Fatalf("actual bootstrap and two builders did not cover three reservations: %+v %v", got, err)
	}
	// A same-name but wrong-label builder fails exact inspection. It still
	// consumes a physical slot while its session may yet need another.
	badBuilder := func(_ context.Context, builder runtimeincus.Builder) (runtimeincus.BuilderObservation, error) {
		if builder.RequestUUID == control.BuildRef(ops[1], 2) {
			return runtimeincus.BuilderObservation{}, nil
		}
		return runtimeincus.BuilderObservation{Exists: true}, nil
	}
	got, err = associateSessionCapacity(context.Background(), reservations, physical, ids[0], "/run/p-endpoints", inspectSession, badBuilder, builderAddress)
	if err != nil || got.PhysicalCount != 3 || got.Occupied[ids[2]] || len(got.Occupied) != 2 {
		t.Fatalf("unverified builder incorrectly covered reservation: %+v %v", got, err)
	}
	// A build settled as not accepted keeps its builder until its leftovers
	// are deleted, and that builder still holds its session's slot.
	abandoned := append([]control.CapacityReservation(nil), reservations...)
	settled := control.CreationEvidence{CapturedOID: strings.Repeat("e", 40), ImageFingerprint: selected,
		Environment: &control.EnvironmentIntent{BaseFingerprint: base}, EnvironmentBuilder: &control.CreationBuilderState{Cycle: 2, State: "abandoned"}}
	abandoned[2].Evidence, _ = json.Marshal(settled)
	got, err = associateSessionCapacity(context.Background(), abandoned, physical, ids[0], "/run/p-endpoints", inspectSession, inspectBuilder, builderAddress)
	if err != nil || got.PhysicalCount != 3 || !got.Occupied[ids[2]] || len(got.Occupied) != 3 {
		t.Fatalf("an abandoned build's builder did not cover its reservation: %+v %v", got, err)
	}
	// Only an attempted or abandoned cycle's build holds a builder: one named
	// for an ended cycle, or for the operation itself, is never this session's.
	ended := append([]control.CapacityReservation(nil), reservations...)
	ev := control.CreationEvidence{CapturedOID: strings.Repeat("e", 40), ImageFingerprint: selected,
		Environment: &control.EnvironmentIntent{BaseFingerprint: base}, EnvironmentBuilder: &control.CreationBuilderState{Cycle: 2, State: "absent"}}
	ended[2].Evidence, _ = json.Marshal(ev)
	stale := physical
	stale.Instances = append(append([]runtimeincus.SessionCapacityInstance(nil), physical.Instances...), runtimeincus.SessionCapacityInstance{Name: "p-builder-" + ops[1]})
	got, err = associateSessionCapacity(context.Background(), ended, stale, ids[0], "/run/p-endpoints", inspectSession, inspectBuilder, builderAddress)
	if err != nil || got.PhysicalCount != 4 || got.Occupied[ids[2]] || len(got.Occupied) != 2 {
		t.Fatalf("a builder of an ended cycle covered a reservation: %+v %v", got, err)
	}
}

func TestCapturedGrantProjectionAndCapacityIdentity(t *testing.T) {
	grant := control.FilesystemGrant{Name: "data", Source: "/var/lib/p-vm/grants/pdev/data", Type: "directory", Access: "read-only", SourceIdentity: &control.SourceIdentity{Device: 7, Inode: 11, OwnerUID: 1000, OwnerGID: 100}}
	policy := control.ProjectPolicy{Network: "none", Command: []string{"/bin/sh"}, FilesystemMounts: []control.FilesystemGrant{grant}}
	native := nativeFilesystemGrants(policy)
	guest := guestFilesystemGrants(policy)
	if len(native) != 1 || native[0].Source != grant.Source || native[0].Inode != 11 || len(guest) != 1 || guest[0].Inode != 11 || guest[0].Access != "read-only" {
		t.Fatalf("captured grant projections differ: %+v %+v", native, guest)
	}
	raw, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	const sid = "11111111-1111-1111-1111-111111111111"
	evidence, _ := json.Marshal(control.BlankProjectEvidence{ImageFingerprint: strings.Repeat("a", 64)})
	reservation := control.CapacityReservation{SessionUUID: sid, Project: "team/data", Kind: "project.create", Evidence: evidence, Policy: raw}
	physical := runtimeincus.SessionCapacity{Limit: 4, Instances: []runtimeincus.SessionCapacityInstance{{Name: "p-" + sid}}}
	got, err := associateSessionCapacity(context.Background(), []control.CapacityReservation{reservation}, physical, sid, "/run/p-endpoints",
		func(_ context.Context, s runtimeincus.Session) (runtimeincus.Observation, error) {
			if len(s.Grants) != 1 || s.Grants[0] != native[0] {
				t.Fatalf("capacity lost grant identity: %+v", s.Grants)
			}
			return runtimeincus.Observation{Exists: true}, nil
		}, func(context.Context, runtimeincus.Builder) (runtimeincus.BuilderObservation, error) {
			t.Fatal("unexpected builder")
			return runtimeincus.BuilderObservation{}, nil
		}, func(context.Context, string) (string, error) {
			t.Fatal("unexpected builder address")
			return "", nil
		})
	if err != nil || !got.Occupied[sid] {
		t.Fatalf("grant session double counted: %+v %v", got, err)
	}
}
