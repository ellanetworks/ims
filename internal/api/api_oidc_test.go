package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

func testOIDC(t *testing.T, store *db.DB) *oidc.Live {
	t.Helper()

	o, err := store.GetOIDC(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return oidc.NewLive(store, o)
}

func validOIDC() map[string]any {
	return map[string]any{
		"enabled":                  true,
		"external_url":             "https://ims.example.com/",
		"issuer":                   "https://idp.example.com/realms/ims",
		"client_id":                "ims",
		"client_secret":            "s3cret-value",
		"display_name":             "Keycloak",
		"scopes":                   []string{"openid", "email", "profile"},
		"session_lifetime_seconds": 28800,
		"trust_unverified_email":   false,
		"ca_certificate":           "",
		"groups_claim":             "groups",
		"group_roles":              []map[string]any{{"group": "noc", "role_id": 3}, {"group": "ops", "role_id": 2}},
	}
}

func body(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestOIDCSettings(t *testing.T) {
	a := newTestAPI(t)

	code, result, msg := a.do(t, http.MethodGet, "/api/v1/oidc", "")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %q", code, msg)
	}

	if got := decode[api.OIDCSettings](t, result); got.Enabled || got.SessionLifetimeSeconds != 86400 ||
		strings.Join(got.Scopes, " ") != "openid email profile" || got.RedirectURL != "" || got.ClientSecretSet {
		t.Fatalf("default settings = %+v", got)
	}

	code, result, msg = a.do(t, http.MethodPut, "/api/v1/oidc", body(t, validOIDC()))
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %q", code, msg)
	}

	if strings.Contains(string(result), "s3cret-value") {
		t.Fatalf("PUT response holds the client secret: %s", result)
	}

	want := api.OIDCSettings{
		Enabled:                true,
		ExternalURL:            "https://ims.example.com",
		RedirectURL:            "https://ims.example.com/api/v1/auth/oidc/callback",
		Issuer:                 "https://idp.example.com/realms/ims",
		ClientID:               "ims",
		ClientSecretSet:        true,
		DisplayName:            "Keycloak",
		Scopes:                 []string{"openid", "email", "profile"},
		SessionLifetimeSeconds: 28800,
		GroupsClaim:            "groups",
		GroupRoles:             []api.OIDCGroupRole{{Group: "noc", RoleID: 3}, {Group: "ops", RoleID: 2}},
	}

	for name, raw := range map[string]json.RawMessage{"PUT": result, "GET": a.get(t, "/api/v1/oidc")} {
		if got := decode[api.OIDCSettings](t, raw); body(t, got) != body(t, want) {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}

	keep := validOIDC()
	delete(keep, "client_secret")
	keep["display_name"] = "Corporate SSO"

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, keep)); code != http.StatusOK {
		t.Fatalf("PUT without a client secret = %d %q", code, msg)
	}

	stored, err := a.store.GetOIDC(context.Background())
	if err != nil || stored.ClientSecret != "s3cret-value" || stored.DisplayName != "Corporate SSO" {
		t.Fatalf("stored after PUT without a client secret: %+v, %v", stored, err)
	}

	events := strings.Join(a.auditEvents(t, "update_oidc"), "\n")
	if strings.Contains(events, "s3cret-value") || !strings.Contains(events, "client_secret") ||
		!strings.Contains(events, "display_name") {
		t.Fatalf("audit events = %q", events)
	}
}

func (a *testAPI) get(t *testing.T, path string) json.RawMessage {
	t.Helper()

	code, result, msg := a.do(t, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %q", path, code, msg)
	}

	return result
}

