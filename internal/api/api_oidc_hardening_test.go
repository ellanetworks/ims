package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/api"
	"github.com/ellanetworks/ims/internal/db"
)

func (a *testAPI) updateOIDC(t *testing.T, idp *fakeIdP, change func(map[string]any)) int {
	t.Helper()

	o := validOIDC()
	o["issuer"] = idp.issuer
	o["ca_certificate"] = idp.ca()
	o["group_roles"] = []map[string]any{}

	change(o)

	code, _, _ := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o))

	return code
}

func (a *testAPI) linked(t *testing.T, email string) bool {
	t.Helper()

	return decode[api.User](t, a.get(t, "/api/v1/users/"+email)).OIDCLinked
}

func TestOIDCLoginChecksTheSettingsWhenItEnds(t *testing.T) {
	tests := map[string]struct {
		change  func(o map[string]any, other *fakeIdP)
		outcome string
	}{
		"OIDC disabled": {func(o map[string]any, _ *fakeIdP) { o["enabled"] = false }, "unavailable"},
		"another issuer": {func(o map[string]any, other *fakeIdP) {
			o["issuer"], o["ca_certificate"] = other.issuer, other.ca()
		}, "failed"},
		"another client": {func(o map[string]any, _ *fakeIdP) { o["client_id"] = "other" }, "failed"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t)
			idp, other := newFakeIdP(t), newFakeIdP(t)
			a.configureOIDC(t, idp, nil)
			a.createUser(t, "nm@example.com", db.RoleNetworkManager)

			var code atomic.Int64

			idp.onToken = func() {
				code.Store(int64(a.updateOIDC(t, idp, func(o map[string]any) { tc.change(o, other) })))
			}

			res := a.oidcLogin(t, idp, oidcLogin{})
			if code.Load() != http.StatusOK {
				t.Fatalf("PUT /api/v1/oidc during the token request = %d", code.Load())
			}

			if res.outcome != tc.outcome || res.session != nil {
				t.Fatalf("outcome %q, session %v, want %q and no session", res.outcome, res.session, tc.outcome)
			}

			if a.linked(t, "nm@example.com") {
				t.Fatal("the user was linked")
			}

			if _, err := a.store.GetIdentity(context.Background(), idp.issuer, "sub-nm"); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("identity: %v, want none", err)
			}
		})
	}
}

func TestOIDCLoginIsSingleUse(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	res := a.oidcLogin(t, idp, oidcLogin{})
	if res.outcome != "success" {
		t.Fatalf("login: %q", res.outcome)
	}

	for range 3 {
		if again := a.oidcCallback(t, res.browser, res.callback, res.cookie); again.outcome != "failed" || again.session != nil {
			t.Fatalf("replay: outcome %q, session %v", again.outcome, again.session)
		}
	}

	failed := a.oidcLogin(t, idp, oidcLogin{callback: func(q url.Values) { q.Set("code", "wrong") }})
	if failed.outcome != "failed" {
		t.Fatalf("login with a wrong code: %q", failed.outcome)
	}

	a.oidcCallback(t, failed.browser, failed.callback, failed.cookie)

	if n := idp.tokens.Load(); n != 2 {
		t.Fatalf("token requests = %d, want 2", n)
	}

	if events := strings.Join(a.auditEvents(t, "auth_oidc_login"), "\n"); !strings.Contains(events, "already used") {
		t.Fatalf("audit events = %q", events)
	}
}

