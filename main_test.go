package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFrontendRouting(t *testing.T) {
	const index = "<!doctype html><div id=\"root\"></div>"
	const script = "console.log('canvas');"
	handler, err := newHandler(fstest.MapFS{
		"index.html":    {Data: []byte(index)},
		"assets/app.js": {Data: []byte(script)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		method, target string
		status         int
		body           string
	}{
		{"GET", "/", 200, index},
		{"GET", "/index.html", 200, index},
		{"GET", "/artifacts/123?view=source", 200, index},
		{"GET", "/artifacts/123/", 200, index},
		{"HEAD", "/artifacts/123", 200, ""},
		{"GET", "/assets/app.js", 200, script},
		{"HEAD", "/assets/app.js", 200, ""},
		{"GET", "/assets/missing.js", 404, ""},
		{"GET", "/assets/missing", 404, ""},
		{"GET", "/assets/", 404, ""},
		{"GET", "/favicon.ico", 404, ""},
		{"GET", "/api", 404, ""},
		{"GET", "/api/artifacts", 404, ""},
		{"POST", "/artifacts/123", 405, ""},
	} {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tt.method, tt.target, nil))
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if tt.status == 200 && w.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.body)
			}
			if tt.body == index && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Error("entry point must be served as HTML")
			}
			if tt.status >= 400 && strings.Contains(w.Body.String(), index) {
				t.Error("error returned app HTML")
			}
			if tt.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Error("missing Allow header")
			}
		})
	}
}

func TestMissingFrontendEntryPoint(t *testing.T) {
	if _, err := newHandler(fstest.MapFS{}); err == nil {
		t.Fatal("expected error for missing index.html")
	}
}

func TestEmbeddedFrontend(t *testing.T) {
	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/artifacts/example", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("embedded app unavailable: status %d", w.Code)
	}
}
