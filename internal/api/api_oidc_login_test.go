package api_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/go-jose/go-jose/v4"
)

const testClientSecret = "s3cret-value"

func (idp *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	basicUser, basicPass, basic := r.BasicAuth()
	_ = r.ParseForm()
	_, inBody := r.PostForm["client_secret"]

	user, pass := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if basic {
		user, _ = url.QueryUnescape(basicUser)
		pass, _ = url.QueryUnescape(basicPass)
	}

	idp.mu.Lock()
	issued, found := idp.codes[r.PostForm.Get("code")]
	delete(idp.codes, r.PostForm.Get("code"))

	idp.authMethod = "client_secret_post"
	if basic {
		idp.authMethod = "client_secret_basic"
	}

	tokenType, onToken := idp.tokenType, idp.onToken
	idp.mu.Unlock()

	if onToken != nil {
		onToken()
	}

	if tokenType == "" {
		tokenType = "Bearer"
	}

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))

	switch {
	case basic && inBody || user != "ims" || pass != testClientSecret:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid_client"}`))
	case !found || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Get("redirect_uri") != "https://ims.example.com/api/v1/auth/oidc/callback" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != issued.challenge:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "invalid_grant"}`))
	default:
		access := issued.access
		if access == "" {
			access = rand.Text()
		}

		idp.mu.Lock()
		idp.userInfos[access] = issued.userInfo
		idp.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "token_type": tokenType, "expires_in": 300, "id_token": issued.idToken,
		})
	}
}

func (idp *fakeIdP) userInfo(w http.ResponseWriter, r *http.Request) {
	scheme, access, _ := strings.Cut(r.Header.Get("Authorization"), " ")

	idp.mu.Lock()
	claims, ok := idp.userInfos[access]
	idp.mu.Unlock()

	if scheme != "Bearer" || !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)

		return
	}

	_ = json.NewEncoder(w).Encode(claims)
}

func (idp *fakeIdP) sign(t *testing.T, signer jose.Signer, claims map[string]any) string {
	t.Helper()

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}

	token, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func (a *testAPI) configureOIDC(t *testing.T, idp *fakeIdP, change func(map[string]any)) {
	t.Helper()

	o := validOIDC()
	o["issuer"] = idp.issuer
	o["ca_certificate"] = idp.ca()
	o["group_roles"] = []map[string]any{}

	if change != nil {
		change(o)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o)); code != http.StatusOK {
		t.Fatalf("PUT /api/v1/oidc = %d %q", code, msg)
	}

	idp.discoveries.Store(0)
}

type oidcLogin struct {
	claims   map[string]any
	userInfo map[string]any
	signer   jose.Signer
	callback func(q url.Values)
	cookie   func(c *http.Cookie) *http.Cookie
	between  func()
	link     *http.Cookie
	access   string
}

type oidcResult struct {
	rec      *httptest.ResponseRecorder
	session  *http.Cookie
	outcome  string
	callback url.Values
	cookie   *http.Cookie
	browser  string
	location string
}

var browsers atomic.Int64

func newBrowser() string {
	n := browsers.Add(1)

	return fmt.Sprintf("10.%d.%d.%d:40000", n>>16&0xff, n>>8&0xff, n&0xff)
}

func (a *testAPI) startOIDC(t *testing.T) (url.Values, *http.Cookie) {
	t.Helper()

	return a.startOIDCFrom(t, newBrowser())
}

func (a *testAPI) startOIDCFrom(t *testing.T, browser string) (url.Values, *http.Cookie) {
	t.Helper()

	return a.startOIDCLink(t, browser, nil)
}

func (a *testAPI) startOIDCLink(t *testing.T, browser string, session *http.Cookie) (url.Values, *http.Cookie) {
	t.Helper()

	path := "/api/v1/auth/oidc/login"
	if session != nil {
		path += "?link=1"
	}

	req := request(http.MethodGet, path, "", "")
	req.RemoteAddr = browser

	if session != nil {
		req.AddCookie(session)
	}

	rec := serve(a.handler, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d %s, Location %q", rec.Code, rec.Body, rec.Header().Get("Location"))
	}

	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range rec.Result().Cookies() {
		if c.Name == api.OIDCLoginCookieName {
			return location.Query(), c
		}
	}

	t.Fatal("no login cookie")

	return nil, nil
}

func (a *testAPI) oidcLogin(t *testing.T, idp *fakeIdP, l oidcLogin) oidcResult {
	t.Helper()

	browser := newBrowser()
	q, cookie := a.startOIDCLink(t, browser, l.link)

	claims := map[string]any{
		"iss": idp.issuer, "sub": "sub-nm", "aud": "ims", "iat": a.now.Unix(), "exp": a.now.Add(5 * time.Minute).Unix(),
		"nonce": q.Get("nonce"), "email": "nm@example.com", "email_verified": true,
	}

	for k, v := range l.claims {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}

	userInfo := map[string]any{}

	for _, k := range []string{"sub", "email", "email_verified", "groups"} {
		if v, ok := claims[k]; ok {
			userInfo[k] = v
		}
	}

	for k, v := range l.userInfo {
		if v == nil {
			delete(userInfo, k)
		} else {
			userInfo[k] = v
		}
	}

	signer := idp.signer
	if l.signer != nil {
		signer = l.signer
	}

	idp.mu.Lock()
	idp.codes["code"] = issuedCode{
		challenge: q.Get("code_challenge"), idToken: idp.sign(t, signer, claims), userInfo: userInfo, access: l.access,
	}
	idp.mu.Unlock()

	if l.between != nil {
		l.between()
	}

	callback := url.Values{"code": {"code"}, "state": {q.Get("state")}, "iss": {idp.issuer}}
	if l.callback != nil {
		l.callback(callback)
	}

	if l.cookie != nil {
		cookie = l.cookie(cookie)
	}

	return a.oidcCallback(t, browser, callback, cookie, l.link)
}

func (a *testAPI) oidcCallback(t *testing.T, browser string, callback url.Values, cookies ...*http.Cookie) oidcResult {
	t.Helper()

	req := request(http.MethodGet, "/api/v1/auth/oidc/callback?"+callback.Encode(), "", "")
	req.RemoteAddr = browser

	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}

	cookie := cookies[0]

	rec := serve(a.handler, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body)
	}

	res := oidcResult{rec: rec, outcome: "success", callback: callback, cookie: cookie, browser: browser}

	if location := rec.Header().Get("Location"); location != "/" && location != "/profile" {
		u, err := url.Parse(location)
		if err != nil || u.Path != "/login" && u.Path != "/profile" {
			t.Fatalf("callback redirects to %q", location)
		}

		res.outcome = u.Query().Get("error")
	}

	res.location = rec.Header().Get("Location")

	for _, c := range rec.Result().Cookies() {
		if c.Name == "ims_session" && c.MaxAge >= 0 && c.Value != "" {
			res.session = c
		}
	}

	return res
}

func TestOIDCLoginInfo(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)

	info := func() api.OIDCLogin {
		code, result, msg := a.doAs(t, "", http.MethodGet, "/api/v1/auth/oidc", "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/v1/auth/oidc = %d %q", code, msg)
		}

		return decode[api.OIDCLogin](t, result)
	}

	if got := info(); got != (api.OIDCLogin{}) {
		t.Fatalf("disabled = %+v", got)
	}

	a.configureOIDC(t, idp, nil)

	if got := info(); got != (api.OIDCLogin{Enabled: true, DisplayName: "Keycloak"}) {
		t.Fatalf("enabled = %+v", got)
	}
}

func TestOIDCLoginRequest(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)

	q, cookie := a.startOIDC(t)

	want := map[string]string{
		"response_type":         "code",
		"client_id":             "ims",
		"redirect_uri":          "https://ims.example.com/api/v1/auth/oidc/callback",
		"scope":                 "openid email profile",
		"code_challenge_method": "S256",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}

	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if len(q.Get(k)) < 22 {
			t.Errorf("%s = %q, want at least 128 random bits", k, q.Get(k))
		}
	}

	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" ||
		cookie.MaxAge != 600 || strings.Contains(cookie.Value, q.Get("state")) {
		t.Fatalf("login cookie = %+v", cookie)
	}

	q2, _ := a.startOIDC(t)
	if q2.Get("state") == q.Get("state") || q2.Get("nonce") == q.Get("nonce") {
		t.Fatal("two logins share a state or a nonce")
	}
}

func TestOIDCLoginLinksByEmailThenBySubject(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

	res := a.oidcLogin(t, idp, oidcLogin{})
	if res.outcome != "success" || res.session == nil {
		t.Fatalf("first login: outcome %q, session %v", res.outcome, res.session)
	}

	if want := a.now.Add(8 * time.Hour); !res.session.Expires.Equal(want) {
		t.Fatalf("session expires %v, want %v", res.session.Expires, want)
	}

	if code := a.refresh(t, res.session); code != http.StatusOK {
		t.Fatalf("refresh after an OIDC login = %d", code)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); !got.OIDCLinked || got.PasswordSet {
		t.Fatalf("user after linking = %+v", got)
	}

	res = a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"email": "renamed@example.com", "email_verified": false}})
	if res.outcome != "success" {
		t.Fatalf("login with another email: %q", res.outcome)
	}

	i, err := a.store.GetIdentity(context.Background(), idp.issuer, "sub-nm")
	if err != nil || i.Email != "renamed@example.com" {
		t.Fatalf("identity = %+v, %v", i, err)
	}

	a.createSSOUser(t, "renamed@example.com", db.RoleAdmin)

	res = a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"email": "renamed@example.com"}})
	if res.outcome != "success" {
		t.Fatalf("login after another user took the email: %q", res.outcome)
	}

	code, result, _ := a.doAs(t, "", http.MethodPost, "/api/v1/auth/refresh", "")
	_ = result

	if code != http.StatusUnauthorized {
		t.Fatalf("refresh without a cookie = %d", code)
	}

	if events := strings.Join(a.auditEvents(t, "auth_oidc_login"), "\n"); strings.Count(events, "logged in through OIDC") != 3 {
		t.Fatalf("audit events = %q", events)
	}

	if events := a.auditEvents(t, "link_user_oidc"); len(events) != 1 {
		t.Fatalf("link audit events = %q", events)
	}
}

func TestOIDCLoginRefusals(t *testing.T) {
	idp := newFakeIdP(t)

	hmacSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		login   func(a *testAPI) oidcLogin
		outcome string
	}{
		"an unknown user": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"email": "nobody@example.com"}}
		}, "not_set_up"},
		"no email": {func(*testAPI) oidcLogin { return oidcLogin{claims: map[string]any{"email": nil}} }, "not_set_up"},
		"an unverified email": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"email_verified": false}}
		}, "email_not_verified"},
		"no email_verified": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"email_verified": nil}}
		}, "email_not_verified"},
		"email_verified as the string true": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"email_verified": "true"}}
		}, "success"},
		"a wrong nonce": {func(*testAPI) oidcLogin { return oidcLogin{claims: map[string]any{"nonce": "other"}} }, "failed"},
		"no nonce":      {func(*testAPI) oidcLogin { return oidcLogin{claims: map[string]any{"nonce": nil}} }, "failed"},
		"a wrong state": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Set("state", "other") }}
		}, "failed"},
		"no state": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Del("state") }}
		}, "failed"},
		"no login cookie": {func(*testAPI) oidcLogin {
			return oidcLogin{cookie: func(*http.Cookie) *http.Cookie { return nil }}
		}, "failed"},
		"a forged login cookie": {func(*testAPI) oidcLogin {
			return oidcLogin{cookie: func(c *http.Cookie) *http.Cookie {
				forged := "A"
				if c.Value[0] == 'A' {
					forged = "B"
				}

				c.Value = forged + c.Value[1:]

				return c
			}}
		}, "failed"},
		"an expired login": {func(a *testAPI) oidcLogin {
			return oidcLogin{between: func() { a.now = a.now.Add(10 * time.Minute) }}
		}, "failed"},
		"a wrong iss parameter": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Set("iss", "https://evil.example.com") }}
		}, "failed"},
		"no iss parameter": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Del("iss") }}
		}, "failed"},
		"no code": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Del("code") }}
		}, "failed"},
		"a wrong code": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Set("code", "other") }}
		}, "failed"},
		"access denied": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }}
		}, "denied"},
		"another error": {func(*testAPI) oidcLogin {
			return oidcLogin{callback: func(q url.Values) { q.Del("code"); q.Set("error", "server_error") }}
		}, "failed"},
		"a token from another issuer": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"iss": "https://evil.example.com"}}
		}, "failed"},
		"a token for another client": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"aud": "other"}}
		}, "failed"},
		"a token issued to another party": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"aud": []string{"ims", "other"}, "azp": "other"}}
		}, "failed"},
		"several audiences without azp": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"aud": []string{"ims", "other"}}}
		}, "failed"},
		"several audiences with azp": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"aud": []string{"ims", "other"}, "azp": "ims"}}
		}, "failed"},
		"an audience array with the client alone": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"aud": []string{"ims"}, "azp": "ims"}}
		}, "success"},
		"a token without an issue time": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"iat": nil}}
		}, "failed"},
		"a token issued in the future": {func(a *testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"iat": a.now.Add(6 * time.Minute).Unix()}}
		}, "failed"},
		"a token issued a little ahead": {func(a *testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"iat": a.now.Add(4 * time.Minute).Unix()}}
		}, "success"},
		"an explicitly unverified email as a string": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"email_verified": "false"}}
		}, "email_not_verified"},
		"an expired token": {func(a *testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"exp": a.now.Add(-2 * time.Minute).Unix()}}
		}, "failed"},
		"a matching at_hash": {func(*testAPI) oidcLogin {
			return oidcLogin{access: "the access token", claims: map[string]any{"at_hash": atHash("the access token")}}
		}, "success"},
		"a wrong at_hash": {func(*testAPI) oidcLogin {
			return oidcLogin{access: "the access token", claims: map[string]any{"at_hash": atHash("another token")}}
		}, "failed"},
		"a token expired within the leeway": {func(a *testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"exp": a.now.Add(-30 * time.Second).Unix()}}
		}, "success"},
		"a symmetric signature": {func(*testAPI) oidcLogin { return oidcLogin{signer: hmacSigner} }, "failed"},
		"a subject too long": {func(*testAPI) oidcLogin {
			return oidcLogin{claims: map[string]any{"sub": strings.Repeat("s", 256)}}
		}, "failed"},
		"settings changed during the login": {func(a *testAPI) oidcLogin {
			return oidcLogin{between: func() {
				a.configureOIDC(t, idp, func(o map[string]any) { o["client_id"] = "other" })
			}}
		}, "failed"},
		"OIDC disabled during the login": {func(a *testAPI) oidcLogin {
			return oidcLogin{between: func() {
				a.configureOIDC(t, idp, func(o map[string]any) { o["enabled"] = false })
			}}
		}, "unavailable"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t)
			a.configureOIDC(t, idp, nil)
			a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

			res := a.oidcLogin(t, idp, tc.login(a))
			if res.outcome != tc.outcome {
				t.Fatalf("outcome %q, want %q", res.outcome, tc.outcome)
			}

			if (res.session != nil) != (tc.outcome == "success") {
				t.Fatalf("session cookie %v with outcome %q", res.session, res.outcome)
			}

			if tc.outcome != "success" {
				events := a.auditEvents(t, "auth_oidc_login")
				if len(events) == 0 || !strings.Contains(events[len(events)-1], tc.outcome+": ") {
					t.Fatalf("audit events = %q", events)
				}
			}
		})
	}
}

func TestOIDCLoginTrustsUnverifiedEmailOnRequest(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) { o["trust_unverified_email"] = true })
	a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

	for _, verified := range []any{false, "false"} {
		res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": "sub-attacker", "email": testAdmin, "email_verified": verified}})
		if res.outcome != "email_not_verified" || res.session != nil {
			t.Fatalf("email_verified %v with trust: outcome %q, session %v", verified, res.outcome, res.session)
		}
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/"+testAdmin)); got.OIDCLinked {
		t.Fatalf("admin after the refused logins = %+v", got)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"email_verified": nil}}); res.outcome != "success" {
		t.Fatalf("outcome %q", res.outcome)
	}
}

func TestOIDCLoginLinksOnce(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

	if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "success" {
		t.Fatalf("first login: %q", res.outcome)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": "sub-other"}}); res.outcome != "linked_elsewhere" {
		t.Fatalf("another account with the same email: %q, want linked_elsewhere", res.outcome)
	}

	if code, _, msg := a.do(t, http.MethodDelete, "/api/v1/users/nm@example.com/oidc", ""); code != http.StatusOK {
		t.Fatalf("unlink = %d %q", code, msg)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": "sub-other"}}); res.outcome != "success" {
		t.Fatalf("login after unlinking: %q", res.outcome)
	}
}

func TestOIDCLoginEndsWithTheSettings(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

	res := a.oidcLogin(t, idp, oidcLogin{})
	if res.outcome != "success" {
		t.Fatalf("login: %q", res.outcome)
	}

	a.configureOIDC(t, idp, func(o map[string]any) { o["enabled"] = false })

	if code := a.refresh(t, res.session); code != http.StatusUnauthorized {
		t.Fatalf("refresh after disabling OIDC = %d, want %d", code, http.StatusUnauthorized)
	}

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/auth/oidc/login", "", ""))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?error=unavailable" {
		t.Fatalf("start while disabled = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOIDCLoginRolesFromGroups(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) {
		o["group_roles"] = []map[string]any{{"group": "admins", "role_id": 1}, {"group": "noc", "role_id": 3}, {"group": "ops", "role_id": 2}}
	})

	login := func(sub, email string, groups any) string {
		t.Helper()

		return a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": sub, "email": email, "groups": groups}}).outcome
	}

	role := func(email string) api.User {
		t.Helper()

		return decode[api.User](t, a.get(t, "/api/v1/users/"+email))
	}

	if got := login("sub-new", "new@example.com", []string{"other", "ops", "noc"}); got != "success" {
		t.Fatalf("first login of a new user: %q", got)
	}

	if got := role("new@example.com"); got.RoleID != db.RoleNetworkManager || got.PasswordSet || !got.OIDCLinked {
		t.Fatalf("new user = %+v, want a network manager without a password", got)
	}

	if got := login("sub-new", "new@example.com", "ops"); got != "success" || role("new@example.com").RoleID != db.RoleReadOnly {
		t.Fatalf("login with fewer groups: %q, role %d", got, role("new@example.com").RoleID)
	}

	lastEvent := func() string {
		t.Helper()

		events := a.auditEvents(t, "auth_oidc_login")

		return events[len(events)-1]
	}

	if got := login("sub-new", "new@example.com", []string{"other", "/admins"}); got != "no_group" {
		t.Fatalf("login with no mapped group: %q, want no_group", got)
	}

	if got, want := lastEvent(), `none of the user's 2 groups is mapped to a role: "other", "/admins"`; !strings.HasSuffix(got, want) {
		t.Fatalf("audit detail = %q, want suffix %q", got, want)
	}

	many := []string{strings.Repeat("g", 100)}
	for i := range 11 {
		many = append(many, fmt.Sprintf("group-%d", i))
	}

	if got := login("sub-new", "new@example.com", many); got != "no_group" {
		t.Fatalf("login with many unmapped groups: %q, want no_group", got)
	}

	if got, want := lastEvent(), `12 groups is mapped to a role: "`+strings.Repeat("g", 24)+`…", "group-0", `; !strings.Contains(got, want) ||
		!strings.HasSuffix(got, `"group-8", and 2 more`) {
		t.Fatalf("audit detail = %q", got)
	}

	if got := login("sub-none", "none@example.com", nil); got != "no_group" {
		t.Fatalf("a new user with no group: %q, want no_group", got)
	}

	if got := lastEvent(); !strings.HasSuffix(got, "the user has no groups") {
		t.Fatalf("audit detail = %q", got)
	}

	res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{
		"sub": "sub-big", "email": "big@example.com", "groups": nil,
		"_claim_names": map[string]string{"groups": "src1"}, "_claim_sources": map[string]any{"src1": map[string]string{"endpoint": "https://graph"}},
	}})
	if res.outcome != "groups_overage" {
		t.Fatalf("group overage: %q", res.outcome)
	}

	admin, err := a.store.GetUser(context.Background(), testAdmin)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.store.CreateIdentity(context.Background(), admin.ID, db.NewIdentity{
		Issuer: idp.issuer, Subject: "sub-admin", At: a.now,
	}); err != nil {
		t.Fatal(err)
	}

	if got := login("sub-admin", testAdmin, []string{"ops"}); got != "success" || role(testAdmin).RoleID != db.RoleAdmin {
		t.Fatalf("login of the last admin with a password as ops: %q, role %d", got, role(testAdmin).RoleID)
	}

	if events := strings.Join(a.auditEvents(t, "update_user"), "\n"); !strings.Contains(events, "not applied") {
		t.Fatalf("update_user audit events = %q", events)
	}
}