func TestOIDCSettingsNeedANewSecretForANewClient(t *testing.T) {
	a := newTestAPI(t)
	idp, other := newFakeIdP(t), newFakeIdP(t)
	a.configureOIDC(t, idp, nil)

	for name, change := range map[string]func(map[string]any){
		"another issuer": func(o map[string]any) { o["issuer"], o["ca_certificate"] = other.issuer, other.ca() },
		"another client": func(o map[string]any) { o["client_id"] = "other" },
		"OIDC disabled and another issuer": func(o map[string]any) {
			o["enabled"], o["issuer"], o["ca_certificate"] = false, other.issuer, other.ca()
		},
	} {
		if code := a.updateOIDC(t, idp, func(o map[string]any) { change(o); delete(o, "client_secret") }); code != http.StatusBadRequest {
			t.Errorf("%s without a client secret = %d, want %d", name, code, http.StatusBadRequest)
		}
	}

	if got := decode[api.OIDCSettings](t, a.get(t, "/api/v1/oidc")); got.Issuer != idp.issuer || got.ClientID != "ims" {
		t.Fatalf("settings after refused updates = %+v", got)
	}

	if code := a.updateOIDC(t, idp, func(o map[string]any) { o["issuer"], o["ca_certificate"] = other.issuer, other.ca() }); code != http.StatusOK {
		t.Fatalf("another issuer with a client secret = %d", code)
	}

	if code := a.updateOIDC(t, other, func(o map[string]any) { delete(o, "client_secret"); o["display_name"] = "x" }); code != http.StatusOK {
		t.Fatalf("the same client without a client secret = %d", code)
	}
}

func TestOIDCTokenRequestAuthenticatesOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		supported []string
		want      string
	}{
		"nothing advertised": {nil, "client_secret_basic"},
		"basic and post":     {[]string{"client_secret_post", "client_secret_basic"}, "client_secret_basic"},
		"post only":          {[]string{"private_key_jwt", "client_secret_post"}, "client_secret_post"},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t)
			idp := newFakeIdP(t)

			if tc.supported != nil {
				idp.metadata["token_endpoint_auth_methods_supported"] = tc.supported
			}

			a.configureOIDC(t, idp, nil)
			a.createUser(t, "nm@example.com", db.RoleNetworkManager)

			code, result, msg := a.do(t, http.MethodPost, "/api/v1/oidc/discovery",
				body(t, map[string]string{"issuer": idp.issuer, "ca_certificate": idp.ca()}))
			if got := decode[api.OIDCDiscovery](t, result); code != http.StatusOK || got.TokenEndpointAuthMethod != tc.want {
				t.Fatalf("discovery = %d %q, method %q, want %q", code, msg, got.TokenEndpointAuthMethod, tc.want)
			}

			if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "success" || idp.authMethod != tc.want {
				t.Fatalf("login: %q with %q, want success with %q", res.outcome, idp.authMethod, tc.want)
			}

			before := idp.tokens.Load()

			if res := a.oidcLogin(t, idp, oidcLogin{callback: func(q url.Values) { q.Set("code", "wrong") }}); res.outcome != "failed" {
				t.Fatalf("login with a wrong code: %q", res.outcome)
			}

			if n := idp.tokens.Load() - before; n != 1 {
				t.Fatalf("token requests for a wrong code = %d, want 1", n)
			}

			if events := a.auditEvents(t, "auth_oidc_login"); !strings.Contains(events[len(events)-1], "invalid_grant") {
				t.Fatalf("audit event = %q, want invalid_grant", events[len(events)-1])
			}
		})
	}

	a := newTestAPI(t)
	idp := newFakeIdP(t)
	idp.metadata["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"}
	a.configureOIDC(t, idp, nil)

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/auth/oidc/login", "", ""))
	if rec.Header().Get("Location") != "/login?error=unavailable" {
		t.Fatalf("start with a provider without client secrets: Location %q", rec.Header().Get("Location"))
	}
}

