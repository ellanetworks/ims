package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	AccessTokenDuration  = 15 * time.Minute
	SessionTokenDuration = 30 * 24 * time.Hour
	SessionCookieName    = "ims_session"
	// SecureSessionCookieName is the name of the session cookie over TLS. The __Host- prefix makes a browser accept
	// it only Secure, for the whole host, and from the host itself, so that no other site can set it.
	SecureSessionCookieName = "__Host-ims_session"
	// sessionTokenLength is the length in bytes of the random value of a session cookie.
	sessionTokenLength = 32
	MaxSessionsPerUser = 10
	LoginRateLimit     = 20
	LoginRateWindow    = time.Minute
	// AccountFailureLimit is the most wrong passwords for an account per AccountFailureWindow, whatever their
	// source: NIST SP 800-63B allows at most 100 consecutive ones.
	AccountFailureLimit  = 100
	AccountFailureWindow = time.Hour
	MaxNumUsers          = 50
	MaxNumAPITokens      = 12
	// APITokenPrefix starts every API token, which tells it from an access token and names the product it is for.
	APITokenPrefix = "ellaims_"
	// apiTokenIDLength is the length of the part of an API token that finds it. The secret that follows is
	// crypto/rand.Text, of about 130 bits.
	apiTokenIDLength = 12
	// MinPasswordLength is the shortest password NIST SP 800-63B allows, and MaxPasswordLength the longest that
	// bcrypt hashes, in bytes.
	MinPasswordLength = 8
	MaxPasswordLength = 72
	// touchInterval is how often at most the last use of an API token is stored.
	touchInterval = time.Minute
)

// AuthStore stores the users, their sessions in the UI, and their API tokens.
type AuthStore interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u db.NewUser) (int64, error)
	GetUser(ctx context.Context, email string) (db.User, error)
	GetUserByID(ctx context.Context, id int64) (db.User, error)
	ListUsers(ctx context.Context, page, perPage int) ([]db.User, int, error)
	UpdateUserRole(ctx context.Context, id int64, role db.Role) error
	UpdateUserPassword(ctx context.Context, id int64, passwordHash string, keepSession []byte) error
	DeleteUser(ctx context.Context, id int64) error

	CreateSession(ctx context.Context, s db.NewSession, maxPerUser int) (int64, error)
	GetSession(ctx context.Context, id int64) (db.Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (db.Session, error)
	DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error

	CreateAPIToken(ctx context.Context, t db.NewAPIToken) (int64, error)
	GetAPITokenByTokenID(ctx context.Context, tokenID string) (db.APIToken, error)
	ListAPITokens(ctx context.Context, userID int64, page, perPage int) ([]db.APIToken, int, error)
	CountAPITokens(ctx context.Context, userID int64) (int, error)
	DeleteAPIToken(ctx context.Context, userID int64, tokenID string) error
	TouchAPIToken(ctx context.Context, id int64, at time.Time) error
}

// Permission is what a route needs the role of its caller to allow.
type Permission string

// public is the permission of the routes that need no authentication.
const public Permission = ""

const (
	PermReadOperator              Permission = "operator:read"
	PermUpdateOperator            Permission = "operator:update"
	PermReadDiameter              Permission = "diameter:read"
	PermUpdateDiameter            Permission = "diameter:update"
	PermReadPolicy                Permission = "policy:read"
	PermUpdatePolicy              Permission = "policy:update"
	PermReadSIP                   Permission = "sip:read"
	PermReadRegistrations         Permission = "registration:read"
	PermReauthenticate            Permission = "registration:reauthenticate"
	PermReadCallRecords           Permission = "call_record:read"
	PermReadCallRecordRetention   Permission = "call_record:read_retention"
	PermUpdateCallRecordRetention Permission = "call_record:update_retention"
	PermReadIPSMGW                Permission = "ip_sm_gw:read"
	PermUpdateIPSMGW              Permission = "ip_sm_gw:update"
	PermReadMCPTT                 Permission = "mcptt:read"
	PermUpdateMCPTT               Permission = "mcptt:update"
	// PermSelf is reading one's own user, changing one's own password, and managing one's own API tokens.
	PermSelf Permission = "user:self"
	// PermManageUsers is managing every user and their API tokens.
	PermManageUsers Permission = "user:admin"
)