func TestOIDCLoginCreatesUsersUpToTheLimit(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) {
		o["group_roles"] = []map[string]any{{"group": "ops", "role_id": 2}}
	})

	ctx := context.Background()
	for i := 1; i < api.MaxNumUsers; i++ {
		if _, err := a.store.CreateUser(ctx, db.NewUser{Email: "u" + strings.Repeat("x", i) + "@example.com", Role: db.RoleReadOnly}, api.MaxNumUsers); err != nil {
			t.Fatal(err)
		}
	}

	res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": "sub-late", "email": "late@example.com", "groups": []string{"ops"}}})
	if res.outcome != "too_many_users" {
		t.Fatalf("outcome %q, want too_many_users", res.outcome)
	}
}

func TestOIDCLoginLinksAUserWithAPasswordFromTheProfile(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "link_required" || res.session != nil {
		t.Fatalf("login of a user with a password: %q, session %v", res.outcome, res.session)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.OIDCLinked {
		t.Fatalf("user after a refused login = %+v", got)
	}

	_, session := a.login(t, "nm@example.com", testPassword)

	res := a.oidcLogin(t, idp, oidcLogin{link: session})
	if res.location != "/profile" || res.session != nil {
		t.Fatalf("link: Location %q, session %v", res.location, res.session)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); !got.OIDCLinked || !got.PasswordSet {
		t.Fatalf("user after the link = %+v", got)
	}

	if events := a.auditEvents(t, "link_user_oidc"); len(events) != 1 {
		t.Fatalf("link events = %q", events)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "success" || res.session == nil {
		t.Fatalf("login after the link: %q", res.outcome)
	}

	_, other := a.login(t, testAdmin, testPassword)
	if res := a.oidcLogin(t, idp, oidcLogin{link: other}); res.location != "/profile?error=linked_elsewhere" {
		t.Fatalf("link of an identity linked to another user: Location %q", res.location)
	}
}

func TestOIDCLinkNeedsASession(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/auth/oidc/login?link=1", "", ""))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/profile?error=failed" {
		t.Fatalf("link without a session = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}

	a.createUser(t, "nm@example.com", db.RoleNetworkManager)
	_, session := a.login(t, "nm@example.com", testPassword)

	res := a.oidcLogin(t, idp, oidcLogin{link: session, between: func() {
		if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com/password", `{"password": "another password"}`); code != http.StatusOK {
			t.Fatalf("change the password = %d %q", code, msg)
		}
	}})
	if res.location != "/profile?error=failed" {
		t.Fatalf("link after the session ended: Location %q", res.location)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.OIDCLinked {
		t.Fatalf("user = %+v", got)
	}
}

func TestOIDCLoginTakesTheHighestRole(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) {
		o["group_roles"] = []map[string]any{
			{"group": "viewers", "role_id": 2},
			{"group": "noc", "role_id": 3},
			{"group": "admins", "role_id": 1},
		}
	})

	for _, tc := range []struct {
		groups  []string
		outcome string
		role    db.Role
	}{
		{[]string{"viewers", "noc"}, "success", db.RoleNetworkManager},
		{[]string{"noc", "admins", "viewers"}, "success", db.RoleAdmin},
		{[]string{"viewers"}, "success", db.RoleReadOnly},
	} {
		sub := "sub-" + strings.Join(tc.groups, "-")
		email := strings.Join(tc.groups, "-") + "@example.com"

		res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": sub, "email": email, "groups": tc.groups}})
		if res.outcome != tc.outcome {
			t.Errorf("groups %v: %q, want %q", tc.groups, res.outcome, tc.outcome)
			continue
		}

		if tc.role != 0 {
			if got := decode[api.User](t, a.get(t, "/api/v1/users/"+email)); got.RoleID != tc.role {
				t.Errorf("groups %v: role %d, want %d", tc.groups, got.RoleID, tc.role)
			}
		}
	}
}

func TestOIDCLogoutRedirectsToTheIdentityProvider(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	idp.metadata["end_session_endpoint"] = idp.issuer + "/logout?realm=ims"
	a.configureOIDC(t, idp, nil)
	a.createSSOUser(t, "nm@example.com", db.RoleNetworkManager)

	logout := func(session *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()

		req := request(http.MethodPost, "/api/v1/auth/logout", "", "")
		req.AddCookie(session)

		return serve(a.handler, req)
	}

	res := a.oidcLogin(t, idp, oidcLogin{})
	if res.outcome != "success" {
		t.Fatalf("login: %q", res.outcome)
	}

	rec := logout(res.session)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Result api.LogoutResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(resp.Result.RedirectURL)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != idp.issuer+"/logout" {
		t.Fatalf("redirect URL = %q", resp.Result.RedirectURL)
	}

	if q := u.Query(); q.Get("realm") != "ims" || q.Get("client_id") != "ims" ||
		q.Get("post_logout_redirect_uri") != "https://ims.example.com/login" {
		t.Fatalf("redirect URL query = %v", q)
	}

	_, session := a.login(t, testAdmin, testPassword)
	if rec := logout(session); rec.Code != http.StatusNoContent {
		t.Fatalf("logout of a password session = %d %s", rec.Code, rec.Body)
	}
}

func atHash(access string) string {
	sum := sha256.Sum256([]byte(access))

	return base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2])
}