func TestOIDCSettingsAreValidated(t *testing.T) {
	a := newTestAPI(t)

	tests := map[string]func(map[string]any){
		"an http issuer":              func(o map[string]any) { o["issuer"] = "http://idp.example.com" },
		"an issuer with a query":      func(o map[string]any) { o["issuer"] = "https://idp.example.com?x=1" },
		"an issuer with a fragment":   func(o map[string]any) { o["issuer"] = "https://idp.example.com#x" },
		"no issuer":                   func(o map[string]any) { o["issuer"] = "" },
		"an http external URL":        func(o map[string]any) { o["external_url"] = "http://ims.example.com" },
		"an external URL with a path": func(o map[string]any) { o["external_url"] = "https://example.com/ims" },
		"no client ID":                func(o map[string]any) { o["client_id"] = "" },
		"no display name":             func(o map[string]any) { o["display_name"] = "" },
		"no openid scope":             func(o map[string]any) { o["scopes"] = []string{"email"} },
		"no email scope":              func(o map[string]any) { o["scopes"] = []string{"openid", "profile"} },
		"an external URL with //":     func(o map[string]any) { o["external_url"] = "https://ims.example.com//" },
		"a repeated scope":            func(o map[string]any) { o["scopes"] = []string{"openid", "openid"} },
		"a scope with a space":        func(o map[string]any) { o["scopes"] = []string{"openid", "a b"} },
		"a short session":             func(o map[string]any) { o["session_lifetime_seconds"] = 299 },
		"a long session":              func(o map[string]any) { o["session_lifetime_seconds"] = 30*86400 + 1 },
		"no session lifetime":         func(o map[string]any) { delete(o, "session_lifetime_seconds") },
		"a CA that is not PEM":        func(o map[string]any) { o["ca_certificate"] = "not a certificate" },
		"group roles without a claim": func(o map[string]any) { o["groups_claim"] = "" },
		"a repeated group": func(o map[string]any) {
			o["group_roles"] = []map[string]any{{"group": "a", "role_id": 2}, {"group": "a", "role_id": 3}}
		},
		"a group with an unknown role":  func(o map[string]any) { o["group_roles"] = []map[string]any{{"group": "a", "role_id": 9}} },
		"a group with no name":          func(o map[string]any) { o["group_roles"] = []map[string]any{{"group": "", "role_id": 2}} },
		"an unknown field":              func(o map[string]any) { o["client_secret_set"] = true },
		"a display name with a newline": func(o map[string]any) { o["display_name"] = "a\nb" },
	}

	for name, change := range tests {
		o := validOIDC()
		change(o)

		if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o)); code != http.StatusBadRequest {
			t.Errorf("PUT with %s = %d %q, want %d", name, code, msg, http.StatusBadRequest)
		}
	}

	o := validOIDC()
	delete(o, "client_secret")

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o)); code != http.StatusBadRequest {
		t.Errorf("enabling without a client secret = %d %q, want %d", code, msg, http.StatusBadRequest)
	}

	if got := decode[api.OIDCSettings](t, a.get(t, "/api/v1/oidc")); got.Enabled {
		t.Fatalf("settings after refused updates: %+v", got)
	}

	disabled := map[string]any{"enabled": false, "scopes": []string{"openid", "email"}, "session_lifetime_seconds": 3600}
	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, disabled)); code != http.StatusOK {
		t.Fatalf("PUT disabled and empty = %d %q", code, msg)
	}
}

func TestOIDCSettingsNeedAnAdminLogin(t *testing.T) {
	a := newTestAPI(t)

	nm := a.userLogin(t, "nm@example.com", db.RoleNetworkManager)
	adminToken := a.createToken(t, a.token, "/api/v1/users/me/api-tokens", `{"name": "admin"}`)

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/oidc", ""},
		{http.MethodPut, "/api/v1/oidc", body(t, validOIDC())},
		{http.MethodPost, "/api/v1/oidc/discovery", `{"issuer": "https://idp.example.com"}`},
	} {
		for name, token := range map[string]string{"a network manager": nm, "an admin API token": adminToken} {
			if code, _, _ := a.doAs(t, token, tc.method, tc.path, tc.body); code != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want %d", tc.method, tc.path, name, code, http.StatusForbidden)
			}
		}
	}
}

