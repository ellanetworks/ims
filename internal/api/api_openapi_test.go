package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPISpec(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(Config{Logger: slog.New(slog.DiscardHandler)}).ServeHTTP(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/openapi+yaml" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}

	if err := yaml.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}

	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi = %q", spec.OpenAPI)
	}

	served := map[string]bool{}

	for _, r := range routes(Config{}) {
		method, path, _ := strings.Cut(r.pattern, " ")
		served[strings.ToLower(method)+" "+path] = true

		if _, ok := spec.Paths[path][strings.ToLower(method)]; !ok {
			t.Errorf("the spec is missing %s", r.pattern)
		}
	}

	for path, ops := range spec.Paths {
		for method := range ops {
			if method != "parameters" && !served[method+" "+path] {
				t.Errorf("the spec has %s %s, which is not served", method, path)
			}
		}
	}
}
