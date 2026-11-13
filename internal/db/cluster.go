package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/mattn/go-sqlite3"
)

var (
	ErrTokenUsed    = errors.New("the join token was already used")
	ErrTokenExpired = errors.New("the join token expired")
)

type Member struct {
	NodeID         string     `json:"node_id"`
	Address        string     `json:"address"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	SchemaVersion  int        `json:"schema_version"`
	Version        string     `json:"version"`
	JoinedAt       time.Time  `json:"joined_at"`
	DrainState     DrainState `json:"drain_state,omitempty"`
	DisplayName    string     `json:"display_name,omitempty"`
	DiameterLabel  string     `json:"diameter_label,omitempty"`
	LabelGenerated bool       `json:"label_generated,omitempty"`
	APIAddress     string     `json:"api_address,omitempty"`
	SCSCFAddresses []string   `json:"scscf_addresses,omitempty"`
	MCPTTAddresses []string   `json:"mcptt_addresses,omitempty"`
}

type DrainState string

const (
	DrainActive   DrainState = "active"
	DrainDraining DrainState = "draining"
	DrainDrained  DrainState = "drained"
)

const (
	memberColumns = `node_id, address, fingerprint, schema_version, version, joined_at, diameter_label, label_generated,
		api_address, scscf_addresses, mcptt_addresses`
	memberReadColumns = memberColumns + `, drain_state, display_name`
)

func scanMember(scan func(...any) error) (Member, error) {
	var (
		m           Member
		fingerprint sql.NullString
		joined      int64
		scscf       string
		mcptt       string
	)

	if err := scan(&m.NodeID, &m.Address, &fingerprint, &m.SchemaVersion, &m.Version,
		&joined, &m.DiameterLabel, &m.LabelGenerated, &m.APIAddress, &scscf, &mcptt, &m.DrainState,
		&m.DisplayName); err != nil {
		return Member{}, err
	}

	m.Fingerprint, m.JoinedAt = fingerprint.String, time.Unix(0, joined).UTC()

	if scscf != "" {
		m.SCSCFAddresses = strings.Split(scscf, ",")
	}

	if mcptt != "" {
		m.MCPTTAddresses = strings.Split(mcptt, ",")
	}

	return m, nil
}

func (d *DB) Members(ctx context.Context) (_ []Member, err error) {
	ctx, done := d.observe(ctx, "Members", poolRead)
	defer done(&err)

	return members(ctx, d.reader())
}

func members(ctx context.Context, q querier) ([]Member, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+memberReadColumns+` FROM cluster_members ORDER BY joined_at, node_id`)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Member

	for rows.Next() {
		m, err := scanMember(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}

		out = append(out, m)
	}

	return out, rows.Err()
}

func (d *DB) Member(ctx context.Context, nodeID string) (_ Member, err error) {
	ctx, done := d.observe(ctx, "Member", poolRead)
	defer done(&err)

	return member(ctx, d.reader(), nodeID)
}

func member(ctx context.Context, q querier, nodeID string) (Member, error) {
	m, err := scanMember(q.QueryRowContext(ctx, `SELECT `+memberReadColumns+` FROM cluster_members WHERE node_id = ?`,
		nodeID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, fmt.Errorf("member %s: %w", nodeID, ErrNotFound)
	}

	return m, err
}

func (d *DB) RegisterMember(ctx context.Context, m Member) error {
	return d.RegisterMemberVia(ctx, d.currentProposer(), m)
}

func (d *DB) RegisterMemberVia(ctx context.Context, via Proposer, m Member) (err error) {
	ctx, done := d.observe(ctx, "RegisterMemberVia", poolWrite)
	defer done(&err)

	_, _, err = submitVia[struct{}](ctx, via, cmdRegisterMember, m)

	return err
}

func withLabel(m Member) (Member, error) {
	if m.DiameterLabel == "" {
		m.DiameterLabel, m.LabelGenerated = settings.NodeLabel(m.NodeID), true
	}

	if err := settings.ValidLabel(m.DiameterLabel); err != nil {
		return Member{}, fmt.Errorf("%w: Diameter host label %q %w", ErrInvalid, m.DiameterLabel, err)
	}

	return m, nil
}

func checkLabel(a *applyCtx, m Member) error {
	var holder string

	err := a.tx.QueryRowContext(a.ctx, `SELECT node_id FROM cluster_members WHERE diameter_label = ? AND node_id != ?`,
		m.DiameterLabel, m.NodeID).Scan(&holder)

	switch {
	case err == nil:
		return fmt.Errorf("%w: the Diameter host label %s is member %s's: remove that member first, or choose another label",
			settings.ErrConflict, m.DiameterLabel, holder)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	s, err := getSettings(a.ctx, a.tx)
	if err != nil {
		return err
	}

	return s.Operator.HostConflict(s.Peers, m.DiameterLabel)
}

func applyRegisterMember(a *applyCtx, m Member) (struct{}, error) {
	m, err := withLabel(m)
	if err != nil {
		return struct{}{}, err
	}

	existing, err := member(a.ctx, a.tx, m.NodeID)

	switch {
	case errors.Is(err, ErrNotFound):
		return struct{}{}, insertMember(a, m)
	case err != nil:
		return struct{}{}, err
	}

	relabeled := existing.DiameterLabel != m.DiameterLabel
	if relabeled {
		if err := checkLabel(a, m); err != nil {
			return struct{}{}, err
		}
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_members SET address = ?, fingerprint = ?,
		schema_version = ?, version = ?, diameter_label = ?, label_generated = ?, api_address = ?,
		scscf_addresses = ?, mcptt_addresses = ? WHERE node_id = ?`, m.Address, nullableString(m.Fingerprint), m.SchemaVersion,
		m.Version, m.DiameterLabel, m.LabelGenerated, m.APIAddress, strings.Join(m.SCSCFAddresses, ","),
		strings.Join(m.MCPTTAddresses, ","), m.NodeID); err != nil {
		if isConstraint(err, sqlite3.ErrConstraintUnique) || isConstraint(err, sqlite3.ErrConstraintPrimaryKey) {
			return struct{}{}, fmt.Errorf("%w: another member has the address %s or the certificate", settings.ErrConflict,
				m.Address)
		}

		return struct{}{}, err
	}

	if existing.Fingerprint != m.Fingerprint || existing.Address != m.Address || relabeled ||
		existing.LabelGenerated != m.LabelGenerated || existing.APIAddress != m.APIAddress ||
		!slices.Equal(existing.SCSCFAddresses, m.SCSCFAddresses) ||
		!slices.Equal(existing.MCPTTAddresses, m.MCPTTAddresses) {
		a.events.Members = true
	}

	if relabeled {
		a.events.Settings = true
	}

	return struct{}{}, nil
}

