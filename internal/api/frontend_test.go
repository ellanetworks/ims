package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/ellanetworks/ims/internal/api"
)

func TestFrontend(t *testing.T) {
	handler := api.Frontend(fstest.MapFS{
		"index.html":         {Data: []byte("<html></html>")},
		"assets/index-a1.js": {Data: []byte("console.log(1)")},
	})

	tests := []struct {
		path         string
		status       int
		cacheControl string
		body         string
	}{
		{path: "/", status: http.StatusOK},
		{path: "/assets/index-a1.js", status: http.StatusOK, cacheControl: "public, max-age=31536000, immutable"},
		{path: "/operator", status: http.StatusOK, body: "<html></html>"},
		{path: "/diameter/peers/0199", status: http.StatusOK, body: "<html></html>"},
		{path: "/assets/missing.js", status: http.StatusNotFound},
		{path: "/api/v1/unknown", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}

			if got := rec.Header().Get("Cache-Control"); got != tt.cacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.cacheControl)
			}

			if tt.body != "" && rec.Body.String() != tt.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.body)
			}
		})
	}
}

func TestFrontendWithoutBuild(t *testing.T) {
	handler := api.Frontend(fstest.MapFS{".gitkeep": {}})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
