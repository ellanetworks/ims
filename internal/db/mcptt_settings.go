package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
	"github.com/mattn/go-sqlite3"
)

// GetSettings are the settings as written: the users and groups in the order they were created in, the IMPUs,
// implicit affiliations, encodings and members of each in their order. A member and an implicit affiliation are the
// MCPTT ID of the user and the URI of the group as those have them.
func (d *DB) GetMCPTTSettings(ctx context.Context) (_ settings.Settings, err error) {
	defer d.observe(poolWrite, &err)()

	// One snapshot of the database (https://sqlite.org/isolation.html), not one per statement.
	tx, err := d.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return settings.Settings{}, fmt.Errorf("get MCPTT settings: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	return getMCPTTSettings(ctx, tx)
}

func getMCPTTSettings(ctx context.Context, tx *sql.Tx) (settings.Settings, error) {
	var s settings.Settings

	if err := tx.QueryRowContext(ctx, `SELECT participating_psi, controlling_psi FROM mcptt_system WHERE id = 1`).
		Scan(&s.System.ParticipatingPSI, &s.System.ControllingPSI); err != nil {
		return settings.Settings{}, fmt.Errorf("get MCPTT system: %w", err)
	}

	var (
		on                 = &s.ServiceConfiguration.OnNetwork
		levels, c7         sql.NullInt64
		t2, t1, t3, t7, t8 sql.NullInt64
		// users and groups are the indexes of the users and groups, by row id.
		users, groups = map[int64]int{}, map[int64]int{}
	)

	if err := tx.QueryRowContext(ctx, `SELECT domain, num_levels_priority_hierarchy, time_limit_ns, t1_ns, t3_ns, t7_ns, t8_ns, c7
		FROM mcptt_service_configuration WHERE id = 1`).Scan(&s.ServiceConfiguration.Domain, &levels, &t2, &t1, &t3, &t7,
		&t8, &c7); err != nil {
		return settings.Settings{}, fmt.Errorf("get MCPTT service configuration: %w", err)
	}

	on.NumLevelsPriorityHierarchy, on.TransmitTime.TimeLimit = intOf(levels), durationOf(t2)
	fc := &on.FCTimersCounters
	fc.T1, fc.T3, fc.T7, fc.T8, fc.C7 = durationOf(t1), durationOf(t3), durationOf(t7), durationOf(t8), intOf(c7)

	err := queryRows(ctx, tx, "get MCPTT users", `SELECT id, mcptt_id, max_affiliations_n2, max_simultaneous_calls_n6 FROM mcptt_users
		ORDER BY id`, func(rows *sql.Rows) error {
		var (
			id     int64
			u      settings.User
			n2, n6 sql.NullInt64
		)

		if err := rows.Scan(&id, &u.MCPTTUserID, &n2, &n6); err != nil {
			return err
		}

		u.MaxAffiliationsN2, u.MaxSimultaneousCallsN6 = intOf(n2), intOf(n6)
		users[id] = len(s.Users)
		s.Users = append(s.Users, u)

		return nil
	})
	if err != nil {
		return settings.Settings{}, err
	}

	err = queryRows(ctx, tx, "get MCPTT groups", `SELECT id, uri, invite_members, max_participants, t4_ns, tng3_ns, minimum_to_start,
		tng1_ns, tng1_action, minimum_affiliated, release_on_initiator_leave FROM mcptt_groups ORDER BY id`,
		func(rows *sql.Rows) error {
			var (
				id                int64
				g                 settings.Group
				t4                sql.NullInt64
				tng3, tng1        int64
				action            string
				invite, initiator bool
			)

			if err := rows.Scan(&id, &g.URI, &invite, &g.MaxParticipants, &t4, &tng3, &g.MinimumToStart, &tng1,
				&action, &g.MinimumAffiliated, &initiator); err != nil {
				return err
			}

			g.InviteMembers, g.ReleaseOnInitiatorLeave = invite, initiator
			g.T4, g.TNG3, g.TNG1, g.TNG1Action = durationOf(t4), time.Duration(tng3), time.Duration(tng1), settings.Action(action)
			groups[id] = len(s.Groups)
			s.Groups = append(s.Groups, g)

			return nil
		})
	if err != nil {
		return settings.Settings{}, err
	}

	if err := queryRows(ctx, tx, "get MCPTT IMPUs", `SELECT user_id, impu FROM mcptt_user_impus ORDER BY user_id, position`,
		func(rows *sql.Rows) error {
			var (
				id   int64
				impu string
			)

			if err := rows.Scan(&id, &impu); err != nil {
				return err
			}

			i, ok := users[id]
			if !ok {
				return orphan("IMPU", impu)
			}

			u := &s.Users[i]
			u.IMPUs = append(u.IMPUs, impu)

			return nil
		}); err != nil {
		return settings.Settings{}, err
	}

	if err := queryRows(ctx, tx, "get MCPTT implicit affiliations", `SELECT a.user_id, g.uri FROM mcptt_user_implicit_affiliations a
		JOIN mcptt_groups g ON g.id = a.group_id ORDER BY a.user_id, a.position`, func(rows *sql.Rows) error {
		var (
			id  int64
			uri string
		)

		if err := rows.Scan(&id, &uri); err != nil {
			return err
		}

		i, ok := users[id]
		if !ok {
			return orphan("implicit affiliation", uri)
		}

		u := &s.Users[i]
		u.ImplicitAffiliations = append(u.ImplicitAffiliations, uri)

		return nil
	}); err != nil {
		return settings.Settings{}, err
	}

	if err := queryRows(ctx, tx, "get MCPTT encodings", `SELECT group_id, name FROM mcptt_group_encodings ORDER BY group_id, position`,
		func(rows *sql.Rows) error {
			var (
				id   int64
				name string
			)

			if err := rows.Scan(&id, &name); err != nil {
				return err
			}

			i, ok := groups[id]
			if !ok {
				return orphan("encoding", name)
			}

			g := &s.Groups[i]
			g.Encodings = append(g.Encodings, name)

			return nil
		}); err != nil {
		return settings.Settings{}, err
	}

	if err := queryRows(ctx, tx, "get MCPTT members", `SELECT group_id, user_id, user_priority, required, recvonly,
		affiliation_required FROM mcptt_group_members ORDER BY group_id, position`, func(rows *sql.Rows) error {
		var (
			gid, uid int64
			e        settings.Entry
		)

		if err := rows.Scan(&gid, &uid, &e.UserPriority, &e.Required, &e.ReceiveOnly, &e.AffiliationRequired); err != nil {
			return err
		}

		ui, uok := users[uid]
		gi, gok := groups[gid]

		if !uok || !gok {
			return orphan("member", strconv.FormatInt(uid, 10))
		}

		e.URI = s.Users[ui].MCPTTUserID
		g := &s.Groups[gi]
		g.List.Entries = append(g.List.Entries, e)

		return nil
	}); err != nil {
		return settings.Settings{}, err
	}

	return s, nil
}

// errOrphan is of a row whose user or group is not in the database, which only a change outside the server makes:
// the foreign keys of the sqlite3 shell are off by default (https://sqlite.org/foreignkeys.html §2).
var errOrphan = errors.New("of no user or group")

func orphan(what, value string) error { return fmt.Errorf("%s %s: %w", what, value, errOrphan) }

// rows calls scan for each row of query.
func queryRows(ctx context.Context, tx *sql.Tx, what, query string, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}

func (d *DB) UpdateMCPTTSystem(ctx context.Context, s settings.System) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `UPDATE mcptt_system SET participating_psi = ?, controlling_psi = ? WHERE id = 1`,
		s.ParticipatingPSI, s.ControllingPSI)

	return oneRow(res, err, "update MCPTT system")
}

