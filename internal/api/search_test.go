package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/store"
)

func searchTarget(query string) string {
	return "/api/search?q=" + url.QueryEscape(query)
}

func saveForSearch(t *testing.T, handler http.Handler, doc store.Doc, artifact string) {
	t.Helper()
	if response := do(t, handler, "POST", "/api/docs/"+doc.ID+"/save", map[string]string{
		"artifact": artifact,
		"snapshot": "AQI=",
	}); response.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", response.Code, response.Body)
	}
}

func TestSearchSavedTextUnicodeMembershipAndArchiveFiltering(t *testing.T) {
	st := newTestStore(t)
	owner := newAPIAs(t, st, authtest.User().ID)

	matching := decode[store.Doc](t, do(t, owner, "POST", "/api/docs", map[string]string{"name": "Café notes"}))
	saveForSearch(t, owner, matching, "Café and 👍🏽")
	unsaved := decode[store.Doc](t, do(t, owner, "POST", "/api/docs", map[string]string{"name": "Never saved"}))
	// A metadata-only match must not be a false positive, and the live journal
	// is deliberately outside the saved-artifact search contract.
	if err := st.Append(t.Context(), unsaved.ID, []byte("CAFÉ and secret")); err != nil {
		t.Fatal(err)
	}
	metadataOnly := decode[store.Doc](t, do(t, owner, "POST", "/api/docs", map[string]string{"name": "secret metadata"}))
	saveForSearch(t, owner, metadataOnly, "unrelated text")

	archived := decode[store.Doc](t, do(t, owner, "POST", "/api/docs", map[string]string{"name": "Archived"}))
	saveForSearch(t, owner, archived, "Café and 👍🏽")
	if response := do(t, owner, "DELETE", "/api/docs/"+archived.ID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("archive = %d: %s", response.Code, response.Body)
	}

	for _, tt := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "unicode case fold", query: "CAFÉ", want: 1},
		{name: "accent remains significant", query: "cafe", want: 0},
		{name: "emoji code points", query: "👍🏽", want: 1},
		{name: "different emoji modifier", query: "👍🏻", want: 0},
		{name: "unsaved text excluded", query: "secret", want: 0},
		{name: "no match", query: "missing", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := do(t, owner, "GET", searchTarget(tt.query), nil)
			if response.Code != http.StatusOK {
				t.Fatalf("search = %d: %s", response.Code, response.Body)
			}
			results := decode[[]store.SearchResult](t, response)
			if len(results) != tt.want {
				t.Fatalf("results = %+v, want %d result(s)", results, tt.want)
			}
			if tt.want == 1 && results[0].DocumentID != matching.ID {
				t.Fatalf("result = %+v, want %s", results[0], matching.ID)
			}
		})
	}

	outsider := newAPIAs(t, st, authtest.OutsiderID)
	outsiderResponse := do(t, outsider, "GET", searchTarget("CAFÉ"), nil)
	if outsiderResponse.Code != http.StatusOK {
		t.Fatalf("outsider search = %d: %s", outsiderResponse.Code, outsiderResponse.Body)
	}
	if got := decode[[]store.SearchResult](t, outsiderResponse); len(got) != 0 {
		t.Fatalf("outsider results = %+v, want none", got)
	}
	// The no-match and non-member responses have the same empty shape, so no
	// result count or authorization error leaks through the endpoint.
	noMatch := do(t, owner, "GET", searchTarget("CAFÉ outsider"), nil)
	if strings.TrimSpace(noMatch.Body.String()) != "[]" {
		t.Errorf("no-match body = %q, want []", noMatch.Body.String())
	}
}

func TestSearchExcludesArchivedProjectsAndBoundsQuery(t *testing.T) {
	st := newTestStore(t)
	handler := newAPIAs(t, st, authtest.User().ID)
	project := decode[store.Project](t, do(t, handler, "POST", "/api/projects", map[string]string{"name": "Archive me"}))
	doc := decode[store.Doc](t, do(t, handler, "POST", "/api/docs", map[string]string{"name": "Project doc", "projectId": project.ID}))
	saveForSearch(t, handler, doc, "archived project needle")
	if response := do(t, handler, "DELETE", "/api/projects/"+project.ID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("archive project = %d: %s", response.Code, response.Body)
	}

	if results := decode[[]store.SearchResult](t, do(t, handler, "GET", searchTarget("needle"), nil)); len(results) != 0 {
		t.Fatalf("archived project results = %+v, want none", results)
	}
	long := "x" + strings.Repeat("y", store.MaxSearchQueryRunes)
	if response := do(t, handler, "GET", searchTarget(long), nil); response.Code != http.StatusBadRequest {
		t.Fatalf("long query = %d: %s", response.Code, response.Body)
	}
	if response := do(t, handler, "GET", "/api/search", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("empty query = %d: %s", response.Code, response.Body)
	}
}
