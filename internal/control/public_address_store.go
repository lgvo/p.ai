package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/strictjson"
)

func reservePublicAddressTx(ctx context.Context, tx *sql.Tx, sessionID string, policyJSON json.RawMessage) error {
	policy, err := parsePolicyNetwork(policyJSON)
	if err != nil || policy != "public-egress" {
		return err
	}
	_, err = reserveFreePublicSlotTx(ctx, tx, sessionID)
	return err
}

func parsePolicyNetwork(policyJSON json.RawMessage) (string, error) {
	if err := strictjson.RejectDuplicateKeys(policyJSON); err != nil {
		return "", err
	}
	var policy ProjectPolicy
	if err := json.Unmarshal(policyJSON, &policy); err != nil {
		return "", err
	}
	return policy.Network, nil
}

func reserveFreePublicSlotTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	used := [4]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT slot FROM session_public_addresses`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			rows.Close()
			return 0, err
		}
		if slot < 0 || slot >= len(used) || used[slot] {
			rows.Close()
			return 0, ErrConflict
		}
		used[slot] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for slot, occupied := range used {
		if !occupied {
			_, err := tx.ExecContext(ctx, `INSERT INTO session_public_addresses(session_uuid,slot) VALUES(?,?)`, sessionID, slot)
			return slot, classifyWrite(err)
		}
	}
	return 0, ErrConflict
}

func (s *Store) PublicAddressSlot(ctx context.Context, sessionID string) (int, error) {
	var slot int
	err := s.db.QueryRowContext(ctx, `SELECT slot FROM session_public_addresses WHERE session_uuid=?`, sessionID).Scan(&slot)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return slot, err
}

// ReserveBuilderPublicAddress returns the address slot of a creating
// session's environment builder: the session's own slot, or a free one held
// for the builder when the session's policy has none. A builder runs only
// before its session's runtime exists, so they never use one address at once.
func (s *Store) ReserveBuilderPublicAddress(ctx context.Context, sessionID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var slot int
	err = tx.QueryRowContext(ctx, `SELECT slot FROM session_public_addresses WHERE session_uuid=?`, sessionID).Scan(&slot)
	if errors.Is(err, sql.ErrNoRows) {
		slot, err = reserveFreePublicSlotTx(ctx, tx, sessionID)
	}
	if err != nil {
		return 0, err
	}
	return slot, tx.Commit()
}

// ReleaseBuilderPublicAddress frees a slot held only for a session's builder,
// once the builder is gone. A public-egress session keeps its own slot.
func (s *Store) ReleaseBuilderPublicAddress(ctx context.Context, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var policyJSON string
	if err := tx.QueryRowContext(ctx, `SELECT policy_json FROM sessions WHERE uuid=?`, sessionID).Scan(&policyJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	network, err := parsePolicyNetwork(json.RawMessage(policyJSON))
	if err != nil || network == "public-egress" {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_public_addresses WHERE session_uuid=?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}
