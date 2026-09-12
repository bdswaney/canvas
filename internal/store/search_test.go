package store

import (
	"context"
	"strings"
	"testing"
)

const searchTestUser = "00000000-0000-4000-8000-00000000000f"

func saveSearchArtifact(t *testing.T, st Store, docID, artifact string) {
	t.Helper()
	if _, err := st.SaveDoc(context.Background(), docID, Save{Artifact: artifact, Snapshot: []byte{1}, SHA256: []byte{1}, AuthorID: searchTestUser}); err != nil {
		t.Fatal(err)
	}
}

func TestMemorySearchUsesLatestSavedArtifactAndUnicodeSubstring(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	if err := st.AddProjectMember(ctx, DefaultProjectID, searchTestUser, ""); err != nil {
		t.Fatal(err)
	}

	old, err := st.CreateDoc(ctx, DefaultProjectID, "Old version")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, old.ID, "legacy needle")
	saveSearchArtifact(t, st, old.ID, "Café and 👍🏽")

	never, err := st.CreateDoc(ctx, DefaultProjectID, "Never saved")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(ctx, never.ID, []byte("never saved needle")); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		query string
		want  int
	}{
		{query: "legacy", want: 0},
		{query: "CAFÉ", want: 1},
		{query: "cafe", want: 0},
		{query: "👍🏽", want: 1},
		{query: "👍🏻", want: 0},
		{query: "needle", want: 0},
	} {
		t.Run(tt.query, func(t *testing.T) {
			results, err := st.SearchDocuments(ctx, searchTestUser, tt.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != tt.want {
				t.Fatalf("search %q returned %+v, want %d result(s)", tt.query, results, tt.want)
			}
		})
	}

	// The document name and id are the complete tie-break order, not map order.
	alpha, err := st.CreateDoc(ctx, DefaultProjectID, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, alpha.ID, "shared")
	zulu, err := st.CreateDoc(ctx, DefaultProjectID, "Zulu")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, zulu.ID, "shared")
	results, err := st.SearchDocuments(ctx, searchTestUser, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Name != "Alpha" || results[1].Name != "Zulu" {
		t.Fatalf("ordered results = %+v, want Alpha then Zulu", results)
	}
}

func TestMemorySearchEnforcesMembershipArchiveAndBounds(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	if err := st.AddProjectMember(ctx, DefaultProjectID, searchTestUser, ""); err != nil {
		t.Fatal(err)
	}

	privateProject, err := st.CreateProject(ctx, "Private", searchTestUser)
	if err != nil {
		t.Fatal(err)
	}
	private, err := st.CreateDoc(ctx, privateProject.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, private.ID, "secret needle")
	if results, err := st.SearchDocuments(ctx, "00000000-0000-4000-8000-0000000000aa", "needle"); err != nil || len(results) != 0 {
		t.Fatalf("non-member search = %+v, %v; want no results", results, err)
	}

	archivedDoc, err := st.CreateDoc(ctx, DefaultProjectID, "Archived doc")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, archivedDoc.ID, "secret needle")
	if err := st.ArchiveDoc(ctx, archivedDoc.ID); err != nil {
		t.Fatal(err)
	}

	archivedProject, err := st.CreateProject(ctx, "Archived project", searchTestUser)
	if err != nil {
		t.Fatal(err)
	}
	archivedProjectDoc, err := st.CreateDoc(ctx, archivedProject.ID, "Archived project doc")
	if err != nil {
		t.Fatal(err)
	}
	saveSearchArtifact(t, st, archivedProjectDoc.ID, "secret needle")
	if err := st.ArchiveProject(ctx, archivedProject.ID); err != nil {
		t.Fatal(err)
	}
	if results, err := st.SearchDocuments(ctx, searchTestUser, "needle"); err != nil || len(results) != 1 || results[0].DocumentID != private.ID {
		t.Fatalf("member search = %+v, %v; want only private active doc", results, err)
	}

	if _, err := st.SearchDocuments(ctx, searchTestUser, ""); err != ErrSearchQueryEmpty {
		t.Errorf("empty query = %v, want ErrSearchQueryEmpty", err)
	}
	if _, err := st.SearchDocuments(ctx, searchTestUser, "x"+strings.Repeat("y", MaxSearchQueryRunes)); err != ErrSearchQueryLong {
		t.Errorf("long query = %v, want ErrSearchQueryLong", err)
	}

	for i := 0; i < MaxSearchResults+1; i++ {
		doc, err := st.CreateDoc(ctx, DefaultProjectID, "Bounded")
		if err != nil {
			t.Fatal(err)
		}
		saveSearchArtifact(t, st, doc.ID, "bounded")
	}
	results, err := st.SearchDocuments(ctx, searchTestUser, "bounded")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != MaxSearchResults {
		t.Fatalf("bounded results = %d, want %d", len(results), MaxSearchResults)
	}
}
