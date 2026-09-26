package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// UnattendedCondition is the last valid report received with no confirmed
// attachment. It is an observation, not an assertion of current agent state.
type UnattendedCondition struct {
	Condition       string `json:"condition"`
	Source          string `json:"source,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Adapter         string `json:"adapter,omitempty"`
	AdapterVersion  string `json:"adapter_version,omitempty"`
	ReceivedAt      string `json:"received_at"`
	ReceiveSequence int64  `json:"receive_sequence"`
}

type StatusReport struct {
	V              int    `json:"v"`
	Source         string `json:"source"`
	Condition      string `json:"condition"`
	Reason         string `json:"reason"`
	Adapter        string `json:"adapter"`
	AdapterVersion string `json:"adapter_version"`
}

type statusRate struct {
	secondStart time.Time
	secondCount int
	minuteStart time.Time
	minuteCount int
}

func (r StatusReport) Valid() bool {
	if r.V != 1 || len(r.Source) > 128 || len(r.Reason) > 256 || len(r.Adapter) == 0 || len(r.Adapter) > 128 || len(r.AdapterVersion) == 0 || len(r.AdapterVersion) > 64 {
		return false
	}
	switch r.Condition {
	case "running", "attention", "idle", "failed", "unknown":
	default:
		return false
	}
	for _, s := range []string{r.Source, r.Reason, r.Adapter, r.AdapterVersion} {
		if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return false
		}
	}
	return true
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
func (s *Store) RecordStatus(ctx context.Context, id string, report StatusReport) (UnattendedCondition, bool, error) {
	return s.RecordStatusWithCommit(ctx, id, report, nil)
}

// RecordStatusWithCommit invokes a nonblocking callback while statusMu still
// orders this committed report against attachment transitions and other reports.
func (s *Store) RecordStatusWithCommit(ctx context.Context, id string, report StatusReport, onCommitted func(UnattendedCondition)) (UnattendedCondition, bool, error) {
	if len(id) != 36 || !report.Valid() {
		return UnattendedCondition{}, false, ErrInvalid
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	var registry string
	if err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM sessions WHERE uuid=?`, id).Scan(&registry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UnattendedCondition{}, false, ErrNotFound
		}
		return UnattendedCondition{}, false, err
	}
	if registry != "established" {
		return UnattendedCondition{}, false, ErrConflict
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
		return UnattendedCondition{}, false, ErrRateLimited
	}
	rate.secondCount++
	rate.minuteCount++
	s.statusRates[id] = rate
	if s.attachments[id] != 0 {
		return UnattendedCondition{}, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UnattendedCondition{}, false, err
	}
	defer tx.Rollback()
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT next_value FROM status_sequence WHERE id=1`).Scan(&seq); err != nil {
		return UnattendedCondition{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE status_sequence SET next_value=? WHERE id=1`, seq+1); err != nil {
		return UnattendedCondition{}, false, err
	}
	value := UnattendedCondition{Condition: report.Condition, Source: report.Source, Reason: report.Reason, Adapter: report.Adapter, AdapterVersion: report.AdapterVersion, ReceivedAt: now.Format(time.RFC3339Nano), ReceiveSequence: seq}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_unattended(session_uuid,condition,source,reason,adapter,adapter_version,received_at,receive_sequence) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(session_uuid) DO UPDATE SET condition=excluded.condition,source=excluded.source,reason=excluded.reason,adapter=excluded.adapter,adapter_version=excluded.adapter_version,received_at=excluded.received_at,receive_sequence=excluded.receive_sequence`, id, value.Condition, value.Source, value.Reason, value.Adapter, value.AdapterVersion, value.ReceivedAt, value.ReceiveSequence)
	if err != nil {
		return UnattendedCondition{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return UnattendedCondition{}, false, err
	}
	if onCommitted != nil {
		onCommitted(value)
	}
	return value, true, nil
}

var ErrRateLimited = errors.New("session status rate limited")

func (s *Store) SessionStatus(ctx context.Context, id string) (int, *UnattendedCondition, error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	var v UnattendedCondition
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
			return 0, false, false, ErrNotFound
		}
		return 0, false, false, err
	}
	if state != "established" {
		return 0, false, false, ErrConflict
	}
	if s.attachments[id] >= 1000000 {
		return 0, false, false, ErrConflict
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
