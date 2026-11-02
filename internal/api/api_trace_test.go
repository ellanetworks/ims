package api_test

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/ims/internal/api"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestAPISpans checks that a request to the API has a server span named by its route, which links to the caller's
// trace context rather than continue it. The UI, the probes and the profiles are not traced.
func TestAPISpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()

	cfg := testConfig(t, openStore(t))
	cfg.Frontend = fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	cfg.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	h := api.NewHandler(cfg)
	token := initialize(t, h)

	for _, path := range []string{"/", "/api/v1/livez", "/api/v1/pprof/heap"} {
		serve(h, request(http.MethodGet, path, "", ""))
	}

	req := request(http.MethodGet, "/api/v1/users/"+testAdmin, "", token)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	if res := serve(h, req); res.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/users/%s = %d %s", testAdmin, res.Code, res.Body)
	}

	spans := rec.Ended()
	if len(spans) != 2 || spans[0].Name() != "POST /api/v1/init" {
		names := make([]string, 0, len(spans))
		for _, s := range spans {
			names = append(names, s.Name())
		}

		t.Fatalf("spans %q, want POST /api/v1/init and the user's", names)
	}

	s := spans[1]
	if s.Name() != "GET /api/v1/users/{email}" || s.SpanKind() != trace.SpanKindServer || s.Parent().IsValid() {
		t.Fatalf("span %q kind %v parent %v, want a server span without a parent", s.Name(), s.SpanKind(), s.Parent())
	}

	if links := s.Links(); len(links) != 1 || links[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("links = %+v, want one to the caller's trace context", links)
	}

	attrs := attribute.NewSet(s.Attributes()...)
	if route, _ := attrs.Value("http.route"); route.AsString() != "/api/v1/users/{email}" {
		t.Errorf("http.route = %q", route.AsString())
	}
}
