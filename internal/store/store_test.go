package store

import (
	"bytes"
	"context"
	"fmt"
	"github.com/bdswaney/canvas/internal/migrate"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreCopiesUpdates(t *testing.T) {
	store := NewMemoryStore()
	doc, err := store.CreateDoc(context.Background(), DefaultProjectID, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	update := []byte{0x01, 0x02}
	if err := store.Append(context.Background(), doc.ID, update); err != nil {
		t.Fatal(err)
	}
	update[0] = 0xff // The caller's buffer must not alias the stored copy.
	stored, err := store.Load(context.Background(), doc.ID)
	if err != nil || len(stored) != 1 || !bytes.Equal(stored[0], []byte{0x01, 0x02}) {
		t.Fatalf("stored %v, %v", stored, err)
	}
	if other, err := store.Load(context.Background(), "elsewhere"); err != nil || len(other) != 0 {
		t.Fatalf("unknown document returned %v, %v", other, err)
	}
}

// TestStores runs one suite against both implementations. The two have drifted
// before — Version.Author once held an id in memory and a username in
// Postgres, and a test that only counted rows saw nothing — so these assert
// which id and which name come back, never how many.
func TestStores(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		// The suite works as one person, who has to belong to both projects
		// or every list it checks comes back empty. The memory store has no
		// accounts table, so any two distinct ids will do.
		const user, stranger = "00000000-0000-4000-8000-00000000000f", "00000000-0000-4000-8000-0000000000aa"
		store := NewMemoryStore()
		if err := store.AddProjectMember(context.Background(), DefaultProjectID, user, ""); err != nil {
			t.Fatal(err)
		}
		elsewhere, err := store.CreateProject(context.Background(), "Elsewhere", user)
		if err != nil {
			t.Fatal(err)
		}
		storeConformance(t, store, DefaultProjectID, elsewhere.ID, user, stranger)
	})
	t.Run("postgres", func(t *testing.T) {
		store, projectID, elsewhereID, userID, strangerID := postgresFixture(t)
		storeConformance(t, store, projectID, elsewhereID, userID, strangerID)
	})
}

