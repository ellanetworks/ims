package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/ellanetworks/ims/internal/db"
)

const maxAuthBody = 4 << 10

type LoginParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type InitializeParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Message string `json:"message"`
	Token   string `json:"token"`
}

type RefreshResponse struct {
	Token string `json:"token"`
}

type SuccessResponse struct {
	Message string `json:"message"`
}

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
			writeError(r.Context(), w, http.StatusBadRequest, "email and password are required", nil, cfg.Logger)
			return
		}

		account := strings.ToLower(params.Email)
		if !a.allowAccount(r.Context(), w, account) {
			return
		}

		u, err := cfg.Auth.GetUser(r.Context(), params.Email)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to log in", err, cfg.Logger)
			return
		}

		hash := []byte(u.PasswordHash)
		if err != nil {
			hash = a.dummyHash()
		}

		ok, checkErr := a.checkPassword(r.Context(), hash, params.Password)
		if checkErr != nil {
			writeError(r.Context(), w, http.StatusServiceUnavailable, "Failed to log in", checkErr, cfg.Logger)
			return
		}

		if !ok || err != nil {
			a.failures.allow(account)
			a.audit(r, "auth_login", params.Email, "User failed to log in")
			writeError(r.Context(), w, http.StatusUnauthorized, "The email or password is incorrect. Try again.", nil, cfg.Logger)

			return
		}

		a.failures.reset(account)

		if err := cfg.Auth.DeleteExpiredSessions(r.Context(), cfg.Now()); err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to log in", err, cfg.Logger)
			return
		}

		token, ok := a.logIn(w, r, u)
		if !ok {
			return
		}

		a.audit(r, "auth_login", u.Email, "User logged in successfully")
		writeResponse(r.Context(), w, LoginResponse{Message: "Login successful", Token: token}, http.StatusOK, cfg.Logger)
	})
}

func (a *auth) allowLogin(w http.ResponseWriter, r *http.Request) bool {
	if a.logins.allow(clientKey(r)) {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(LoginRateWindow.Seconds())))
	writeError(r.Context(), w, http.StatusTooManyRequests, "Too many login attempts. Please try again later.", nil, a.cfg.Logger)

	return false
}

func (a *auth) allowAccount(ctx context.Context, w http.ResponseWriter, account string) bool {
	if !a.failures.exceeded(account) {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(AccountFailureWindow.Seconds())))
	writeError(ctx, w, http.StatusTooManyRequests, "Too many failed attempts for this account. Please try again later.", nil,
		a.cfg.Logger)

	return false
}

func (a *auth) logIn(w http.ResponseWriter, r *http.Request, u db.User) (string, bool) {
	sessionID, err := a.startSession(w, r, u)
	if err != nil {
		writeError(r.Context(), w, http.StatusInternalServerError, "Failed to start a session", err, a.cfg.Logger)
		return "", false
	}

	token, err := a.accessToken(u, sessionID)
	if err != nil {
		writeError(r.Context(), w, http.StatusInternalServerError, "Failed to issue an access token", err, a.cfg.Logger)
		return "", false
	}

	return token, true
}

func Refresh(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unauthorized := func(message string, err error) {
			a.clearSessionCookie(w)
			writeError(r.Context(), w, http.StatusUnauthorized, message, err, cfg.Logger)
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
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to refresh the session", err, cfg.Logger)
			return
		}

		if !cfg.Now().Before(s.ExpiresAt) {
			if err := cfg.Auth.DeleteSessionByTokenHash(r.Context(), hash); err != nil {
				cfg.Logger.WarnContext(r.Context(), "failed to delete an expired session", "error", err)
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
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to refresh the session", err, cfg.Logger)
			return
		}

		token, err := a.accessToken(u, s.ID)
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to issue an access token", err, cfg.Logger)
			return
		}

		writeResponse(r.Context(), w, RefreshResponse{Token: token}, http.StatusOK, cfg.Logger)
	})
}

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
				writeError(r.Context(), w, http.StatusInternalServerError, "Failed to log out", err, cfg.Logger)
				return
			}
		}

		a.clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})
}

func Initialize(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowLogin(w, r) {
			return
		}

		initialized := func() bool {
			n, err := cfg.Auth.CountUsers(r.Context())
			if err != nil {
				writeError(r.Context(), w, http.StatusInternalServerError, "Failed to initialize", err, cfg.Logger)
				return true
			}

			if n > 0 {
				writeError(r.Context(), w, http.StatusForbidden, "System already initialized", nil, cfg.Logger)
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
			writeError(r.Context(), w, http.StatusBadRequest, "Invalid email format", nil, cfg.Logger)
			return
		}

		if msg, ok := validPassword(params.Password); !ok {
			writeError(r.Context(), w, http.StatusBadRequest, msg, nil, cfg.Logger)
			return
		}

		hash, err := a.hashPassword(r.Context(), params.Password)
		if err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to hash password", err, cfg.Logger)
			return
		}

		a.initialize.Lock()
		defer a.initialize.Unlock()

		if initialized() {
			return
		}

		u := db.User{Email: email, Role: db.RoleAdmin, PasswordHash: hash}

		if u.ID, err = cfg.Auth.CreateUser(r.Context(), db.NewUser{
			Email: u.Email, Role: u.Role,
			PasswordHash: u.PasswordHash,
		}); err != nil {
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to create the first user", err, cfg.Logger)
			return
		}

		a.audit(r, "initialize", u.Email, "System initialized with its first user")

		token, ok := a.logIn(w, r, u)
		if !ok {
			return
		}

		writeResponse(r.Context(), w, LoginResponse{Message: "System initialized successfully", Token: token}, http.StatusCreated,
			cfg.Logger)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, cfg Config) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		writeError(r.Context(), w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
		return false
	}

	return true
}

func normalEmail(s string) (string, bool) {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return "", false
	}

	return strings.ToLower(s), true
}

func validPassword(p string) (string, bool) {
	if len(p) < MinPasswordLength || len(p) > MaxPasswordLength {
		return fmt.Sprintf("password must be %d to %d bytes long", MinPasswordLength, MaxPasswordLength), false
	}

	return "", true
}
