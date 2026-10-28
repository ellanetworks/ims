package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/golang-jwt/jwt/v5"
)

// login logs a user in, and returns its access token and its session cookie.
func (a *testAPI) login(t *testing.T, email, password string) (string, *http.Cookie) {
	t.Helper()

	rec := serve(a.handler, request(http.MethodPost, "/api/v1/auth/login",
		`{"email": "`+email+`", "password": "`+password+`"}`, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s = %d %s", email, rec.Code, rec.Body)
	}

	var resp struct {
		Result api.LoginResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Result.Token, sessionCookie(t, rec)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	res := rec.Result()

	defer func() { _ = res.Body.Close() }()

	for _, c := range res.Cookies() {
		if c.Name == api.SessionCookieName {
			return c
		}
	}

	t.Fatal("no session cookie")

	return nil
}

// refresh asks for a new access token with a session cookie, and returns the status.
func (a *testAPI) refresh(t *testing.T, cookie *http.Cookie) int {
	t.Helper()

	req := request(http.MethodPost, "/api/v1/auth/refresh", "", "")
	if cookie != nil {
		req.AddCookie(cookie)
	}

	return serve(a.handler, req).Code
}

func TestInitialize(t *testing.T) {
	a := newUninitializedAPI(t)

	initialized := func() bool {
		t.Helper()

		code, result, _ := a.doAs(t, "", http.MethodGet, "/api/v1/status", "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/v1/status = %d", code)
		}

		return decode[api.Status](t, result).Initialized
	}

	if initialized() {
		t.Fatal("initialized before the first user")
	}

	for name, body := range map[string]string{
		"no email":          `{"password": "correct horse"}`,
		"invalid email":     `{"email": "admin", "password": "correct horse"}`,
		"display name":      `{"email": "Admin <admin@example.com>", "password": "correct horse"}`,
		"short password":    `{"email": "admin@example.com", "password": "short"}`,
		"long password":     `{"email": "admin@example.com", "password": "` + strings.Repeat("x", 73) + `"}`,
		"unknown field":     `{"email": "admin@example.com", "password": "correct horse", "role_id": 1}`,
		"not JSON":          `email=admin@example.com`,
		"oversized request": `{"email": "admin@example.com", "password": "` + strings.Repeat("x", 8<<10) + `"}`,
	} {
		if code, _, _ := a.doAs(t, "", http.MethodPost, "/api/v1/init", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want %d", name, code, http.StatusBadRequest)
		}
	}

	rec := serve(a.handler, request(http.MethodPost, "/api/v1/init",
		`{"email": "Admin@Example.com", "password": "correct horse"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("init = %d %s", rec.Code, rec.Body)
	}

	sessionCookie(t, rec)

	if !initialized() {
		t.Fatal("not initialized after the first user")
	}

	if code, _, errMsg := a.doAs(t, "", http.MethodPost, "/api/v1/init",
		`{"email": "other@example.com", "password": "correct horse"}`); code != http.StatusForbidden {
		t.Fatalf("second init = %d %q, want %d", code, errMsg, http.StatusForbidden)
	}

	// The email is stored in lower case, and the admin can log in with it.
	token, _ := a.login(t, "admin@example.com", "correct horse")

	code, result, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", "")
	if got := decode[api.User](t, result); code != http.StatusOK || got != (api.User{Email: "admin@example.com", RoleID: 1}) {
		t.Fatalf("GET /api/v1/users/me = %d %+v", code, got)
	}
}

func TestInitializeOnce(t *testing.T) {
	a := newUninitializedAPI(t)

	const callers = 10

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)

	for i := range callers {
		wg.Go(func() {
			rec := serve(a.handler, request(http.MethodPost, "/api/v1/init",
				`{"email": "admin`+string(rune('a'+i))+`@example.com", "password": "correct horse"}`, ""))

			mu.Lock()
			defer mu.Unlock()

			if rec.Code == http.StatusCreated {
				created++
			}
		})
	}

	wg.Wait()

	if n, err := a.store.CountUsers(t.Context()); created != 1 || n != 1 || err != nil {
		t.Fatalf("%d of %d concurrent inits succeeded, and %d users exist (%v), want 1", created, callers, n, err)
	}
}

func TestLogin(t *testing.T) {
	a := newTestAPI(t)

	rec := serve(a.handler, request(http.MethodPost, "/api/v1/auth/login",
		`{"email": "ADMIN@example.com", "password": "`+testPassword+`"}`, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}

	c := sessionCookie(t, rec)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Path != "/" {
		t.Fatalf("cookie = %+v, want HttpOnly, SameSite=Lax, not Secure without TLS, Path=/", c)
	}

	if want := testNow.Add(api.SessionTokenDuration); !c.Expires.Equal(want) {
		t.Fatalf("cookie expires %v, want %v", c.Expires, want)
	}

	// An unknown email and a wrong password get the same answer, so that it does not tell which emails exist.
	code, _, unknownEmail := a.doAs(t, "", http.MethodPost, "/api/v1/auth/login",
		`{"email": "nobody@example.com", "password": "wrong password"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("unknown email = %d, want %d", code, http.StatusUnauthorized)
	}

	if code, _, msg := a.doAs(t, "", http.MethodPost, "/api/v1/auth/login",
		`{"email": "`+testAdmin+`", "password": "wrong password"}`); code != http.StatusUnauthorized || msg != unknownEmail {
		t.Fatalf("wrong password = %d %q, want %d %q like an unknown email", code, msg, http.StatusUnauthorized,
			unknownEmail)
	}

	if code, _, _ := a.doAs(t, "", http.MethodPost, "/api/v1/auth/login", `{"email": "`+testAdmin+`"}`); code != http.StatusBadRequest {
		t.Fatalf("login without password = %d, want %d", code, http.StatusBadRequest)
	}
}

// loginFrom sends a login from an address, and returns its status.
func (a *testAPI) loginFrom(addr, email, password string) int {
	req := request(http.MethodPost, "/api/v1/auth/login", `{"email": "`+email+`", "password": "`+password+`"}`, "")
	req.RemoteAddr = addr

	return serve(a.handler, req).Code
}

func TestLoginRateLimit(t *testing.T) {
	a := newTestAPI(t)

	const client = "198.51.100.1:4242"

	for i := range api.LoginRateLimit {
		if code := a.loginFrom(client, testAdmin, "wrong password"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want %d", i+1, code, http.StatusUnauthorized)
		}
	}

	req := request(http.MethodPost, "/api/v1/auth/login", `{"email": "`+testAdmin+`", "password": "`+testPassword+`"}`, "")
	req.RemoteAddr = client

	rec := serve(a.handler, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("attempt %d = %d, Retry-After %q, want %d, 60", api.LoginRateLimit+1, rec.Code,
			rec.Header().Get("Retry-After"), http.StatusTooManyRequests)
	}

	if code := a.loginFrom("198.51.100.2:4242", testAdmin, testPassword); code != http.StatusOK {
		t.Fatalf("login from another address = %d, want %d", code, http.StatusOK)
	}

	a.now = a.now.Add(api.LoginRateWindow)

	if code := a.loginFrom(client, testAdmin, testPassword); code != http.StatusOK {
		t.Fatalf("login after the window = %d, want %d", code, http.StatusOK)
	}
}

// TestLoginRateLimitPerIPv6Prefix checks that the addresses of an IPv6 /64, which a single host commonly has, share
// their limit.
func TestLoginRateLimitPerIPv6Prefix(t *testing.T) {
	a := newTestAPI(t)

	for i := range api.LoginRateLimit {
		a.loginFrom(fmt.Sprintf("[2001:db8::%x]:4242", i+1), testAdmin, "wrong password")
	}

	if code := a.loginFrom("[2001:db8::ffff]:4242", testAdmin, testPassword); code != http.StatusTooManyRequests {
		t.Fatalf("login from the same /64 = %d, want %d", code, http.StatusTooManyRequests)
	}

	if code := a.loginFrom("[2001:db8:0:1::1]:4242", testAdmin, testPassword); code != http.StatusOK {
		t.Fatalf("login from another /64 = %d, want %d", code, http.StatusOK)
	}
}

// TestAccountFailureLimit checks that the wrong passwords for an account are limited whatever their source, that the
// wrong current passwords of a password change count with them, and that a login resets them.
func TestAccountFailureLimit(t *testing.T) {
	a := newTestAPI(t)

	for i := range api.AccountFailureLimit - 1 {
		a.loginFrom(fmt.Sprintf("198.51.%d.%d:4242", i/250, i%250+1), "ADMIN@example.com", "wrong password")
	}

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/me/password",
		`{"current_password": "wrong password", "password": "new password"}`); code != http.StatusBadRequest {
		t.Fatalf("wrong current password = %d, want %d", code, http.StatusBadRequest)
	}

	rec := serve(a.handler, func() *http.Request {
		req := request(http.MethodPost, "/api/v1/auth/login", `{"email": "`+testAdmin+`", "password": "`+testPassword+`"}`, "")
		req.RemoteAddr = "203.0.113.1:4242"

		return req
	}())
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("login after %d failures = %d, Retry-After %q", api.AccountFailureLimit, rec.Code,
			rec.Header().Get("Retry-After"))
	}

	if code := a.loginFrom("203.0.113.2:4242", "nobody@example.com", "wrong password"); code != http.StatusUnauthorized {
		t.Fatalf("login to another account = %d, want %d", code, http.StatusUnauthorized)
	}

	a.now = a.now.Add(api.AccountFailureWindow)

	if code := a.loginFrom("203.0.113.3:4242", testAdmin, testPassword); code != http.StatusOK {
		t.Fatalf("login after the window = %d, want %d", code, http.StatusOK)
	}
}

// TestInitAfterInitialization checks that a call to init on an initialized system is refused, and rate-limited
// like a login.
func TestInitAfterInitialization(t *testing.T) {
	a := newTestAPI(t)
	body := `{"email": "other@example.com", "password": "correct horse"}`

	for i := 1; i < api.LoginRateLimit; i++ {
		if code, _, _ := a.doAs(t, "", http.MethodPost, "/api/v1/init", body); code != http.StatusForbidden {
			t.Fatalf("init %d = %d, want %d", i, code, http.StatusForbidden)
		}
	}

	if code, _, _ := a.doAs(t, "", http.MethodPost, "/api/v1/init", body); code != http.StatusTooManyRequests {
		t.Fatalf("init %d = %d, want %d", api.LoginRateLimit+1, code, http.StatusTooManyRequests)
	}
}

// TestEndedSessionEndsAccessTokens checks that an access token does not outlive its session: after a logout, or a
// change of role, it no longer authenticates.
func TestEndedSessionEndsAccessTokens(t *testing.T) {
	a := newTestAPI(t)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	token, cookie := a.login(t, "nm@example.com", testPassword)

	req := request(http.MethodPost, "/api/v1/auth/logout", "", "")
	req.AddCookie(cookie)
	serve(a.handler, req)

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("access token after logout = %d, want %d", code, http.StatusUnauthorized)
	}

	token, _ = a.login(t, "nm@example.com", testPassword)

	if code, _, _ := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 2}`); code != http.StatusOK {
		t.Fatal(code)
	}

	if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("access token after a demotion = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestRefresh(t *testing.T) {
	a := newTestAPI(t)
	_, cookie := a.login(t, testAdmin, testPassword)

	rec := serve(a.handler, func() *http.Request {
		req := request(http.MethodPost, "/api/v1/auth/refresh", "", "")
		req.AddCookie(cookie)

		return req
	}())
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Result api.RefreshResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if code, _, _ := a.doAs(t, resp.Result.Token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusOK {
		t.Fatalf("refreshed token: GET /api/v1/users/me = %d", code)
	}

	for name, c := range map[string]*http.Cookie{
		"no cookie":       nil,
		"not base64":      {Name: api.SessionCookieName, Value: "!!!"},
		"wrong length":    {Name: api.SessionCookieName, Value: "YWJj"},
		"unknown session": {Name: api.SessionCookieName, Value: strings.Repeat("A", 43)},
	} {
		req := request(http.MethodPost, "/api/v1/auth/refresh", "", "")
		if c != nil {
			req.AddCookie(c)
		}

		rec := serve(a.handler, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want %d", name, rec.Code, http.StatusUnauthorized)
		}

		if c := sessionCookie(t, rec); c.MaxAge >= 0 {
			t.Errorf("%s: cookie not cleared: %+v", name, c)
		}
	}

	a.now = a.now.Add(api.SessionTokenDuration)

	if code := a.refresh(t, cookie); code != http.StatusUnauthorized {
		t.Fatalf("refresh of an expired session = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestLogout(t *testing.T) {
	a := newTestAPI(t)
	_, cookie := a.login(t, testAdmin, testPassword)

	req := request(http.MethodPost, "/api/v1/auth/logout", "", "")
	req.AddCookie(cookie)

	rec := serve(a.handler, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}

	if c := sessionCookie(t, rec); c.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}

	if code := a.refresh(t, cookie); code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want %d", code, http.StatusUnauthorized)
	}

	if code := serve(a.handler, request(http.MethodPost, "/api/v1/auth/logout", "", "")).Code; code != http.StatusNoContent {
		t.Fatalf("logout without a session = %d, want %d", code, http.StatusNoContent)
	}
}

func TestSessionLimitPerUser(t *testing.T) {
	a := newTestAPI(t)

	var cookies []*http.Cookie

	for i := range api.MaxSessionsPerUser + 1 {
		a.now = testNow.Add(time.Duration(i) * time.Second)

		_, c := a.login(t, testAdmin, testPassword)
		cookies = append(cookies, c)
	}

	if code := a.refresh(t, cookies[0]); code != http.StatusUnauthorized {
		t.Fatalf("refresh of the oldest session = %d, want %d", code, http.StatusUnauthorized)
	}

	if code := a.refresh(t, cookies[1]); code != http.StatusOK {
		t.Fatalf("refresh of the second oldest session = %d, want %d", code, http.StatusOK)
	}
}

func TestAccessTokens(t *testing.T) {
	a := newTestAPI(t)

	sign := func(method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
		t.Helper()

		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	// The session of the initialization is the first.
	valid := jwt.MapClaims{
		"id": 1, "email": testAdmin, "role_id": 1, "sid": 1, "iat": testNow.Unix(),
		"exp": testNow.Add(time.Minute).Unix(),
	}
	without := func(key string) jwt.MapClaims {
		c := jwt.MapClaims{}

		for k, v := range valid {
			if k != key {
				c[k] = v
			}
		}

		return c
	}
	with := func(key string, value any) jwt.MapClaims {
		c := without(key)
		c[key] = value

		return c
	}

	if code, _, _ := a.doAs(t, sign(jwt.SigningMethodHS256, testJWTSecret, valid), http.MethodGet, "/api/v1/users/me",
		""); code != http.StatusOK {
		t.Fatalf("valid token = %d", code)
	}

	for name, token := range map[string]string{
		"expired":           sign(jwt.SigningMethodHS256, testJWTSecret, with("exp", testNow.Unix())),
		"without expiry":    sign(jwt.SigningMethodHS256, testJWTSecret, without("exp")),
		"issued later":      sign(jwt.SigningMethodHS256, testJWTSecret, with("iat", testNow.Add(time.Hour).Unix())),
		"other secret":      sign(jwt.SigningMethodHS256, []byte("another secret of 32 bytes......"), valid),
		"HS512":             sign(jwt.SigningMethodHS512, testJWTSecret, valid),
		"none":              sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid),
		"unknown user":      sign(jwt.SigningMethodHS256, testJWTSecret, with("id", 42)),
		"without session":   sign(jwt.SigningMethodHS256, testJWTSecret, without("sid")),
		"unknown session":   sign(jwt.SigningMethodHS256, testJWTSecret, with("sid", 42)),
		"not a JWT":         "garbage",
		"truncated":         sign(jwt.SigningMethodHS256, testJWTSecret, valid)[:20],
		"API token prefix":  api.APITokenPrefix + "abc",
		"API token no ID":   api.APITokenPrefix + "_secret",
		"API token unknown": api.APITokenPrefix + "abcdefghijkl_secret",
	} {
		if code, _, _ := a.doAs(t, token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want %d", name, code, http.StatusUnauthorized)
		}
	}

	a.now = testNow.Add(api.AccessTokenDuration)

	if code, _, _ := a.doAs(t, a.token, http.MethodGet, "/api/v1/users/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("access token after %v = %d, want %d", api.AccessTokenDuration, code, http.StatusUnauthorized)
	}
}