type fakeIdP struct {
	server   *httptest.Server
	issuer   string
	metadata map[string]any
	keys     jose.JSONWebKeySet
	signer   jose.Signer

	mu         sync.Mutex
	codes      map[string]issuedCode
	userInfos  map[string]map[string]any
	authMethod string
	tokenType  string
	onToken    func()
	onDiscover func(w http.ResponseWriter) bool
	extra      http.HandlerFunc

	discoveries atomic.Int64
	tokens      atomic.Int64
	userInfoHit atomic.Int64
}

type issuedCode struct {
	challenge string
	idToken   string
	userInfo  map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: "sig"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}

	idp := &fakeIdP{signer: signer, codes: map[string]issuedCode{}, userInfos: map[string]map[string]any{}}
	idp.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/realms/ims/.well-known/openid-configuration":
			idp.discoveries.Add(1)

			idp.mu.Lock()
			onDiscover := idp.onDiscover
			metadata := clone(idp.metadata)
			idp.mu.Unlock()

			if onDiscover == nil || !onDiscover(w) {
				_ = json.NewEncoder(w).Encode(metadata)
			}
		case "/realms/ims/keys":
			_ = json.NewEncoder(w).Encode(idp.keys)
		case "/realms/ims/token":
			idp.tokens.Add(1)
			idp.token(w, r)
		case "/realms/ims/userinfo":
			idp.userInfoHit.Add(1)
			idp.userInfo(w, r)
		default:
			idp.mu.Lock()
			extra := idp.extra
			idp.mu.Unlock()

			if extra != nil {
				extra(w, r)
				return
			}

			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.server.Close)

	idp.issuer = idp.server.URL + "/realms/ims"
	idp.metadata = map[string]any{
		"issuer":                                         idp.issuer,
		"authorization_endpoint":                         idp.issuer + "/auth",
		"token_endpoint":                                 idp.issuer + "/token",
		"jwks_uri":                                       idp.issuer + "/keys",
		"userinfo_endpoint":                              idp.issuer + "/userinfo",
		"response_types_supported":                       []string{"code", "id_token"},
		"id_token_signing_alg_values_supported":          []string{"HS256", "ES256"},
		"code_challenge_methods_supported":               []string{"plain", "S256"},
		"authorization_response_iss_parameter_supported": true,
	}
	idp.keys = jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: key.Public(), KeyID: "sig", Algorithm: "ES256", Use: "sig"},
		{Key: []byte("0123456789abcdef0123456789abcdef"), KeyID: "hmac", Algorithm: "HS256", Use: "sig"},
	}}

	return idp
}

func (idp *fakeIdP) ca() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.server.Certificate().Raw}))
}

