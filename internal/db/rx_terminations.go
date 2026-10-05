package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// An Rx session the P-CSCF still owes the PCRF an STR for (RFC 6733 §8.4).
// Cause 0 means not yet known.
type RxTermination struct {
	SessionID string
	IMPI      string
	UEAddress netip.Addr
	Cause     uint32
	Class     [][]byte
	CreatedAt time.Time
}

const rxTerminationColumns = `session_id, impi, ue_address, cause, class, created_at`

// SaveRxTermination keeps the first CreatedAt, and the stored cause and Class
// unless t carries them.
func (d *DB) SaveRxTermination(ctx context.Context, t RxTermination) error {
	if t.SessionID == "" || !t.UEAddress.IsValid() {
		return errors.New("save Rx termination: missing session or address")
	}

	var class sql.NullString

	if len(t.Class) > 0 {
		b, err := json.Marshal(t.Class)
		if err != nil {
			return fmt.Errorf("save Rx termination: %w", err)
		}

		class = sql.NullString{String: string(b), Valid: true}
	}

	_, err := d.conn.ExecContext(ctx,
		`INSERT INTO rx_terminations (`+rxTerminationColumns+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			cause = CASE WHEN excluded.cause != 0 THEN excluded.cause ELSE cause END,
			class = COALESCE(excluded.class, class)`,
		t.SessionID, t.IMPI, t.UEAddress.String(), t.Cause, class, t.CreatedAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("save Rx termination: %w", err)
	}

	return nil
}

func (d *DB) GetRxTermination(ctx context.Context, sessionID string) (RxTermination, error) {
	t, err := scanRxTermination(d.conn.QueryRowContext(ctx,
		`SELECT `+rxTerminationColumns+` FROM rx_terminations WHERE session_id = ?`, sessionID))
	if err != nil {
		return RxTermination{}, fmt.Errorf("get Rx termination: %w", err)
	}

	return t, nil
}

func (d *DB) DeleteRxTermination(ctx context.Context, sessionID string) error {
	if _, err := d.conn.ExecContext(ctx, `DELETE FROM rx_terminations WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete Rx termination: %w", err)
	}

	return nil
}

func (d *DB) ListRxTerminations(ctx context.Context) ([]RxTermination, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+rxTerminationColumns+` FROM rx_terminations ORDER BY created_at, rowid`)
	if err != nil {
		return nil, fmt.Errorf("list Rx terminations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := []RxTermination{}

	for rows.Next() {
		t, err := scanRxTermination(rows)
		if err != nil {
			return nil, fmt.Errorf("list Rx terminations: %w", err)
		}

		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Rx terminations: %w", err)
	}

	return out, nil
}

func scanRxTermination(row scanner) (RxTermination, error) {
	var (
		t         RxTermination
		ue        string
		class     sql.NullString
		createdAt int64
	)

	err := row.Scan(&t.SessionID, &t.IMPI, &ue, &t.Cause, &class, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RxTermination{}, ErrNotFound
	}

	if err != nil {
		return RxTermination{}, err
	}

	if t.UEAddress, err = netip.ParseAddr(ue); err != nil {
		return RxTermination{}, err
	}

	if class.Valid {
		if err := json.Unmarshal([]byte(class.String), &t.Class); err != nil {
			return RxTermination{}, err
		}
	}

	t.CreatedAt = time.Unix(0, createdAt).UTC()

	return t, nil
}
