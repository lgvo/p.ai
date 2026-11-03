package control

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

type statusRate struct {
	secondStart time.Time
	secondCount int
	minuteStart time.Time
	minuteCount int
}

// AllowSessionAttempt budgets all private RPC lines before parsing or SQLite
// lookup, including malformed notifications. It is deliberately looser than
// the valid semantic-report budget.
func (s *Store) AllowSessionAttempt(id string) bool {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	now := time.Now()
	rate := s.attemptRates[id]
	if now.Sub(rate.secondStart) >= time.Second {
		rate.secondStart, rate.secondCount = now, 0
	}
	if now.Sub(rate.minuteStart) >= time.Minute {
		rate.minuteStart, rate.minuteCount = now, 0
	}
	if rate.secondCount >= 100 || rate.minuteCount >= 600 {
		return false
	}
	rate.secondCount++
	rate.minuteCount++
	s.attemptRates[id] = rate
	return true
}

// RecordStatus serializes receiving, rate control, and attachment transitions.
// Its boolean indicates a committed unattended change eligible for event delivery.
func (s *Store) RecordStatus(ctx context.Context, id string, report api.StatusReport) (api.UnattendedCondition, bool, error) {
	return s.RecordStatusWithCommit(ctx, id, report, nil)
}

// RecordStatusWithCommit invokes a nonblocking callback while statusMu still
// orders this committed report against attachment transitions and other reports.
func (s *Store) RecordStatusWithCommit(ctx context.Context, id string, report api.StatusReport, onCommitted func(api.UnattendedCondition)) (api.UnattendedCondition, bool, error) {
	if len(id) != 36 || report.Validate() != nil {
		return api.UnattendedCondition{}, false, ErrInvalid
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	var registry string
	if err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM sessions WHERE uuid=?`, id).Scan(&registry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return api.UnattendedCondition{}, false, NotFound(api.ReasonSession, api.TargetSession(id))
		}
		return api.UnattendedCondition{}, false, err
	}
	if registry != "established" {
		return api.UnattendedCondition{}, false, Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	now := time.Now().UTC()
	rate := s.statusRates[id]
	if now.Sub(rate.secondStart) >= time.Second {
		rate.secondStart, rate.secondCount = now, 0
	}
	if now.Sub(rate.minuteStart) >= time.Minute {
		rate.minuteStart, rate.minuteCount = now, 0
	}
	if rate.secondCount >= 20 || rate.minuteCount >= 240 {
		return api.UnattendedCondition{}, false, ErrRateLimited
	}
	rate.secondCount++
	rate.minuteCount++
	s.statusRates[id] = rate
	if s.attachments[id] != 0 {
		return api.UnattendedCondition{}, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.UnattendedCondition{}, false, err
	}
	defer tx.Rollback()
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT next_value FROM status_sequence WHERE id=1`).Scan(&seq); err != nil {
		return api.UnattendedCondition{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE status_sequence SET next_value=? WHERE id=1`, seq+1); err != nil {
		return api.UnattendedCondition{}, false, err
	}
	value := api.UnattendedCondition{Condition: report.Condition, Source: report.Source, Reason: report.Reason, Adapter: report.Adapter, AdapterVersion: report.AdapterVersion, ReceivedAt: now.Format(time.RFC3339Nano), ReceiveSequence: seq}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_unattended(session_uuid,condition,source,reason,adapter,adapter_version,received_at,receive_sequence) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(session_uuid) DO UPDATE SET condition=excluded.condition,source=excluded.source,reason=excluded.reason,adapter=excluded.adapter,adapter_version=excluded.adapter_version,received_at=excluded.received_at,receive_sequence=excluded.receive_sequence`, id, value.Condition, value.Source, value.Reason, value.Adapter, value.AdapterVersion, value.ReceivedAt, value.ReceiveSequence)
	if err != nil {
		return api.UnattendedCondition{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return api.UnattendedCondition{}, false, err
	}
	if onCommitted != nil {
		onCommitted(value)
	}
	return value, true, nil
}

var ErrRateLimited = errors.New("session status rate limited")

// forgetSession drops the in-memory status state of a session whose row is
// gone, so that state does not outlive it.
func (s *Store) forgetSession(id string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	delete(s.attachments, id)
	delete(s.statusRates, id)
	delete(s.attemptRates, id)
}

// forgetIfGone drops the in-memory status state of session id once its row is
// gone. A failed read leaves the state.
func (s *Store) forgetIfGone(ctx context.Context, id string) {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE uuid=?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		s.forgetSession(id)
	}
}

func (s *Store) SessionStatus(ctx context.Context, id string) (int, *api.UnattendedCondition, error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	var v api.UnattendedCondition
	err := s.db.QueryRowContext(ctx, `SELECT condition,source,reason,adapter,adapter_version,received_at,receive_sequence FROM session_unattended WHERE session_uuid=?`, id).Scan(&v.Condition, &v.Source, &v.Reason, &v.Adapter, &v.AdapterVersion, &v.ReceivedAt, &v.ReceiveSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return s.attachments[id], nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	return s.attachments[id], &v, nil
}

// ConfirmAttachment and ReleaseAttachment are the reducer seam for connection-
// owned leases. No session RPC method exposes these transitions in this slice.
func (s *Store) ConfirmAttachment(ctx context.Context, id string) (count int, confirmed bool, cleared bool, err error) {
	return s.ConfirmAttachmentWithCommit(ctx, id, nil)
}

// ConfirmAttachmentWithCommit queues a confirmed presence transition before
// another report or attachment transition may pass the reducer lock.
func (s *Store) ConfirmAttachmentWithCommit(ctx context.Context, id string, onCommitted func(int, bool)) (count int, confirmed bool, cleared bool, err error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	var state string
	if err = s.db.QueryRowContext(ctx, `SELECT registry_state FROM sessions WHERE uuid=?`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, false, NotFound(api.ReasonSession, api.TargetSession(id))
		}
		return 0, false, false, err
	}
	if state != "established" {
		return 0, false, false, Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	if s.attachments[id] >= 1000000 {
		return 0, false, false, Full(api.ReasonAttachmentCapacity)
	}
	if s.attachments[id] == 0 {
		var result sql.Result
		result, err = s.db.ExecContext(ctx, `DELETE FROM session_unattended WHERE session_uuid=?`, id)
		if err != nil {
			return 0, false, false, err
		}
		var removed int64
		removed, err = result.RowsAffected()
		if err != nil {
			return 0, false, false, err
		}
		cleared = removed != 0
	}
	s.attachments[id]++
	if onCommitted != nil {
		onCommitted(s.attachments[id], cleared)
	}
	return s.attachments[id], true, cleared, nil
}

func (s *Store) ReleaseAttachment(id string) (int, bool) {
	return s.ReleaseAttachmentWithCommit(id, nil)
}

// ReleaseAttachmentWithCommit preserves order with concurrently received
// reports when a confirmed lease finishes teardown.
func (s *Store) ReleaseAttachmentWithCommit(id string, onCommitted func(int)) (int, bool) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.attachments[id] == 0 {
		return 0, false
	}
	s.attachments[id]--
	if onCommitted != nil {
		onCommitted(s.attachments[id])
	}
	return s.attachments[id], true
}
