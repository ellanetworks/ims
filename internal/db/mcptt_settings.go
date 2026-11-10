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

func (d *DB) GetMCPTTSettings(ctx context.Context) (_ settings.Settings, err error) {
	defer d.observe(poolRead, &err)()

	tx, err := d.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return settings.Settings{}, fmt.Errorf("get MCPTT settings: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	return getMCPTTSettings(ctx, tx)
}

func getMCPTTSettings(ctx context.Context, tx *sql.Tx) (settings.Settings, error) {
	var s settings.Settings

	var (
		t2            sql.NullInt64
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

var errOrphan = errors.New("of no user or group")

func orphan(what, value string) error { return fmt.Errorf("%s %s: %w", what, value, errOrphan) }

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

	_, err = run[struct{}](ctx, d, cmdUpdateMCPTTServiceConfiguration, "update MCPTT service configuration", c)

	return err
}

func applyUpdateMCPTTServiceConfiguration(a *applyCtx, c settings.ServiceConfiguration) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `UPDATE mcptt_service_configuration SET domain = ?, time_limit_ns = ?
		WHERE id = 1`, c.Domain, nullDuration(c.OnNetwork.TransmitTime.TimeLimit))
	if err == nil {
		err = checkAffected(res)
	}

	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func init() {
	register(cmdUpdateMCPTTServiceConfiguration, handle(applyUpdateMCPTTServiceConfiguration))
	register(cmdCreateMCPTTUser, handle(applyCreateMCPTTUser))
	register(cmdUpdateMCPTTUser, handle(applyUpdateMCPTTUser))
	register(cmdDeleteMCPTTUser, handle(applyDeleteMCPTTUser))
	register(cmdCreateMCPTTGroup, handle(applyCreateMCPTTGroup))
	register(cmdUpdateMCPTTGroup, handle(applyUpdateMCPTTGroup))
	register(cmdDeleteMCPTTGroup, handle(applyDeleteMCPTTGroup))
	register(cmdPutMCPTTMember, handle(applyPutMCPTTMember))
	register(cmdDeleteMCPTTMember, handle(applyDeleteMCPTTMember))
}

func (d *DB) CreateMCPTTUser(ctx context.Context, u settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdCreateMCPTTUser, "create MCPTT user", u)

	return err
}

func applyCreateMCPTTUser(a *applyCtx, p settings.User) (struct{}, error) {
	err := createMCPTTUserTx(a.ctx, a.tx, p)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func createMCPTTUserTx(ctx context.Context, tx *sql.Tx, u settings.User) error {
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
}

func (d *DB) UpdateMCPTTUser(ctx context.Context, u settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdUpdateMCPTTUser, "update MCPTT user", u)

	return err
}

func applyUpdateMCPTTUser(a *applyCtx, p settings.User) (struct{}, error) {
	err := updateMCPTTUserTx(a.ctx, a.tx, p)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func updateMCPTTUserTx(ctx context.Context, tx *sql.Tx, u settings.User) error {
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
}

func putMCPTTUserLists(ctx context.Context, tx *sql.Tx, id int64, u settings.User) error {
	for i, impu := range u.IMPUs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcptt_user_impus (user_id, position, impu) VALUES (?, ?, ?)`,
			id, i, impu); err != nil {
			return err
		}
	}

	return putMCPTTImplicitAffiliations(ctx, tx, id, u)
}

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

func (d *DB) DeleteMCPTTUser(ctx context.Context, mcpttID string) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdDeleteMCPTTUser, "delete MCPTT user", mcpttID)

	return err
}

func applyDeleteMCPTTUser(a *applyCtx, p string) (struct{}, error) {
	err := deleteMCPTTUserTx(a.ctx, a.tx, p)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func deleteMCPTTUserTx(ctx context.Context, tx *sql.Tx, mcpttID string) error {
	id, err := mcpttRowID(ctx, tx, "mcptt_users", mcpttID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM mcptt_users WHERE id = ?`, id)

	return err
}

func (d *DB) CreateMCPTTGroup(ctx context.Context, g settings.Group, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdCreateMCPTTGroup, "create MCPTT group", createMCPTTGroupCmd{G: g, Users: users})

	return err
}

type createMCPTTGroupCmd struct {
	G     settings.Group
	Users []settings.User
}

