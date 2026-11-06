package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
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

	cfg := Config{BcryptCost: bcrypt.MinCost, Now: time.Now}

	for _, r := range routes(cfg, newAuth(cfg)) {
		method, path, _ := strings.Cut(r.pattern, " ")
		path = strings.TrimSuffix(path, "{$}")
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

func TestOpenAPISpecCompositionIsOpen(t *testing.T) {
	var spec map[string]any
	if err := yaml.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}

	schemas, _ := spec["components"].(map[string]any)["schemas"].(map[string]any)

	var walk func(node any, path string)

	walk = func(node any, path string) {
		switch n := node.(type) {
		case map[string]any:
			for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
				members, _ := n[kw].([]any)
				if _, props := n["properties"]; kw != "allOf" && !props {
					continue
				}

				for i, m := range members {
					member, _ := m.(map[string]any)
					if ref, ok := member["$ref"].(string); ok {
						member, _ = schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
					}

					for _, closing := range []string{"additionalProperties", "unevaluatedProperties"} {
						if open, ok := member[closing].(bool); ok && !open {
							t.Errorf("%s/%s/%d has %s: false; use unevaluatedProperties on the composing schema", path, kw, i, closing)
						}
					}
				}
			}

			for k, v := range n {
				walk(v, path+"/"+k)
			}
		case []any:
			for i, v := range n {
				walk(v, fmt.Sprintf("%s/%d", path, i))
			}
		}
	}

	walk(spec, "#")
}