// readPermissions are reading every setting and every state of the IMS.
var readPermissions = []Permission{
	PermReadOperator, PermReadDiameter, PermReadPolicy, PermReadSIP, PermReadRegistrations, PermReadCallRecords,
	PermReadCallRecordRetention, PermReadIPSMGW, PermReadMCPTT, PermSelf,
}

// operatePermissions are changing the settings of the IMS, and acting on its registrations.
var operatePermissions = []Permission{
	PermUpdateOperator, PermUpdateDiameter, PermUpdatePolicy, PermReauthenticate, PermUpdateCallRecordRetention,
	PermUpdateIPSMGW, PermUpdateMCPTT,
}

var permissions = map[db.Role][]Permission{
	db.RoleAdmin:          slices.Concat(readPermissions, operatePermissions, []Permission{PermManageUsers}),
	db.RoleReadOnly:       readPermissions,
	db.RoleNetworkManager: slices.Concat(readPermissions, operatePermissions),
}

func allowed(role db.Role, perm Permission) bool {
	return slices.Contains(permissions[role], perm)
}

func validRole(role db.Role) bool {
	_, ok := permissions[role]
	return ok
}

// Principal is the user that a request authenticated as.
type Principal struct {
	UserID int64
	Email  string
	Role   db.Role
	// APIToken is whether the request authenticated with an API token, rather than an access token from a login.
	APIToken bool
}

type principalKey struct{}

// PrincipalFrom returns the user that the request of ctx authenticated as.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// auth holds what the authentication routes and middleware share.
type auth struct {
	cfg Config
	// dummyHash is checked against the password of a login with an unknown email, so that a login takes as long
	// whether or not its email exists. It is computed in the background, so that it does not hold up the start.
	dummyHash func() []byte
	// logins limits the logins and initializations per client, and failures the wrong passwords per account.
	logins   *rateLimiter
	failures *rateLimiter
	// bcrypt bounds the concurrent bcrypt computations, which take tens of milliseconds of CPU each, so that a flood of
	// logins cannot take every CPU.
	bcrypt chan struct{}
	// initialize serializes the creation of the first user.
	initialize sync.Mutex

	mu      sync.Mutex
	touched map[int64]time.Time
}

// newAuth panics when cfg.BcryptCost is out of range, which is a programming error.
func newAuth(cfg Config) *auth {
	if c := cfg.BcryptCost; c != 0 && (c < bcrypt.MinCost || c > bcrypt.MaxCost) {
		panic(fmt.Sprintf("api: bcrypt cost %d is out of range", c))
	}

	dummyHash := sync.OnceValue(func() []byte {
		password := make([]byte, 32)
		_, _ = rand.Read(password) // It never fails.

		hash, _ := bcrypt.GenerateFromPassword(password, cfg.BcryptCost) // It fails only of a cost out of range.

		return hash
	})

	go dummyHash()

	return &auth{
		cfg:       cfg,
		dummyHash: dummyHash,
		logins:    newRateLimiter(LoginRateLimit, LoginRateWindow, cfg.Now),
		failures:  newRateLimiter(AccountFailureLimit, AccountFailureWindow, cfg.Now),
		bcrypt:    make(chan struct{}, runtime.GOMAXPROCS(0)),
		touched:   map[int64]time.Time{},
	}
}

// hashPassword returns the bcrypt hash of a password.
func (a *auth) hashPassword(ctx context.Context, password string) (string, error) {
	if err := a.acquireBcrypt(ctx); err != nil {
		return "", err
	}

	defer func() { <-a.bcrypt }()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), a.cfg.BcryptCost)

	return string(hash), err
}

