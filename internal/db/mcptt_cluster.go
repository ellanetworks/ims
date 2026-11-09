package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/ims/internal/mcptt/affiliation"
	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/sip"
)

type putMCPTTAffiliation struct {
	NodeID    string   `json:"node_id"`
	Group     string   `json:"group"`
	User      string   `json:"user"`
	Clients   []string `json:"clients"`
	ExpiresAt int64    `json:"expires_at"`
}

// TS 24.379 clause 9.2.2.3.2
func (d *DB) PutMCPTTAffiliation(ctx context.Context, nodeID string, r affiliation.Row) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdPutMCPTTAffiliation, "save the MCPTT group affiliation", putMCPTTAffiliation{
		NodeID: nodeID, Group: r.Group.String(), User: r.User.String(), Clients: nonNilList(r.Clients),
		ExpiresAt: r.Expires.UnixMilli(),
	})

	return err
}

func applyPutMCPTTAffiliation(a *applyCtx, p putMCPTTAffiliation) (struct{}, error) {
	group, err := sip.ParseURI(p.Group)
	if err != nil {
		return struct{}{}, fmt.Errorf("%w: group %q: %w", ErrInvalid, p.Group, err)
	}

	user, err := sip.ParseURI(p.User)
	if err != nil {
		return struct{}{}, fmt.Errorf("%w: MCPTT ID %q: %w", ErrInvalid, p.User, err)
	}

	gk, uk := mcpttsettings.Key(group), mcpttsettings.Key(user)
	if gk == "" || uk == "" {
		return struct{}{}, fmt.Errorf("%w: group %q or MCPTT ID %q", ErrInvalid, p.Group, p.User)
	}

	if len(p.Clients) == 0 {
		_, err := a.tx.ExecContext(a.ctx, `DELETE FROM mcptt_group_affiliations WHERE group_key = ? AND user_key = ? AND node_id = ?`,
			gk, uk, p.NodeID)

		return struct{}{}, err
	}

	if err := checkMember(a, p.NodeID); err != nil {
		return struct{}{}, err
	}

	clients, err := json.Marshal(p.Clients)
	if err != nil {
		return struct{}{}, err
	}

	_, err = a.tx.ExecContext(a.ctx, `INSERT INTO mcptt_group_affiliations (group_key, user_key, node_id, group_uri, mcptt_id,
		clients, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (group_key, user_key, node_id) DO UPDATE SET
		group_uri = excluded.group_uri, mcptt_id = excluded.mcptt_id, clients = excluded.clients, expires_at = excluded.expires_at`,
		gk, uk, p.NodeID, p.Group, p.User, string(clients), p.ExpiresAt)

	return struct{}{}, err
}

func checkMember(a *applyCtx, nodeID string) error {
	_, err := member(a.ctx, a.tx, nodeID)
	return err
}

// TS 24.379 clause 9.2.2.3.2
func (d *DB) MCPTTGroupAffiliations(ctx context.Context, group sip.URI) (_ []affiliation.Row, err error) {
	defer d.observe(poolRead, &err)()

	return d.groupAffiliations(ctx, `WHERE group_key = ?`, mcpttsettings.Key(group))
}

func (d *DB) MCPTTNodeAffiliations(ctx context.Context, nodeID string) (_ []affiliation.Row, err error) {
	defer d.observe(poolRead, &err)()

	return d.groupAffiliations(ctx, `WHERE node_id = ?`, nodeID)
}

