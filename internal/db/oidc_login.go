package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mattn/go-sqlite3"
)

var (
	ErrOIDCDisabled    = errors.New("OIDC is disabled")
	ErrOIDCChanged     = errors.New("the OIDC settings changed")
	ErrNotLinked       = errors.New("no user is linked to the identity")
	ErrNoUser          = errors.New("no user has the email")
	ErrNoRole          = errors.New("the groups give no role")
	ErrLinkedElsewhere = errors.New("the user is linked to another identity")
	ErrStateUsed       = errors.New("the login was already used")
)

type OIDCLogin struct {
	Issuer             string
	ClientID           string
	Subject            string
	Email              string
	LinkEmail          string
	MapsGroups         bool
	Role               Role
	MaxUsers           int
	Session            NewSession
	MaxSessionsPerUser int
}

type OIDCLoginResult struct {
	User         User
	IdentityID   int64
	SessionID    int64
	Linked       bool
	Created      bool
	PreviousRole Role
	RoleKept     bool
}

func (r OIDCLoginResult) RoleChanged() bool {
	return !r.Created && r.User.Role != r.PreviousRole
}

func (d *DB) LogInWithOIDC(ctx context.Context, l OIDCLogin) (res OIDCLoginResult, err error) {
	defer d.observe(poolWrite, &err)()

	return run[OIDCLoginResult](ctx, d, cmdLogInWithOIDC, "OIDC login", l)
}

func applyLogInWithOIDC(a *applyCtx, l OIDCLogin) (OIDCLoginResult, error) {
	return logInWithOIDC(a.ctx, a.tx, l)
}

func logInWithOIDC(ctx context.Context, tx *sql.Tx, l OIDCLogin) (OIDCLoginResult, error) {
	if err := checkOIDC(ctx, tx, l.Issuer, l.ClientID); err != nil {
		return OIDCLoginResult{}, err
	}

	res, err := findOIDCUser(ctx, tx, l)
	if err != nil {
		return OIDCLoginResult{}, err
	}

	res.PreviousRole = res.User.Role

	if !res.Created {
		if res.User, res.RoleKept, err = syncRole(ctx, tx, res.User, l); err != nil {
			return OIDCLoginResult{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE user_identities SET email = ?, last_login_at = ? WHERE id = ?`, l.Email,
		l.Session.CreatedAt.UnixNano(), res.IdentityID); err != nil {
		return OIDCLoginResult{}, err
	}

	s := l.Session
	s.UserID, s.Method, s.IdentityID = res.User.ID, SessionOIDC, res.IdentityID

	if res.SessionID, err = createSession(ctx, tx, s, l.MaxSessionsPerUser); err != nil {
		return OIDCLoginResult{}, err
	}

	res.User.HasIdentity = true

	return res, nil
}

func checkOIDC(ctx context.Context, tx *sql.Tx, issuer, clientID string) error {
	var (
		enabled              bool
		current, currentUser string
	)

	if err := tx.QueryRowContext(ctx, `SELECT enabled, issuer, client_id FROM oidc WHERE id = 1`).Scan(&enabled, &current,
		&currentUser); err != nil {
		return err
	}

	switch {
	case !enabled:
		return ErrOIDCDisabled
	case current != issuer || currentUser != clientID:
		return ErrOIDCChanged
	}

	return nil
}

func findOIDCUser(ctx context.Context, tx *sql.Tx, l OIDCLogin) (OIDCLoginResult, error) {
	var res OIDCLoginResult

	err := tx.QueryRowContext(ctx, `SELECT id, user_id FROM user_identities WHERE issuer = ? AND subject = ?`, l.Issuer,
		l.Subject).Scan(&res.IdentityID, &res.User.ID)
	if err == nil {
		res.User, err = getUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, res.User.ID))
		return res, err
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return OIDCLoginResult{}, err
	}

	if l.LinkEmail == "" {
		return OIDCLoginResult{}, ErrNotLinked
	}

	identity := NewIdentity{Issuer: l.Issuer, Subject: l.Subject, Email: l.Email, At: l.Session.CreatedAt}

	res.User, err = getUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, l.LinkEmail))
	if err == nil {
		res.IdentityID, err = createIdentity(ctx, tx, res.User.ID, identity)
		if errors.Is(err, ErrDuplicate) {
			return OIDCLoginResult{}, ErrLinkedElsewhere
		}

		res.Linked = true

		return res, err
	}

	if !errors.Is(err, ErrNotFound) {
		return OIDCLoginResult{}, err
	}

	switch {
	case !l.MapsGroups:
		return OIDCLoginResult{}, ErrNoUser
	case l.Role == 0:
		return OIDCLoginResult{}, ErrNoRole
	}

	res.User = User{Email: l.LinkEmail, Role: l.Role}
	if res.User.ID, err = createUser(ctx, tx, NewUser{Email: l.LinkEmail, Role: l.Role}, l.MaxUsers); err != nil {
		return OIDCLoginResult{}, err
	}

	res.Created = true
	res.IdentityID, err = createIdentity(ctx, tx, res.User.ID, identity)

	return res, err
}

func syncRole(ctx context.Context, tx *sql.Tx, u User, l OIDCLogin) (User, bool, error) {
	switch {
	case !l.MapsGroups || u.RolePinned:
		return u, false, nil
	case l.Role == 0:
		return User{}, false, ErrNoRole
	case l.Role == u.Role:
		return u, false, nil
	}

	if l.Role != RoleAdmin {
		err := keepPasswordAdmin(ctx, tx, u.ID)
		if errors.Is(err, ErrLastPasswordAdmin) {
			return u, true, nil
		}

		if err != nil {
			return User{}, false, err
		}
	}

	if err := updateUser(ctx, tx, `UPDATE users SET role_id = ? WHERE id = ? AND role_pinned = 0`, l.Role,
		u.ID); err != nil {
		return User{}, false, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); err != nil {
		return User{}, false, err
	}

	u.Role = l.Role

	return u, false, nil
}

func (d *DB) UseOIDCState(ctx context.Context, stateHash []byte, expiresAt, now time.Time) (err error) {
	defer d.observe(poolWrite, &err)()

	_, err = run[struct{}](ctx, d, cmdUseOIDCState, "use OIDC state", useOIDCState{
		StateHash: stateHash,
		ExpiresAt: expiresAt.UnixNano(), Now: now.UnixNano(),
	})

	return err
}

type useOIDCState struct {
	StateHash []byte
	ExpiresAt int64
	Now       int64
}

func applyUseOIDCState(a *applyCtx, p useOIDCState) (struct{}, error) {
	if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM oidc_used_states WHERE expires_at <= ?`, p.Now); err != nil {
		return struct{}{}, err
	}

	_, err := a.tx.ExecContext(a.ctx, `INSERT INTO oidc_used_states (state_hash, expires_at) VALUES (?, ?)`,
		p.StateHash, p.ExpiresAt)
	if isConstraint(err, sqlite3.ErrConstraintPrimaryKey) {
		return struct{}{}, ErrStateUsed
	}

	return struct{}{}, err
}

func init() {
	register(cmdLogInWithOIDC, handle(applyLogInWithOIDC))
	register(cmdUseOIDCState, handle(applyUseOIDCState))
}
