package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

	var (
		t2 sql.NullInt64
		// users and groups are the indexes of the users and groups, by row id.
		users, groups = map[int64]int{}, map[int64]int{}
	)

	if err := tx.QueryRowContext(ctx, `SELECT domain, time_limit_ns FROM mcptt_service_configuration WHERE id = 1`).Scan(
		&s.ServiceConfiguration.Domain, &t2); err != nil {
		return settings.Settings{}, fmt.Errorf("get MCPTT service configuration: %w", err)
	}

	s.ServiceConfiguration.OnNetwork.TransmitTime.TimeLimit = durationOf(t2)

	err := queryRows(ctx, tx, "get MCPTT users", `SELECT id, mcptt_id, alias FROM mcptt_users ORDER BY id`,
		func(rows *sql.Rows) error {
			var (
				id int64
				u  settings.User
			)

			if err := rows.Scan(&id, &u.MCPTTUserID, &u.Alias); err != nil {
				return err
			}

			users[id] = len(s.Users)
			s.Users = append(s.Users, u)

			return nil
		})
	if err != nil {
		return settings.Settings{}, err
	}

	// The groups are prearranged groups, the only kind supported.
	err = queryRows(ctx, tx, "get MCPTT groups", `SELECT id, uri, display_name, t4_ns, tng3_ns, release_on_initiator_leave
		FROM mcptt_groups ORDER BY id`, func(rows *sql.Rows) error {
		var (
			id   int64
			g    = settings.Group{InviteMembers: true}
			t4   sql.NullInt64
			tng3 int64
		)

		if err := rows.Scan(&id, &g.URI, &g.DisplayName, &t4, &tng3, &g.ReleaseOnInitiatorLeave); err != nil {
			return err
		}

		g.T4, g.TNG3 = durationOf(t4), time.Duration(tng3)
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

	if err := queryRows(ctx, tx, "get MCPTT members", `SELECT group_id, user_id, recvonly FROM mcptt_group_members
		ORDER BY group_id, position`, func(rows *sql.Rows) error {
		var (
			gid, uid int64
			e        settings.Entry
		)

		if err := rows.Scan(&gid, &uid, &e.ReceiveOnly); err != nil {
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

func (d *DB) UpdateMCPTTServiceConfiguration(ctx context.Context, c settings.ServiceConfiguration) (err error) {
	defer d.observe(poolWrite, &err)()

	res, err := d.conn.ExecContext(ctx, `UPDATE mcptt_service_configuration SET domain = ?, time_limit_ns = ? WHERE id = 1`,
		c.Domain, nullDuration(c.OnNetwork.TransmitTime.TimeLimit))

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

		res, err := tx.ExecContext(ctx, `INSERT INTO mcptt_users (mcptt_id, key, alias) VALUES (?, ?, ?)`, u.MCPTTUserID, k,
			u.Alias)
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

		if _, err := tx.ExecContext(ctx, `UPDATE mcptt_users SET mcptt_id = ?, alias = ? WHERE id = ?`, u.MCPTTUserID, u.Alias,
			id); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_user_impus WHERE user_id = ?`, id); err != nil {
			return err
		}

		return putMCPTTUserLists(ctx, tx, id, u)
	})
}

// putUserLists inserts the IMPUs and replaces the implicit affiliations of u, of row id.
func putMCPTTUserLists(ctx context.Context, tx *sql.Tx, id int64, u settings.User) error {
	for i, impu := range u.IMPUs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_user_impus (user_id, position, impu) VALUES (?, ?, ?)`,
			id, i, impu); err != nil {
			return err
		}
	}

	return putMCPTTImplicitAffiliations(ctx, tx, id, u)
}

// putMCPTTAffiliationsOf replaces the implicit affiliations of users, of a change of a group or member.
func putMCPTTAffiliationsOf(ctx context.Context, tx *sql.Tx, users []settings.User) error {
	for _, u := range users {
		id, err := mcpttRowID(ctx, tx, "mcptt_users", u.MCPTTUserID)
		if err != nil {
			return err
		}

		if err := putMCPTTImplicitAffiliations(ctx, tx, id, u); err != nil {
			return err
		}
	}

	return nil
}

// putMCPTTImplicitAffiliations replaces the implicit affiliations of u, of row id: groups it is a member of.
func putMCPTTImplicitAffiliations(ctx context.Context, tx *sql.Tx, id int64, u settings.User) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_user_implicit_affiliations WHERE user_id = ?`, id); err != nil {
		return err
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

// DeleteUser deletes the user of MCPTT ID mcpttID, and its memberships; ErrNotFound if there is none.
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

// CreateGroup adds g, its members users of the database, and replaces the implicit affiliations of users; ErrDuplicate
// if a group has its URI.
func (d *DB) CreateMCPTTGroup(ctx context.Context, g settings.Group, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "create MCPTT group", func(tx *sql.Tx) error {
		k, err := mcpttKey(g.URI)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO mcptt_groups (uri, key, display_name, t4_ns, tng3_ns,
			release_on_initiator_leave) VALUES (?, ?, ?, ?, ?, ?)`, g.URI, k, g.DisplayName, nullDuration(g.T4), int64(g.TNG3),
			g.ReleaseOnInitiatorLeave)
		if err != nil {
			return duplicate(err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return err
		}

		if err := putMCPTTGroupLists(ctx, tx, id, g); err != nil {
			return err
		}

		return putMCPTTAffiliationsOf(ctx, tx, users)
	})
}