func (d *DB) UpdateMCPTTServiceConfiguration(ctx context.Context, c settings.ServiceConfiguration) (err error) {
	defer d.observe(poolWrite, &err)()

	on := c.OnNetwork
	fc := on.FCTimersCounters

	res, err := d.conn.ExecContext(ctx, `UPDATE mcptt_service_configuration SET domain = ?, num_levels_priority_hierarchy = ?,
		time_limit_ns = ?, t1_ns = ?, t3_ns = ?, t7_ns = ?, t8_ns = ?, c7 = ? WHERE id = 1`, c.Domain,
		nullInt(on.NumLevelsPriorityHierarchy), nullDuration(on.TransmitTime.TimeLimit), nullDuration(fc.T1),
		nullDuration(fc.T3), nullDuration(fc.T7), nullDuration(fc.T8), nullInt(fc.C7))

	return oneRow(res, err, "update MCPTT service configuration")
}

// one is the error of an update of a row of one, res of err: of no row, the row was deleted outside the server.
func oneRow(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%s: %d rows: %w", what, n, errors.Join(err, ErrNotFound))
	}

	return nil
}

// CreateUser adds u, its implicit affiliations groups of the database; ErrDuplicate if a user has its MCPTT ID.
func (d *DB) CreateMCPTTUser(ctx context.Context, u settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create MCPTT user", func(tx *sql.Tx) error {
		k, err := mcpttKey(u.MCPTTUserID)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO mcptt_users (mcptt_id, key, max_affiliations_n2,
			max_simultaneous_calls_n6) VALUES (?, ?, ?, ?)`, u.MCPTTUserID, k, nullInt(u.MaxAffiliationsN2),
			nullInt(u.MaxSimultaneousCallsN6))
		if err != nil {
			return duplicate(err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return err
		}

		return putMCPTTUserLists(ctx, tx, id, u)
	})
}

// UpdateUser replaces the user of u's MCPTT ID with u; ErrNotFound if there is none.
func (d *DB) UpdateMCPTTUser(ctx context.Context, u settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update MCPTT user", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_users", u.MCPTTUserID)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE mcptt_users SET mcptt_id = ?, max_affiliations_n2 = ?,
			max_simultaneous_calls_n6 = ? WHERE id = ?`, u.MCPTTUserID, nullInt(u.MaxAffiliationsN2),
			nullInt(u.MaxSimultaneousCallsN6), id); err != nil {
			return err
		}

		for _, q := range []string{
			`DELETE FROM mcptt_user_impus WHERE user_id = ?`,
			`DELETE FROM mcptt_user_implicit_affiliations WHERE user_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}

		return putMCPTTUserLists(ctx, tx, id, u)
	})
}

// putUserLists inserts the IMPUs and implicit affiliations of u, of row id.
func putMCPTTUserLists(ctx context.Context, tx *sql.Tx, id int64, u settings.User) error {
	for i, impu := range u.IMPUs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_user_impus (user_id, position, impu) VALUES (?, ?, ?)`,
			id, i, impu); err != nil {
			return err
		}
	}

	for i, g := range u.ImplicitAffiliations {
		gid, err := mcpttRowID(ctx, tx, "mcptt_groups", g)
		if err != nil {
			return fmt.Errorf("implicit affiliation %s: %w", g, err)
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_user_implicit_affiliations (user_id, group_id, position)
			VALUES (?, ?, ?)`, id, gid, i); err != nil {
			return err
		}
	}

	return nil
}

// DeleteUser deletes the user of MCPTT ID mcpttID; ErrNotFound if there is none. A user that is a member of a group
// is not deleted.
func (d *DB) DeleteMCPTTUser(ctx context.Context, mcpttID string) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "delete MCPTT user", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_users", mcpttID)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM mcptt_users WHERE id = ?`, id)

		return err
	})
}