func TestOIDCLoginReadsUserInfo(t *testing.T) {
	tests := map[string]struct {
		login    oidcLogin
		groups   bool
		tokenTyp string
		outcome  string
		role     db.Role
		calls    int64
	}{
		"a complete ID token": {oidcLogin{}, false, "", "success", db.RoleNetworkManager, 0},
		"the email in UserInfo only": {oidcLogin{
			claims:   map[string]any{"email": nil, "email_verified": nil},
			userInfo: map[string]any{"email": "nm@example.com", "email_verified": true},
		}, false, "", "success", db.RoleNetworkManager, 1},
		"email_verified in UserInfo only": {oidcLogin{
			claims:   map[string]any{"email_verified": nil},
			userInfo: map[string]any{"email_verified": true},
		}, false, "", "success", db.RoleNetworkManager, 1},
		"an unverified email in UserInfo": {oidcLogin{
			claims:   map[string]any{"email_verified": nil},
			userInfo: map[string]any{"email_verified": false},
		}, false, "", "email_not_verified", db.RoleNetworkManager, 1},
		"the groups in UserInfo only": {oidcLogin{
			userInfo: map[string]any{"groups": []string{"ops"}},
		}, true, "", "success", db.RoleReadOnly, 1},
		"groups in neither": {oidcLogin{}, true, "", "no_group", db.RoleNetworkManager, 1},
		"UserInfo for another subject": {oidcLogin{
			claims:   map[string]any{"email": nil},
			userInfo: map[string]any{"sub": "sub-other", "email": "nm@example.com"},
		}, false, "", "failed", db.RoleNetworkManager, 1},
		"UserInfo without a subject": {oidcLogin{
			claims:   map[string]any{"email": nil},
			userInfo: map[string]any{"sub": nil, "email": "nm@example.com"},
		}, false, "", "failed", db.RoleNetworkManager, 1},
		"an access token that is not a bearer token": {oidcLogin{
			claims: map[string]any{"email": nil},
		}, false, "N_A", "failed", db.RoleNetworkManager, 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t)
			idp := newFakeIdP(t)
			idp.tokenType = tc.tokenTyp
			a.configureOIDC(t, idp, func(o map[string]any) {
				if tc.groups {
					o["group_roles"] = []map[string]any{{"group": "ops", "role_id": 2}}
				}
			})
			a.createUser(t, "nm@example.com", db.RoleNetworkManager)

			if res := a.oidcLogin(t, idp, tc.login); res.outcome != tc.outcome {
				t.Fatalf("outcome %q, want %q", res.outcome, tc.outcome)
			}

			if n := idp.userInfoHit.Load(); n != tc.calls {
				t.Fatalf("UserInfo requests = %d, want %d", n, tc.calls)
			}

			if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.RoleID != tc.role ||
				got.OIDCLinked != (tc.outcome == "success") {
				t.Fatalf("user = %+v", got)
			}
		})
	}

	a := newTestAPI(t)
	idp := newFakeIdP(t)
	delete(idp.metadata, "userinfo_endpoint")
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	if res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"email": nil}}); res.outcome != "not_set_up" {
		t.Fatalf("no email and no UserInfo endpoint: %q", res.outcome)
	}
}

func TestOIDCProviderRedirects(t *testing.T) {
	var captured atomic.Int64

	capture := newFakeIdP(t)
	capture.extra = func(w http.ResponseWriter, _ *http.Request) {
		captured.Add(1)
		w.WriteHeader(http.StatusOK)
	}

	a := newTestAPI(t)
	idp := newFakeIdP(t)
	idp.metadata["token_endpoint"] = idp.issuer + "/moved-token"
	idp.extra = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, capture.issuer+"/token-capture", http.StatusTemporaryRedirect)
	}
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "failed" || captured.Load() != 0 {
		t.Fatalf("a redirected token request: outcome %q, %d requests to the target", res.outcome, captured.Load())
	}

	other := newFakeIdP(t)
	other.onDiscover = func(w http.ResponseWriter) bool {
		w.Header().Set("Location", "http://127.0.0.1:1/secret")
		w.WriteHeader(http.StatusFound)

		return true
	}

	code, _, msg := a.do(t, http.MethodPost, "/api/v1/oidc/discovery",
		body(t, map[string]string{"issuer": other.issuer, "ca_certificate": other.ca()}))
	if code != http.StatusBadGateway || !strings.Contains(msg, "not https") {
		t.Fatalf("discovery redirected to http = %d %q", code, msg)
	}

	other.onDiscover = func(w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("INTERNAL-ONLY-SECRET ", 1000)))

		return true
	}

	code, _, msg = a.do(t, http.MethodPost, "/api/v1/oidc/discovery",
		body(t, map[string]string{"issuer": other.issuer, "ca_certificate": other.ca()}))
	if code != http.StatusBadGateway || len(msg) > 300 {
		t.Fatalf("discovery of a failing provider = %d, %d bytes of message", code, len(msg))
	}
}

