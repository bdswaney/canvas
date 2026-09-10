package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/server"
	"github.com/bdswaney/canvas/internal/store"
)

func TestEmbeddedFrontend(t *testing.T) {
	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(assets, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/artifacts/example", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("embedded app unavailable: status %d", w.Code)
	}
}