func insertMember(a *applyCtx, m Member) error {
	if err := checkTombstone(a, m.NodeID); err != nil {
		return err
	}

	if err := checkLabel(a, m); err != nil {
		return err
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO cluster_members (`+memberColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.NodeID, m.Address, nullableString(m.Fingerprint), m.SchemaVersion, m.Version,
		a.leaderTime, m.DiameterLabel, m.LabelGenerated, m.APIAddress,
		strings.Join(m.SCSCFAddresses, ","), strings.Join(m.MCPTTAddresses, ",")); err != nil {
		if isConstraint(err, sqlite3.ErrConstraintUnique) || isConstraint(err, sqlite3.ErrConstraintPrimaryKey) {
			return fmt.Errorf("%w: another member has the address %s or the certificate", settings.ErrConflict, m.Address)
		}

		return err
	}

	a.events.Members = true
	a.events.Settings = true

	return nil
}

type joinToken struct {
	TokenID   string `json:"token_id"`
	ExpiresAt int64  `json:"expires_at"`
}

func (d *DB) CreateJoinToken(ctx context.Context, tokenID string, expiresAt time.Time) (err error) {
	ctx, done := d.observe(ctx, "CreateJoinToken", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdCreateJoinToken, joinToken{TokenID: tokenID, ExpiresAt: expiresAt.UnixNano()})

	return err
}

func applyCreateJoinToken(a *applyCtx, t joinToken) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `INSERT INTO cluster_join_tokens (token_id, expires_at) VALUES (?, ?)`,
		t.TokenID, t.ExpiresAt)
	if isConstraint(err, sqlite3.ErrConstraintUnique) || isConstraint(err, sqlite3.ErrConstraintPrimaryKey) {
		return struct{}{}, fmt.Errorf("join token %s: %w", t.TokenID, ErrDuplicate)
	}

	return struct{}{}, err
}

type admitMember struct {
	TokenID string `json:"token_id"`
	Member  Member `json:"member"`
}

func (d *DB) AdmitMember(ctx context.Context, tokenID string, m Member) (err error) {
	ctx, done := d.observe(ctx, "AdmitMember", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdAdmitMember, admitMember{TokenID: tokenID, Member: m})

	return err
}

func applyAdmitMember(a *applyCtx, p admitMember) (struct{}, error) {
	if p.TokenID == "" {
		return admitWithSecret(a, p.Member)
	}

	var (
		expiresAt   int64
		nodeID      sql.NullString
		fingerprint sql.NullString
	)

	err := a.tx.QueryRowContext(a.ctx, `SELECT expires_at, node_id, fingerprint FROM cluster_join_tokens
		WHERE token_id = ?`, p.TokenID).Scan(&expiresAt, &nodeID, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrNotFound)
	}

	if err != nil {
		return struct{}{}, err
	}

	m, err := withLabel(p.Member)
	if err != nil {
		return struct{}{}, err
	}

	if nodeID.Valid {
		if nodeID.String != m.NodeID || fingerprint.String != m.Fingerprint {
			return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrTokenUsed)
		}

		return applyRegisterMember(a, m)
	}

	if a.leaderTime > expiresAt {
		return struct{}{}, fmt.Errorf("join token %s: %w", p.TokenID, ErrTokenExpired)
	}

	existing, err := member(a.ctx, a.tx, m.NodeID)
	admitted := err == nil

	switch {
	case admitted && (existing.Fingerprint != m.Fingerprint || existing.Address != m.Address):
		return struct{}{}, fmt.Errorf("%w: node %s is already a member", settings.ErrConflict, m.NodeID)
	case !admitted && !errors.Is(err, ErrNotFound):
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_join_tokens SET node_id = ?, fingerprint = ? WHERE token_id = ?`,
		m.NodeID, m.Fingerprint, p.TokenID); err != nil {
		return struct{}{}, err
	}

	if admitted {
		return applyRegisterMember(a, m)
	}

	return struct{}{}, insertMember(a, m)
}

func admitWithSecret(a *applyCtx, m Member) (struct{}, error) {
	m, err := withLabel(m)
	if err != nil {
		return struct{}{}, err
	}

	existing, err := member(a.ctx, a.tx, m.NodeID)

	switch {
	case err == nil && (existing.Fingerprint != m.Fingerprint || existing.Address != m.Address):
		return struct{}{}, fmt.Errorf("%w: node %s is already a member", settings.ErrConflict, m.NodeID)
	case err == nil:
		return applyRegisterMember(a, m)
	case !errors.Is(err, ErrNotFound):
		return struct{}{}, err
	}

	return struct{}{}, insertMember(a, m)
}

type RemovalReason string

const (
	RemovedDrained RemovalReason = "drained"
	RemovedForced  RemovalReason = "forced"
)

type Tombstone struct {
	NodeID      string        `json:"node_id"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	RemovedAt   time.Time     `json:"removed_at"`
	Reason      RemovalReason `json:"reason"`
}

type removeMember struct {
	NodeID string        `json:"node_id"`
	Reason RemovalReason `json:"reason"`
}

func (r *removeMember) UnmarshalJSON(b []byte) error {
	var id string
	if err := json.Unmarshal(b, &id); err == nil {
		*r = removeMember{NodeID: id, Reason: RemovedForced}
		return nil
	}

	type plain removeMember

	return json.Unmarshal(b, (*plain)(r))
}

func (d *DB) RemoveMember(ctx context.Context, nodeID string, reason RemovalReason) (err error) {
	ctx, done := d.observe(ctx, "RemoveMember", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdRemoveMember, removeMember{NodeID: nodeID, Reason: reason})

	return err
}

func applyRemoveMember(a *applyCtx, p removeMember) (struct{}, error) {
	switch p.Reason {
	case RemovedDrained, RemovedForced:
	default:
		return struct{}{}, fmt.Errorf("%w: removal reason %q", ErrInvalid, p.Reason)
	}

	var fingerprint sql.NullString

	err := a.tx.QueryRowContext(a.ctx, `SELECT fingerprint FROM cluster_members WHERE node_id = ?`, p.NodeID).
		Scan(&fingerprint)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return struct{}{}, fmt.Errorf("member %s: %w", p.NodeID, ErrNotFound)
	case err != nil:
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM cluster_members WHERE node_id = ?`, p.NodeID); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO cluster_removed_members (node_id, fingerprint, removed_at, reason)
		VALUES (?, ?, ?, ?) ON CONFLICT (node_id) DO UPDATE SET fingerprint = excluded.fingerprint,
		removed_at = excluded.removed_at, reason = excluded.reason`, p.NodeID, fingerprint, a.leaderTime,
		p.Reason); err != nil {
		return struct{}{}, err
	}

	if _, err := closeCallRecordsOf(a, p.NodeID, nil); err != nil {
		return struct{}{}, err
	}

	if err := deleteMCPTTRowsOf(a, p.NodeID); err != nil {
		return struct{}{}, err
	}

	a.events.Members = true
	a.events.Settings = true
	a.events.Removed = append(a.events.Removed, p.NodeID)

	return struct{}{}, nil
}

func (d *DB) Tombstones(ctx context.Context) (_ []Tombstone, err error) {
	ctx, done := d.observe(ctx, "Tombstones", poolRead)
	defer done(&err)

	rows, err := d.reader().QueryContext(ctx, `SELECT node_id, fingerprint, removed_at, reason
		FROM cluster_removed_members ORDER BY removed_at, node_id`)
	if err != nil {
		return nil, fmt.Errorf("list removed members: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Tombstone

	for rows.Next() {
		var (
			t           Tombstone
			fingerprint sql.NullString
			removed     int64
		)

		if err := rows.Scan(&t.NodeID, &fingerprint, &removed, &t.Reason); err != nil {
			return nil, fmt.Errorf("list removed members: %w", err)
		}

		t.Fingerprint, t.RemovedAt = fingerprint.String, time.Unix(0, removed).UTC()
		out = append(out, t)
	}

	return out, rows.Err()
}

func checkTombstone(a *applyCtx, nodeID string) error {
	var removed int64

	err := a.tx.QueryRowContext(a.ctx, `SELECT removed_at FROM cluster_removed_members WHERE node_id = ?`, nodeID).
		Scan(&removed)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}

	return fmt.Errorf("%w: node %s was removed from the cluster: delete its data directory to join it again",
		settings.ErrConflict, nodeID)
}

const MaxDisplayNameLength = 63

type displayName struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
}

func (d *DB) SetDisplayName(ctx context.Context, nodeID, name string) (err error) {
	ctx, done := d.observe(ctx, "SetDisplayName", poolWrite)
	defer done(&err)

	if len(name) > MaxDisplayNameLength {
		return fmt.Errorf("%w: the display name must be at most %d bytes", ErrInvalid, MaxDisplayNameLength)
	}

	_, err = submit[struct{}](ctx, d, cmdSetDisplayName, displayName{NodeID: nodeID, Name: name})

	return err
}

func applySetDisplayName(a *applyCtx, p displayName) (struct{}, error) {
	if len(p.Name) > MaxDisplayNameLength {
		return struct{}{}, fmt.Errorf("%w: display name of %d bytes", ErrInvalid, len(p.Name))
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_members SET display_name = ? WHERE node_id = ?`, p.Name, p.NodeID)
	if err != nil {
		return struct{}{}, err
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return struct{}{}, fmt.Errorf("member %s: %w", p.NodeID, ErrNotFound)
	}

	a.events.Members = true

	return struct{}{}, nil
}

