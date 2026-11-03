package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

// capacityPolicy is a stored project policy without a network, with the
// given grants.
func capacityPolicy(t *testing.T, grants ...control.FilesystemGrant) (control.ProjectPolicy, json.RawMessage) {
	t.Helper()
	policy := control.ProjectPolicy{Network: "none", FilesystemMounts: append([]control.FilesystemGrant{}, grants...), Command: []string{"/bin/sh"}}
	raw, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	return policy, raw
}

// bootstrapReservation is a project creation's session, created from the
// base image.
func (p *proofWorld) bootstrapReservation(uuid string, policy json.RawMessage) control.CapacityReservation {
	raw, _ := json.Marshal(control.BlankProjectEvidence{ImageFingerprint: string(p.world.BaseImage())})
	return control.CapacityReservation{SessionUUID: uuid, Project: "app", Policy: policy, Kind: "project.create", Evidence: raw}
}

// creatingReservation is a session creation of operation op whose second
// build cycle, of commit, is in state.
func (p *proofWorld) creatingReservation(uuid, op, commit, state string, policy json.RawMessage) control.CapacityReservation {
	base := string(p.world.BaseImage())
	raw, _ := json.Marshal(control.CreationEvidence{CapturedOID: commit, ImageFingerprint: base,
		Environment: &control.EnvironmentIntent{BaseFingerprint: base}, EnvironmentBuilder: &control.CreationBuilderState{Cycle: 2, State: state}})
	return control.CapacityReservation{SessionUUID: uuid, Project: "app", Policy: policy, Kind: "session.create", OperationID: op, Evidence: raw}
}

var (
	capacityIDs = []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
	capacityOps = []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	commitD     = strings.Repeat("d", 40)
	commitE     = strings.Repeat("e", 40)
)

func TestCapacityCountsEveryObjectAndLetsAReservationsRuntimeOrBuilderHoldItsSlot(t *testing.T) {
	p := newProofWorld(t)
	policy, raw := capacityPolicy(t)
	p.create(p.l.runtimeSpec(capacityIDs[0], "app", string(p.world.BaseImage()), sessionGrants(policy), ""))
	p.leaveBuilder(control.BuildRef(capacityOps[0], 2), "app", commitD)
	p.leaveBuilder(control.BuildRef(capacityOps[1], 2), "app", commitE)
	p.world.AddUnlabeled()
	reservations := []control.CapacityReservation{
		p.bootstrapReservation(capacityIDs[0], raw),
		p.creatingReservation(capacityIDs[1], capacityOps[0], commitD, "attempted", raw),
		p.creatingReservation(capacityIDs[2], capacityOps[1], commitE, "abandoned", raw),
	}
	got, err := p.l.observeSessionCapacity(t.Context(), reservations)
	if err != nil || got.Limit != 8 || got.PhysicalCount != 4 || len(got.Occupied) != 3 {
		t.Fatalf("capacity = %+v, %v; want four objects in a scope of eight, the runtime and both builders holding their reservations' slots", got, err)
	}
}

func TestABuilderHoldsItsReservationsSlotOnlyWithItsCreationsLabels(t *testing.T) {
	p := newProofWorld(t)
	_, raw := capacityPolicy(t)
	p.leaveBuilder(control.BuildRef(capacityOps[0], 2), "app", commitD)
	other := p.creatingReservation(capacityIDs[1], capacityOps[0], commitE, "attempted", raw)
	got, err := p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{other})
	if err != nil || got.PhysicalCount != 1 || len(got.Occupied) != 0 {
		t.Fatalf("a builder of another commit = %+v, %v; want it counted and holding no slot", got, err)
	}
	moved := p.creatingReservation(capacityIDs[1], capacityOps[0], commitD, "attempted", raw)
	moved.Project = "elsewhere"
	if got, err = p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{moved}); err != nil || len(got.Occupied) != 0 {
		t.Fatalf("a builder of another project = %+v, %v; want it holding no slot", got, err)
	}
}