func TestOIDCProviderResponsesAreCapped(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)

	var written atomic.Int64

	idp.onDiscover = func(w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusOK)

		chunk := []byte(strings.Repeat(" ", 64<<10))

		for range 1 << 14 {
			n, err := w.Write(chunk)
			written.Add(int64(n))

			if err != nil {
				break
			}
		}

		return true
	}

	code, _, msg := a.do(t, http.MethodPost, "/api/v1/oidc/discovery",
		body(t, map[string]string{"issuer": idp.issuer, "ca_certificate": idp.ca()}))
	if code != http.StatusBadGateway || !strings.Contains(msg, "larger than") {
		t.Fatalf("discovery of a 1 GiB document = %d %q", code, msg)
	}

	a.configureOIDC(t, idp, nil)

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/auth/oidc/login", "", ""))
	if rec.Header().Get("Location") != "/login?error=unavailable" {
		t.Fatalf("start with a 1 GiB document: Location %q", rec.Header().Get("Location"))
	}

	if n := written.Load(); n > 64<<20 {
		t.Fatalf("the provider wrote %d bytes", n)
	}
}

func TestOIDCProviderIsFetchedOnce(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)

	release := make(chan struct{})
	idp.onDiscover = func(http.ResponseWriter) bool {
		<-release
		return false
	}

	var wg sync.WaitGroup

	locations := make([]string, 8)

	for i := range locations {
		wg.Go(func() {
			req := request(http.MethodGet, "/api/v1/auth/oidc/login", "", "")
			req.RemoteAddr = newBrowser()
			locations[i] = serve(a.handler, req).Header().Get("Location")
		})
	}

	for idp.discoveries.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for _, l := range locations {
		if !strings.HasPrefix(l, idp.issuer+"/auth?") {
			t.Fatalf("locations = %q", locations)
		}
	}

	if n := idp.discoveries.Load(); n != 1 {
		t.Fatalf("discovery requests = %d, want 1", n)
	}

	down := newFakeIdP(t)
	down.onDiscover = func(w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	a.configureOIDC(t, down, nil)

	start := func() string {
		req := request(http.MethodGet, "/api/v1/auth/oidc/login", "", "")
		req.RemoteAddr = newBrowser()

		return serve(a.handler, req).Header().Get("Location")
	}

	for range 3 {
		if l := start(); l != "/login?error=unavailable" {
			t.Fatalf("start with the provider down: %q", l)
		}
	}

	if n := down.discoveries.Load(); n != 1 {
		t.Fatalf("discovery requests while down = %d, want 1", n)
	}

	a.now = a.now.Add(time.Minute)
	down.onDiscover = nil

	if l := start(); !strings.HasPrefix(l, down.issuer+"/auth?") {
		t.Fatalf("start once the provider is back: %q", l)
	}
}

func TestOIDCLoginIgnoresUnicodeCaseFolding(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "kate@example.com", db.RoleAdmin)

	res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"sub": "sub-kelvin", "email": "Kate@example.com"}})
	if res.outcome != "not_set_up" || a.linked(t, "kate@example.com") {
		t.Fatalf("a lookalike email: outcome %q", res.outcome)
	}

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/users", `{"email": "Kate@example.com", "role_id": 2}`); code != http.StatusCreated {
		t.Fatalf("create a user with a Kelvin sign = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/"+url.PathEscape("Kate@example.com"))); got.RoleID != db.RoleReadOnly {
		t.Fatalf("the lookalike user = %+v", got)
	}
}