func applyCreateMCPTTGroup(a *applyCtx, p createMCPTTGroupCmd) (struct{}, error) {
	err := createMCPTTGroupTx(a.ctx, a.tx, p.G, p.Users)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func createMCPTTGroupTx(ctx context.Context, tx *sql.Tx, g settings.Group, users []settings.User) error {
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
}

func (d *DB) UpdateMCPTTGroup(ctx context.Context, g settings.Group, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdUpdateMCPTTGroup, "update MCPTT group", updateMCPTTGroupCmd{G: g, Users: users})

	return err
}

type updateMCPTTGroupCmd struct {
	G     settings.Group
	Users []settings.User
}

func applyUpdateMCPTTGroup(a *applyCtx, p updateMCPTTGroupCmd) (struct{}, error) {
	err := updateMCPTTGroupTx(a.ctx, a.tx, p.G, p.Users)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func updateMCPTTGroupTx(ctx context.Context, tx *sql.Tx, g settings.Group, users []settings.User) error {
	id, err := mcpttRowID(ctx, tx, "mcptt_groups", g.URI)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE mcptt_groups SET uri = ?, display_name = ?, t4_ns = ?, tng3_ns = ?,
		release_on_initiator_leave = ? WHERE id = ?`, g.URI, g.DisplayName, nullDuration(g.T4), int64(g.TNG3),
		g.ReleaseOnInitiatorLeave, id); err != nil {
		return err
	}

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
}

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

func (d *DB) DeleteMCPTTGroup(ctx context.Context, uri string) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdDeleteMCPTTGroup, "delete MCPTT group", uri)

	return err
}

func applyDeleteMCPTTGroup(a *applyCtx, p string) (struct{}, error) {
	err := deleteMCPTTGroupTx(a.ctx, a.tx, p)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func deleteMCPTTGroupTx(ctx context.Context, tx *sql.Tx, uri string) error {
	id, err := mcpttRowID(ctx, tx, "mcptt_groups", uri)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM mcptt_groups WHERE id = ?`, id)

	return err
}

func (d *DB) PutMCPTTMember(ctx context.Context, group string, e settings.Entry, users []settings.User) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdPutMCPTTMember, "put MCPTT member", putMCPTTMemberCmd{Group: group, Entry: e, Users: users})

	return err
}

type putMCPTTMemberCmd struct {
	Group string
	Entry settings.Entry
	Users []settings.User
}

func applyPutMCPTTMember(a *applyCtx, p putMCPTTMemberCmd) (struct{}, error) {
	err := putMCPTTMemberTx(a.ctx, a.tx, p.Group, p.Entry, p.Users)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func putMCPTTMemberTx(ctx context.Context, tx *sql.Tx, group string, e settings.Entry, users []settings.User) error {
	id, err := mcpttRowID(ctx, tx, "mcptt_groups", group)
	if err != nil {
		return err
	}

	uid, err := mcpttRowID(ctx, tx, "mcptt_users", e.URI)
	if err != nil {
		return fmt.Errorf("member %s: %w", e.URI, err)
	}

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
}

func (d *DB) DeleteMCPTTMember(ctx context.Context, group, mcpttID string) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdDeleteMCPTTMember, "delete MCPTT member", deleteMCPTTMemberCmd{Group: group, MCPTTID: mcpttID})

	return err
}

type deleteMCPTTMemberCmd struct {
	Group   string
	MCPTTID string
}

func applyDeleteMCPTTMember(a *applyCtx, p deleteMCPTTMemberCmd) (struct{}, error) {
	err := deleteMCPTTMemberTx(a.ctx, a.tx, p.Group, p.MCPTTID)
	if err == nil {
		a.events.MCPTT = true
	}

	return struct{}{}, err
}

func deleteMCPTTMemberTx(ctx context.Context, tx *sql.Tx, group string, mcpttID string) error {
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
}

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

func mcpttKey(uri string) (string, error) {
	u, err := sip.ParseURI(uri)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrInvalid, uri, err)
	}

	k := settings.Key(u)
	if k == "" {
		return "", fmt.Errorf("%w: %s is neither a SIP nor a tel URI", ErrInvalid, uri)
	}

	return k, nil
}

func duplicate(err error) error {
	if se := (sqlite3.Error{}); errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrDuplicate
	}

	return err
}

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
