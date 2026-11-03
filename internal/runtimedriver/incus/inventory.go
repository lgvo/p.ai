package incus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The config keys that hold the P labels of a session's runtime and of a
// builder, by the label core reads them under. The reference keys name the
// object.
const (
	sessionRefKey = "user.p.session_uuid"
	builderRefKey = "user.p.builder_request_uuid"
)

var (
	sessionLabelKeys = map[string]string{
		runtimedriver.LabelProject:  "user.p.project_path",
		runtimedriver.LabelInstance: "user.p.instance_uuid",
	}
	builderLabelKeys = map[string]string{
		runtimedriver.LabelBuild:        builderRefKey,
		runtimedriver.LabelProject:      "user.p.builder_project_path",
		runtimedriver.LabelSourceCommit: "user.p.builder_commit_oid",
		runtimedriver.LabelBaseImage:    "user.p.builder_base_image",
		runtimedriver.LabelInstance:     "user.p.instance",
	}
)

// Inventory lists every instance of the configured Incus project, P's or
// not, with the project's container limit as its capacity: 0 when the
// project states none. Each instance's reference and labels are read from
// the P labels it carries, whichever P instance wrote them.
func (d *Driver) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	b := d.backend
	capacity, err := b.projectCapacity(ctx)
	if err != nil {
		return runtimedriver.Inventory{}, err
	}
	instances, err := b.instanceInventory(ctx)
	if err != nil {
		return runtimedriver.Inventory{}, err
	}
	inv := runtimedriver.Inventory{Capacity: capacity, Objects: make([]runtimedriver.Object, 0, len(instances))}
	for _, in := range instances {
		o, err := inventoryObject(in)
		if err != nil {
			return runtimedriver.Inventory{}, err
		}
		inv.Objects = append(inv.Objects, o)
	}
	slices.SortFunc(inv.Objects, func(a, b runtimedriver.Object) int { return strings.Compare(a.Name, b.Name) })
	return inv, nil
}

// projectCapacity is the configured project's limits.containers, 0 when it
// is unset. A limit the inventory bound cannot list in full is an error.
func (b *Backend) projectCapacity(ctx context.Context) (int, error) {
	raw, err := b.command(ctx, "project", "list", "--format", "json")
	if err != nil {
		return 0, err
	}
	var projects []projectJSON
	if err := decode(raw, &projects); err != nil {
		return 0, err
	}
	found := 0
	value := ""
	for _, p := range projects {
		if p.Name == b.config.Project {
			found++
			value = p.Config["limits.containers"]
		}
	}
	if found != 1 {
		return 0, errors.New("configured Incus project not visible exactly once")
	}
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > runtimedriver.MaxObjects {
		return 0, fmt.Errorf("Incus project container limit %q is not between 1 and %d", value, runtimedriver.MaxObjects)
	}
	return limit, nil
}

// instanceInventory reads every instance of the Incus project, P's or not,
// at most runtimedriver.MaxObjects of them. What it cannot account for
// exactly is an error, never a shorter list: an unreadable listing, one over
// the bound, an empty or repeated name, an instance that is not a container
// (the project's limit counts containers), one without its config, or P
// labels that its expanded config does not repeat exactly.
func (b *Backend) instanceInventory(ctx context.Context) ([]instanceJSON, error) {
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil {
		return nil, errors.Join(err, errors.New("Incus container inventory unavailable"))
	}
	if len(instances) > runtimedriver.MaxObjects {
		return nil, fmt.Errorf("Incus container inventory exceeds %d instances", runtimedriver.MaxObjects)
	}
	seen := make(map[string]bool, len(instances))
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] {
			return nil, errors.New("Incus container inventory ambiguous")
		}
		seen[in.Name] = true
		if in.Type != "container" {
			return nil, fmt.Errorf("Incus instance %q is not a container", in.Name)
		}
		if in.Config == nil || in.ExpandedConfig == nil {
			return nil, fmt.Errorf("Incus instance %q has no readable config", in.Name)
		}
		if !maps.Equal(pLabels(in.Config), pLabels(in.ExpandedConfig)) {
			return nil, fmt.Errorf("Incus instance %q has P labels its expanded config does not repeat", in.Name)
		}
	}
	return instances, nil
}

// pLabels are the user.p.* keys of an instance config.
func pLabels(config map[string]string) map[string]string {
	labels := map[string]string{}
	for key, value := range config {
		if strings.HasPrefix(key, "user.p.") {
			labels[key] = value
		}
	}
	return labels
}

// inventoryObject reads one listed instance into the driver's object. An
// instance carrying both a session's and a builder's labels is an error.
func inventoryObject(in instanceJSON) (runtimedriver.Object, error) {
	o := runtimedriver.Object{Name: in.Name, Incarnation: incarnation(volatileIdentity(in)), State: stateOf(in.Status)}
	session, builder := storedLabels(in.Config, sessionLabelKeys), storedLabels(in.Config, builderLabelKeys)
	sessionRef := in.Config[sessionRefKey]
	switch {
	case (sessionRef != "" || len(session) != 0) && len(builder) != 0:
		return runtimedriver.Object{}, fmt.Errorf("Incus instance %q carries both a session's and a builder's labels", in.Name)
	case sessionRef != "" || len(session) != 0:
		o.Labels = session
		if sessionRef != "" {
			o.Ref = runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: sessionRef}
		}
	case len(builder) != 0:
		o.Labels = builder
		if id := builder[runtimedriver.LabelBuild]; id != "" {
			o.Ref = runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: id}
		}
	}
	return o, nil
}

// storedLabels are the labels whose keys config holds with a value, or nil.
func storedLabels(config map[string]string, keys map[string]string) runtimedriver.Labels {
	var labels runtimedriver.Labels
	for label, key := range keys {
		if value := config[key]; value != "" {
			if labels == nil {
				labels = runtimedriver.Labels{}
			}
			labels[label] = value
		}
	}
	return labels
}

// sessionIdentityFree proves the whole project free of the session's
// identity before its runtime is initialized: an instance at its name, or
// any instance carrying its UUID, whoever labeled it, is a mismatch. A free
// name alone proves nothing, since an external rename can leave the UUID on
// another instance.
func (b *Backend) sessionIdentityFree(ctx context.Context, s Session) error {
	instances, err := b.instanceInventory(ctx)
	if err != nil {
		return err
	}
	for _, in := range instances {
		if in.Name == b.name(s) || in.Config[sessionRefKey] == s.SessionUUID {
			return mismatch("competing session runtime identity at " + in.Name)
		}
	}
	return nil
}
