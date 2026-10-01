package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// observeSessionCapacity is invoked under the store's project/ref admission
// lock. A verified builder occupies its pending session's single reservation;
// every other physical container still counts, including foreign instances.
func (l *lifecycle) observeSessionCapacity(ctx context.Context, reservations []control.CapacityReservation) (control.CapacityObservation, error) {
	physical, err := l.runtime.SessionCapacity(ctx)
	if err != nil {
		return control.CapacityObservation{}, err
	}
	return associateSessionCapacity(ctx, reservations, physical, l.instanceID, l.cfg.EndpointPrefix,
		l.runtime.InspectCreated, l.runtime.InspectBuilder, l.publicSessionIPv4)
}

func associateSessionCapacity(ctx context.Context, reservations []control.CapacityReservation, physical runtimeincus.SessionCapacity,
	instanceID, endpointPrefix string,
	inspectSession func(context.Context, runtimeincus.Session) (runtimeincus.Observation, error),
	inspectBuilder func(context.Context, runtimeincus.Builder) (runtimeincus.BuilderObservation, error),
	publicAddress ...func(context.Context, string, control.ProjectPolicy) (string, error)) (control.CapacityObservation, error) {
	names := make(map[string]bool, len(physical.Instances))
	for _, instance := range physical.Instances {
		names[instance.Name] = true
	}
	observed := control.CapacityObservation{Limit: physical.Limit, PhysicalCount: len(physical.Instances), Occupied: make(map[string]bool)}
	for _, reservation := range reservations {
		if names["p-"+reservation.SessionUUID] {
			policy, policyErr := control.ParseStoredProjectPolicy(reservation.Policy)
			if policyErr != nil {
				continue // physical slot remains counted; future init remains reserved
			}
			publicIPv4 := ""
			if policy.Network == "public-egress" {
				if len(publicAddress) != 1 {
					continue
				}
				publicIPv4, policyErr = publicAddress[0](ctx, reservation.SessionUUID, policy)
				if policyErr != nil {
					continue
				}
			}
			image := ""
			switch reservation.Kind {
			case "project.create":
				var ev control.BlankProjectEvidence
				if json.Unmarshal(reservation.Evidence, &ev) != nil {
					return control.CapacityObservation{}, errors.New("project capacity identity unavailable")
				}
				image = ev.ImageFingerprint
			case "session.create":
				var ev control.CreationEvidence
				if json.Unmarshal(reservation.Evidence, &ev) != nil {
					return control.CapacityObservation{}, errors.New("session capacity identity unavailable")
				}
				image = ev.ImageFingerprint
			default:
				return control.CapacityObservation{}, errors.New("session capacity operation unavailable")
			}
			session := runtimeincus.Session{InstanceUUID: instanceID, SessionUUID: reservation.SessionUUID,
				ProjectPath: reservation.Project, ContractVersion: "1", ImageFingerprint: image,
				EndpointSource: filepath.Join(endpointPrefix, reservation.SessionUUID), Grants: nativeFilesystemGrants(policy), PublicIPv4: publicIPv4}
			if native, e := inspectSession(ctx, session); e == nil && native.Exists {
				observed.Occupied[reservation.SessionUUID] = true
				continue
			}
		}
		if reservation.Kind != "session.create" || !names["p-builder-"+reservation.OperationID] {
			continue
		}
		var ev control.CreationEvidence
		if json.Unmarshal(reservation.Evidence, &ev) != nil || ev.Environment == nil || ev.CapturedOID == "" {
			continue
		}
		if ev.BuilderTreeOID == "" {
			continue // older unresolved builder: physical plus reservation, conservatively
		}
		builder := runtimeincus.Builder{RequestUUID: reservation.OperationID, ProjectPath: reservation.Project,
			CommitOID: ev.CapturedOID, TreeOID: ev.BuilderTreeOID, BaseImageFingerprint: ev.Environment.BaseFingerprint,
			ContractVersion: "1"}
		if native, err := inspectBuilder(ctx, builder); err == nil && native.Exists {
			observed.Occupied[reservation.SessionUUID] = true
		}
	}
	return observed, nil
}