func (d *DB) DeleteExpiredJoinTokens(ctx context.Context, before time.Time) (err error) {
	ctx, done := d.observe(ctx, "DeleteExpiredJoinTokens", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdDeleteExpiredJoinTokens, before.UnixNano())

	return err
}

func applyDeleteExpiredJoinTokens(a *applyCtx, before int64) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `DELETE FROM cluster_join_tokens WHERE expires_at < ?`, before)

	return struct{}{}, err
}

type drainState struct {
	NodeID string     `json:"node_id"`
	State  DrainState `json:"state"`
}

func (d *DB) SetDrainState(ctx context.Context, nodeID string, state DrainState) (err error) {
	ctx, done := d.observe(ctx, "SetDrainState", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdSetDrainState, drainState{NodeID: nodeID, State: state})

	return err
}

func applySetDrainState(a *applyCtx, p drainState) (struct{}, error) {
	switch p.State {
	case DrainActive, DrainDraining, DrainDrained:
	default:
		return struct{}{}, fmt.Errorf("%w: drain state %q", ErrInvalid, p.State)
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE cluster_members SET drain_state = ? WHERE node_id = ? AND drain_state != ?`,
		p.State, p.NodeID, p.State)
	if err != nil {
		return struct{}{}, err
	}

	if n, _ := res.RowsAffected(); n > 0 {
		a.events.Members = true
		return struct{}{}, nil
	}

	if _, err := member(a.ctx, a.tx, p.NodeID); err != nil {
		return struct{}{}, err
	}

	return struct{}{}, nil
}
