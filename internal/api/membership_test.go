package api

import (
	"context"
	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/store"
	"net/http"
	"testing"
)

func newAPIAs(t *testing.T, st store.Store, userID string) http.Handler {
	t.Helper()
	return mount(st, authtest.Stub{Valid: true, UserID: userID})
}

// A signed-in account that belongs to no project sees nothing and can reach
// nothing. Everything is answered as not found rather than forbidden, so that
// ids cannot be probed for existence.
func TestNonMemberSeesNothing(t *testing.T) {
	st := newTestStore(t)
	owner := newDocAPI(t, st)

	doc := decode[store.Doc](t, do(t, owner, "POST", "/api/docs", map[string]string{"name": "Notes"}))

	outsider := newAPIAs(t, st, authtest.OutsiderID)

	if projects := decode[[]store.Project](t, do(t, outsider, "GET", "/api/projects", nil)); len(projects) != 0 {
		t.Errorf("projects = %+v, want none", projects)
	}
	if docs := decode[[]store.Doc](t, do(t, outsider, "GET", "/api/docs", nil)); len(docs) != 0 {
		t.Errorf("docs = %+v, want none", docs)
	}

	// Every single-row route, including the ones that write.
	refused := []struct {
		method, target string
		body           any
	}{
		{"GET", "/api/docs/" + doc.ID, nil},
		{"GET", "/api/docs/" + doc.ID + "/versions", nil},
		{"GET", "/api/docs/" + doc.ID + "/versions/1/artifact", nil},
		{"POST", "/api/docs/" + doc.ID + "/save", map[string]string{"artifact": "x", "snapshot": "AQI="}},
		{"POST", "/api/docs/" + doc.ID + "/restore/1", nil},
		{"DELETE", "/api/docs/" + doc.ID, nil},
		{"GET", "/api/projects/" + store.DefaultProjectID + "/members", nil},
		{"POST", "/api/projects/" + store.DefaultProjectID + "/members", map[string]string{"userId": authtest.OutsiderID}},
		{"POST", "/api/docs", map[string]string{"name": "Sneak", "projectId": store.DefaultProjectID}},
	}
	for _, refuse := range refused {
		if w := do(t, outsider, refuse.method, refuse.target, refuse.body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404: %s", refuse.method, refuse.target, w.Code, w.Body)
		}
	}

	// Nothing above should have taken effect: the document is still there and
	// still readable by somebody who belongs to its project.
	if w := do(t, owner, "GET", "/api/docs/"+doc.ID, nil); w.Code != http.StatusOK {
		t.Errorf("the document did not survive a non-member's attempts: %d", w.Code)
	}
}

// Creating a project has to make the creator a member, or they cannot see
// what they just made.
func TestCreatingAProjectJoinsIt(t *testing.T) {
	handler := newDocAPI(t, newTestStore(t))
	project := decode[store.Project](t, do(t, handler, "POST", "/api/projects", map[string]string{"name": "Platform"}))

	projects := decode[[]store.Project](t, do(t, handler, "GET", "/api/projects", nil))
	if !containsProject(projects, project.ID) {
		t.Fatalf("projects = %+v, want the one just created", projects)
	}
	members := decode[[]store.Member](t, do(t, handler, "GET", "/api/projects/"+project.ID+"/members", nil))
	if len(members) != 1 || members[0].UserID != authtest.User().ID {
		t.Errorf("members = %+v, want just the creator %s", members, authtest.User().ID)
	}
}

func TestMembersCanBeAddedAndRemoved(t *testing.T) {
	st := newTestStore(t)
	handler := newDocAPI(t, st)
	doc := decode[store.Doc](t, do(t, handler, "POST", "/api/docs", map[string]string{"name": "Notes"}))

	if w := do(t, handler, "POST", "/api/projects/"+store.DefaultProjectID+"/members",
		map[string]string{"userId": authtest.OutsiderID}); w.Code != http.StatusNoContent {
		t.Fatalf("add member = %d: %s", w.Code, w.Body)
	}

	// The new member can now reach the project's documents.
	joined := newAPIAs(t, st, authtest.OutsiderID)
	if w := do(t, joined, "GET", "/api/docs/"+doc.ID, nil); w.Code != http.StatusOK {
		t.Errorf("new member reading a doc = %d: %s", w.Code, w.Body)
	}

	if w := do(t, handler, "DELETE", "/api/projects/"+store.DefaultProjectID+"/members/"+authtest.OutsiderID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove member = %d: %s", w.Code, w.Body)
	}
	if w := do(t, joined, "GET", "/api/docs/"+doc.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("removed member reading a doc = %d, want 404", w.Code)
	}
}