func TestOIDCRefusedLoginDoesNotLink(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) {
		o["group_roles"] = []map[string]any{{"group": "noc", "role_id": 3}}
	})
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	for _, claims := range []map[string]any{
		{"groups": []string{"other"}},
		{"groups": nil},
		{"groups": nil, "_claim_names": map[string]string{"groups": "src1"}},
	} {
		if res := a.oidcLogin(t, idp, oidcLogin{claims: claims}); res.outcome == "success" {
			t.Fatalf("login with %v: success", claims)
		}
	}

	if a.linked(t, "nm@example.com") || len(a.auditEvents(t, "link_user_oidc")) != 0 {
		t.Fatalf("linked %t, link events %q", a.linked(t, "nm@example.com"), a.auditEvents(t, "link_user_oidc"))
	}
}

func TestCreatePinnedUser(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, func(o map[string]any) {
		o["group_roles"] = []map[string]any{{"group": "ops", "role_id": 2}}
	})

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/users", `{"email": "nm@example.com", "role_id": 3, "role_pinned": true}`); code != http.StatusCreated {
		t.Fatalf("create a pinned user = %d %q", code, msg)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); !got.RolePinned || got.RoleID != db.RoleNetworkManager {
		t.Fatalf("user = %+v", got)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{claims: map[string]any{"groups": []string{"ops"}}}); res.outcome != "success" {
		t.Fatalf("login: %q", res.outcome)
	}

	if got := decode[api.User](t, a.get(t, "/api/v1/users/nm@example.com")); got.RoleID != db.RoleNetworkManager || !got.OIDCLinked {
		t.Fatalf("user after the first login = %+v", got)
	}

	if events := strings.Join(a.auditEvents(t, "create_user"), "\n"); !strings.Contains(events, "pinned true") {
		t.Fatalf("audit events = %q", events)
	}
}

func TestRemoveWhatTheUserDoesNotHave(t *testing.T) {
	a := newTestAPI(t)
	idp := newFakeIdP(t)
	a.configureOIDC(t, idp, nil)
	a.createUser(t, "nm@example.com", db.RoleNetworkManager)

	if code, _, msg := a.do(t, http.MethodDelete, "/api/v1/users/nm@example.com/oidc", ""); code != http.StatusNotFound {
		t.Fatalf("unlink a user without a link = %d %q, want %d", code, msg, http.StatusNotFound)
	}

	if code, _, msg := a.do(t, http.MethodPost, "/api/v1/users", `{"email": "sso@example.com", "role_id": 2}`); code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, msg)
	}

	if code, _, msg := a.do(t, http.MethodDelete, "/api/v1/users/sso@example.com/password", ""); code != http.StatusConflict {
		t.Fatalf("remove the password of a user without one = %d %q, want %d", code, msg, http.StatusConflict)
	}

	if events := append(a.auditEvents(t, "delete_user_oidc_link"), a.auditEvents(t, "delete_user_password")...); len(events) != 0 {
		t.Fatalf("audit events = %q", events)
	}

	if res := a.oidcLogin(t, idp, oidcLogin{}); res.outcome != "success" {
		t.Fatalf("login: %q", res.outcome)
	}

	o := validOIDC()
	o["issuer"], o["client_secret"] = "https://other.example.com", "another secret"

	if code, _, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o)); code != http.StatusOK {
		t.Fatalf("PUT /api/v1/oidc = %d %q", code, msg)
	}

	if code, _, msg := a.do(t, http.MethodDelete, "/api/v1/users/nm@example.com/oidc", ""); code != http.StatusNotFound {
		t.Fatalf("unlink a user linked to another issuer = %d %q, want %d", code, msg, http.StatusNotFound)
	}
}

func TestOIDCExternalURLIsAnOrigin(t *testing.T) {
	a := newTestAPI(t)

	for in, want := range map[string]string{
		"https://IMS.example.com:8443/": "https://ims.example.com:8443",
		"https://ims.example.com/":      "https://ims.example.com",
	} {
		o := validOIDC()
		o["external_url"] = in

		code, result, msg := a.do(t, http.MethodPut, "/api/v1/oidc", body(t, o))
		if got := decode[api.OIDCSettings](t, result); code != http.StatusOK || got.ExternalURL != want ||
			got.RedirectURL != want+"/api/v1/auth/oidc/callback" {
			t.Errorf("external URL %q = %d %q, %+v", in, code, msg, got)
		}
	}
}
