package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
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
	}, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true}, nil)
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
	if _, err := New(fstest.MapFS{}, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true}, nil); err == nil {
		t.Fatal("expected error for missing index.html")
	}
}

func TestBrowserSecurityHeadersCoverEveryHTTPResponse(t *testing.T) {
	const index = "<!doctype html><div id=\"root\"></div>"
	handler, err := New(
		fstest.MapFS{
			"index.html":    {Data: []byte(index)},
			"assets/app.js": {Data: []byte("console.log('canvas');")},
		},
		relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, method, target string
		status               int
	}{
		{"SPA", http.MethodGet, "/", http.StatusOK},
		{"asset", http.MethodGet, "/assets/app.js", http.StatusOK},
		{"session API", http.MethodGet, "/api/session", http.StatusOK},
		{"MCP", http.MethodPost, "/api/mcp", http.StatusNoContent},
		{"WebSocket upgrade rejection", http.MethodGet, "/api/sync/doc/00000000-0000-4000-8000-000000000001", http.StatusUpgradeRequired},
		{"not found", http.MethodGet, "/missing.js", http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tt.method, tt.target, nil))
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
			assertBrowserSecurityHeaders(t, response.Header())
		})
	}
}

func TestThemeHashMatchesExactServedIndex(t *testing.T) {
	// Read the production Vite artifact, not the source entry point. The Go
	// binary embeds dist/, and a frontend transform must not be able to change
	// the inline bootstrap without updating this policy test.
	index, err := os.ReadFile("../../dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(index, []byte("<script>"))
	end := bytes.Index(index, []byte("</script>"))
	if start < 0 || end < start+len("<script>") {
		t.Fatal("index.html must contain the inline theme bootstrap")
	}
	bootstrap := index[start+len("<script>") : end]
	digest := sha256.Sum256(bootstrap)
	wantHash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"

	handler, err := New(fstest.MapFS{"index.html": {Data: index}}, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !bytes.Equal(response.Body.Bytes(), index) {
		t.Fatal("served entry point is not the exact embedded index bytes")
	}
	assertBrowserSecurityHeaders(t, response.Header())
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "script-src 'self' "+wantHash+";") {
		t.Fatalf("CSP does not admit the exact theme bootstrap hash %s: %s", wantHash, response.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(response.Header().Get("Content-Security-Policy"), "script-src 'unsafe-inline'") {
		t.Fatal("CSP must not allow unsafe inline scripts")
	}
}

func assertBrowserSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()
	csp := header.Values("Content-Security-Policy")
	if len(csp) != 1 {
		t.Fatalf("Content-Security-Policy values = %v, want exactly one", csp)
	}
	for _, directive := range []string{
		"default-src 'self'",
		"base-uri 'none'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		"script-src 'self'",
		"script-src-attr 'none'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self'",
		"font-src 'self'",
		"media-src 'none'",
		"frame-src 'none'",
		"connect-src 'self' ws: wss:",
	} {
		if !strings.Contains(csp[0], directive) {
			t.Errorf("CSP %q does not contain %q", csp[0], directive)
		}
	}
	if got := header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, app must leave HSTS to the proxy", got)
	}
}

// The docs API must not shadow the app's own client-side routes.
func TestDocRoutesDoNotSwallowTheApp(t *testing.T) {
	handler, err := New(
		fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		relay.NewHub(newTestStore(t), nil), nil, authtest.Stub{Valid: true}, nil,
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