// checkPassword reports whether a password is the one of a bcrypt hash, or fails when the request is cancelled.
func (a *auth) checkPassword(ctx context.Context, hash []byte, password string) (bool, error) {
	if err := a.acquireBcrypt(ctx); err != nil {
		return false, err
	}

	defer func() { <-a.bcrypt }()

	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil, nil
}

func (a *auth) acquireBcrypt(ctx context.Context) error {
	select {
	case a.bcrypt <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	errInvalidToken = errors.New("invalid token")
	// errUnavailable is a failure to check a token, rather than a token that is not valid.
	errUnavailable = errors.New("cannot check the token")
)

// storeError returns the error of looking up what a token names: not found means that the token is not valid.
func storeError(what string, err error) error {
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("%s: %w", what, err)
	}

	return fmt.Errorf("%w: %s: %w", errUnavailable, what, err)
}

// authenticate passes on the requests with a valid bearer token, with their principal in their context. The token is
// either an access token from a login, or an API token.
func (a *auth) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.principal(r)
		if errors.Is(err, errUnavailable) {
			writeError(w, http.StatusInternalServerError, "Failed to authenticate", err, a.cfg.Logger)
			return
		}

		if err != nil {
			// A request without credentials is not an authentication attempt, and logging each would let anyone fill
			// the log.
			if r.Header.Get("Authorization") != "" {
				a.audit(r, "user_authentication", "", "Unauthorized: "+err.Error())
			}

			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "Invalid token", err, a.cfg.Logger)

			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (a *auth) principal(r *http.Request) (Principal, error) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return Principal{}, errors.New("no bearer token")
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, errors.New("empty bearer token")
	}

	var (
		userID int64
		err    error
	)

	isAPIToken := strings.HasPrefix(token, APITokenPrefix)
	if isAPIToken {
		userID, err = a.checkAPIToken(r.Context(), token)
	} else {
		userID, err = a.checkAccessToken(r.Context(), token)
	}

	if err != nil {
		return Principal{}, err
	}

	// The user is read again, so that deleting it or changing its role takes effect at once.
	u, err := a.cfg.Auth.GetUserByID(r.Context(), userID)
	if err != nil {
		return Principal{}, storeError("get user", err)
	}

	return Principal{UserID: u.ID, Email: u.Email, Role: u.Role, APIToken: isAPIToken}, nil
}

func (a *auth) checkAPIToken(ctx context.Context, token string) (int64, error) {
	tokenID, secret, ok := strings.Cut(strings.TrimPrefix(token, APITokenPrefix), "_")
	if !ok || tokenID == "" || secret == "" {
		return 0, errInvalidToken
	}

	t, err := a.cfg.Auth.GetAPITokenByTokenID(ctx, tokenID)
	if err != nil {
		return 0, storeError("get API token", err)
	}

	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(hash[:], t.TokenHash) != 1 {
		return 0, errInvalidToken
	}

	now := a.cfg.Now()
	if !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt) {
		return 0, errors.New("API token expired")
	}

	a.touch(ctx, t.ID, now)

	return t.UserID, nil
}

// touch stores the last use of an API token, at most every touchInterval, so that a busy client does not write on
// each request.
func (a *auth) touch(ctx context.Context, id int64, now time.Time) {
	a.mu.Lock()

	if last, ok := a.touched[id]; ok && now.Sub(last) < touchInterval {
		a.mu.Unlock()
		return
	}

	a.touched[id] = now
	a.mu.Unlock()

	if err := a.cfg.Auth.TouchAPIToken(ctx, id, now); err != nil {
		a.cfg.Logger.Warn("failed to store the last use of an API token", slog.Any("error", err))
	}
}

type claims struct {
	ID     int64   `json:"id"`
	Email  string  `json:"email"`
	RoleID db.Role `json:"role_id"`
	// SessionID is the session that the access token is of, which it does not outlive.
	SessionID int64 `json:"sid"`
	jwt.RegisteredClaims
}

