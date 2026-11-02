package api_test

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var gzipMagic = []byte{0x1f, 0x8b}

func TestPprof(t *testing.T) {
	a := newTestAPI(t)

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		code     int
		contains []byte
	}{
		{"index", http.MethodGet, "/api/v1/pprof/", "", http.StatusOK, []byte("<title>/debug/pprof/</title>")},
		{"heap", http.MethodGet, "/api/v1/pprof/heap", "", http.StatusOK, gzipMagic},
		{"heap after gc", http.MethodGet, "/api/v1/pprof/heap?gc=1", "", http.StatusOK, gzipMagic},
		{"heap text", http.MethodGet, "/api/v1/pprof/heap?debug=1", "", http.StatusOK, []byte("heap profile:")},
		{"allocs", http.MethodGet, "/api/v1/pprof/allocs", "", http.StatusOK, gzipMagic},
		{"goroutine", http.MethodGet, "/api/v1/pprof/goroutine", "", http.StatusOK, gzipMagic},
		{"goroutine dump", http.MethodGet, "/api/v1/pprof/goroutine?debug=2", "", http.StatusOK, []byte("goroutine ")},
		{"threadcreate", http.MethodGet, "/api/v1/pprof/threadcreate", "", http.StatusOK, gzipMagic},
		{"delta", http.MethodGet, "/api/v1/pprof/allocs?seconds=1", "", http.StatusOK, gzipMagic},
		{"cmdline", http.MethodGet, "/api/v1/pprof/cmdline", "", http.StatusOK, []byte("api.test")},
		{"cpu", http.MethodGet, "/api/v1/pprof/profile?seconds=1", "", http.StatusOK, gzipMagic},
		{"trace", http.MethodGet, "/api/v1/pprof/trace?seconds=0.1", "", http.StatusOK, []byte("go 1.")},
		{"symbol count", http.MethodGet, "/api/v1/pprof/symbol", "", http.StatusOK, []byte("num_symbols: 1")},
		{"symbol", http.MethodPost, "/api/v1/pprof/symbol", "0x1", http.StatusOK, []byte("num_symbols: 1")},
		{"block", http.MethodGet, "/api/v1/pprof/block", "", http.StatusNotFound, nil},
		{"mutex", http.MethodGet, "/api/v1/pprof/mutex", "", http.StatusNotFound, nil},
		{"unknown profile", http.MethodGet, "/api/v1/pprof/nothing", "", http.StatusNotFound, nil},
		{"subpath", http.MethodGet, "/api/v1/pprof/heap/x", "", http.StatusNotFound, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(a.handler, request(tc.method, tc.path, tc.body, a.token))

			if rec.Code != tc.code || !bytes.Contains(rec.Body.Bytes(), tc.contains) {
				t.Fatalf("%s %s = %d %.200q, want %d with %q", tc.method, tc.path, rec.Code, rec.Body, tc.code, tc.contains)
			}
		})
	}
}

func TestPprofIndexLinks(t *testing.T) {
	a := newTestAPI(t)

	index := serve(a.handler, request(http.MethodGet, "/api/v1/pprof/", "", a.token)).Body.String()
	links := regexp.MustCompile(`href=['"]([^'"]+)['"]`).FindAllStringSubmatch(index, -1)

	if len(links) == 0 {
		t.Fatal("the index has no links")
	}

	for _, link := range links {
		href := link[1]
		if strings.Contains(href, "://") || strings.HasPrefix(href, "#") {
			continue
		}

		path := "/api/v1/pprof/" + href
		if strings.HasPrefix(href, "profile?") {
			path += "&seconds=1"
		}

		want := http.StatusOK
		if strings.HasPrefix(href, "block?") || strings.HasPrefix(href, "mutex?") {
			want = http.StatusNotFound
		}

		rec := serve(a.handler, request(http.MethodGet, path, "", a.token))
		if rec.Code != want || strings.Contains(rec.Body.String(), "<title>/debug/pprof/</title>") {
			t.Errorf("GET %s = %d %.100q", path, rec.Code, rec.Body)
		}
	}
}

func TestPprofWithAPIToken(t *testing.T) {
	a := newTestAPI(t)
	token := a.createToken(t, a.token, "/api/v1/users/me/api-tokens", `{"name": "pyroscope"}`)

	rec := serve(a.handler, request(http.MethodGet, "/api/v1/pprof/heap", "", token))
	if rec.Code != http.StatusOK || !bytes.HasPrefix(rec.Body.Bytes(), gzipMagic) {
		t.Fatalf("GET /api/v1/pprof/heap with an API token = %d %.100q", rec.Code, rec.Body)
	}
}
