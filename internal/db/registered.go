package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const registeredWhere = `FROM registrations r JOIN bindings b ON b.registration_id = r.id
	WHERE b.expires_at > ? AND (? = '' OR r.impi LIKE ? ESCAPE '\' OR EXISTS (
		SELECT 1 FROM set_identities i WHERE i.set_id = r.set_id AND i.uri LIKE ? ESCAPE '\'))`

func (d *DB) ListRegisteredIMPIs(ctx context.Context, search string, now time.Time, page, perPage int) (_ []string, _ int, err error) {
	ctx, done := d.observe(ctx, "ListRegisteredIMPIs", poolLocal)
	defer done(&err)

	like := "%" + escapeLike(search) + "%"
	args := []any{now.UTC().UnixNano(), search, like, like}

	var total int
	if err := d.local.QueryRowContext(ctx, `SELECT COUNT(DISTINCT r.impi) `+registeredWhere, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list registered private identities: %w", err)
	}

	rows, err := d.local.QueryContext(ctx, `SELECT DISTINCT r.impi `+registeredWhere+` ORDER BY r.impi LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list registered private identities: %w", err)
	}

	defer func() { _ = rows.Close() }()

	impis := []string{}

	for rows.Next() {
		var impi string
		if err := rows.Scan(&impi); err != nil {
			return nil, 0, fmt.Errorf("list registered private identities: %w", err)
		}

		impis = append(impis, impi)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list registered private identities: %w", err)
	}

	return impis, total, nil
}

func (d *DB) CountRegisteredIMPIs(ctx context.Context, now time.Time) (_ int, err error) {
	ctx, done := d.observe(ctx, "CountRegisteredIMPIs", poolLocal)
	defer done(&err)

	var n int
	if err := d.localRead.QueryRowContext(ctx, `SELECT COUNT(DISTINCT r.impi) `+registeredWhere,
		now.UTC().UnixNano(), "", "", "").Scan(&n); err != nil {
		return 0, fmt.Errorf("count registered private identities: %w", err)
	}

	return n, nil
}

func (d *DB) ListPCSCFRegistrationsByIMPI(ctx context.Context, impi string) (_ []PCSCFRegistration, err error) {
	ctx, done := d.observe(ctx, "ListPCSCFRegistrationsByIMPI", poolLocal)
	defer done(&err)

	rows, err := d.local.QueryContext(ctx,
		`SELECT `+pcscfRegistrationColumns+` FROM pcscf_registrations WHERE impi = ? ORDER BY id`, impi)
	if err != nil {
		return nil, fmt.Errorf("list P-CSCF registrations of %s: %w", impi, err)
	}

	defer func() { _ = rows.Close() }()

	regs := []PCSCFRegistration{}

	for rows.Next() {
		raw, err := scanRawPCSCFRegistration(rows)
		if err != nil {
			return nil, fmt.Errorf("list P-CSCF registrations of %s: %w", impi, err)
		}

		if r, err := raw.parse(); err == nil {
			regs = append(regs, r)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list P-CSCF registrations of %s: %w", impi, err)
	}

	return regs, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
