package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
)

const (
	minTokenNameLength = 3
	maxTokenNameLength = 50
)

type User struct {
	Email  string  `json:"email"`
	RoleID db.Role `json:"role_id"`
}

type CreateUserParams struct {
	Email    string  `json:"email"`
	Password string  `json:"password"`
	RoleID   db.Role `json:"role_id"`
}

type UpdateUserParams struct {
	RoleID db.Role `json:"role_id"`
}

type UpdateUserPasswordParams struct {
	Password string `json:"password"`
}

type UpdateMyPasswordParams struct {
	CurrentPassword string `json:"current_password"`
	Password        string `json:"password"`
}

type ListUsersResponse struct {
	Items      []User `json:"items"`
	Page       int    `json:"page"`
	PerPage    int    `json:"per_page"`
	TotalCount int    `json:"total_count"`
}

// APIToken is an API token without its secret, which only its creation returns.
type APIToken struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

type CreateAPITokenParams struct {
	Name string `json:"name"`
	// ExpiresAt is an RFC 3339 time, or empty for a token that does not expire.
	ExpiresAt string `json:"expires_at"`
}

type CreateAPITokenResponse struct {
	Token string `json:"token"`
}

type ListAPITokensResponse struct {
	Items      []APIToken `json:"items"`
	Page       int        `json:"page"`
	PerPage    int        `json:"per_page"`
	TotalCount int        `json:"total_count"`
}

func GetMyUser(a *auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		writeResponse(w, User{Email: p.Email, RoleID: p.Role}, http.StatusOK, a.cfg.Logger)
	})
}

// UpdateMyPassword changes the password of the caller, and ends its other sessions.
func UpdateMyPassword(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params UpdateMyPasswordParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		p, _ := PrincipalFrom(r.Context())

		u, err := cfg.Auth.GetUserByID(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve user", err, cfg.Logger)
			return
		}

		// The wrong current passwords count with the failed logins of the account, so that a stolen access token cannot
		// guess the password here instead.
		account := strings.ToLower(u.Email)
		if !a.allowAccount(w, account) {
			return
		}

		ok, err := a.checkPassword(r.Context(), []byte(u.PasswordHash), params.CurrentPassword)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Failed to check the password", err, cfg.Logger)
			return
		}

		// Not 401, which tells the UI that its session is over.
		if !ok {
			a.failures.allow(account)
			writeError(w, http.StatusBadRequest, "Current password is incorrect", nil, cfg.Logger)

			return
		}

		if !a.setPassword(w, r, u, params.Password, a.sessionHash(r)) {
			return
		}

		writeResponse(w, SuccessResponse{Message: "Password updated successfully"}, http.StatusOK, cfg.Logger)
	})
}

// setPassword stores the new password of a user and ends its sessions but keepSession, or writes the error.
func (a *auth) setPassword(w http.ResponseWriter, r *http.Request, u db.User, password string, keepSession []byte) bool {
	if msg, ok := validPassword(password); !ok {
		writeError(w, http.StatusBadRequest, msg, nil, a.cfg.Logger)
		return false
	}

	hash, err := a.hashPassword(r.Context(), password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to hash password", err, a.cfg.Logger)
		return false
	}

	err = a.cfg.Auth.UpdateUserPassword(r.Context(), u.ID, hash, keepSession)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "User not found", nil, a.cfg.Logger)
		return false
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update password", err, a.cfg.Logger)
		return false
	}

	p, _ := PrincipalFrom(r.Context())
	a.audit(r, "update_user_password", p.Email, "Password of "+u.Email+" updated")

	return true
}