// projectID is where the suite works; elsewhereID is a second project it only
// puts strays in, so that "everything" and "this project" are demonstrably
// different answers rather than accidentally equal ones. userID belongs to
// both; strangerID belongs to neither. The caller owns all of them, because
// cleaning them up is the caller's problem.
func storeConformance(t *testing.T, store Store, projectID, elsewhereID, userID, strangerID string) {
	t.Helper()
	ctx := context.Background()

	strays, err := store.CreateDoc(ctx, elsewhereID, "Stray")
	if err != nil {
		t.Fatal(err)
	}

	notes, err := store.CreateDoc(ctx, projectID, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDoc(ctx, projectID, "Agenda"); err != nil {
		t.Fatal(err)
	}
	if notes.ProjectID != projectID {
		t.Errorf("doc project = %q, want %q", notes.ProjectID, projectID)
	}

	// Listing by project must return this project's docs, and no others.
	docs, err := store.Docs(ctx, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if names := docNames(docs); names != "Agenda,Notes" {
		t.Errorf("docs in project = %q, want \"Agenda,Notes\"", names)
	}
	// An empty project id means every project, which is a different code path
	// in both stores: a skipped filter in memory, a NULL comparison in SQL.
	all, err := store.Docs(ctx, "", userID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsDoc(all, notes.ID) || !containsDoc(all, strays.ID) {
		t.Errorf("docs across projects = %q, want both %s and the stray %s",
			docNames(all), notes.Name, strays.Name)
	}
	if containsDoc(docs, strays.ID) {
		t.Errorf("docs in project included %s from another project", strays.Name)
	}

	membershipConformance(t, store, projectID, userID, strangerID)
	archiveConformance(t, store, elsewhereID, strays.ID, userID)
}

// archiveConformance checks the paths where an archived row would otherwise
// stay usable. Doc is the important one: the socket handler's existence check
// goes through it, so an archived document that still resolves there is one
// people keep editing live. SaveDoc is next, because a save into an archived
// document writes history nothing would notice.
func archiveConformance(t *testing.T, store Store, projectID, docID, userID string) {
	t.Helper()
	ctx := context.Background()

	if err := store.ArchiveDoc(ctx, docID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Doc(ctx, docID); err != ErrNotFound {
		t.Errorf("reading an archived doc = %v, want ErrNotFound (the socket checks this)", err)
	}
	if _, err := store.SaveDoc(ctx, docID, Save{Artifact: "x", SHA256: []byte{1}, Snapshot: []byte{1}}); err != ErrNotFound {
		t.Errorf("saving into an archived doc = %v, want ErrNotFound", err)
	}
	if docs, err := store.Docs(ctx, projectID, userID); err != nil || containsDoc(docs, docID) {
		t.Errorf("archived doc still listed: %q, %v", docNames(docs), err)
	}

	// Archiving a project hides everything inside it, including rows that
	// were never archived themselves.
	live, err := store.CreateDoc(ctx, projectID, "Still here")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveProject(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Doc(ctx, live.ID); err != ErrNotFound {
		t.Errorf("doc in an archived project = %v, want ErrNotFound", err)
	}
	if docs, err := store.Docs(ctx, "", userID); err != nil || containsDoc(docs, live.ID) {
		t.Errorf("doc in an archived project still listed: %q, %v", docNames(docs), err)
	}
	projects, err := store.Projects(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if project.ID == projectID {
			t.Errorf("archived project still listed: %+v", project)
		}
	}
	// Membership is the gate every handler shares, so an archived project has
	// to stop being reachable there rather than in each caller.
	if member, err := store.ProjectMember(ctx, projectID, userID); err != nil || member {
		t.Errorf("member of an archived project = %v, %v; want false", member, err)
	}

	// Archiving twice is not a second event.
	if err := store.ArchiveProject(ctx, projectID); err != ErrNotFound {
		t.Errorf("archiving twice = %v, want ErrNotFound", err)
	}
}

// membershipConformance checks the boundary itself: a stranger belongs to
// nothing and every list is empty for them, and adding them changes exactly
// that. The lists are filtered in SQL on one path and in Go on the other, so
// this is the assertion most likely to catch the two drifting apart.
// strangerID has to be a real account on the Postgres path, because
// project_members.user_id references "SessionUsers", so the harness supplies
// it rather than the suite inventing one.
func membershipConformance(t *testing.T, store Store, projectID, userID, stranger string) {
	t.Helper()
	ctx := context.Background()

	if member, err := store.ProjectMember(ctx, projectID, stranger); err != nil || member {
		t.Fatalf("stranger is a member = %v, %v; want false", member, err)
	}
	if member, err := store.ProjectMember(ctx, projectID, userID); err != nil || !member {
		t.Fatalf("owner is a member = %v, %v; want true", member, err)
	}

	if projects, err := store.Projects(ctx, stranger); err != nil || len(projects) != 0 {
		t.Errorf("stranger's projects = %+v, %v; want none", projects, err)
	}
	if docs, err := store.Docs(ctx, "", stranger); err != nil || len(docs) != 0 {
		t.Errorf("stranger's docs = %q, %v; want none", docNames(docs), err)
	}
	if docs, err := store.Docs(ctx, projectID, stranger); err != nil || len(docs) != 0 {
		t.Errorf("stranger's docs in the project = %q, %v; want none", docNames(docs), err)
	}

	// Membership is what changes the answer, and nothing else.
	if err := store.AddProjectMember(ctx, projectID, stranger, userID); err != nil {
		t.Fatal(err)
	}
	// Adding twice is the state the caller asked for, not an error.
	if err := store.AddProjectMember(ctx, projectID, stranger, userID); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if member, err := store.ProjectMember(ctx, projectID, stranger); err != nil || !member {
		t.Fatalf("after adding, member = %v, %v; want true", member, err)
	}
	projects, err := store.Projects(ctx, stranger)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != projectID {
		t.Errorf("added member's projects = %+v, want just %s", projects, projectID)
	}
	if docs, err := store.Docs(ctx, "", stranger); err != nil || len(docs) == 0 {
		t.Errorf("added member's docs = %q, %v; want the project's", docNames(docs), err)
	}

	members, err := store.ProjectMembers(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMember(members, stranger) || !containsMember(members, userID) {
		t.Errorf("members = %+v, want both %s and %s", members, userID, stranger)
	}

	if err := store.RemoveProjectMember(ctx, projectID, stranger); err != nil {
		t.Fatal(err)
	}
	if member, err := store.ProjectMember(ctx, projectID, stranger); err != nil || member {
		t.Errorf("after removing, member = %v, %v; want false", member, err)
	}
	if docs, err := store.Docs(ctx, "", stranger); err != nil || len(docs) != 0 {
		t.Errorf("removed member's docs = %q, %v; want none", docNames(docs), err)
	}
}

func containsMember(members []Member, userID string) bool {
	for _, m := range members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

func containsDoc(docs []Doc, id string) bool {
	for _, doc := range docs {
		if doc.ID == id {
			return true
		}
	}
	return false
}

func docNames(docs []Doc) string {
	names := make([]string, 0, len(docs))
	for _, doc := range docs {
		names = append(names, doc.Name)
	}
	return strings.Join(names, ",")
}

// postgresFixture opens the scratch database named by DATABASE_URL — the
// container started by mise run db — and hands back a project and a user that
// exist only for this test. Dropping the project cascades to everything the
// suite creates inside it.
func postgresFixture(t *testing.T) (store *PostgresStore, projectID, elsewhereID, userID, strangerID string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	if err := migrate.Run(url); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	// Two scratch projects. Dropping them cascades to every document the
	// suite creates inside them.
	stamp := time.Now().UnixNano()

	// A real "SessionUsers" row, so that the id-to-username join is exercised
	// rather than only the id column.
	username := fmt.Sprintf("tester-%d", stamp)
	if err := store.pool.QueryRow(context.Background(),
		`INSERT INTO "SessionUsers" ("Id", "Username") VALUES (gen_random_uuid(), $1) RETURNING "Id"::text`,
		username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	// A second real account that joins nothing, so the membership checks have
	// something to be refused as. project_members references "SessionUsers",
	// so an invented uuid would fail the foreign key rather than the check.
	if err := store.pool.QueryRow(context.Background(),
		`INSERT INTO "SessionUsers" ("Id", "Username") VALUES (gen_random_uuid(), $1) RETURNING "Id"::text`,
		fmt.Sprintf("stranger-%d", stamp)).Scan(&strangerID); err != nil {
		t.Fatal(err)
	}

	// The projects are created by that user, which is what makes them a
	// member: the suite sees nothing in a project it does not belong to.
	project, err := store.CreateProject(context.Background(), fmt.Sprintf("test-%d", stamp), userID)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := store.CreateProject(context.Background(), fmt.Sprintf("test-elsewhere-%d", stamp), userID)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		clean := context.Background()
		store.pool.Exec(clean, "DELETE FROM projects WHERE id = ANY($1::uuid[])",
			[]string{project.ID, elsewhere.ID})
		store.pool.Exec(clean, `DELETE FROM "SessionUsers" WHERE "Id" = ANY($1::uuid[])`,
			[]string{userID, strangerID})
	})

	// Postgres resolves a stored id to a username by joining "SessionUsers";
	// the memory store has no such table, so that join is only exercised
	// here. Membership is where it now happens.
	members, err := store.ProjectMembers(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != userID || members[0].Username != username {
		t.Errorf("members = %+v, want the creator %s resolved to %q", members, userID, username)
	}

	return store, project.ID, elsewhere.ID, userID, strangerID
}
