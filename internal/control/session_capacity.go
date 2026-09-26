package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// CapacityReservation is a durable identity that may still consume a native
// container slot. A pending builder can occupy the same reservation until it
// is deleted before the session instance is initialized.
type CapacityReservation struct {
	SessionUUID string
	Project     string
	Policy      json.RawMessage
	OperationID string
	Kind        string
	Evidence    json.RawMessage
}

type CapacityObservation struct {
	Limit         int
	PhysicalCount int
	Occupied      map[string]bool // exact verified native session or builder, keyed by session UUID
}

type CapacityObserver func(context.Context, []CapacityReservation) (CapacityObservation, error)

// checkSessionAdmission runs under gitAuthority, shared by project and session
// creation. The caller's native inventory is fresh at this point; the sole
// daemon lock and this authority lock serialize P's durable admissions.
func (s *Store) checkSessionAdmission(ctx context.Context, observe CapacityObserver) error {
	return s.checkSessionCapacity(ctx, observe, 2)
}

// Repair consumes an existing missing session reservation, so it needs only
// the one inspection-helper slot rather than another session slot.
func (s *Store) checkSessionRepairCapacity(ctx context.Context, observe CapacityObserver) error {
	return s.checkSessionCapacity(ctx, observe, 1)
}

func (s *Store) checkSessionCapacity(ctx context.Context, observe CapacityObserver, extra int) error {
	if observe == nil {
		return ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.uuid,s.project_path,s.policy_json,o.id,o.kind,o.evidence_json
	 FROM sessions s LEFT JOIN operations o ON o.session_uuid=s.uuid AND o.kind IN ('project.create','session.create')
	 WHERE s.registry_state IN ('creating','established','removing')`)
	if err != nil {
		return err
	}
	var reservations []CapacityReservation
	seen := map[string]bool{}
	for rows.Next() {
		var item CapacityReservation
		var policy string
		var operationID, kind, evidence sql.NullString
		if err = rows.Scan(&item.SessionUUID, &item.Project, &policy, &operationID, &kind, &evidence); err != nil {
			break
		}
		if seen[item.SessionUUID] || !validUUID(item.SessionUUID) {
			err = errors.New("session capacity reservation ambiguous")
			break
		}
		seen[item.SessionUUID] = true
		item.OperationID, item.Kind = operationID.String, kind.String
		item.Policy = json.RawMessage(policy)
		item.Evidence = json.RawMessage(evidence.String)
		reservations = append(reservations, item)
		if len(reservations) > 64 {
			err = errors.New("session capacity reservation inventory exceeds bound")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	// Every project request has a pinned bootstrap UUID before it has a
	// session row, whether origin-backed or blank. Count the rowless intent.
	rows, err = s.db.QueryContext(ctx, `SELECT o.id,o.project_path,o.evidence_json FROM operations o
	 LEFT JOIN sessions s ON s.uuid=o.session_uuid
	 WHERE o.kind='project.create' AND s.uuid IS NULL AND o.status IN ('running','blocked','unknown')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item CapacityReservation
		var evidence string
		if err = rows.Scan(&item.OperationID, &item.Project, &evidence); err != nil {
			break
		}
		var ev BlankProjectEvidence
		if err = json.Unmarshal([]byte(evidence), &ev); err != nil || !validUUID(ev.BootstrapUUID) || seen[ev.BootstrapUUID] {
			err = errors.New("project capacity reservation ambiguous")
			break
		}
		item.SessionUUID, item.Kind, item.Evidence, item.Policy = ev.BootstrapUUID, "project.create", json.RawMessage(evidence), ev.Policy
		seen[item.SessionUUID] = true
		reservations = append(reservations, item)
		if len(reservations) > 64 {
			err = errors.New("session capacity reservation inventory exceeds bound")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	observation, err := observe(ctx, reservations)
	if err != nil {
		return err
	}
	if observation.Limit < 2 || observation.Limit > 64 || observation.PhysicalCount < 0 || observation.PhysicalCount > observation.Limit || len(observation.Occupied) > len(reservations) {
		return errors.New("session capacity observation unavailable")
	}
	needed := observation.PhysicalCount + extra
	for _, reservation := range reservations {
		if !observation.Occupied[reservation.SessionUUID] {
			needed++
		}
	}
	if needed > observation.Limit {
		return ErrConflict
	}
	return nil
}
