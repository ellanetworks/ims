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
	AccessTokenDuration     = 15 * time.Minute
	SessionTokenDuration    = 30 * 24 * time.Hour
	SessionCookieName       = "ims_session"
	SecureSessionCookieName = "__Host-ims_session"
	sessionTokenLength      = 32
	MaxSessionsPerUser      = 10
	LoginRateLimit          = 20
	LoginRateWindow         = time.Minute
	AccountFailureLimit     = 100
	AccountFailureWindow    = time.Hour
	MaxNumUsers             = 50
	MaxNumAPITokens         = 12
	APITokenPrefix          = "ellaims_"
	apiTokenIDLength        = 12
	MinPasswordLength       = 8
	MaxPasswordLength       = 72
	touchInterval           = time.Minute
)

type AuthStore interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u db.NewUser, maxUsers int) (int64, error)
	GetUser(ctx context.Context, email string) (db.User, error)
	GetUserByID(ctx context.Context, id int64) (db.User, error)
	ListUsers(ctx context.Context, page, perPage int) ([]db.User, int, error)
	UpdateUserRole(ctx context.Context, id int64, role db.Role, pinned bool) error
	UpdateUserPassword(ctx context.Context, id int64, passwordHash string, keepSession []byte) error
	DeleteUser(ctx context.Context, id int64) error
	DeleteIdentities(ctx context.Context, userID int64) error
	LogInWithOIDC(ctx context.Context, l db.OIDCLogin) (db.OIDCLoginResult, error)
	UseOIDCState(ctx context.Context, stateHash []byte, expiresAt, now time.Time) error

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

type Permission string

const public Permission = ""

const (
	PermReadOperator              Permission = "operator:read"
	PermUpdateOperator            Permission = "operator:update"
	PermReadDiameter              Permission = "diameter:read"
	PermUpdateDiameter            Permission = "diameter:update"
	PermReadPolicy                Permission = "policy:read"
	PermUpdatePolicy              Permission = "policy:update"
	PermReadSIP                   Permission = "sip:read"
	PermReadSIPTrunks             Permission = "sip_trunk:read"
	PermUpdateSIPTrunks           Permission = "sip_trunk:update"
	PermReadRegistrations         Permission = "registration:read"
	PermReauthenticate            Permission = "registration:reauthenticate"
	PermReadCallRecords           Permission = "call_record:read"
	PermReadCallRecordRetention   Permission = "call_record:read_retention"
	PermUpdateCallRecordRetention Permission = "call_record:update_retention"
	PermReadIPSMGW                Permission = "ip_sm_gw:read"
	PermUpdateIPSMGW              Permission = "ip_sm_gw:update"
	PermReadMCPTT                 Permission = "mcptt:read"
	PermUpdateMCPTT               Permission = "mcptt:update"
	PermSelf                      Permission = "user:self"
	PermManageUsers               Permission = "user:admin"
	PermReadProfiles              Permission = "pprof:read"
	PermReadOIDC                  Permission = "oidc:read"
	PermUpdateOIDC                Permission = "oidc:update"
	PermBackup                    Permission = "backup:manage"
	PermReadCluster               Permission = "cluster:read"
	PermManageCluster             Permission = "cluster:manage"
)

var readPermissions = []Permission{
	PermReadOperator, PermReadDiameter, PermReadPolicy, PermReadSIP, PermReadSIPTrunks, PermReadRegistrations,
	PermReadCallRecords, PermReadCallRecordRetention, PermReadIPSMGW, PermReadMCPTT, PermReadCluster, PermSelf,
}

var operatePermissions = []Permission{
	PermUpdateOperator, PermUpdateDiameter, PermUpdatePolicy, PermUpdateSIPTrunks, PermReauthenticate,
	PermUpdateCallRecordRetention, PermUpdateIPSMGW, PermUpdateMCPTT,
}

