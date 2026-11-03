package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Core's proofs about runtimes decide from the driver's Inspect and
// Inventory alone, inside a boundary the driver's Check has just proven. A
// runtime is gone only when Inspect finds nothing at its address and a
// complete inventory holds nothing carrying its reference, since an external
// rename can move a runtime away from its address. Whichever P instance
// labeled an object, carrying the reference is enough to refuse.

// runtimePresentError reports that a runtime of the session, or an object
// carrying the reference, exists where its absence was to be proven: at its
// address, or anywhere in the inventory.
type runtimePresentError struct{ text string }

func (e *runtimePresentError) Error() string { return e.text }

// proveRuntimeAbsent proves the runtime of s gone.
func proveRuntimeAbsent(ctx context.Context, d runtimedriver.Driver, s runtimedriver.Session) error {
	inv, err := absentAtAddress(ctx, d, s)
	if err != nil {
		return err
	}
	return noneCarries(inv, s.Ref)
}

// proveFailedCreationAbsent proves that a failed creation left nothing: the
// runtime of s is gone and no object carries the creation's build
// reference. Only the durable phase can prove the build was never sent: an
// inventory miss does not settle a delayed request.
func proveFailedCreationAbsent(ctx context.Context, d runtimedriver.Driver, s runtimedriver.Session, build string) error {
	inv, err := absentAtAddress(ctx, d, s)
	if err != nil {
		return err
	}
	if err := noneCarries(inv, s.Ref); err != nil {
		return err
	}
	return noneCarries(inv, runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: build})
}

// proveAssembledSource proves that the runtime a failed creation assembled
// is still exactly the one its cleanup reviewed: Inspect sees it complete
// and stopped at incarnation at, no other object carries the session's
// reference, and none carries the creation's build reference. Nothing else
// is ever adopted or removed.
func proveAssembledSource(ctx context.Context, d runtimedriver.Driver, s runtimedriver.Session, build string, at runtimedriver.Incarnation) error {
	if at == "" {
		return errors.New("failed creation runtime incarnation unknown")
	}
	if err := d.Check(ctx); err != nil {
		return err
	}
	inv, err := d.Inventory(ctx)
	if err != nil {
		return err
	}
	if builders := carrying(inv, runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: build}); len(builders) != 0 {
		return fmt.Errorf("failed creation has an unexpected builder %s", builders[0].Name)
	}
	runtimes := carrying(inv, s.Ref)
	if len(runtimes) != 1 || runtimes[0].Incarnation != at || runtimes[0].State != runtimedriver.StateStopped {
		return errors.New("failed creation runtime is not exactly one stopped runtime at its incarnation")
	}
	// The inventory does not say where the runtime is: it must be at its
	// address, so one renamed away before or after the listing is refused.
	o, err := d.Inspect(ctx, s)
	if err != nil || o.State != runtimedriver.StateStopped || !o.Complete || o.Incarnation != at {
		return errors.Join(err, errors.New("failed creation stopped runtime changed"))
	}
	return nil
}

// proveProjectReviewed proves that nothing labeled with project remains
// beyond what its deletion reviewed: every runtime or builder labeled with
// it is a reviewed session's runtime at the incarnation the review saw, and
// every image of it is a reviewed one. Builders are never reviewed. It grants
// no cleanup authority for anything else.
func proveProjectReviewed(ctx context.Context, d runtimedriver.Driver, project string, runtimes map[runtimedriver.Ref]runtimedriver.Incarnation, images map[runtimedriver.ImageRef]bool) error {
	if err := d.Check(ctx); err != nil {
		return err
	}
	inv, err := d.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, o := range inv.Objects {
		if o.Labels[runtimedriver.LabelProject] != project {
			continue
		}
		if at := runtimes[o.Ref]; at == "" || o.Incarnation != at {
			return fmt.Errorf("unreviewed project runtime %s; manual exact-identity investigation required", o.Name)
		}
	}
	all, err := d.Images(ctx)
	if err != nil {
		return err
	}
	for _, img := range all {
		if img.Labels[runtimedriver.LabelProject] == project && !images[img.Ref] {
			return fmt.Errorf("unreviewed project image %s; exact owned image investigation required", img.Ref)
		}
	}
	return nil
}

// absentAtAddress checks the boundary, proves that Inspect finds nothing at
// the address of s, and returns a fresh inventory.
func absentAtAddress(ctx context.Context, d runtimedriver.Driver, s runtimedriver.Session) (runtimedriver.Inventory, error) {
	if err := d.Check(ctx); err != nil {
		return runtimedriver.Inventory{}, err
	}
	o, err := d.Inspect(ctx, s)
	if err != nil {
		return runtimedriver.Inventory{}, errors.Join(err, errors.New("expected session runtime is not authoritatively absent"))
	}
	if o.State != runtimedriver.StateAbsent {
		return runtimedriver.Inventory{}, &runtimePresentError{"expected session runtime is not authoritatively absent"}
	}
	return d.Inventory(ctx)
}

// noneCarries refuses an inventory holding an object that carries r.
func noneCarries(inv runtimedriver.Inventory, r runtimedriver.Ref) error {
	if found := carrying(inv, r); len(found) != 0 {
		return &runtimePresentError{fmt.Sprintf("competing %s identity at %s", r.Kind, found[0].Name)}
	}
	return nil
}

// carrying is every object of inv that carries r.
func carrying(inv runtimedriver.Inventory, r runtimedriver.Ref) []runtimedriver.Object {
	var found []runtimedriver.Object
	for _, o := range inv.Objects {
		if o.Ref == r {
			found = append(found, o)
		}
	}
	return found
}

// recordedIncarnation is the driver's incarnation of a runtime whose Incus
// identity a review recorded, or empty when the record has none. It is the
// one place core knows how the Incus driver writes an incarnation; removal
// records the driver's incarnation once it moves to the driver.
func recordedIncarnation(incusUUID, generation string) runtimedriver.Incarnation {
	if incusUUID == "" || generation == "" {
		return ""
	}
	return runtimedriver.Incarnation(incusUUID + "/" + generation)
}
