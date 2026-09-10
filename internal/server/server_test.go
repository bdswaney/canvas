package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/store"
)

func TestFrontendRouting(t *testing.T) {
	const index = "<!doctype html><div id=\"root\"></div>"
	const script = "console.log('canvas');"
	handler, err := New(fstest.MapFS{
		"index.html":    {Data: []byte(index)},
		"assets/app.js": {Data: []byte(script)},
	}, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true})
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
		// The sync route exists but rejects a non-WebSocket GET; it must never fall
		// through to the app HTML.
		{"GET", "/api/sync/doc/00000000-0000-4000-8000-000000000001", 426, ""},
		{"POST", "/api/sync/doc/00000000-0000-4000-8000-000000000001", 405, ""},
		{"POST", "/artifacts/123", 405, ""},
		// Session endpoints exist and are not the app HTML.
		{"GET", "/api/session", 200, `{"authenticated":true}`},
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
	if _, err := New(fstest.MapFS{}, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true}); err == nil {
		t.Fatal("expected error for missing index.html")
	}
}

// The docs API must not shadow the app's own client-side routes.
func TestDocRoutesDoNotSwallowTheApp(t *testing.T) {
	handler, err := New(
		fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		relay.NewHub(newTestStore(t), nil), nil, authtest.Stub{Valid: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/docs/anything", nil))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`id="root"`)) {
		t.Fatalf("client route returned %d: %s", w.Code, w.Body)
	}
}