// CreateGroup adds g, its members users of the database; ErrDuplicate if a group has its URI.
func (d *DB) CreateMCPTTGroup(ctx context.Context, g settings.Group) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create MCPTT group", func(tx *sql.Tx) error {
		k, err := mcpttKey(g.URI)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO mcptt_groups (uri, key, invite_members, max_participants, t4_ns, tng3_ns,
			minimum_to_start, tng1_ns, tng1_action, minimum_affiliated, release_on_initiator_leave)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, g.URI, k, g.InviteMembers, g.MaxParticipants, nullDuration(g.T4),
			int64(g.TNG3), g.MinimumToStart, int64(g.TNG1), string(g.TNG1Action), g.MinimumAffiliated,
			g.ReleaseOnInitiatorLeave)
		if err != nil {
			return duplicate(err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return err
		}

		return putMCPTTGroupLists(ctx, tx, id, g)
	})
}

// UpdateGroup replaces the group of g's URI with g, members included; ErrNotFound if there is none.
func (d *DB) UpdateMCPTTGroup(ctx context.Context, g settings.Group) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update MCPTT group", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_groups", g.URI)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE mcptt_groups SET uri = ?, invite_members = ?, max_participants = ?, t4_ns = ?,
			tng3_ns = ?, minimum_to_start = ?, tng1_ns = ?, tng1_action = ?, minimum_affiliated = ?,
			release_on_initiator_leave = ? WHERE id = ?`, g.URI, g.InviteMembers, g.MaxParticipants,
			nullDuration(g.T4), int64(g.TNG3), g.MinimumToStart, int64(g.TNG1), string(g.TNG1Action),
			g.MinimumAffiliated, g.ReleaseOnInitiatorLeave, id); err != nil {
			return err
		}

		for _, q := range []string{
			`DELETE FROM mcptt_group_encodings WHERE group_id = ?`,
			`DELETE FROM mcptt_group_members WHERE group_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}

		return putMCPTTGroupLists(ctx, tx, id, g)
	})
}