// UpdateGroup replaces the group of g's URI with g, members included, and the implicit affiliations of users;
// ErrNotFound if there is none. A member removed goes from the implicit affiliations of its user.
func (d *DB) UpdateMCPTTGroup(ctx context.Context, g settings.Group, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update MCPTT group", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_groups", g.URI)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE mcptt_groups SET uri = ?, display_name = ?, t4_ns = ?, tng3_ns = ?,
			release_on_initiator_leave = ? WHERE id = ?`, g.URI, g.DisplayName, nullDuration(g.T4), int64(g.TNG3),
			g.ReleaseOnInitiatorLeave, id); err != nil {
			return err
		}

		// The members kept are updated, not deleted: their implicit affiliations go with their rows.
		kept := make([]any, 0, len(g.List.Entries)+1)
		kept = append(kept, id)

		for _, e := range g.List.Entries {
			uid, err := mcpttRowID(ctx, tx, "mcptt_users", e.URI)
			if err != nil {
				return fmt.Errorf("member %s: %w", e.URI, err)
			}

			kept = append(kept, uid)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM mcptt_group_members WHERE group_id = ? AND user_id NOT IN (SELECT value
			FROM json_each(?))`, id, jsonInts(kept[1:])); err != nil {
			return err
		}

		if err := putMCPTTGroupLists(ctx, tx, id, g); err != nil {
			return err
		}

		return putMCPTTAffiliationsOf(ctx, tx, users)
	})
}

// putGroupLists inserts or updates the members of g, of row id.
func putMCPTTGroupLists(ctx context.Context, tx *sql.Tx, id int64, g settings.Group) error {
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

	_, err = tx.ExecContext(ctx, `INSERT INTO mcptt_group_members (group_id, user_id, position, recvonly) VALUES (?, ?, ?, ?)
		ON CONFLICT (group_id, user_id) DO UPDATE SET position = excluded.position, recvonly = excluded.recvonly`,
		groupID, uid, position, e.ReceiveOnly)

	return err
}

// DeleteGroup deletes the group of URI uri, its members and their implicit affiliations to it; ErrNotFound if there
// is none.
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
// where it is, and replaces the implicit affiliations of users; ErrNotFound if there is no such group.
func (d *DB) PutMCPTTMember(ctx context.Context, group string, e settings.Entry, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "put MCPTT member", func(tx *sql.Tx) error {
		id, err := mcpttRowID(ctx, tx, "mcptt_groups", group)
		if err != nil {
			return err
		}

		uid, err := mcpttRowID(ctx, tx, "mcptt_users", e.URI)
		if err != nil {
			return fmt.Errorf("member %s: %w", e.URI, err)
		}

		// Where the member is, or after the others.
		var position int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT position FROM mcptt_group_members
			WHERE group_id = ? AND user_id = ?), (SELECT COALESCE(MAX(position), -1) + 1 FROM mcptt_group_members
			WHERE group_id = ?))`, id, uid, id).Scan(&position); err != nil {
			return err
		}

		if err := putMCPTTMember(ctx, tx, id, position, e); err != nil {
			return err
		}

		return putMCPTTAffiliationsOf(ctx, tx, users)
	})
}

// DeleteMember removes the member of MCPTT ID mcpttID from the group of URI group, and the group from the implicit
// affiliations of its user; ErrNotFound if either is not.
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

// jsonInts is ids as a JSON array, for json_each.
func jsonInts(ids []any) string {
	var b strings.Builder

	b.WriteByte('[')

	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprint(&b, id)
	}

	b.WriteByte(']')

	return b.String()
}

func nullDuration(v *time.Duration) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func durationOf(v sql.NullInt64) *time.Duration {
	if !v.Valid {
		return nil
	}

	d := time.Duration(v.Int64)

	return &d
}
