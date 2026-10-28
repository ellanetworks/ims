package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/internal/api"
	"gopkg.in/yaml.v3"
)

type specOperation struct {
	Security *[]map[string][]string `yaml:"security"`
}

// bearer is whether an operation of the spec needs a bearer token: whether its security, or the spec's when it has
// none, names BearerAuth.
func (o specOperation) bearer(global []map[string][]string) bool {
	security := global
	if o.Security != nil {
		security = *o.Security
	}

	for _, s := range security {
		if _, ok := s["BearerAuth"]; ok {
			return true
		}
	}

	return false
}

func loadSpec(t *testing.T, a *testAPI) (map[string]specOperation, []map[string][]string) {
	t.Helper()

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/openapi.yaml", "", ""))

	var spec struct {
		Security []map[string][]string           `yaml:"security"`
		Paths    map[string]map[string]yaml.Node `yaml:"paths"`
	}

	if err := yaml.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}

	ops := map[string]specOperation{}

	for path, methods := range spec.Paths {
		for method, node := range methods {
			// Not an operation, but the parameters of all of the path's.
			if method == "parameters" {
				continue
			}

			var op specOperation
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}

			ops[strings.ToUpper(method)+" "+path] = op
		}
	}

	return ops, spec.Security
}

// TestRoutesMatchSpec checks that the spec documents every route, with the authentication the route needs, and no
// other.
func TestRoutesMatchSpec(t *testing.T) {
	a := newTestAPI(t)
	ops, global := loadSpec(t, a)
	routes := api.Routes()

	for pattern, public := range routes {
		if pattern == "GET /" {
			if !public {
				t.Errorf("the frontend is not public")
			}

			continue
		}

		op, ok := ops[strings.TrimSuffix(pattern, "{$}")]
		if !ok {
			t.Errorf("spec is missing %s", pattern)
			continue
		}

		if op.bearer(global) == public {
			t.Errorf("%s: public %t, but the spec says it needs a bearer token: %t", pattern, public, op.bearer(global))
		}
	}

	for pattern := range ops {
		_, ok := routes[pattern]
		_, exact := routes[pattern+"{$}"]

		if !ok && !exact {
			t.Errorf("spec documents %s, which is not a route", pattern)
		}
	}
}

// TestProtectedRoutesRequireToken checks every route that needs a bearer token against requests without a valid one.
func TestProtectedRoutesRequireToken(t *testing.T) {
	a := newTestAPI(t)

	paths := strings.NewReplacer("{id}", "1", "{email}", testAdmin, "{profile}", "heap", "{$}", "")

	for pattern, public := range api.Routes() {
		if public {
			continue
		}

		method, path, _ := strings.Cut(pattern, " ")
		path = paths.Replace(path)

		for name, header := range map[string]string{
			"no header":      "",
			"basic":          "Basic YWRtaW46cGFzc3dvcmQ=",
			"empty bearer":   "Bearer ",
			"garbage":        "Bearer garbage",
			"bad API token":  "Bearer " + api.APITokenPrefix + "bad",
			"token alone":    a.token,
			"another scheme": "Token " + a.token,
		} {
			req := request(method, path, "{}", "")
			if header != "" {
				req.Header.Set("Authorization", header)
			}

			rec := serve(a.handler, req)

			var resp api.ErrorResponse

			_ = json.Unmarshal(rec.Body.Bytes(), &resp)

			if rec.Code != http.StatusUnauthorized || resp.Error != "Invalid token" ||
				rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s with %s: %d %q, WWW-Authenticate %q", pattern, name, rec.Code, resp.Error,
					rec.Header().Get("WWW-Authenticate"))
			}
		}
	}
}

func TestPublicRoutes(t *testing.T) {
	a := newTestAPI(t)

	for _, route := range []string{"GET /api/v1/status", "GET /api/v1/metrics", "GET /api/v1/openapi.yaml"} {
		method, path, _ := strings.Cut(route, " ")
		if code := serve(a.handler, request(method, path, "", "")).Code; code != http.StatusOK {
			t.Errorf("%s without a token = %d, want %d", route, code, http.StatusOK)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	a := newTestAPI(t)

	for _, req := range []*http.Request{
		request(http.MethodGet, "/api/v1/status", "", ""),
		request(http.MethodGet, "/api/v1/users/me", "", ""),
		request(http.MethodGet, "/api/v1/users/me", "", a.token),
	} {
		h := serve(a.handler, req).Header()

		for name, want := range map[string]string{
			"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer",
			// Only with TLS.
			"Strict-Transport-Security": "",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s %s: %s = %q, want %q", req.Method, req.URL, name, got, want)
			}
		}
	}

	cfg := testConfig(t, openStore(t))
	cfg.SecureCookie = true

	if got := serve(api.NewHandler(cfg), request(http.MethodGet, "/api/v1/status", "", "")).Header().
		Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("Strict-Transport-Security with TLS = %q", got)
	}
}

func TestCrossOriginRequestsAreRejected(t *testing.T) {
	a := newTestAPI(t)
	_, cookie := a.login(t, testAdmin, testPassword)

	tests := map[string]struct {
		headers map[string]string
		want    int
	}{
		"from another site":            {map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"from another origin":          {map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		"from the UI":                  {map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		"from a client without a page": {nil, http.StatusOK},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			req := request(http.MethodPost, "/api/v1/auth/refresh", "", "")
			req.AddCookie(cookie)

			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			if code := serve(a.handler, req).Code; code != tc.want {
				t.Fatalf("refresh = %d, want %d", code, tc.want)
			}
		})
	}
}
