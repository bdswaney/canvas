package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func newDocAPI(t *testing.T, store Store) http.Handler {
	t.Helper()
	handler, err := newHandler(
		fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		newHub(store), nil, stubAuth{valid: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func do(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, target, reader))
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return value
}

func TestSaveCreatesVersions(t *testing.T) {
	handler := newDocAPI(t, NewMemoryStore())

	created := do(t, handler, "POST", "/api/docs", map[string]string{"name": "Notes"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body)
	}
	doc := decode[Doc](t, created)

	snapshot := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02})
	for i, artifact := range []string{"# One", "# Two"} {
		saved := do(t, handler, "POST", "/api/docs/"+doc.ID+"/save", map[string]string{
			"artifact": artifact,
			"snapshot": snapshot,
		})
		if saved.Code != http.StatusOK {
			t.Fatalf("save = %d: %s", saved.Code, saved.Body)
		}
		result := decode[struct {
			Version int    `json:"version"`
			SHA256  []byte `json:"sha256"`
		}](t, saved)
		if result.Version != i+1 {
			t.Errorf("version = %d, want %d", result.Version, i+1)
		}
		// The server hashes the artifact itself; the client cannot claim one.
		want := sha256.Sum256([]byte(artifact))
		if !bytes.Equal(result.SHA256, want[:]) {
			t.Errorf("sha256 = %x, want %x", result.SHA256, want)
		}
	}

	// The doc now reports the hash of what was saved, which is what a client
	// compares against to decide whether it holds unsaved changes.
	fetched := decode[Doc](t, do(t, handler, "GET", "/api/docs/"+doc.ID, nil))
	latest := sha256.Sum256([]byte("# Two"))
	if fetched.CurrentVersion != 2 || !bytes.Equal(fetched.SavedSHA256, latest[:]) {
		t.Errorf("doc = %+v, want version 2 and the last artifact's hash", fetched)
	}

	versions := decode[[]Version](t, do(t, handler, "GET", "/api/docs/"+doc.ID+"/versions", nil))
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}

	// Restoring hands back the older artifact for the client to apply; the
	// server cannot turn text back into CRDT state on its own.
	restored := do(t, handler, "POST", "/api/docs/"+doc.ID+"/restore/1", nil)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", restored.Code, restored.Body)
	}
	if got := decode[struct {
		Artifact string `json:"artifact"`
	}](t, restored).Artifact; got != "# One" {
		t.Errorf("restored artifact = %q, want %q", got, "# One")
	}
}

func TestSaveRejectsBadRequests(t *testing.T) {
	store := NewMemoryStore()
	handler := newDocAPI(t, store)
	doc := decode[Doc](t, do(t, handler, "POST", "/api/docs", map[string]string{"name": "Notes"}))

	for _, tt := range []struct {
		name   string
		target string
		body   any
		status int
	}{
		{"unknown document", "/api/docs/00000000-0000-4000-8000-0000000000ff/save",
			map[string]string{"artifact": "x", "snapshot": base64.StdEncoding.EncodeToString([]byte{1})}, http.StatusNotFound},
		{"snapshot is not base64", "/api/docs/" + doc.ID + "/save",
			map[string]string{"artifact": "x", "snapshot": "not base64!"}, http.StatusBadRequest},
		{"snapshot is missing", "/api/docs/" + doc.ID + "/save",
			map[string]string{"artifact": "x"}, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := do(t, handler, "POST", tt.target, tt.body).Code; got != tt.status {
				t.Errorf("status = %d, want %d", got, tt.status)
			}
		})
	}

	// A failed save must not consume a version number.
	if fetched := decode[Doc](t, do(t, handler, "GET", "/api/docs/"+doc.ID, nil)); fetched.CurrentVersion != 0 {
		t.Errorf("current version = %d, want 0", fetched.CurrentVersion)
	}
}

func TestUnknownDocumentIsNotFound(t *testing.T) {
	handler := newDocAPI(t, NewMemoryStore())
	for _, target := range []string{
		"/api/docs/00000000-0000-4000-8000-0000000000ff",
		"/api/docs/00000000-0000-4000-8000-0000000000ff/versions",
		"/api/docs/00000000-0000-4000-8000-0000000000ff/restore/1",
	} {
		method := "GET"
		if target[len(target)-1] == '1' {
			method = "POST"
		}
		if got := do(t, handler, method, target, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", method, target, got)
		}
	}
}

// The docs API must not shadow the app's own client-side routes.
func TestDocRoutesDoNotSwallowTheApp(t *testing.T) {
	handler := newDocAPI(t, NewMemoryStore())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/docs/anything", nil))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`id="root"`)) {
		t.Fatalf("client route returned %d: %s", w.Code, w.Body)
	}
}
