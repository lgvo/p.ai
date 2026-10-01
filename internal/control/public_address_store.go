package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func reservePublicAddressTx(ctx context.Context, tx *sql.Tx, sessionID string, policyJSON json.RawMessage) error {
	if err := rejectDuplicateKeys(policyJSON); err != nil {
		return err
	}
	var policy ProjectPolicy
	if err := json.Unmarshal(policyJSON, &policy); err != nil {
		return err
	}
	if policy.Network != "public-egress" {
		return nil
	}
	used := [4]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT slot FROM session_public_addresses`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			rows.Close()
			return err
		}
		if slot < 0 || slot >= len(used) || used[slot] {
			rows.Close()
			return ErrConflict
		}
		used[slot] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for slot, occupied := range used {
		if !occupied {
			_, err := tx.ExecContext(ctx, `INSERT INTO session_public_addresses(session_uuid,slot) VALUES(?,?)`, sessionID, slot)
			return classifyWrite(err)
		}
	}
	return ErrConflict
}

func (s *Store) PublicAddressSlot(ctx context.Context, sessionID string) (int, error) {
	var slot int
	err := s.db.QueryRowContext(ctx, `SELECT slot FROM session_public_addresses WHERE session_uuid=?`, sessionID).Scan(&slot)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return slot, err
}