// putGroupLists inserts the encodings and members of g, of row id.
func putMCPTTGroupLists(ctx context.Context, tx *sql.Tx, id int64, g settings.Group) error {
	for i, name := range g.Encodings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_group_encodings (group_id, position, name) VALUES (?, ?, ?)`,
			id, i, name); err != nil {
			return err
		}
	}

	for i, e := range g.List.Entries {
		if err := putMCPTTMember(ctx, tx, id, i, e); err != nil {
			return err
		}
	}

	return nil
}

func putMCPTTMember(ctx context.Context, tx *sql.Tx, groupID int64, position int, e settings.Entry) error {
	uid, err := mcpttRowID(ctx, tx, "mcptt_users", e.URI)
	if err != nil {
		return fmt.Errorf("member %s: %w", e.URI, err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO mcptt_group_members (group_id, user_id, position, user_priority, required,
		recvonly, affiliation_required) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (group_id, user_id) DO UPDATE SET user_priority = excluded.user_priority,
		required = excluded.required, recvonly = excluded.recvonly, affiliation_required = excluded.affiliation_required`,
		groupID, uid, position, e.UserPriority, e.Required, e.ReceiveOnly, e.AffiliationRequired)

	return err
}

// DeleteGroup deletes the group of URI uri; ErrNotFound if there is none. A group that is an implicit affiliation of
// a user is not deleted.
func (d *DB) DeleteMCPTTGroup(ctx context.Context, uri string) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "delete MCPTT group", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_groups", uri)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM mcptt_groups WHERE id = ?`, id)

		return err
	})
}

// PutMember adds e to the members of the group of URI group, after the others, or replaces the member of its MCPTT ID
// where it is; ErrNotFound if there is no such group.
func (d *DB) PutMCPTTMember(ctx context.Context, group string, e settings.Entry) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "put MCPTT member", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_groups", group)
		if err != nil {
			return err
		}

		var next int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM mcptt_group_members
			WHERE group_id = ?`, id).Scan(&next); err != nil {
			return err
		}

		return putMCPTTMember(ctx, tx, id, next, e)
	})
}

// DeleteMember removes the member of MCPTT ID mcpttID from the group of URI group; ErrNotFound if either is not.
func (d *DB) DeleteMCPTTMember(ctx context.Context, group, mcpttID string) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "delete MCPTT member", func(tx *sql.Tx) error {
		gid, err := mcpttRowID(ctx, tx, "mcptt_groups", group)
		if err != nil {
			return err
		}

		uid, err := mcpttRowID(ctx, tx, "mcptt_users", mcpttID)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `DELETE FROM mcptt_group_members WHERE group_id = ? AND user_id = ?`, gid, uid)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, ErrNotFound)
		}

		return nil
	})
}

// rowID is the id of the row of table, users or groups, of the identity of uri; ErrNotFound if none.
func mcpttRowID(ctx context.Context, tx *sql.Tx, table, uri string) (int64, error) {
	k, err := mcpttKey(uri)
	if err != nil {
		return 0, err
	}

	var id int64

	switch err := tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE key = ?`, k).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("%s: %w", uri, ErrNotFound)
	case err != nil:
		return 0, err
	}

	return id, nil
}

// key is the key of the identity of uri (settings.Key).
func mcpttKey(uri string) (string, error) {
	u, err := sip.ParseURI(uri)
	if err != nil {
		return "", fmt.Errorf("%s: %w", uri, err)
	}

	k := settings.Key(u)
	if k == "" {
		return "", fmt.Errorf("%s is neither a SIP nor a tel URI", uri)
	}

	return k, nil
}

// duplicate is err, ErrDuplicate if it is of a unique key.
func duplicate(err error) error {
	if se := (sqlite3.Error{}); errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrDuplicate
	}

	return err
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func nullDuration(v *time.Duration) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func intOf(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}

	n := int(v.Int64)

	return &n
}

func durationOf(v sql.NullInt64) *time.Duration {
	if !v.Valid {
		return nil
	}

	d := time.Duration(v.Int64)

	return &d
}