func TestDiscoverOIDC(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)

	discover := func(issuer, ca string) (int, json.RawMessage, string) {
		return a.do(t, http.MethodPost, "/api/v1/oidc/discovery",
			body(t, map[string]string{"issuer": issuer, "ca_certificate": ca}))
	}

	code, result, msg := discover(idp.issuer, idp.ca())
	if code != http.StatusOK {
		t.Fatalf("discovery = %d %q", code, msg)
	}

	want := api.OIDCDiscovery{
		Issuer:                  idp.issuer,
		AuthorizationEndpoint:   idp.issuer + "/auth",
		TokenEndpoint:           idp.issuer + "/token",
		UserInfoEndpoint:        idp.issuer + "/userinfo",
		JWKSURI:                 idp.issuer + "/keys",
		SigningAlgorithms:       []string{"ES256"},
		TokenEndpointAuthMethod: "client_secret_basic",
		Keys:                    1,
		PKCE:                    true,
		IssuerParameter:         true,
	}
	if got := decode[api.OIDCDiscovery](t, result); body(t, got) != body(t, want) {
		t.Fatalf("discovery = %+v, want %+v", got, want)
	}

	if code, _, msg := discover(idp.issuer, ""); code != http.StatusBadGateway {
		t.Errorf("discovery without trusting the provider's CA = %d %q, want %d", code, msg, http.StatusBadGateway)
	}

	if code, _, msg := discover(idp.issuer+"/", idp.ca()); code != http.StatusBadGateway ||
		!strings.Contains(msg, "did not match") {
		t.Errorf("discovery with another issuer = %d %q, want %d about the issuer", code, msg, http.StatusBadGateway)
	}

	for name, issuer := range map[string]string{"an http issuer": "http://idp.example.com", "no issuer": ""} {
		if code, _, msg := discover(issuer, ""); code != http.StatusBadRequest {
			t.Errorf("discovery with %s = %d %q, want %d", name, code, msg, http.StatusBadRequest)
		}
	}

	tests := map[string]func(){
		"only symmetric algorithms": func() { idp.metadata["id_token_signing_alg_values_supported"] = []string{"HS256", "none"} },
		"no code flow":              func() { idp.metadata["response_types_supported"] = []string{"id_token"} },
		"an http token endpoint":    func() { idp.metadata["token_endpoint"] = "http://idp.example.com/token" },
		"no jwks_uri":               func() { delete(idp.metadata, "jwks_uri") },
		"no public signing key":     func() { idp.keys.Keys = idp.keys.Keys[1:] },
		"an http userinfo endpoint": func() { idp.metadata["userinfo_endpoint"] = "http://idp.example.com/userinfo" },
		"no client secret authentication": func() {
			idp.metadata["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt", "tls_client_auth"}
		},
	}

	for name, change := range tests {
		saved, savedKeys := clone(idp.metadata), idp.keys

		change()

		if code, _, msg := discover(idp.issuer, idp.ca()); code != http.StatusBadGateway {
			t.Errorf("discovery with %s = %d %q, want %d", name, code, msg, http.StatusBadGateway)
		}

		idp.metadata, idp.keys = saved, savedKeys
	}
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}

	return out
}

func (a *testAPI) sessionAs(t *testing.T, email string) string {
	t.Helper()

	ctx := context.Background()

	u, err := a.store.GetUser(ctx, email)
	if err != nil {
		t.Fatal(err)
	}

	id, err := a.store.CreateSession(ctx, db.NewSession{
		UserID: u.ID, TokenHash: []byte("session of " + email), Method: db.SessionPassword, CreatedAt: a.now,
		ExpiresAt: a.now.Add(time.Hour),
	}, api.MaxSessionsPerUser)
	if err != nil {
		t.Fatal(err)
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id": u.ID, "email": u.Email, "role_id": u.Role, "sid": id,
		"iat": a.now.Unix(), "exp": a.now.Add(api.AccessTokenDuration).Unix(),
	}).SignedString(testJWTSecret)
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func TestUsersWithoutAPassword(t *testing.T) {
	a := newTestAPI(t)

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/users", `{"email": "sso@example.com", "role_id": 1}`); code != http.StatusCreated {
		t.Fatalf("create a user without a password = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/sso@example.com")); got != (api.User{
		Email: "sso@example.com", RoleID: db.RoleAdmin,
	}) {
		t.Fatalf("user = %+v", got)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/me")); got != (api.User{
		Email: testAdmin, RoleID: db.RoleAdmin, PasswordSet: true,
	}) {
		t.Fatalf("me = %+v", got)
	}

	sso := a.sessionAs(t, "sso@example.com")

	code, _, msg := a.doAs(t, sso, http.MethodPut, "/api/v1/users/me/password",
		`{"current_password": "", "password": "a new password"}`)
	if code != http.StatusConflict {
		t.Fatalf("change my password without one = %d %q, want %d", code, msg, http.StatusConflict)
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodDelete, "/api/v1/users/" + testAdmin + "/password", ""},
		{http.MethodDelete, "/api/v1/users/" + testAdmin, ""},
		{http.MethodPut, "/api/v1/users/" + testAdmin, `{"role_id": 2}`},
	} {
		code, _, msg := a.doAs(t, sso, tc.method, tc.path, tc.body)
		if code != http.StatusConflict {
			t.Errorf("%s %s of the last admin with a password = %d %q, want %d", tc.method, tc.path, code, msg,
				http.StatusConflict)
		}
	}

	a.createUser(t, "second@example.com", db.RoleAdmin)

	if code, _, msg := a.doAs(t, sso, http.MethodDelete, "/api/v1/users/"+testAdmin+"/password", ""); code != http.StatusOK {
		t.Fatalf("remove a password with another admin with one = %d %q", code, msg)
	}

	if code, _, _ := a.doAs(t, "", http.MethodPost, "/api/v1/auth/login",
		`{"email": "`+testAdmin+`", "password": "`+testPassword+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("login after removing the password = %d, want %d", code, http.StatusUnauthorized)
	}

	if code, _, _ := a.doAs(t, sso, http.MethodDelete, "/api/v1/users/sso@example.com/password", ""); code != http.StatusForbidden {
		t.Fatalf("remove my own password = %d, want %d", code, http.StatusForbidden)
	}

	if events := a.auditEvents(t, "delete_user_password"); len(events) != 1 {
		t.Fatalf("audit events = %q", events)
	}
}

