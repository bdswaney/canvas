package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
)

const (
	owner     = "00000000-0000-4000-8000-00000000000f"
	outsider  = "00000000-0000-4000-8000-0000000000aa"
	textField = "notes"
)

// injector stands in for the relay: it journals the update the way the hub
// would and records that a broadcast was asked for.
type injector struct {
	store    store.Store
	injected int
}

func (i *injector) Inject(ctx context.Context, docID string, update []byte) error {
	i.injected++
	return i.store.Append(ctx, docID, update)
}

// session wires a server to an in-process client, so the tests exercise the
// protocol rather than calling handlers directly.
func session(t *testing.T, user string) (*mcp.ClientSession, store.Store, *ydoc.Engine, *injector) {
	t.Helper()
	engine, err := ydoc.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })

	st := store.NewMemoryStore()
	if err := st.AddProjectMember(t.Context(), store.DefaultProjectID, owner, ""); err != nil {
		t.Fatal(err)
	}
	relay := &injector{store: st}
	server := New(st, engine, relay, auth.User{ID: user, Username: "tester"})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { server.Run(context.Background(), serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, st, engine, relay
}

func callResult(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res := callResult(t, cs, name, args)
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestToolsAreAdvertised(t *testing.T) {
	cs, _, _, _ := session(t, owner)
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"list_projects": false, "create_project": false, "list_documents": false,
		"read_document": false, "document_history": false, "create_document": false,
		"edit_document": false, "save_document": false, "upsert_document": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
		if tool.Description == "" {
			t.Errorf("%s has no description; an assistant chooses tools by them", tool.Name)
		}
		if want[tool.Name] && tool.OutputSchema == nil {
			t.Errorf("%s has no structured output schema", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s is not advertised", name)
		}
	}
}

func TestCreateReadAndEdit(t *testing.T) {
	cs, _, _, relay := session(t, owner)

	created, isErr := call(t, cs, "create_document", map[string]any{
		"projectId": store.DefaultProjectID, "name": "Notes",
	})
	if isErr {
		t.Fatalf("create: %s", created)
	}
	docID := strings.Fields(strings.TrimPrefix(created, "Created "))[0]

	// A new document is empty, not missing.
	if body, isErr := call(t, cs, "read_document", map[string]any{"documentId": docID}); isErr || body != "" {
		t.Fatalf("read of a new document = %q, isErr=%v", body, isErr)
	}

	if out, isErr := call(t, cs, "edit_document", map[string]any{
		"documentId": docID, "text": "# Notes\n\nFirst line.\n",
	}); isErr {
		t.Fatalf("edit: %s", out)
	}
	if relay.injected != 1 {
		t.Errorf("relay was handed %d updates, want 1 — an edit nobody sees is not an edit", relay.injected)
	}

	body, isErr := call(t, cs, "read_document", map[string]any{"documentId": docID})
	if isErr || body != "# Notes\n\nFirst line.\n" {
		t.Fatalf("read back = %q, isErr=%v", body, isErr)
	}
}

func TestProjectBootstrapStructuredResultsAndIdempotentImport(t *testing.T) {
	cs, st, _, _ := session(t, owner)

	projectResult := callResult(t, cs, "create_project", map[string]any{"name": "Imports"})
	if projectResult.IsError || projectResult.StructuredContent == nil {
		t.Fatalf("create_project result = %+v, want structured success", projectResult)
	}
	var projectID string
	for _, project := range mustProjects(t, st, owner) {
		if project.Name == "Imports" {
			projectID = project.ID
		}
	}
	if projectID == "" {
		t.Fatal("created project was not visible to its creator")
	}

	if result := callResult(t, cs, "upsert_document", map[string]any{"projectId": projectID, "name": "Missing key", "text": "x"}); !result.IsError {
		t.Fatal("upsert without sourceKey succeeded")
	}

	emptyCreated, isErr := call(t, cs, "create_document", map[string]any{"projectId": projectID, "name": "Empty"})
	if isErr {
		t.Fatalf("create empty document: %s", emptyCreated)
	}
	emptyID := strings.Fields(strings.TrimPrefix(emptyCreated, "Created "))[0]
	if result := callResult(t, cs, "save_document", map[string]any{"documentId": emptyID}); result.IsError || result.StructuredContent == nil {
		t.Fatalf("save empty document = %+v, want structured success", result)
	}
	if body, isErr := call(t, cs, "read_document", map[string]any{"documentId": emptyID, "version": 1}); isErr || body != "" {
		t.Fatalf("saved empty document = %q, isErr=%v", body, isErr)
	}

	first := callResult(t, cs, "upsert_document", map[string]any{
		"projectId": projectID,
		"sourceKey": "github:issue:40",
		"name":      "MCP workflow",
		"text":      "first import",
		"save":      true,
	})
	if first.IsError || first.StructuredContent == nil {
		t.Fatalf("first upsert = %+v, want structured success", first)
	}
	retry := callResult(t, cs, "upsert_document", map[string]any{
		"projectId": projectID,
		"sourceKey": "github:issue:40",
		"name":      "MCP workflow",
		"text":      "first import",
		"save":      true,
	})
	if retry.IsError || retry.StructuredContent == nil {
		t.Fatalf("identical saved upsert retry = %+v, want structured success", retry)
	}
	versions, err := st.Versions(t.Context(), importedID(t, st, projectID, owner, "github:issue:40"))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("identical saved retry created versions = %+v, want one version", versions)
	}

	second := callResult(t, cs, "upsert_document", map[string]any{
		"projectId": projectID,
		"sourceKey": "github:issue:40",
		"name":      "MCP workflow updated",
		"text":      "second import",
		"save":      true,
	})
	if second.IsError || second.StructuredContent == nil {
		t.Fatalf("changed upsert = %+v, want structured success", second)
	}

	docs, err := st.Docs(t.Context(), projectID, owner)
	if err != nil {
		t.Fatal(err)
	}
	var imported store.Doc
	var found int
	for _, doc := range docs {
		if doc.SourceKey == "github:issue:40" {
			imported = doc
			found++
		}
	}
	if found != 1 || imported.Name != "MCP workflow updated" {
		t.Fatalf("source-key docs = %+v, found %d; want one renamed document", docs, found)
	}
	if body, isErr := call(t, cs, "read_document", map[string]any{"documentId": imported.ID}); isErr || body != "second import" {
		t.Fatalf("repeated upsert live text = %q, isErr=%v", body, isErr)
	}
	if body, isErr := call(t, cs, "read_document", map[string]any{"documentId": imported.ID, "version": 1}); isErr || body != "first import" {
		t.Fatalf("saved first import = %q, isErr=%v", body, isErr)
	}
}

func mustProjects(t *testing.T, st store.Store, userID string) []store.Project {
	t.Helper()
	projects, err := st.Projects(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return projects
}

func importedID(t *testing.T, st store.Store, projectID, userID, sourceKey string) string {
	t.Helper()
	docs, err := st.Docs(t.Context(), projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if doc.SourceKey == sourceKey {
			return doc.ID
		}
	}
	t.Fatalf("source key %q not found", sourceKey)
	return ""
}

// The edit has to merge rather than overwrite: that is the whole reason it
// goes through the CRDT instead of writing an artifact.
func TestEditPreservesAConcurrentChange(t *testing.T) {
	cs, st, engine, _ := session(t, owner)

	created, _ := call(t, cs, "create_document", map[string]any{
		"projectId": store.DefaultProjectID, "name": "Shared",
	})
	docID := strings.Fields(strings.TrimPrefix(created, "Created "))[0]

	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": "alpha bravo charlie"})

	// Somebody typing in the editor appends, journalled the way the relay does.
	state, err := st.Load(t.Context(), docID)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := engine.Merge(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	human, err := engine.SetText(t.Context(), merged, textField, "alpha bravo charlie delta")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(t.Context(), docID, human); err != nil {
		t.Fatal(err)
	}

	// Now the assistant rewrites a word it read before that happened.
	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": "alpha BRAVO charlie delta"})

	body, _ := call(t, cs, "read_document", map[string]any{"documentId": docID})
	if !strings.Contains(body, "BRAVO") || !strings.Contains(body, "delta") {
		t.Fatalf("an edit was lost: %q", body)
	}
}

// Membership is the boundary, and it has to hold here too. Anything out of
// reach is reported as missing rather than forbidden, so ids cannot be probed.
func TestOutsiderSeesAndReachesNothing(t *testing.T) {
	ownerSession, st, _, _ := session(t, owner)
	created, _ := call(t, ownerSession, "create_document", map[string]any{
		"projectId": store.DefaultProjectID, "name": "Private",
	})
	docID := strings.Fields(strings.TrimPrefix(created, "Created "))[0]

	// A second server over the same store, as somebody who joined nothing.
	engine, err := ydoc.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	server := New(st, engine, &injector{store: st}, auth.User{ID: outsider, Username: "outsider"})
	ct, stt := mcp.NewInMemoryTransports()
	go func() { server.Run(context.Background(), stt) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if body, _ := call(t, cs, "list_projects", map[string]any{}); !strings.Contains(body, "no projects") {
		t.Errorf("outsider's projects = %q, want none", body)
	}
	if body, _ := call(t, cs, "list_documents", map[string]any{}); !strings.Contains(body, "No documents") {
		t.Errorf("outsider's documents = %q, want none", body)
	}
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"read_document", map[string]any{"documentId": docID}},
		{"document_history", map[string]any{"documentId": docID}},
		{"edit_document", map[string]any{"documentId": docID, "text": "mine now"}},
		{"save_document", map[string]any{"documentId": docID}},
		{"upsert_document", map[string]any{"projectId": store.DefaultProjectID, "sourceKey": "github:issue:private", "name": "Sneak", "text": "mine now"}},
		{"create_document", map[string]any{"projectId": store.DefaultProjectID, "name": "Sneak"}},
	} {
		body, isErr := call(t, cs, tool.name, tool.args)
		if !isErr {
			t.Errorf("%s succeeded for an outsider: %s", tool.name, body)
		}
	}

	// And the document is untouched.
	body, _ := call(t, ownerSession, "read_document", map[string]any{"documentId": docID})
	if body != "" {
		t.Errorf("the outsider changed the document: %q", body)
	}
}

// Reads default to the live document, which is the point of the server being
// able to interpret the journal at all. A saved version is still reachable.
func TestReadsSeeUnsavedTextAndNamedVersions(t *testing.T) {
	cs, st, _, _ := session(t, owner)
	created, _ := call(t, cs, "create_document", map[string]any{
		"projectId": store.DefaultProjectID, "name": "Draft",
	})
	docID := strings.Fields(strings.TrimPrefix(created, "Created "))[0]

	// A save records "committed", then the live document moves on.
	if _, err := st.SaveDoc(t.Context(), docID, store.Save{
		Artifact: "committed", SHA256: []byte{1}, Snapshot: []byte{1}, AuthorID: owner,
	}); err != nil {
		t.Fatal(err)
	}
	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": "unsaved work"})

	if body, _ := call(t, cs, "read_document", map[string]any{"documentId": docID}); body != "unsaved work" {
		t.Errorf("default read = %q, want the live text", body)
	}
	if body, _ := call(t, cs, "read_document", map[string]any{"documentId": docID, "version": 1}); body != "committed" {
		t.Errorf("version 1 = %q, want the saved artifact", body)
	}
	if body, _ := call(t, cs, "document_history", map[string]any{"documentId": docID}); !strings.Contains(body, "version 1") {
		t.Errorf("history = %q", body)
	}
}