func ListUsers(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		users, total, err := cfg.Auth.ListUsers(r.Context(), page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list users", err, cfg.Logger)
			return
		}

		resp := ListUsersResponse{Items: make([]User, 0, len(users)), Page: page, PerPage: perPage, TotalCount: total}
		for _, u := range users {
			resp.Items = append(resp.Items, User{Email: u.Email, RoleID: u.Role})
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func CreateUser(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CreateUserParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		email, ok := normalEmail(params.Email)
		if !ok {
			writeError(w, http.StatusBadRequest, "Invalid email format", nil, cfg.Logger)
			return
		}

		if !validRole(params.RoleID) {
			writeError(w, http.StatusBadRequest, "Invalid role ID", nil, cfg.Logger)
			return
		}

		if msg, ok := validPassword(params.Password); !ok {
			writeError(w, http.StatusBadRequest, msg, nil, cfg.Logger)
			return
		}

		n, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to count users", err, cfg.Logger)
			return
		}

		if n >= MaxNumUsers {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Maximum number of users reached (%d)", MaxNumUsers), nil,
				cfg.Logger)

			return
		}

		hash, err := a.hashPassword(r.Context(), params.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to hash password", err, cfg.Logger)
			return
		}

		_, err = cfg.Auth.CreateUser(r.Context(), db.NewUser{Email: email, Role: params.RoleID, PasswordHash: hash})
		if errors.Is(err, db.ErrDuplicate) {
			writeError(w, http.StatusConflict, "User already exists", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create user", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_user", p.Email, fmt.Sprintf("User %s created with role %d", email, params.RoleID))
		writeResponse(w, SuccessResponse{Message: "User created successfully"}, http.StatusCreated, cfg.Logger)
	})
}

func GetUser(a *auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := lookupUser(w, r, a.cfg); ok {
			writeResponse(w, User{Email: u.Email, RoleID: u.Role}, http.StatusOK, a.cfg.Logger)
		}
	})
}

// UpdateUser changes the role of a user, and ends its sessions. An admin cannot change its own role, so that there is
// always an admin.
func UpdateUser(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params UpdateUserParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if !validRole(params.RoleID) {
			writeError(w, http.StatusBadRequest, "Invalid role ID", nil, cfg.Logger)
			return
		}

		u, ok := lookupUser(w, r, cfg)
		if !ok {
			return
		}

		p, _ := PrincipalFrom(r.Context())
		if u.ID == p.UserID {
			writeError(w, http.StatusForbidden, "Cannot modify your own account", nil, cfg.Logger)
			return
		}

		err := cfg.Auth.UpdateUserRole(r.Context(), u.ID, params.RoleID)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "User not found", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to update user", err, cfg.Logger)
			return
		}

		a.audit(r, "update_user", p.Email, fmt.Sprintf("Role of %s set to %d", u.Email, params.RoleID))
		writeResponse(w, SuccessResponse{Message: "User updated successfully"}, http.StatusOK, cfg.Logger)
	})
}

// UpdateUserPassword sets the password of a user, and ends all of its sessions. An admin changes its own password with
// UpdateMyPassword, which needs the current one.
func UpdateUserPassword(a *auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params UpdateUserPasswordParams
		if !decodeBody(w, r, &params, a.cfg) {
			return
		}

		u, ok := lookupUser(w, r, a.cfg)
		if !ok {
			return
		}

		if p, _ := PrincipalFrom(r.Context()); u.ID == p.UserID {
			writeError(w, http.StatusForbidden, "Change your own password with PUT /api/v1/users/me/password", nil,
				a.cfg.Logger)

			return
		}

		if !a.setPassword(w, r, u, params.Password, nil) {
			return
		}

		writeResponse(w, SuccessResponse{Message: "Password updated successfully"}, http.StatusOK, a.cfg.Logger)
	})
}

// DeleteUser deletes a user, with its sessions and API tokens. An admin cannot delete itself.
func DeleteUser(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := lookupUser(w, r, cfg)
		if !ok {
			return
		}

		p, _ := PrincipalFrom(r.Context())
		if u.ID == p.UserID {
			writeError(w, http.StatusForbidden, "Cannot delete your own account", nil, cfg.Logger)
			return
		}

		err := cfg.Auth.DeleteUser(r.Context(), u.ID)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "User not found", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to delete user", err, cfg.Logger)
			return
		}

		a.audit(r, "delete_user", p.Email, "User "+u.Email+" deleted")
		writeResponse(w, SuccessResponse{Message: "User deleted successfully"}, http.StatusOK, cfg.Logger)
	})
}

// tokenOwner returns the user whose API tokens a request is about: the caller, or the user of the path for a request
// under /users/{email}.
type tokenOwner func(w http.ResponseWriter, r *http.Request) (db.User, bool)

func myself(cfg Config) tokenOwner {
	return func(w http.ResponseWriter, r *http.Request) (db.User, bool) {
		p, _ := PrincipalFrom(r.Context())

		u, err := cfg.Auth.GetUserByID(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve user", err, cfg.Logger)
			return db.User{}, false
		}

		return u, true
	}
}

