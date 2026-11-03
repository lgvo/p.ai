package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// observeSessionCapacity is invoked under the store's project/ref admission
// lock, inside a boundary the driver's Check has just proven. Every object
// in scope counts, foreign ones included; a reservation's own runtime, or the
// builder of its creation's build, holds the reservation's slot instead of
// counting twice.
func (l *lifecycle) observeSessionCapacity(ctx context.Context, reservations []control.CapacityReservation) (control.CapacityObservation, error) {
	if err := l.driver.Check(ctx); err != nil {
		return control.CapacityObservation{}, err
	}
	inv, err := l.driver.Inventory(ctx)
	if err != nil {
		return control.CapacityObservation{}, control.Failed(api.ReasonRuntime, err)
	}
	observed := control.CapacityObservation{Limit: inv.Capacity, PhysicalCount: len(inv.Objects), Occupied: make(map[string]bool)}
	for _, reservation := range reservations {
		occupied, err := l.reservationOccupied(ctx, inv, reservation)
		if err != nil {
			return control.CapacityObservation{}, err
		}
		if occupied {
			observed.Occupied[reservation.SessionUUID] = true
		}
	}
	return observed, nil
}

// reservationOccupied says whether an object of inv holds the reservation's
// slot: the session's runtime, when Inspect of its spec finds it, or the
// builder of its creation's attempted or abandoned build, carrying the
// build's reference with the creation's project, commit and base image.
func (l *lifecycle) reservationOccupied(ctx context.Context, inv runtimedriver.Inventory, reservation control.CapacityReservation) (bool, error) {
	if len(carrying(inv, runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: reservation.SessionUUID})) != 0 {
		policy, err := control.ParseStoredProjectPolicy(reservation.Policy)
		if err != nil {
			return false, nil // its object stays counted; a future init stays reserved
		}
		publicIPv4, err := l.publicSessionIPv4(ctx, reservation.SessionUUID, policy)
		if err != nil {
			return false, nil
		}
		image, err := reservationImage(reservation)
		if err != nil {
			return false, err
		}
		spec := l.runtimeSpec(reservation.SessionUUID, reservation.Project, image, sessionGrants(policy), publicIPv4)
		if o, err := l.driver.Inspect(ctx, spec); err == nil && o.State != runtimedriver.StateAbsent {
			return true, nil
		}
	}
	if reservation.Kind != "session.create" {
		return false, nil
	}
	// Only the build of an attempted or abandoned cycle can hold a builder.
	var ev control.CreationEvidence
	if json.Unmarshal(reservation.Evidence, &ev) != nil || ev.Environment == nil || ev.CapturedOID == "" ||
		ev.EnvironmentBuilder == nil || ev.EnvironmentBuilder.State != "attempted" && ev.EnvironmentBuilder.State != "abandoned" {
		return false, nil
	}
	build := runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: control.BuildRef(reservation.OperationID, ev.EnvironmentBuilder.Cycle)}
	for _, o := range carrying(inv, build) {
		if o.Labels[runtimedriver.LabelProject] == reservation.Project && o.Labels[runtimedriver.LabelSourceCommit] == ev.CapturedOID &&
			o.Labels[runtimedriver.LabelBaseImage] == ev.Environment.BaseFingerprint {
			return true, nil
		}
	}
	return false, nil
}

// reservationImage is the image a reservation's creation captured.
func reservationImage(reservation control.CapacityReservation) (string, error) {
	switch reservation.Kind {
	case "project.create":
		var ev control.BlankProjectEvidence
		if json.Unmarshal(reservation.Evidence, &ev) != nil {
			return "", errors.New("project capacity identity unavailable")
		}
		return ev.ImageFingerprint, nil
	case "session.create":
		var ev control.CreationEvidence
		if json.Unmarshal(reservation.Evidence, &ev) != nil {
			return "", errors.New("session capacity identity unavailable")
		}
		return ev.ImageFingerprint, nil
	}
	return "", errors.New("session capacity operation unavailable")
}
