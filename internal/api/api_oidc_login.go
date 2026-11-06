package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/oidc"
)

const (
	OIDCLoginCookieName = "__Host-ims_oidc_login"
	maxErrorDetail      = 300
	maxListedGroups     = 10
	maxListedGroupBytes = 24
)

const (
	OIDCSuccess          = "success"
	OIDCRateLimited      = "rate_limited"
	OIDCUnavailable      = "unavailable"
	OIDCFailed           = "failed"
	OIDCDenied           = "denied"
	OIDCNotSetUp         = "not_set_up"
	OIDCEmailNotVerified = "email_not_verified"
	OIDCLinkedElsewhere  = "linked_elsewhere"
	OIDCNoGroup          = "no_group"
	OIDCGroupsOverage    = "groups_overage"
	OIDCTooManyUsers     = "too_many_users"
	OIDCError            = "error"
)

type OIDCLogin struct {
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"display_name,omitempty"`
}

type oidcRefusal struct {
	outcome string
	detail  string
}

func (e oidcRefusal) Error() string { return e.outcome + ": " + e.detail }

func refuse(outcome, format string, args ...any) error {
	return oidcRefusal{outcome: outcome, detail: clip(fmt.Sprintf(format, args...))}
}

func GetOIDCLogin(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := cfg.OIDC.Get()

		resp := OIDCLogin{Enabled: o.Enabled}
		if o.Enabled {
			resp.DisplayName = o.DisplayName
		}

		writeResponse(r.Context(), w, resp, http.StatusOK, cfg.Logger)
	})
}

// OpenID Connect Core 1.0 section 3.1.2.1
func StartOIDCLogin(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		if !a.logins.allow(clientKey(r)) {
			a.oidcDone(w, r, "", refuse(OIDCRateLimited, "too many login attempts"))
			return
		}

		c, err := cfg.OIDC.Client(r.Context(), cfg.Now)
		if err != nil {
			a.oidcDone(w, r, "", refuse(OIDCUnavailable, "%v", err))
			return
		}

		l := c.NewLogin(cfg.Now())

		value, err := oidc.Seal(cfg.OIDCCookieKey, OIDCLoginCookieName, l)
		if err != nil {
			a.oidcDone(w, r, "", err)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     OIDCLoginCookieName,
			Value:    value,
			Path:     "/",
			MaxAge:   int(oidc.LoginLifetime.Seconds()),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, c.AuthCodeURL(l.State, l.Nonce, l.Verifier), http.StatusSeeOther)
	})
}

// OpenID Connect Core 1.0 section 3.1.2.7
// RFC 6749 section 10.12
func OIDCCallback(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.SetCookie(w, &http.Cookie{
			Name: OIDCLoginCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true,
			SameSite: http.SameSiteLaxMode,
		})

		c, cl, err := a.oidcAuthenticate(r)
		if err != nil {
			a.oidcDone(w, r, "", err)
			return
		}

		if err := cfg.Auth.DeleteExpiredSessions(r.Context(), cfg.Now()); err != nil {
			a.oidcDone(w, r, actor(cl), err)
			return
		}

		u, err := a.oidcLogIn(w, r, c, cl)
		if err != nil {
			a.oidcDone(w, r, actor(cl), err)
			return
		}

		a.oidcDone(w, r, u.Email, nil)
	})
}

func (a *auth) oidcAuthenticate(r *http.Request) (*oidc.Client, oidc.Claims, error) {
	cfg := a.cfg

	cookie, err := r.Cookie(OIDCLoginCookieName)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "no login in progress")
	}

	l, err := oidc.Open(cfg.OIDCCookieKey, OIDCLoginCookieName, cookie.Value, cfg.Now())
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(l.State)) != 1 {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "the state does not match the login")
	}

	state := sha256.Sum256([]byte(l.State))

	err = cfg.Auth.UseOIDCState(r.Context(), state[:], time.Unix(l.ExpiresAt, 0), cfg.Now())
	if errors.Is(err, db.ErrStateUsed) {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "the login was already used")
	}

	if err != nil {
		return nil, oidc.Claims{}, err
	}

	c, err := cfg.OIDC.Client(r.Context(), cfg.Now)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCUnavailable, "%v", err)
	}

	if !c.Owns(l) {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "the OIDC settings changed during the login")
	}

	if err := c.CheckIssuer(q.Get("iss"), q.Has("iss")); err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	if e := q.Get("error"); e != "" {
		outcome := OIDCFailed
		if e == "access_denied" {
			outcome = OIDCDenied
		}

		return nil, oidc.Claims{}, refuse(outcome, "the identity provider returned %s: %s", e, q.Get("error_description"))
	}

	code := q.Get("code")
	if code == "" {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "no authorization code")
	}

	cl, err := c.Exchange(r.Context(), code, l.Verifier, l.Nonce)
	if err != nil {
		return nil, oidc.Claims{}, refuse(OIDCFailed, "%v", err)
	}

	return c, cl, nil
}

func (a *auth) oidcLogIn(w http.ResponseWriter, r *http.Request, c *oidc.Client, cl oidc.Claims) (db.User, error) {
	o := c.Settings()
	role, groupErr := groupRole(c, cl)

	l := db.OIDCLogin{
		Issuer: o.Issuer, ClientID: o.ClientID, Subject: cl.Subject, Email: cl.Email, MapsGroups: c.MapsGroups(),
		Role: role, MaxUsers: MaxNumUsers, MaxSessionsPerUser: MaxSessionsPerUser,
	}

	var unlinked error

	email, ok := normalEmail(cl.Email)

	switch {
	case !ok:
		unlinked = refuse(OIDCNotSetUp, "the identity provider gave no email address")
	case !c.TrustsEmail(cl):
		unlinked = refuse(OIDCEmailNotVerified, "the identity provider has not verified %s", email)
	default:
		l.LinkEmail = email
	}

	raw, s := a.newSession(db.SessionOIDC, o.SessionLifetime)
	l.Session = s

	res, err := a.cfg.Auth.LogInWithOIDC(r.Context(), l)

	switch {
	case errors.Is(err, db.ErrOIDCDisabled):
		return db.User{}, refuse(OIDCUnavailable, "%v", oidc.ErrDisabled)
	case errors.Is(err, db.ErrOIDCChanged):
		return db.User{}, refuse(OIDCFailed, "the OIDC settings changed during the login")
	case errors.Is(err, db.ErrNotLinked):
		return db.User{}, unlinked
	case errors.Is(err, db.ErrNoUser):
		return db.User{}, refuse(OIDCNotSetUp, "no user has the email %s", email)
	case errors.Is(err, db.ErrNoRole):
		return db.User{}, groupErr
	case errors.Is(err, db.ErrLinkedElsewhere):
		return db.User{}, refuse(OIDCLinkedElsewhere, "%s is linked to another account at the identity provider", email)
	case errors.Is(err, db.ErrTooManyUsers):
		return db.User{}, refuse(OIDCTooManyUsers, "%d users exist", MaxNumUsers)
	case err != nil:
		return db.User{}, err
	}

	u := res.User

	switch {
	case res.Created:
		a.audit(r, "create_user", u.Email, fmt.Sprintf("User %s created from the identity provider with role %d", u.Email,
			u.Role))
	case res.Linked:
		a.audit(r, "link_user_oidc", u.Email, "User linked to the identity provider account "+cl.Subject)
	}

	switch {
	case res.RoleKept:
		a.audit(r, "update_user", u.Email, fmt.Sprintf("Role %d from the identity provider not applied to %s, the last admin with a password", role, u.Email))
	case res.RoleChanged():
		a.audit(r, "update_user", u.Email, fmt.Sprintf("Role of %s set to %d from the identity provider", u.Email, u.Role))
	}

	a.setSessionCookie(w, raw, s.ExpiresAt)

	return u, nil
}

func groupRole(c *oidc.Client, cl oidc.Claims) (db.Role, error) {
	if !c.MapsGroups() {
		return 0, nil
	}

	if cl.GroupsOverage {
		return 0, refuse(OIDCGroupsOverage, "%v", oidc.ErrOverage)
	}

	role, ok := c.RoleFor(cl.Groups)
	if !ok {
		if len(cl.Groups) == 0 {
			return 0, refuse(OIDCNoGroup, "the user has no groups")
		}

		return 0, refuse(OIDCNoGroup, "none of the user's %d groups is mapped to a role: %s", len(cl.Groups), listGroups(cl.Groups))
	}

	return role, nil
}

func (a *auth) oidcDone(w http.ResponseWriter, r *http.Request, who string, err error) {
	outcome, target := OIDCSuccess, "/"

	var refusal oidcRefusal

	switch {
	case err == nil:
		a.audit(r, "auth_oidc_login", who, "User logged in through OIDC")
	case errors.As(err, &refusal):
		outcome = refusal.outcome
		a.audit(r, "auth_oidc_login", who, "User failed to log in through OIDC: "+refusal.Error())
	default:
		outcome = OIDCError

		a.cfg.Logger.ErrorContext(r.Context(), "OIDC login failed", slog.Any("error", err))
		a.audit(r, "auth_oidc_login", who, "User failed to log in through OIDC: internal error")
	}

	a.cfg.Requests.oidcLogin(outcome)

	if outcome != OIDCSuccess {
		target = "/login?" + url.Values{"error": {outcome}}.Encode()
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

func actor(cl oidc.Claims) string {
	if cl.Email != "" {
		return cl.Email
	}

	return cl.Subject
}

func listGroups(groups []string) string {
	shown := groups[:min(len(groups), maxListedGroups)]
	quoted := make([]string, len(shown))

	for i, g := range shown {
		if len(g) > maxListedGroupBytes {
			g = strings.ToValidUTF8(g[:maxListedGroupBytes], "") + "…"
		}

		quoted[i] = strconv.Quote(g)
	}

	list := strings.Join(quoted, ", ")
	if more := len(groups) - len(shown); more > 0 {
		list += fmt.Sprintf(", and %d more", more)
	}

	return list
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, s)

	if len(s) > maxErrorDetail {
		s = strings.ToValidUTF8(s[:maxErrorDetail], "")
	}

	return s
}