func userOfPath(cfg Config) tokenOwner {
	return func(w http.ResponseWriter, r *http.Request) (db.User, bool) {
		return lookupUser(w, r, cfg)
	}
}

func ListAPITokens(a *auth, owner tokenOwner) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		u, ok := owner(w, r)
		if !ok {
			return
		}

		tokens, total, err := cfg.Auth.ListAPITokens(r.Context(), u.ID, page, perPage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list API tokens", err, cfg.Logger)
			return
		}

		resp := ListAPITokensResponse{
			Items: make([]APIToken, 0, len(tokens)), Page: page, PerPage: perPage,
			TotalCount: total,
		}
		for _, t := range tokens {
			resp.Items = append(resp.Items, apiTokenOf(t))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

// CreateAPIToken creates an API token, which the response holds: the only time it is shown.
func CreateAPIToken(a *auth, owner tokenOwner) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params CreateAPITokenParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if n := len([]rune(params.Name)); n < minTokenNameLength || n > maxTokenNameLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Token name must be between %d and %d characters",
				minTokenNameLength, maxTokenNameLength), nil, cfg.Logger)

			return
		}

		now := cfg.Now()

		var expiresAt time.Time

		if params.ExpiresAt != "" {
			var err error
			if expiresAt, err = time.Parse(time.RFC3339, params.ExpiresAt); err != nil {
				writeError(w, http.StatusBadRequest, "expires_at must be an RFC 3339 time", err, cfg.Logger)
				return
			}

			if !expiresAt.After(now) {
				writeError(w, http.StatusBadRequest, "expires_at must be in the future", nil, cfg.Logger)
				return
			}
		}

		u, ok := owner(w, r)
		if !ok {
			return
		}

		n, err := cfg.Auth.CountAPITokens(r.Context(), u.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to count API tokens", err, cfg.Logger)
			return
		}

		if n >= MaxNumAPITokens {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Maximum number of API tokens reached (%d)", MaxNumAPITokens),
				nil, cfg.Logger)

			return
		}

		token, tokenID, hash := newAPIToken()

		_, err = cfg.Auth.CreateAPIToken(r.Context(), db.NewAPIToken{
			TokenID: tokenID, Name: params.Name, TokenHash: hash,
			UserID: u.ID, CreatedAt: now, ExpiresAt: expiresAt,
		})
		if errors.Is(err, db.ErrDuplicate) {
			writeError(w, http.StatusConflict, "API token already exists", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create API token", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_api_token", p.Email, fmt.Sprintf("API token %s (%s) created for %s", tokenID, params.Name,
			u.Email))
		writeResponse(w, CreateAPITokenResponse{Token: token}, http.StatusCreated, cfg.Logger)
	})
}

func DeleteAPIToken(a *auth, owner tokenOwner) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := owner(w, r)
		if !ok {
			return
		}

		tokenID := r.PathValue("id")

		err := cfg.Auth.DeleteAPIToken(r.Context(), u.ID, tokenID)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "API token not found", nil, cfg.Logger)
			return
		}

		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to delete API token", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "delete_api_token", p.Email, fmt.Sprintf("API token %s of %s deleted", tokenID, u.Email))
		writeResponse(w, SuccessResponse{Message: "API token deleted successfully"}, http.StatusOK, cfg.Logger)
	})
}

func lookupUser(w http.ResponseWriter, r *http.Request, cfg Config) (db.User, bool) {
	u, err := cfg.Auth.GetUser(r.Context(), r.PathValue("email"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "User not found", nil, cfg.Logger)
		return db.User{}, false
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to retrieve user", err, cfg.Logger)
		return db.User{}, false
	}

	return u, true
}

func apiTokenOf(t db.APIToken) APIToken {
	out := APIToken{ID: t.TokenID, Name: t.Name, CreatedAt: formatTime(t.CreatedAt)}

	if !t.ExpiresAt.IsZero() {
		out.ExpiresAt = formatTime(t.ExpiresAt)
	}

	if !t.LastUsedAt.IsZero() {
		out.LastUsedAt = formatTime(t.LastUsedAt)
	}

	return out
}
