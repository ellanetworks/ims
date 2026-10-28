package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
)

// createUser creates a user, as the admin, with testPassword.
func (a *testAPI) createUser(t *testing.T, email string, role db.Role) {
	t.Helper()

	body, _ := json.Marshal(api.CreateUserParams{Email: email, Password: testPassword, RoleID: role})

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/users", string(body)); code != http.StatusCreated {
		t.Fatalf("create user %s = %d %q", email, code, msg)
	}
}

// createToken creates an API token at path, as a token, and returns it.
func (a *testAPI) createToken(t *testing.T, token, path, body string) string {
	t.Helper()

	code, result, msg := a.doAs(t, token, http.MethodPost, path, body)
	if code != http.StatusCreated {
		t.Fatalf("POST %s = %d %q", path, code, msg)
	}

	return decode[api.CreateAPITokenResponse](t, result).Token
}

// userLogin creates a user, logs it in, and returns its access token.
// userToken creates a Network Manager, and an API token of it, which it returns.
func (a *testAPI) userToken(t *testing.T, email string) string {
	t.Helper()

	a.createUser(t, email, db.RoleNetworkManager)

	return a.createToken(t, a.token, "/api/v1/users/"+email+"/api-tokens", `{"name": "test"}`)
}

func listTokens(t *testing.T, a *testAPI, token, path string) []api.APIToken {
	t.Helper()

	code, result, msg := a.doAs(t, token, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %q", path, code, msg)
	}

	return decode[api.ListAPITokensResponse](t, result).Items
}

