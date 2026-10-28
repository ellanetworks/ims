package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
)

// maxAuthBody is the largest body of a request to the authentication and user routes.
const maxAuthBody = 4 << 10

type LoginParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type InitializeParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the response to a login or to the initialization: the access token of the user, whose session is
// in a cookie.
type LoginResponse struct {
	Message string `json:"message"`
	Token   string `json:"token"`
}

type RefreshResponse struct {
	Token string `json:"token"`
}

// SuccessResponse is the response to a change that returns nothing else.
type SuccessResponse struct {
	Message string `json:"message"`
}

// Login checks the email and password of a user, starts a session in a cookie, and returns an access token.
func Login(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowLogin(w, r) {
			return
		}

		var params LoginParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if params.Email == "" || params.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required", nil, cfg.Logger)
			return
		}

		account := strings.ToLower(params.Email)
		if !a.allowAccount(w, account) {
			return
		}

		u, err := cfg.Auth.GetUser(r.Context(), params.Email)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "Failed to log in", err, cfg.Logger)
			return
		}

		hash := []byte(u.PasswordHash)
		if err != nil {
			hash = a.dummyHash()
		}

		ok, checkErr := a.checkPassword(r.Context(), hash, params.Password)
		if checkErr != nil {
			writeError(w, http.StatusServiceUnavailable, "Failed to log in", checkErr, cfg.Logger)
			return
		}

		if !ok || err != nil {
			a.failures.allow(account)
			a.audit(r, "auth_login", params.Email, "User failed to log in")
			writeError(w, http.StatusUnauthorized, "The email or password is incorrect. Try again.", nil, cfg.Logger)

			return
		}

		a.failures.reset(account)

		if err := cfg.Auth.DeleteExpiredSessions(r.Context(), cfg.Now()); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to log in", err, cfg.Logger)
			return
		}

		token, ok := a.logIn(w, r, u)
		if !ok {
			return
		}

		a.audit(r, "auth_login", u.Email, "User logged in successfully")
		writeResponse(w, LoginResponse{Message: "Login successful", Token: token}, http.StatusOK, cfg.Logger)
	})
}

// allowLogin counts an attempt to log in or to initialize from the client of a request, and writes 429 when there have
// been too many.
func (a *auth) allowLogin(w http.ResponseWriter, r *http.Request) bool {
	if a.logins.allow(clientKey(r)) {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(LoginRateWindow.Seconds())))
	writeError(w, http.StatusTooManyRequests, "Too many login attempts. Please try again later.", nil, a.cfg.Logger)

	return false
}

// allowAccount writes 429 when an account has had too many wrong passwords, from whatever clients.
func (a *auth) allowAccount(w http.ResponseWriter, account string) bool {
	if !a.failures.exceeded(account) {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(AccountFailureWindow.Seconds())))
	writeError(w, http.StatusTooManyRequests, "Too many failed attempts for this account. Please try again later.", nil,
		a.cfg.Logger)

	return false
}

// logIn starts a session of a user and returns its access token, or writes the error.
func (a *auth) logIn(w http.ResponseWriter, r *http.Request, u db.User) (string, bool) {
	sessionID, err := a.startSession(w, r, u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to start a session", err, a.cfg.Logger)
		return "", false
	}

	token, err := a.accessToken(u, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to issue an access token", err, a.cfg.Logger)
		return "", false
	}

	return token, true
}

// Refresh returns a new access token for the session in the cookie of the request.
func Refresh(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unauthorized := func(message string, err error) {
			a.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, message, err, cfg.Logger)
		}

		hash := a.sessionHash(r)
		if hash == nil {
			unauthorized("No valid session", nil)
			return
		}

		s, err := cfg.Auth.GetSessionByTokenHash(r.Context(), hash)
		if errors.Is(err, db.ErrNotFound) {
			unauthorized("No valid session", err)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to refresh the session", err, cfg.Logger)
			return
		}

		if !cfg.Now().Before(s.ExpiresAt) {
			if err := cfg.Auth.DeleteSessionByTokenHash(r.Context(), hash); err != nil {
				cfg.Logger.Warn("failed to delete an expired session", "error", err)
			}

			unauthorized("Session expired", nil)

			return
		}

		u, err := cfg.Auth.GetUserByID(r.Context(), s.UserID)
		if errors.Is(err, db.ErrNotFound) {
			unauthorized("No valid session", err)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to refresh the session", err, cfg.Logger)
			return
		}

		token, err := a.accessToken(u, s.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to issue an access token", err, cfg.Logger)
			return
		}

		writeResponse(w, RefreshResponse{Token: token}, http.StatusOK, cfg.Logger)
	})
}

// Logout ends the session in the cookie of the request, if any, and so its access tokens.
func Logout(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hash := a.sessionHash(r); hash != nil {
			if s, err := cfg.Auth.GetSessionByTokenHash(r.Context(), hash); err == nil {
				if u, err := cfg.Auth.GetUserByID(r.Context(), s.UserID); err == nil {
					a.audit(r, "auth_logout", u.Email, "User logged out")
				}
			}

			if err := cfg.Auth.DeleteSessionByTokenHash(r.Context(), hash); err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to log out", err, cfg.Logger)
				return
			}
		}

		a.clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})
}

// Initialize creates the first user, an admin, and logs it in. It fails once a user exists, before it hashes the
// password, so that an initialized system spends no CPU on it. It is rate-limited like a login.
func Initialize(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowLogin(w, r) {
			return
		}

		initialized := func() bool {
			n, err := cfg.Auth.CountUsers(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Failed to initialize", err, cfg.Logger)
				return true
			}

			if n > 0 {
				writeError(w, http.StatusForbidden, "System already initialized", nil, cfg.Logger)
				return true
			}

			return false
		}

		if initialized() {
			return
		}

		var params InitializeParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		email, ok := normalEmail(params.Email)
		if !ok {
			writeError(w, http.StatusBadRequest, "Invalid email format", nil, cfg.Logger)
			return
		}

		if msg, ok := validPassword(params.Password); !ok {
			writeError(w, http.StatusBadRequest, msg, nil, cfg.Logger)
			return
		}

		hash, err := a.hashPassword(r.Context(), params.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to hash password", err, cfg.Logger)
			return
		}

		a.initialize.Lock()
		defer a.initialize.Unlock()

		// Another request may have created the first user while this one hashed.
		if initialized() {
			return
		}

		u := db.User{Email: email, Role: db.RoleAdmin, PasswordHash: hash}

		if u.ID, err = cfg.Auth.CreateUser(r.Context(), db.NewUser{
			Email: u.Email, Role: u.Role,
			PasswordHash: u.PasswordHash,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create the first user", err, cfg.Logger)
			return
		}

		a.audit(r, "initialize", u.Email, "System initialized with its first user")

		token, ok := a.logIn(w, r, u)
		if !ok {
			return
		}

		writeResponse(w, LoginResponse{Message: "System initialized successfully", Token: token}, http.StatusCreated,
			cfg.Logger)
	})
}

// decodeBody decodes the small JSON body of a request into v, rejecting unknown fields, or writes the error.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, cfg Config) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
		return false
	}

	return true
}

// normalEmail returns a bare email address in lower case, or false for anything else, such as one with a display
// name.
func normalEmail(s string) (string, bool) {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return "", false
	}

	return strings.ToLower(s), true
}

// validPassword returns why a new password is not valid.
func validPassword(p string) (string, bool) {
	if len(p) < MinPasswordLength || len(p) > MaxPasswordLength {
		return fmt.Sprintf("password must be %d to %d bytes long", MinPasswordLength, MaxPasswordLength), false
	}

	return "", true
}
