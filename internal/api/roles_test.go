package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// TestRoles checks the role that each route allows, behind the authentication of NewHandler: each route serves a stub,
// so that the permission of the route, not its handler, answers.
func TestRoles(t *testing.T) {
	const (
		admin = 1 << iota
		readOnly
		networkManager
		all      = admin | readOnly | networkManager
		operator = admin | networkManager
	)

	want := map[string]int{
		"GET /api/v1/operator":                                      all,
		"PUT /api/v1/operator":                                      operator,
		"GET /api/v1/diameter":                                      all,
		"GET /api/v1/diameter/peers":                                all,
		"POST /api/v1/diameter/peers":                               operator,
		"GET /api/v1/diameter/peers/{id}":                           all,
		"PUT /api/v1/diameter/peers/{id}":                           operator,
		"DELETE /api/v1/diameter/peers/{id}":                        operator,
		"GET /api/v1/diameter/routes":                               all,
		"GET /api/v1/diameter/routes/{application}":                 all,
		"PUT /api/v1/diameter/routes/{application}":                 operator,
		"GET /api/v1/policy":                                        all,
		"PUT /api/v1/policy":                                        operator,
		"GET /api/v1/sip":                                           all,
		"GET /api/v1/registrations":                                 all,
		"POST /api/v1/registrations/{impi}/reauthenticate":          operator,
		"GET /api/v1/mcptt":                                         all,
		"GET /api/v1/mcptt/service-configuration":                   all,
		"PUT /api/v1/mcptt/service-configuration":                   operator,
		"GET /api/v1/mcptt/users":                                   all,
		"POST /api/v1/mcptt/users":                                  operator,
		"GET /api/v1/mcptt/users/{mcptt_id}":                        all,
		"PUT /api/v1/mcptt/users/{mcptt_id}":                        operator,
		"DELETE /api/v1/mcptt/users/{mcptt_id}":                     operator,
		"GET /api/v1/mcptt/groups":                                  all,
		"POST /api/v1/mcptt/groups":                                 operator,
		"GET /api/v1/mcptt/groups/{group_id}":                       all,
		"PUT /api/v1/mcptt/groups/{group_id}":                       operator,
		"DELETE /api/v1/mcptt/groups/{group_id}":                    operator,
		"PUT /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}":    operator,
		"DELETE /api/v1/mcptt/groups/{group_id}/members/{mcptt_id}": operator,
		"GET /api/v1/mcptt/registrations":                           all,
		"GET /api/v1/mcptt/affiliations":                            all,
		"GET /api/v1/mcptt/calls":                                   all,
		"GET /api/v1/mcptt/calls/{call_id}":                         all,
		"GET /api/v1/call-records":                                  all,
		"GET /api/v1/call-records/retention":                        all,
		"PUT /api/v1/call-records/retention":                        operator,
		"GET /api/v1/call-records/{id}":                             all,
		"GET /api/v1/ip-sm-gw":                                      all,
		"PUT /api/v1/ip-sm-gw":                                      operator,
		"GET /api/v1/users/me":                                      all,
		"PUT /api/v1/users/me/password":                             all,
		"GET /api/v1/users/me/api-tokens":                           all,
		"POST /api/v1/users/me/api-tokens":                          all,
		"DELETE /api/v1/users/me/api-tokens/{id}":                   all,
		"GET /api/v1/users":                                         admin,
		"POST /api/v1/users":                                        admin,
		"GET /api/v1/users/{email}":                                 admin,
		"PUT /api/v1/users/{email}":                                 admin,
		"DELETE /api/v1/users/{email}":                              admin,
		"PUT /api/v1/users/{email}/password":                        admin,
		"GET /api/v1/users/{email}/api-tokens":                      admin,
		"POST /api/v1/users/{email}/api-tokens":                     admin,
		"DELETE /api/v1/users/{email}/api-tokens/{id}":              admin,
	}

	store := openAuthStore(t)
	cfg := Config{
		Auth: store, JWTSecret: []byte("0123456789abcdef0123456789abcdef"), BcryptCost: bcrypt.MinCost,
		Now: time.Now, Logger: slog.New(slog.DiscardHandler),
	}
	a := newAuth(cfg)
	mux := http.NewServeMux()
	protected := 0

	for _, rt := range routes(cfg, a) {
		if rt.perm == public {
			continue
		}

		protected++

		mux.Handle(rt.pattern, a.authenticate(a.authorize(rt.perm, http.HandlerFunc(func(w http.ResponseWriter,
			_ *http.Request,
		) {
			w.WriteHeader(http.StatusNoContent)
		}))))
	}

	if len(want) != protected {
		t.Fatalf("the test covers %d routes, but %d need a token", len(want), protected)
	}

	bits := map[db.Role]int{db.RoleAdmin: admin, db.RoleReadOnly: readOnly, db.RoleNetworkManager: networkManager}
	tokens := map[db.Role]string{}

	for role := range bits {
		id, err := store.CreateUser(t.Context(), db.NewUser{
			Email: strings.Repeat("x", int(role)) + "@example.com",
			Role:  role, PasswordHash: "unused",
		})
		if err != nil {
			t.Fatal(err)
		}

		u, err := store.GetUserByID(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}

		sessionID, err := store.CreateSession(t.Context(), db.NewSession{
			UserID: id, TokenHash: []byte{byte(role)},
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}, MaxSessionsPerUser)
		if err != nil {
			t.Fatal(err)
		}

		if tokens[role], err = a.accessToken(u, sessionID); err != nil {
			t.Fatal(err)
		}
	}

	path := strings.NewReplacer("{id}", "1", "{application}", "cx", "{impi}", "i", "{mcptt_id}", "m",
		"{group_id}", "g", "{call_id}", "c", "{email}", "nobody@example.com")

	for route, allowedRoles := range want {
		method, pattern, _ := strings.Cut(route, " ")

		for role, token := range tokens {
			req := httptest.NewRequestWithContext(t.Context(), method, path.Replace(pattern), nil)
			req.Header.Set("Authorization", "Bearer "+token)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			allowed := allowedRoles&bits[role] != 0
			if wantCode := map[bool]int{true: http.StatusNoContent, false: http.StatusForbidden}[allowed]; rec.Code != wantCode {
				t.Errorf("%s as role %d = %d, want %d", route, role, rec.Code, wantCode)
			}
		}
	}
}