var permissions = map[db.Role][]Permission{
	db.RoleAdmin:          slices.Concat(readPermissions, operatePermissions, []Permission{PermManageUsers, PermReadProfiles, PermReadOIDC, PermUpdateOIDC, PermBackup, PermManageCluster}),
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

type Principal struct {
	UserID   int64
	Email    string
	Role     db.Role
	APIToken bool
}

type principalKey struct{}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

type auth struct {
	cfg        Config
	dummyHash  func() []byte
	logins     *rateLimiter
	failures   *rateLimiter
	bcrypt     chan struct{}
	initialize sync.Mutex

	mu      sync.Mutex
	touched map[int64]time.Time
}

func newAuth(cfg Config) *auth {
	if c := cfg.BcryptCost; c != 0 && (c < bcrypt.MinCost || c > bcrypt.MaxCost) {
		panic(fmt.Sprintf("api: bcrypt cost %d is out of range", c))
	}

	dummyHash := sync.OnceValue(func() []byte {
		password := make([]byte, 32)
		_, _ = rand.Read(password)

		hash, _ := bcrypt.GenerateFromPassword(password, cfg.BcryptCost)

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

func (a *auth) hashPassword(ctx context.Context, password string) (string, error) {
	if err := a.acquireBcrypt(ctx); err != nil {
		return "", err
	}

	defer func() { <-a.bcrypt }()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), a.cfg.BcryptCost)

	return string(hash), err
}

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
	errUnavailable  = errors.New("cannot check the token")
)

func storeError(what string, err error) error {
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("%s: %w", what, err)
	}

	return fmt.Errorf("%w: %s: %w", errUnavailable, what, err)
}

func (a *auth) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.principal(r)
		if errors.Is(err, errUnavailable) {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to authenticate", err, a.cfg.Logger)
			return
		}

		if err != nil {
			if r.Header.Get("Authorization") != "" {
				a.audit(r, "user_authentication", "", "Unauthorized: "+err.Error())
			}

			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(r.Context(), w, http.StatusUnauthorized, "Invalid token", err, a.cfg.Logger)

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

func (a *auth) touch(ctx context.Context, id int64, now time.Time) {
	a.mu.Lock()

	if last, ok := a.touched[id]; ok && now.Sub(last) < touchInterval {
		a.mu.Unlock()
		return
	}

	a.touched[id] = now
	a.mu.Unlock()

	if err := a.cfg.Auth.TouchAPIToken(ctx, id, now); err != nil {
		a.cfg.Logger.WarnContext(ctx, "failed to store the last use of an API token", slog.Any("error", err))
	}
}

type claims struct {
	ID        int64   `json:"id"`
	Email     string  `json:"email"`
	RoleID    db.Role `json:"role_id"`
	SessionID int64   `json:"sid"`
	jwt.RegisteredClaims
}

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
	}).SignedString(a.cfg.JWTSecret())
}

func (a *auth) checkAccessToken(ctx context.Context, token string) (int64, error) {
	var c claims

	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return a.cfg.JWTSecret(), nil },
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

func (a *auth) authorize(perm Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r.Context()); !ok || !allowed(p.Role, perm) {
			writeError(r.Context(), w, http.StatusForbidden, "Forbidden", nil, a.cfg.Logger)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *auth) needsLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); p.APIToken {
			writeError(r.Context(), w, http.StatusForbidden, "This needs a login, not an API token", nil, a.cfg.Logger)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *auth) startSession(w http.ResponseWriter, r *http.Request, u db.User) (int64, error) {
	raw, s := a.newSession(db.SessionPassword, SessionTokenDuration)
	s.UserID = u.ID

	id, err := a.cfg.Auth.CreateSession(r.Context(), s, MaxSessionsPerUser)
	if err != nil {
		return 0, err
	}

	a.setSessionCookie(w, raw, s.ExpiresAt)

	return id, nil
}

func (a *auth) newSession(method db.SessionMethod, lifetime time.Duration) ([]byte, db.NewSession) {
	raw := make([]byte, sessionTokenLength)
	_, _ = rand.Read(raw)

	hash := sha256.Sum256(raw)
	now := a.cfg.Now()

	return raw, db.NewSession{TokenHash: hash[:], Method: method, CreatedAt: now, ExpiresAt: now.Add(lifetime)}
}

func (a *auth) setSessionCookie(w http.ResponseWriter, raw []byte, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
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

func randIntN(n int) int {
	b := make([]byte, 1)

	for {
		_, _ = rand.Read(b)

		if int(b[0]) < 256-256%n {
			return int(b[0]) % n
		}
	}
}

const maxActorLength = 254

func (a *auth) audit(r *http.Request, action, actor, details string) {
	if len(actor) > maxActorLength {
		actor = actor[:maxActorLength]
	}

	a.cfg.Logger.InfoContext(r.Context(), "audit event", slog.String("action", action), slog.String("actor", actor),
		slog.String("ip", clientIP(r)), slog.String("details", details))
}