func TestUnlinkUser(t *testing.T) {
	a := newTestAPI(t)
	ctx := context.Background()

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, validOIDC())); code != http.StatusOK {
		t.Fatalf("PUT /api/v1/oidc = %d %q", code, msg)
	}

	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	u, err := a.store.GetUser(ctx, "nm@example.com")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.store.CreateIdentity(ctx, u.ID, db.NewIdentity{
		Issuer: "https://idp.example.com/realms/ims", Subject: "sub", At: a.now,
	}); err != nil {
		t.Fatal(err)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); !got.OIDCLinked {
		t.Fatalf("user before unlinking = %+v", got)
	}

	if code, _, msg := a.do(t, http.MethodDelete, "/api/v1/users/nm@example.com/oidc", ""); code != http.StatusOK {
		t.Fatalf("unlink = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.OIDCLinked {
		t.Fatalf("user after unlinking = %+v", got)
	}

	if code, _, _ := a.do(t, http.MethodDelete, "/api/v1/users/nobody@example.com/oidc", ""); code != http.StatusNotFound {
		t.Fatalf("unlink an unknown user = %d, want %d", code, http.StatusNotFound)
	}
}

func TestRoleFromGroupsNeedsAPin(t *testing.T) {
	a := newTestAPI(t)
	ctx := context.Background()

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, validOIDC())); code != http.StatusOK {
		t.Fatalf("PUT /api/v1/oidc = %d %q", code, msg)
	}

	a.createUser(t, "nm@example.com", db.RoleNetworkManager)
	a.createUser(t, "local@example.com", db.RoleNetworkManager)

	u, err := a.store.GetUser(ctx, "nm@example.com")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.store.CreateIdentity(ctx, u.ID, db.NewIdentity{
		Issuer: "https://idp.example.com/realms/ims", Subject: "sub", At: a.now,
	}); err != nil {
		t.Fatal(err)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/local@example.com", `{"role_id": 2}`); code != http.StatusOK {
		t.Fatalf("change the role of a user without a link = %d %q", code, msg)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 3}`); code != http.StatusOK {
		t.Fatalf("keep the role of a user with groups = %d %q", code, msg)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 2}`); code != http.StatusConflict {
		t.Fatalf("change the role of a user with groups = %d %q, want %d", code, msg, http.StatusConflict)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 2, "role_pinned": true}`); code != http.StatusOK {
		t.Fatalf("change and pin the role = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.RoleID != db.RoleReadOnly || !got.RolePinned {
		t.Fatalf("user = %+v", got)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 3}`); code != http.StatusOK {
		t.Fatalf("change a pinned role = %d %q", code, msg)
	}

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/users/nm@example.com", `{"role_id": 3, "role_pinned": false}`); code != http.StatusOK {
		t.Fatalf("unpin = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.RolePinned {
		t.Fatalf("user after unpinning = %+v", got)
	}
}