func TestUsers(t *testing.T) {
	a := newTestAPI(t)

	a.createUser(t, "NM@example.com", db.RoleNetworkManager)

	code, result, _ := a.do(t, http.MethodGet, "/api/v1/users/nm@example.com", "")
	if got := decode[api.User](t, result); code != http.StatusOK || got != (api.User{Email: "nm@example.com", RoleID: 3}) {
		t.Fatalf("GET user = %d %+v", code, got)
	}

	code, result, _ = a.do(t, http.MethodGet, "/api/v1/users?per_page=1&page=2", "")
	if got := decode[api.ListUsersResponse](t, result); code != http.StatusOK || got.TotalCount != 2 ||
		len(got.Items) != 1 || got.Items[0].Email != "nm@example.com" {
		t.Fatalf("GET users page 2 = %d %+v", code, got)
	}

	for name, tc := range map[string]struct {
		method, path, body string
		want               int
	}{
		"duplicate in another case": {http.MethodPost, "/api/v1/users", `{"email": "nm@EXAMPLE.com", "password": "correct horse", "role_id": 2}`, http.StatusConflict},
		"invalid email":             {http.MethodPost, "/api/v1/users", `{"email": "nm", "password": "correct horse", "role_id": 2}`, http.StatusBadRequest},
		"invalid role":              {http.MethodPost, "/api/v1/users", `{"email": "x@example.com", "password": "correct horse", "role_id": 5}`, http.StatusBadRequest},
		"no role":                   {http.MethodPost, "/api/v1/users", `{"email": "x@example.com", "password": "correct horse"}`, http.StatusBadRequest},
		"short password":            {http.MethodPost, "/api/v1/users", `{"email": "x@example.com", "password": "short", "role_id": 2}`, http.StatusBadRequest},
		"unknown user":              {http.MethodGet, "/api/v1/users/x@example.com", "", http.StatusNotFound},
		"invalid new role":          {http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 0}`, http.StatusBadRequest},
		"own role":                  {http.MethodPut, "/api/v1/users/" + testAdmin, `{"role_id": 2}`, http.StatusForbidden},
		"own account":               {http.MethodDelete, "/api/v1/users/" + testAdmin, "", http.StatusForbidden},
		"short new password":        {http.MethodPut, "/api/v1/users/nm@example.com/password", `{"password": "short"}`, http.StatusBadRequest},
		"own password":              {http.MethodPut, "/api/v1/users/" + testAdmin + "/password", `{"password": "new password"}`, http.StatusForbidden},
		"unknown user's password":   {http.MethodPut, "/api/v1/users/x@example.com/password", `{"password": "correct horse"}`, http.StatusNotFound},
		"delete unknown user":       {http.MethodDelete, "/api/v1/users/x@example.com", "", http.StatusNotFound},
	} {
		if code, _, msg := a.do(t, tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s: %d %q, want %d", name, code, msg, tc.want)
		}
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 2}`); code != http.StatusOK {
		t.Fatalf("update role = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com/password", `{"password": "new password"}`); code != http.StatusOK {
		t.Fatalf("update password = %d", code)
	}

	token, _ := a.login(t, "nm@example.com", "new password")

	if code, result, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusOK ||
		decode[api.User](t, result).RoleID != db.RoleReadOnly {
		t.Fatalf("GET me after the updates = %d %s", code, result)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/users/nm@example.com", ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("access token of a deleted user = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestMaxNumUsers(t *testing.T) {
	a := newTestAPI(t)

	for i := 1; i < api.MaxNumUsers; i++ {
		a.createUser(t, fmt.Sprintf("user%d@example.com", i), db.RoleReadOnly)
	}

	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/users",
		`{"email": "onemore@example.com", "password": "correct horse", "role_id": 2}`); code != http.StatusBadRequest {
		t.Fatalf("user %d = %d, want %d", api.MaxNumUsers+1, code, http.StatusBadRequest)
	}
}

func TestUserChangesEndSessions(t *testing.T) {
	a := newTestAPI(t)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	_, cookie := a.login(t, "nm@example.com", testPassword)

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 2}`); code != http.StatusOK {
		t.Fatal(code)
	}

	if code := a.refresh(t, cookie); code != http.StatusUnauthorized {
		t.Fatalf("refresh after a role change = %d, want %d", code, http.StatusUnauthorized)
	}

	_, cookie = a.login(t, "nm@example.com", testPassword)

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com/password", `{"password": "new password"}`); code != http.StatusOK {
		t.Fatal(code)
	}

	if code := a.refresh(t, cookie); code != http.StatusUnauthorized {
		t.Fatalf("refresh after a password reset = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestUpdateMyPassword(t *testing.T) {
	a := newTestAPI(t)
	a.createUser(t, "ro@example.com", db.RoleReadOnly)

	token, current := a.login(t, "ro@example.com", testPassword)
	_, other := a.login(t, "ro@example.com", testPassword)

	update := func(body string) (int, string) {
		req := request(http.MethodPut, "/api/v1/users/me/password", body, token)
		req.AddCookie(current)

		rec := serve(a.handler, req)

		var resp api.ErrorResponse

		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		return rec.Code, resp.Error
	}

	if code, _ := update(`{"current_password": "wrong password", "password": "new password"}`); code != http.StatusBadRequest {
		t.Fatalf("wrong current password = %d, want %d", code, http.StatusBadRequest)
	}

	if code, _ := update(`{"current_password": "` + testPassword + `", "password": "short"}`); code != http.StatusBadRequest {
		t.Fatalf("short new password = %d, want %d", code, http.StatusBadRequest)
	}

	if code, msg := update(`{"current_password": "` + testPassword + `", "password": "new password"}`); code != http.StatusOK {
		t.Fatalf("update = %d %q", code, msg)
	}

	if code := a.refresh(t, current); code != http.StatusOK {
		t.Fatalf("refresh of the session that changed the password = %d, want %d", code, http.StatusOK)
	}

	if code := a.refresh(t, other); code != http.StatusUnauthorized {
		t.Fatalf("refresh of another session = %d, want %d", code, http.StatusUnauthorized)
	}

	a.login(t, "ro@example.com", "new password")
}

func TestAPITokens(t *testing.T) {
	a := newTestAPI(t)
	token := a.userToken(t, "app@example.com")
	login, _ := a.login(t, "app@example.com", testPassword)

	if !strings.HasPrefix(token, api.APITokenPrefix) {
		t.Fatalf("token %q does not start with %s", token, api.APITokenPrefix)
	}

	tokens := listTokens(t, a, token, "/api/v1/users/me/api-tokens")
	if len(tokens) != 1 || tokens[0].Name != "test" || tokens[0].ExpiresAt != "" ||
		tokens[0].CreatedAt != "2026-09-29T10:00:00.000Z" || !strings.Contains(token, "_"+tokens[0].ID+"_") {
		t.Fatalf("tokens = %+v", tokens)
	}

	// The listing above used the token.
	if tokens := listTokens(t, a, a.token, "/api/v1/users/app@example.com/api-tokens"); tokens[0].LastUsedAt != "2026-09-29T10:00:00.000Z" {
		t.Fatalf("last used = %q", tokens[0].LastUsedAt)
	}

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"same name":        {`{"name": "test"}`, http.StatusConflict},
		"short name":       {`{"name": "ab"}`, http.StatusBadRequest},
		"long name":        {`{"name": "` + strings.Repeat("a", 51) + `"}`, http.StatusBadRequest},
		"past expiry":      {`{"name": "past", "expires_at": "2026-09-29T09:00:00Z"}`, http.StatusBadRequest},
		"invalid expiry":   {`{"name": "bad", "expires_at": "tomorrow"}`, http.StatusBadRequest},
		"unknown field":    {`{"name": "new", "scopes": []}`, http.StatusBadRequest},
		"expiring":         {`{"name": "expiring", "expires_at": "2026-09-30T10:00:00Z"}`, http.StatusCreated},
		"not expiring yet": {`{"name": "later", "expires_at": "2027-01-01T00:00:00+01:00"}`, http.StatusCreated},
	} {
		if code, _, msg := a.doAs(t, login, http.MethodPost, "/api/v1/users/me/api-tokens", tc.body); code != tc.want {
			t.Errorf("%s: %d %q, want %d", name, code, msg, tc.want)
		}
	}

	id := tokens[0].ID
	tokenID, secret, _ := strings.Cut(strings.TrimPrefix(token, api.APITokenPrefix), "_")

	if code, _, _ := a.doAs(t, api.APITokenPrefix+tokenID+"_"+strings.ToLower(secret), http.MethodGet,
		"/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want %d", code, http.StatusUnauthorized)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/users/me/api-tokens/"+id, ""); code != http.StatusNotFound {
		t.Fatalf("delete another user's token as mine = %d, want %d", code, http.StatusNotFound)
	}

	if code, _, _ := a.doAs(t, token, http.MethodDelete, "/api/v1/users/me/api-tokens/"+id, ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("deleted token = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestAPITokenExpiry(t *testing.T) {
	a := newTestAPI(t)
	token := a.createToken(t, a.token, "/api/v1/users/me/api-tokens",
		`{"name": "expiring", "expires_at": "2026-09-29T11:00:00Z"}`)

	a.now = testNow.Add(59 * time.Minute)

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusOK {
		t.Fatalf("before expiry = %d", code)
	}

	a.now = testNow.Add(time.Hour)

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("at expiry = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestAdminManagesAnotherUsersTokens(t *testing.T) {
	a := newTestAPI(t)
	a.createUser(t, "app@example.com", db.RoleNetworkManager)

	path := "/api/v1/users/app@example.com/api-tokens"
	token := a.createToken(t, a.token, path, `{"name": "platform"}`)

	tokens := listTokens(t, a, a.token, path)
	if len(tokens) != 1 {
		t.Fatalf("tokens = %+v", tokens)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/users/nobody@example.com/api-tokens/"+tokens[0].ID, ""); code != http.StatusNotFound {
		t.Fatalf("delete under an unknown user = %d, want %d", code, http.StatusNotFound)
	}

	if code, _, _ := a.do(t, http.MethodDelete, path+"/"+tokens[0].ID, ""); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("deleted token = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestMaxNumAPITokens(t *testing.T) {
	a := newTestAPI(t)

	for i := range api.MaxNumAPITokens {
		a.createToken(t, a.token, "/api/v1/users/me/api-tokens", fmt.Sprintf(`{"name": "token %d"}`, i))
	}

	if code, _, _ := a.do(t, http.MethodPost, "/api/v1/users/me/api-tokens", `{"name": "one more"}`); code != http.StatusBadRequest {
		t.Fatalf("token %d = %d, want %d", api.MaxNumAPITokens+1, code, http.StatusBadRequest)
	}
}

// TestAPITokensCannotCreateCredentials checks that an API token cannot create other API tokens, which would outlive
// its revocation, change a password, or manage users, but can do the rest of what its role allows.
func TestAPITokensCannotCreateCredentials(t *testing.T) {
	a := newTestAPI(t)
	app := a.userToken(t, "app@example.com")
	admin := a.createToken(t, a.token, "/api/v1/users/me/api-tokens", `{"name": "automation"}`)

	for _, tc := range []struct {
		token, method, path, body string
		want                      int
	}{
		{app, http.MethodPost, "/api/v1/users/me/api-tokens", `{"name": "sibling"}`, http.StatusForbidden},
		{app, http.MethodPut, "/api/v1/users/me/password", `{"current_password": "correct horse", "password": "new password"}`, http.StatusForbidden},
		{admin, http.MethodPost, "/api/v1/users", `{"email": "x@example.com", "password": "correct horse", "role_id": 1}`, http.StatusForbidden},
		{admin, http.MethodPost, "/api/v1/users/app@example.com/api-tokens", `{"name": "other"}`, http.StatusForbidden},
		{admin, http.MethodGet, "/api/v1/users", "", http.StatusForbidden},
		{app, http.MethodGet, "/api/v1/users/me", "", http.StatusOK},
		{app, http.MethodGet, "/api/v1/users/me/api-tokens", "", http.StatusOK},
		{admin, http.MethodGet, "/api/v1/users/me/api-tokens", "", http.StatusOK},
	} {
		if code, _, msg := a.doAs(t, tc.token, tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s %s with an API token = %d %q, want %d", tc.method, tc.path, code, msg, tc.want)
		}
	}
}