func (d *DB) groupAffiliations(ctx context.Context, where string, arg any) ([]affiliation.Row, error) {
	rows, err := d.reader().QueryContext(ctx, `SELECT group_uri, mcptt_id, node_id, clients, expires_at
		FROM mcptt_group_affiliations `+where+` ORDER BY group_key, user_key, node_id`, arg)
	if err != nil {
		return nil, fmt.Errorf("get MCPTT group affiliations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []affiliation.Row

	for rows.Next() {
		var (
			r                    affiliation.Row
			group, user, clients string
			expires              int64
		)

		if err := rows.Scan(&group, &user, &r.Node, &clients, &expires); err != nil {
			return nil, fmt.Errorf("get MCPTT group affiliations: %w", err)
		}

		if r.Group, err = sip.ParseURI(group); err != nil {
			continue
		}

		if r.User, err = sip.ParseURI(user); err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(clients), &r.Clients); err != nil {
			continue
		}

		r.Expires = time.UnixMilli(expires)
		out = append(out, r)
	}

	return out, rows.Err()
}

// TS 23.379 clause 7.4.2.3.2
type MCPTTGroupCall struct {
	Group     string
	NodeID    string
	Claim     string
	StartedAt time.Time
}

type claimMCPTTGroupCall struct {
	Group  string `json:"group"`
	NodeID string `json:"node_id"`
	Claim  string `json:"claim"`
}

// TS 23.379 clause 7.4.2.3.2
func (d *DB) ClaimMCPTTGroupCall(ctx context.Context, group sip.URI, nodeID, claim string) (_ MCPTTGroupCall, err error) {
	defer d.observe(poolWrite, &err)()

	return run[MCPTTGroupCall](ctx, d, cmdClaimMCPTTGroupCall, "claim the MCPTT group call", claimMCPTTGroupCall{
		Group: group.String(), NodeID: nodeID, Claim: claim,
	})
}

func applyClaimMCPTTGroupCall(a *applyCtx, p claimMCPTTGroupCall) (MCPTTGroupCall, error) {
	group, err := sip.ParseURI(p.Group)
	if err != nil {
		return MCPTTGroupCall{}, fmt.Errorf("%w: group %q: %w", ErrInvalid, p.Group, err)
	}

	gk := mcpttsettings.Key(group)
	if gk == "" || p.Claim == "" {
		return MCPTTGroupCall{}, fmt.Errorf("%w: group %q or claim %q", ErrInvalid, p.Group, p.Claim)
	}

	if err := checkMember(a, p.NodeID); err != nil {
		return MCPTTGroupCall{}, err
	}

	c, err := groupCall(a.ctx, a.tx, gk)

	switch {
	case err == nil && c.NodeID != p.NodeID:
		return c, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return MCPTTGroupCall{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO mcptt_group_calls (group_key, group_uri, node_id, claim, started_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (group_key) DO UPDATE SET group_uri = excluded.group_uri, claim = excluded.claim,
		started_at = excluded.started_at`, gk, p.Group, p.NodeID, p.Claim, a.leaderTime); err != nil {
		return MCPTTGroupCall{}, err
	}

	return MCPTTGroupCall{Group: p.Group, NodeID: p.NodeID, Claim: p.Claim, StartedAt: time.Unix(0, a.leaderTime).UTC()}, nil
}

type releaseMCPTTGroupCall struct {
	Group  string `json:"group"`
	NodeID string `json:"node_id"`
	Claim  string `json:"claim"`
}

func (d *DB) ReleaseMCPTTGroupCall(ctx context.Context, group sip.URI, nodeID, claim string) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdReleaseMCPTTGroupCall, "release the MCPTT group call", releaseMCPTTGroupCall{
		Group: group.String(), NodeID: nodeID, Claim: claim,
	})

	return err
}

func applyReleaseMCPTTGroupCall(a *applyCtx, p releaseMCPTTGroupCall) (struct{}, error) {
	group, err := sip.ParseURI(p.Group)
	if err != nil {
		return struct{}{}, fmt.Errorf("%w: group %q: %w", ErrInvalid, p.Group, err)
	}

	_, err = a.tx.ExecContext(a.ctx, `DELETE FROM mcptt_group_calls WHERE group_key = ? AND node_id = ? AND claim = ?`,
		mcpttsettings.Key(group), p.NodeID, p.Claim)

	return struct{}{}, err
}

func (d *DB) ReleaseMCPTTGroupCallsOf(ctx context.Context, nodeID string) (_ int64, err error) {
	defer d.observe(poolWrite, &err)()

	return run[int64](ctx, d, cmdReleaseMCPTTGroupCallsOf, "release the MCPTT group calls of a node", nodeID)
}

func applyReleaseMCPTTGroupCallsOf(a *applyCtx, nodeID string) (int64, error) {
	res, err := a.tx.ExecContext(a.ctx, `DELETE FROM mcptt_group_calls WHERE node_id = ?`, nodeID)
	if err != nil {
		return 0, err
	}

	return res.RowsAffected()
}

func deleteMCPTTRowsOf(a *applyCtx, nodeID string) error {
	for _, q := range []string{`DELETE FROM mcptt_group_calls WHERE node_id = ?`, `DELETE FROM mcptt_group_affiliations WHERE node_id = ?`} {
		if _, err := a.tx.ExecContext(a.ctx, q, nodeID); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) MCPTTGroupCall(ctx context.Context, group sip.URI) (_ MCPTTGroupCall, err error) {
	defer d.observe(poolRead, &err)()

	return groupCall(ctx, d.reader(), mcpttsettings.Key(group))
}

func groupCall(ctx context.Context, q querier, gk string) (MCPTTGroupCall, error) {
	var (
		c       MCPTTGroupCall
		started int64
	)

	err := q.QueryRowContext(ctx, `SELECT group_uri, node_id, claim, started_at FROM mcptt_group_calls WHERE group_key = ?`, gk).
		Scan(&c.Group, &c.NodeID, &c.Claim, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPTTGroupCall{}, fmt.Errorf("MCPTT group call %s: %w", gk, ErrNotFound)
	}

	if err != nil {
		return MCPTTGroupCall{}, fmt.Errorf("get the MCPTT group call: %w", err)
	}

	c.StartedAt = time.Unix(0, started).UTC()

	return c, nil
}

func (d *DB) MCPTTGroupCalls(ctx context.Context) (_ []MCPTTGroupCall, err error) {
	defer d.observe(poolRead, &err)()

	rows, err := d.reader().QueryContext(ctx, `SELECT group_uri, node_id, claim, started_at FROM mcptt_group_calls ORDER BY group_key`)
	if err != nil {
		return nil, fmt.Errorf("list MCPTT group calls: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []MCPTTGroupCall

	for rows.Next() {
		var (
			c       MCPTTGroupCall
			started int64
		)

		if err := rows.Scan(&c.Group, &c.NodeID, &c.Claim, &started); err != nil {
			return nil, fmt.Errorf("list MCPTT group calls: %w", err)
		}

		c.StartedAt = time.Unix(0, started).UTC()
		out = append(out, c)
	}

	return out, rows.Err()
}

func init() {
	register(cmdPutMCPTTAffiliation, handle(applyPutMCPTTAffiliation))
	register(cmdClaimMCPTTGroupCall, handle(applyClaimMCPTTGroupCall))
	register(cmdReleaseMCPTTGroupCall, handle(applyReleaseMCPTTGroupCall))
	register(cmdReleaseMCPTTGroupCallsOf, handle(applyReleaseMCPTTGroupCallsOf))
}