// A project with no members is unreachable by anyone, including whoever would
// have to put it right, so the last one cannot leave.
func TestAProjectKeepsAtLeastOneMember(t *testing.T) {
	handler := newDocAPI(t, newTestStore(t))
	target := "/api/projects/" + store.DefaultProjectID + "/members/" + authtest.User().ID
	if w := do(t, handler, "DELETE", target, nil); w.Code != http.StatusConflict {
		t.Fatalf("removing the last member = %d, want 409: %s", w.Code, w.Body)
	}
	if members := decode[[]store.Member](t, do(t, handler, "GET", "/api/projects/"+store.DefaultProjectID+"/members", nil)); len(members) != 1 {
		t.Errorf("members = %+v, want the last one kept", members)
	}
}

func containsProject(projects []store.Project, id string) bool {
	for _, project := range projects {
		if project.ID == id {
			return true
		}
	}
	return false
}

// Archiving hides things from every read path and keeps their history, which
// is the whole reason it is not a delete.
func TestArchivingKeepsHistory(t *testing.T) {
	st := newTestStore(t)
	handler := newDocAPI(t, st)

	doc := decode[store.Doc](t, do(t, handler, "POST", "/api/docs", map[string]string{"name": "Notes"}))
	saved := do(t, handler, "POST", "/api/docs/"+doc.ID+"/save",
		map[string]string{"artifact": "# Notes", "snapshot": "AQI="})
	if saved.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", saved.Code, saved.Body)
	}

	if w := do(t, handler, "DELETE", "/api/docs/"+doc.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive = %d: %s", w.Code, w.Body)
	}

	// Gone from every read path, including the one the socket uses.
	if w := do(t, handler, "GET", "/api/docs/"+doc.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("reading an archived doc = %d, want 404", w.Code)
	}
	if docs := decode[[]store.Doc](t, do(t, handler, "GET", "/api/docs", nil)); len(docs) != 0 {
		t.Errorf("docs = %+v, want none", docs)
	}
	if w := do(t, handler, "POST", "/api/docs/"+doc.ID+"/save",
		map[string]string{"artifact": "more", "snapshot": "AQI="}); w.Code != http.StatusNotFound {
		t.Errorf("saving into an archived doc = %d, want 404", w.Code)
	}
	if w := do(t, handler, "GET", "/api/docs/"+doc.ID+"/versions/1/artifact", nil); w.Code != http.StatusNotFound {
		t.Errorf("reading an archived artifact = %d, want 404", w.Code)
	}

	// The history is still there. Nothing in the API hands it back yet, which
	// is the point: it survives for whoever un-archives the document.
	versions, err := st.Versions(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("stored versions = %+v, want the one that was saved", versions)
	}
	artifact, err := st.Artifact(context.Background(), doc.ID, 1)
	if err != nil || artifact != "# Notes" {
		t.Errorf("stored artifact = %q, %v; want the saved text", artifact, err)
	}
}

// Archiving a project takes its documents out of view with it, even ones that
// were never archived themselves.
func TestArchivingAProjectHidesWhatIsInIt(t *testing.T) {
	handler := newDocAPI(t, newTestStore(t))

	project := decode[store.Project](t, do(t, handler, "POST", "/api/projects", map[string]string{"name": "Platform"}))
	doc := decode[store.Doc](t, do(t, handler, "POST", "/api/docs",
		map[string]string{"name": "Notes", "projectId": project.ID}))

	if w := do(t, handler, "DELETE", "/api/projects/"+project.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("archive project = %d: %s", w.Code, w.Body)
	}
	for _, target := range []string{
		"/api/docs/" + doc.ID,
		"/api/projects/" + project.ID + "/members",
	} {
		if w := do(t, handler, "GET", target, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s after archiving the project = %d, want 404", target, w.Code)
		}
	}
	if projects := decode[[]store.Project](t, do(t, handler, "GET", "/api/projects", nil)); containsProject(projects, project.ID) {
		t.Errorf("archived project still listed: %+v", projects)
	}
}
