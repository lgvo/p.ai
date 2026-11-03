package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// listedContainer is a container as Incus lists it, with config in both its
// own and its expanded config.
func listedContainer(name, status string, config map[string]string) instanceJSON {
	expanded := maps.Clone(config)
	if expanded == nil {
		expanded = map[string]string{}
	}
	expanded["security.idmap.isolated"] = "true"
	own := maps.Clone(config)
	if own == nil {
		own = map[string]string{}
	}
	return instanceJSON{Name: name, Type: "container", Status: status, Config: own, ExpandedConfig: expanded}
}

// listingDriver is the driver over a stubbed Incus whose listing of the
// whole project is listing's.
func listingDriver(t *testing.T, listing func() ([]byte, error)) *Driver {
	t.Helper()
	b := newIncusWorld(t).backend()
	base := b.run
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if slices.Equal(argv[3:], []string{"list", "--format", "json"}) {
			return listing()
		}
		return base(ctx, binary, argv, env)
	}
	return NewDriver(b)
}

func listingOf(instances ...instanceJSON) func() ([]byte, error) {
	return func() ([]byte, error) { return json.Marshal(instances) }
}

// inventoryObjectNamed is the object of the inventory named name.
func inventoryObjectNamed(t *testing.T, inv runtimedriver.Inventory, name string) runtimedriver.Object {
	t.Helper()
	for _, o := range inv.Objects {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("Inventory lists no %s: %+v", name, inv.Objects)
	return runtimedriver.Object{}
}

// Each instance's reference and labels come from the P labels it carries,
// whoever wrote them: a session's runtime and a builder each by their own
// keys, a label without a reference by its key, and nothing for an instance
// P never labeled.
func TestInventoryReadsEachInstanceFromItsLabels(t *testing.T) {
	const build = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name   string
		config map[string]string
		ref    runtimedriver.Ref
		labels runtimedriver.Labels
	}{
		{name: "session", config: map[string]string{"user.p.session_uuid": testUUID, "user.p.project_path": "team/app", "user.p.instance_uuid": testInstance,
			"user.p.contract_version": "1", "user.p.image_fingerprint": testImage},
			ref:    runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: testUUID},
			labels: runtimedriver.Labels{runtimedriver.LabelProject: "team/app", runtimedriver.LabelInstance: testInstance}},
		{name: "builder", config: map[string]string{"user.p.builder_request_uuid": build, "user.p.builder_project_path": "team/app",
			"user.p.builder_commit_oid": "0123456789abcdef0123456789abcdef01234567", "user.p.builder_base_image": testImage,
			"user.p.builder_contract_version": "1", "user.p.instance": testInstance},
			ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: build},
			labels: runtimedriver.Labels{runtimedriver.LabelBuild: build, runtimedriver.LabelProject: "team/app",
				runtimedriver.LabelSourceCommit: "0123456789abcdef0123456789abcdef01234567", runtimedriver.LabelBaseImage: testImage, runtimedriver.LabelInstance: testInstance}},
		{name: "session-labeled-by-hand", config: map[string]string{"user.p.session_uuid": testUUID},
			ref: runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: testUUID}},
		{name: "project-without-reference", config: map[string]string{"user.p.project_path": "team/app"},
			labels: runtimedriver.Labels{runtimedriver.LabelProject: "team/app"}},
		{name: "builder-project-without-reference", config: map[string]string{"user.p.builder_project_path": "team/app"},
			labels: runtimedriver.Labels{runtimedriver.LabelProject: "team/app"}},
		{name: "unlabeled", config: map[string]string{"user.other": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := listingDriver(t, listingOf(listedContainer("renamed", "Stopped", tc.config)))
			inv, err := d.Inventory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			o := inventoryObjectNamed(t, inv, "renamed")
			if o.Ref != tc.ref || !maps.Equal(o.Labels, tc.labels) {
				t.Fatalf("Inventory reads %+v, want reference %+v and labels %v", o, tc.ref, tc.labels)
			}
		})
	}
}