// Only an attempted or abandoned cycle's build can hold a builder: one of an
// ended cycle, or named for the operation itself, is never this session's.
func TestOnlyAnAttemptedOrAbandonedBuildsBuilderHoldsASlot(t *testing.T) {
	p := newProofWorld(t)
	_, raw := capacityPolicy(t)
	p.leaveBuilder(control.BuildRef(capacityOps[0], 2), "app", commitD)
	p.leaveBuilder(capacityOps[0], "app", commitD)
	ended := p.creatingReservation(capacityIDs[1], capacityOps[0], commitD, "absent", raw)
	got, err := p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{ended})
	if err != nil || got.PhysicalCount != 2 || len(got.Occupied) != 0 {
		t.Fatalf("an ended cycle = %+v, %v; want both builders counted and no slot held", got, err)
	}
}

func TestARuntimeHoldsItsSlotOnlyWhenInspectFindsItAsSpecified(t *testing.T) {
	grant := control.FilesystemGrant{Name: "data", Source: "/var/lib/p-vm/grants/app/data", Type: "directory", Access: "read-only",
		SourceIdentity: &control.SourceIdentity{Device: 7, Inode: 11, OwnerUID: 1000, OwnerGID: 100}}
	policy, raw := capacityPolicy(t, grant)
	p := newProofWorld(t)
	spec := p.l.runtimeSpec(capacityIDs[0], "app", string(p.world.BaseImage()), sessionGrants(policy), "")
	p.create(spec)
	got, err := p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{p.bootstrapReservation(capacityIDs[0], raw)})
	if err != nil || !got.Occupied[capacityIDs[0]] {
		t.Fatalf("the runtime with its captured grant = %+v, %v; want it holding its slot", got, err)
	}
	moved := grant
	moved.SourceIdentity = &control.SourceIdentity{Device: 7, Inode: 12, OwnerUID: 1000, OwnerGID: 100}
	_, changed := capacityPolicy(t, moved)
	got, err = p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{p.bootstrapReservation(capacityIDs[0], changed)})
	if err != nil || got.PhysicalCount != 1 || len(got.Occupied) != 0 {
		t.Fatalf("a runtime whose grant differs from the policy = %+v, %v; want it counted and holding no slot", got, err)
	}
	p.world.RenameAway(spec.Ref)
	got, err = p.l.observeSessionCapacity(t.Context(), []control.CapacityReservation{p.bootstrapReservation(capacityIDs[0], raw)})
	if err != nil || got.PhysicalCount != 1 || len(got.Occupied) != 0 {
		t.Fatalf("a runtime renamed away = %+v, %v; want it counted and holding no slot", got, err)
	}
}

func TestCapacityNeedsAProvenBoundaryAndACompleteInventory(t *testing.T) {
	p := newProofWorld(t)
	p.l.driver = failingCheck{p.driver}
	if _, err := p.l.observeSessionCapacity(t.Context(), nil); !errors.Is(err, errBoundary) {
		t.Fatalf("capacity with an unproven boundary = %v, want the check's error", err)
	}
	p.l.driver = p.driver
	p.world.PartialInventory()
	if got, err := p.l.observeSessionCapacity(t.Context(), nil); err == nil {
		t.Fatalf("capacity from a partial inventory = %+v, want an error", got)
	}
}

func TestCapturedGrantProjection(t *testing.T) {
	grant := control.FilesystemGrant{Name: "data", Source: "/var/lib/p-vm/grants/pdev/data", Type: "directory", Access: "read-only", SourceIdentity: &control.SourceIdentity{Device: 7, Inode: 11, OwnerUID: 1000, OwnerGID: 100}}
	spec := sessionGrants(control.ProjectPolicy{Network: "none", Command: []string{"/bin/sh"}, FilesystemMounts: []control.FilesystemGrant{grant}})
	if len(spec) != 1 || spec[0].Source != grant.Source || spec[0].Inode != 11 || spec[0].Device != 7 || spec[0].Access != "read-only" {
		t.Fatalf("captured grant projection = %+v", spec)
	}
}