// accessToken returns an access token of a user, in a session.
func (a *auth) accessToken(u db.User, sessionID int64) (string, error) {
	now := a.cfg.Now()

	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		ID:        u.ID,
		Email:     u.Email,
		RoleID:    u.Role,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenDuration)),
		},
	}).SignedString(a.cfg.JWTSecret)
}

// checkAccessToken returns the user of a valid access token, whose session still exists: a logout, or a change of
// role or password, ends the access tokens of the sessions that it ends.
func (a *auth) checkAccessToken(ctx context.Context, token string) (int64, error) {
	var c claims

	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return a.cfg.JWTSecret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(), jwt.WithTimeFunc(a.cfg.Now)); err != nil {
		return 0, err
	}

	s, err := a.cfg.Auth.GetSession(ctx, c.SessionID)
	if err != nil {
		return 0, storeError("get session", err)
	}

	if s.UserID != c.ID || !a.cfg.Now().Before(s.ExpiresAt) {
		return 0, errors.New("session ended")
	}

	return c.ID, nil
}

// authorize passes on the requests whose principal's role allows perm.
func (a *auth) authorize(perm Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r.Context()); !ok || !allowed(p.Role, perm) {
			writeError(w, http.StatusForbidden, "Forbidden", nil, a.cfg.Logger)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// needsLogin passes on the requests authenticated by a login, and refuses those with an API token: a leaked API
// token must not be able to create others, which would outlive its revocation, or to take the account.
func (a *auth) needsLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); p.APIToken {
			writeError(w, http.StatusForbidden, "This needs a login, not an API token", nil, a.cfg.Logger)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// startSession stores a new session of a user, sets its cookie, and returns its ID.
func (a *auth) startSession(w http.ResponseWriter, r *http.Request, u db.User) (int64, error) {
	raw := make([]byte, sessionTokenLength)
	_, _ = rand.Read(raw) // It never fails.

	hash := sha256.Sum256(raw)
	now := a.cfg.Now()
	expiresAt := now.Add(SessionTokenDuration)

	id, err := a.cfg.Auth.CreateSession(r.Context(), db.NewSession{
		UserID: u.ID, TokenHash: hash[:], CreatedAt: now,
		ExpiresAt: expiresAt,
	}, MaxSessionsPerUser)
	if err != nil {
		return 0, err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})

	return id, nil
}

func (a *auth) cookieName() string {
	if a.cfg.SecureCookie {
		return SecureSessionCookieName
	}

	return SessionCookieName
}

func (a *auth) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// sessionHash returns the hash of the session token in the cookie of a request, or nil when it has none valid.
func (a *auth) sessionHash(r *http.Request) []byte {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil {
		return nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != sessionTokenLength {
		return nil
	}

	hash := sha256.Sum256(raw)

	return hash[:]
}

// newAPIToken returns a new API token, its token ID, and the hash of its secret.
func newAPIToken() (token, tokenID string, hash []byte) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	id := make([]byte, apiTokenIDLength)
	for i := range id {
		id[i] = alphabet[randIntN(len(alphabet))]
	}

	secret := rand.Text()
	sum := sha256.Sum256([]byte(secret))

	return APITokenPrefix + string(id) + "_" + secret, string(id), sum[:]
}

// randIntN returns a uniform random integer in [0, n), for n up to 256.
func randIntN(n int) int {
	b := make([]byte, 1)

	for {
		_, _ = rand.Read(b) // It never fails.

		if int(b[0]) < 256-256%n {
			return int(b[0]) % n
		}
	}
}

// maxActorLength is the longest an email address can be (RFC 5321), past which the actor of an audit event is cut.
const maxActorLength = 254

// audit logs an event of the audit trail: a login, a change to a user or an API token, or a failed authentication.
func (a *auth) audit(r *http.Request, action, actor, details string) {
	if len(actor) > maxActorLength {
		actor = actor[:maxActorLength]
	}

	a.cfg.Logger.Info("audit event", slog.String("action", action), slog.String("actor", actor),
		slog.String("ip", clientIP(r)), slog.String("details", details))
}
