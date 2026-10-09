package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// registeredWhere selects the registrations with a binding unexpired at the first argument, and that the search in
// the second argument matches: a part of the private identity or of one of the public identities, or all if empty.
const registeredWhere = `FROM registrations r JOIN bindings b ON b.registration_id = r.id
	WHERE b.expires_at > ? AND (? = '' OR r.impi LIKE ? ESCAPE '\' OR EXISTS (
		SELECT 1 FROM registration_identities i WHERE i.registration_id = r.id AND i.uri LIKE ? ESCAPE '\'))`

// ListRegisteredIMPIs returns a page of the private identities registered at now, in order, and their count. A
// search matches a part of the private identity or of one of its public identities, and an empty one matches all.
func (d *DB) ListRegisteredIMPIs(ctx context.Context, search string, now time.Time, page, perPage int) (_ []string, _ int, err error) {
	defer d.observe(poolWrite, &err)()

	like := "%" + escapeLike(search) + "%"
	args := []any{now.UTC().UnixNano(), search, like, like}

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT r.impi) `+registeredWhere, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list registered private identities: %w", err)
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT DISTINCT r.impi `+registeredWhere+` ORDER BY r.impi LIMIT ? OFFSET ?`,
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

// ListPCSCFRegistrationsByIMPI returns the P-CSCF's registrations of a private identity, one per UE address. It
// skips those it cannot parse, which ListPCSCFRegistrations deletes.
func (d *DB) ListPCSCFRegistrationsByIMPI(ctx context.Context, impi string) (_ []PCSCFRegistration, err error) {
	defer d.observe(poolWrite, &err)()

	rows, err := d.conn.QueryContext(ctx,
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