// A runtime's object has the incarnation Inspect reports for it.
func TestInventoryReportsTheIncarnationInspectReports(t *testing.T) {
	w := newIncusWorld(t)
	d := w.driver()
	s := testSessionSpec(t)
	created, err := d.Create(context.Background(), s, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	inv, err := d.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o := inventoryObjectNamed(t, inv, testRuntime); created.Incarnation == "" || o.Incarnation != created.Incarnation || o.State != runtimedriver.StateStopped {
		t.Fatalf("Inventory lists %+v, want it stopped at %s", o, created.Incarnation)
	}
	w.change(testRuntime, func(in *worldInstance) { in.config["volatile.uuid.generation"] = "restored" })
	inv, err = d.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o := inventoryObjectNamed(t, inv, testRuntime); o.Incarnation != "" {
		t.Fatalf("an instance without a UUID generation is listed at %s, want no incarnation", o.Incarnation)
	}
}

// An instance that is neither running nor stopped, such as one starting,
// stopping, frozen or failed, is present in another state.
func TestInventoryListsAnInstanceInTransitionAsPresent(t *testing.T) {
	for status, want := range map[string]runtimedriver.State{
		"Running": runtimedriver.StateRunning, "Stopped": runtimedriver.StateStopped, "Stopping": runtimedriver.StateOther,
		"Starting": runtimedriver.StateOther, "Frozen": runtimedriver.StateOther, "Error": runtimedriver.StateOther,
	} {
		d := listingDriver(t, listingOf(listedContainer("peer", status, nil)))
		inv, err := d.Inventory(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if o := inventoryObjectNamed(t, inv, "peer"); o.State != want {
			t.Fatalf("an instance %s is listed %s, want %s", status, o.State, want)
		}
	}
}

// The capacity is the project's limits.containers, 0 when unset; a limit
// the inventory bound cannot list in full, or that is not a count, is an
// error.
func TestInventoryCapacityIsTheProjectContainerLimit(t *testing.T) {
	for limit, want := range map[string]int{"": 0, "1": 1, "8": 8, "64": 64} {
		w := newIncusWorld(t)
		w.containerLimit = limit
		inv, err := w.driver().Inventory(context.Background())
		if err != nil || inv.Capacity != want {
			t.Fatalf("limits.containers %q: capacity %d, %v; want %d", limit, inv.Capacity, err, want)
		}
	}
	for _, limit := range []string{"0", "65", "-1", "many"} {
		w := newIncusWorld(t)
		w.containerLimit = limit
		if inv, err := w.driver().Inventory(context.Background()); err == nil {
			t.Fatalf("limits.containers %q gave %+v, want an error", limit, inv)
		}
	}
}

func TestInventoryRefusesAProjectItCannotSeeExactlyOnce(t *testing.T) {
	for _, projects := range [][]projectJSON{
		{},
		{{Name: "other", Config: map[string]string{"limits.containers": "4"}}},
		{{Name: "user-1000", Config: map[string]string{"limits.containers": "4"}}, {Name: "user-1000", Config: map[string]string{"limits.containers": "8"}}},
	} {
		b := newIncusWorld(t).backend()
		base := b.run
		b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
			if slices.Equal(argv[3:], []string{"project", "list", "--format", "json"}) {
				return json.Marshal(projects)
			}
			return base(ctx, binary, argv, env)
		}
		if inv, err := NewDriver(b).Inventory(context.Background()); err == nil {
			t.Fatalf("projects %+v gave %+v, want an error", projects, inv)
		}
	}
}

// The inventory lists up to runtimedriver.MaxObjects instances, beyond the
// four the absence proofs used to read.
func TestInventoryListsUpToTheBound(t *testing.T) {
	instances := make([]instanceJSON, runtimedriver.MaxObjects)
	for i := range instances {
		instances[i] = listedContainer(fmt.Sprintf("peer-%02d", i), "Stopped", nil)
	}
	inv, err := listingDriver(t, listingOf(instances...)).Inventory(context.Background())
	if err != nil || len(inv.Objects) != runtimedriver.MaxObjects {
		t.Fatalf("Inventory of %d instances = %d objects, %v", runtimedriver.MaxObjects, len(inv.Objects), err)
	}
}

// What the reader cannot account for exactly is an error, never a shorter
// list.
func TestInventoryIsAnErrorRatherThanAPartialList(t *testing.T) {
	labeled := func(config, expanded map[string]string) instanceJSON {
		return instanceJSON{Name: "peer", Type: "container", Status: "Stopped", Config: config, ExpandedConfig: expanded}
	}
	over := make([]instanceJSON, runtimedriver.MaxObjects+1)
	for i := range over {
		over[i] = listedContainer(fmt.Sprintf("peer-%02d", i), "Stopped", nil)
	}
	vm := listedContainer("vm", "Stopped", nil)
	vm.Type = "virtual-machine"
	for name, listing := range map[string]func() ([]byte, error){
		"unreachable": func() ([]byte, error) { return nil, errors.New("Incus unavailable") },
		"truncated": func() ([]byte, error) {
			raw, err := json.Marshal([]instanceJSON{listedContainer("peer", "Stopped", nil)})
			return raw[:len(raw)/2], err
		},
		"over the bound":   listingOf(over...),
		"empty name":       listingOf(listedContainer("", "Stopped", nil)),
		"duplicate name":   listingOf(listedContainer("peer", "Stopped", nil), listedContainer("peer", "Running", nil)),
		"virtual machine":  listingOf(vm),
		"no config":        listingOf(labeled(nil, map[string]string{})),
		"no expanded":      listingOf(labeled(map[string]string{}, nil)),
		"label only own":   listingOf(labeled(map[string]string{"user.p.session_uuid": testUUID}, map[string]string{})),
		"label expanded":   listingOf(labeled(map[string]string{}, map[string]string{"user.p.session_uuid": testUUID})),
		"label differs":    listingOf(labeled(map[string]string{"user.p.project_path": "team/app"}, map[string]string{"user.p.project_path": "team/other"})),
		"builder disagree": listingOf(labeled(map[string]string{"user.p.builder_request_uuid": testUUID}, map[string]string{})),
		"both families": listingOf(listedContainer("peer", "Stopped", map[string]string{
			"user.p.session_uuid": testUUID, "user.p.builder_request_uuid": "22222222-2222-4222-8222-222222222222"})),
		"both projects": listingOf(listedContainer("peer", "Stopped", map[string]string{
			"user.p.project_path": "team/app", "user.p.builder_project_path": "team/app"})),
	} {
		if inv, err := listingDriver(t, listing).Inventory(context.Background()); err == nil {
			t.Errorf("%s: Inventory = %+v, want an error", name, inv)
		}
	}
}
