package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
)

const (
	owner     = "00000000-0000-4000-8000-00000000000f"
	outsider  = "00000000-0000-4000-8000-0000000000aa"
	textField = "notes"
)

// injector stands in for the relay: it journals the update the way the hub
// would and records that the actor-aware injection path was called.
type injector struct {
	store    store.Store
	injected int
	last     []byte
}

func (i *injector) InjectFor(ctx context.Context, docID, _ string, update []byte) error {
	i.injected++
	i.last = update
	return i.store.Append(ctx, docID, update)
}

// pausedHub lets the race tests stop an already-authorized MCP edit immediately
// before it enters the Hub. Revocation can then complete before InjectFor
// rechecks the durable state.
type pausedHub struct {
	hub    *relay.Hub
	paused chan struct{}
	resume chan struct{}
}

func (p *pausedHub) InjectFor(ctx context.Context, docID, actorID string, update []byte) error {
	close(p.paused)
	<-p.resume
	return p.hub.InjectFor(ctx, docID, actorID, update)
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
	return connect(t, st, engine, relay, user), st, engine, relay
}

// connect serves one account over an existing store.
func connect(t *testing.T, st store.Store, engine *ydoc.Engine, relay Broadcaster, user string) *mcp.ClientSession {
	t.Helper()
	server := New(st, engine, relay, auth.User{ID: user, Username: "tester"})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { server.Run(context.Background(), serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestMCPEditRechecksAccessAfterRemovalOrArchive(t *testing.T) {
	cases := []struct {
		name   string
		revoke func(*relay.Hub, string) error
	}{
		{
			name: "member removal",
			revoke: func(h *relay.Hub, _ string) error {
				return h.RemoveProjectMember(context.Background(), store.DefaultProjectID, outsider)
			},
		},
		{
			name: "document archive",
			revoke: func(h *relay.Hub, docID string) error {
				return h.ArchiveDoc(context.Background(), docID)
			},
		},
		{
			name: "project archive",
			revoke: func(h *relay.Hub, _ string) error {
				return h.ArchiveProject(context.Background(), store.DefaultProjectID)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewMemoryStore()
			if err := st.AddProjectMember(t.Context(), store.DefaultProjectID, owner, ""); err != nil {
				t.Fatal(err)
			}
			if err := st.AddProjectMember(t.Context(), store.DefaultProjectID, outsider, owner); err != nil {
				t.Fatal(err)
			}
			doc, err := st.CreateDoc(t.Context(), store.DefaultProjectID, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			hub := relay.NewHub(st, nil)
			gate := &pausedHub{hub: hub, paused: make(chan struct{}), resume: make(chan struct{})}
			engine, err := ydoc.New(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { engine.Close(context.Background()) })
			cs := connect(t, st, engine, gate, outsider)

			result := make(chan struct {
				result *mcp.CallToolResult
				err    error
			}, 1)
			go func() {
				res, callErr := cs.CallTool(context.Background(), &mcp.CallToolParams{
					Name: "edit_document", Arguments: map[string]any{
						"documentId": doc.ID, "text": "must not land",
					},
				})
				result <- struct {
					result *mcp.CallToolResult
					err    error
				}{res, callErr}
			}()
			<-gate.paused

			if err := tc.revoke(hub, doc.ID); err != nil {
				t.Fatal(err)
			}
			close(gate.resume)
			out := <-result
			if out.err != nil {
				t.Fatal(out.err)
			}
			if !out.result.IsError {
				t.Fatalf("edit after %s was accepted: %v", tc.name, texts(out.result))
			}
			if got := texts(out.result); len(got) == 0 || got[0] != "no document "+doc.ID {
				t.Fatalf("edit after %s = %q; want no-document error", tc.name, got)
			}
			updates, err := st.Load(t.Context(), doc.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(updates) != 0 {
				t.Fatalf("edit after %s appended %x", tc.name, updates)
			}
		})
	}
}

func callResult(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func texts(res *mcp.CallToolResult) []string {
	var out []string
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			out = append(out, tc.Text)
		}
	}
	return out
}

// call returns a tool's first text block. read_document puts the document in a
// block of its own, exactly as it reads, and its baseVersion in the next.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res := callResult(t, cs, name, args)
	blocks := texts(res)
	if len(blocks) == 0 {
		return "", res.IsError
	}
	return blocks[0], res.IsError
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	fields, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content in %+v", res)
	}
	return fields
}

// baseVersion pulls the version token out of a result's structured content.
func baseVersion(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	version, _ := structured(t, res)["baseVersion"].(string)
	if version == "" {
		t.Fatalf("no baseVersion in %#v", res.StructuredContent)
	}
	return version
}

func create(t *testing.T, cs *mcp.ClientSession, name string) string {
	t.Helper()
	created, isErr := call(t, cs, "create_document", map[string]any{
		"projectId": store.DefaultProjectID, "name": name,
	})
	if isErr {
		t.Fatalf("create: %s", created)
	}
	return strings.Fields(strings.TrimPrefix(created, "Created "))[0]
}

// humanEdit journals a change the way somebody typing in the editor would.
func humanEdit(t *testing.T, st store.Store, engine *ydoc.Engine, docID, next string) {
	t.Helper()
	updates, err := st.Load(t.Context(), docID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := engine.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}
	update, err := engine.SetText(t.Context(), state, textField, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(t.Context(), docID, update); err != nil {
		t.Fatal(err)
	}
}

// plan is about the size of the document that exposed the problem: a command
// named near the top and again near the bottom, with kilobytes between.
func plan() string {
	var b strings.Builder
	b.WriteString("# SSO plan\n\nStart with `canvas-old login`. Café, naïve, 👍 — not only ASCII.\n\n")
	for i := range 100 {
		fmt.Fprintf(&b, "Step %d: configure one more part of the rollout, and check it before moving on 🎉.\n", i)
	}
	b.WriteString("\nWhen it is done, run `canvas-old login` again to confirm.\n")
	return b.String()
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
		"archive_document": false,
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
		// A client uses the hint to ask before running it.
		if tool.Name == "archive_document" {
			if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("archive_document is not marked destructive: %+v", tool.Annotations)
			}
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

// Two small changes far apart are two small operations. Trimming only the
// shared prefix and suffix deleted and inserted the span between them again:
// 8,741 bytes in the journal for a few dozen changed characters.
func TestTwoDistantEditsWriteASmallUpdate(t *testing.T) {
	cs, _, _, relay := session(t, owner)
	docID := create(t, cs, "Plan")

	body := plan()
	if out, isErr := call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": body}); isErr {
		t.Fatalf("edit: %s", out)
	}
	whole := len(relay.last)

	next := strings.ReplaceAll(body, "canvas-old", "canvas-new")
	if out, isErr := call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": next}); isErr {
		t.Fatalf("edit: %s", out)
	}
	small := len(relay.last)
	t.Logf("document %d bytes: writing it journalled %d bytes, two distant edits %d", len(body), whole, small)

	if small > 300 {
		t.Errorf("two small edits journalled %d bytes (the whole document was %d); "+
			"the text between them was rewritten", small, whole)
	}
	if got, _ := call(t, cs, "read_document", map[string]any{"documentId": docID}); got != next {
		t.Fatalf("read back differs from the requested text")
	}
}

// The stale read: the assistant reads, a person edits the middle, and the
// assistant sends text based on what it read. With baseVersion that edit is
// refused, so the person's work survives, and the refusal carries what the
// assistant needs to try again.
func TestStaleEditIsRefused(t *testing.T) {
	cs, st, engine, relay := session(t, owner)
	docID := create(t, cs, "Plan")
	body := plan()
	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": body})

	read := callResult(t, cs, "read_document", map[string]any{"documentId": docID})
	if blocks := texts(read); len(blocks) < 2 || blocks[0] != body || !strings.Contains(blocks[1], baseVersion(t, read)) {
		t.Fatal("read_document must return the document exactly, then its baseVersion in text")
	}
	readVersion := baseVersion(t, read)

	const person = " (a person wrote this)"
	withPerson := strings.Replace(body, "Step 50:", "Step 50:"+person, 1)
	humanEdit(t, st, engine, docID, withPerson)
	injected := relay.injected

	stale := callResult(t, cs, "edit_document", map[string]any{
		"documentId":  docID,
		"text":        strings.Replace(body, "canvas-old", "canvas-new", 1),
		"baseVersion": readVersion,
	})
	if !stale.IsError {
		t.Fatalf("an edit based on a stale read was accepted: %v", texts(stale))
	}
	if relay.injected != injected {
		t.Fatal("the refused edit was written anyway")
	}
	if got, _ := call(t, cs, "read_document", map[string]any{"documentId": docID}); got != withPerson {
		t.Fatalf("the person's edit did not survive: %q", got)
	}
	if blocks := texts(stale); len(blocks) < 2 || blocks[1] != withPerson {
		t.Fatalf("the refusal does not carry the current text: %q", blocks)
	}
	if conflict, _ := structured(t, stale)["conflict"].(bool); !conflict {
		t.Errorf("the refusal is not marked as a conflict: %#v", stale.StructuredContent)
	}
	fresh := baseVersion(t, stale)
	if fresh == readVersion {
		t.Fatal("the refusal handed back the stale version")
	}

	// Redone against the current text, it goes through and both survive.
	redone := callResult(t, cs, "edit_document", map[string]any{
		"documentId":  docID,
		"text":        strings.Replace(withPerson, "canvas-old", "canvas-new", 1),
		"baseVersion": fresh,
	})
	if redone.IsError {
		t.Fatalf("the redone edit was refused: %v", texts(redone))
	}
	got, _ := call(t, cs, "read_document", map[string]any{"documentId": docID})
	if !strings.Contains(got, person) || !strings.Contains(got, "canvas-new") {
		t.Fatalf("an edit was lost: %q", got)
	}

	// The version an edit returns chains into the next one without a re-read.
	chained := callResult(t, cs, "edit_document", map[string]any{
		"documentId":  docID,
		"text":        got + "\nOne more line.\n",
		"baseVersion": baseVersion(t, redone),
	})
	if chained.IsError {
		t.Fatalf("an edit based on the previous edit's version was refused: %v", texts(chained))
	}
}

// Without baseVersion the text is applied to the document as it is now,
// including reverting a change the caller never saw. The tool description
// says so; this pins that omitting it still works.
func TestEditWithoutBaseVersionAppliesToTheCurrentText(t *testing.T) {
	cs, st, engine, _ := session(t, owner)
	docID := create(t, cs, "Plan")
	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": "one\ntwo\nthree\n"})
	humanEdit(t, st, engine, docID, "one\ntwo and a half\nthree\n")

	if out, isErr := call(t, cs, "edit_document", map[string]any{
		"documentId": docID, "text": "ONE\ntwo\nthree\n",
	}); isErr {
		t.Fatalf("edit without baseVersion: %s", out)
	}
	if got, _ := call(t, cs, "read_document", map[string]any{"documentId": docID}); got != "ONE\ntwo\nthree\n" {
		t.Fatalf("read back = %q", got)
	}
}

// Archiving hides a document and keeps its history, the same as the web app.
// Once archived it is out of reach like a missing document, including for a
// second archive.
func TestArchiveHidesTheDocumentAndKeepsHistory(t *testing.T) {
	cs, st, _, _ := session(t, owner)
	docID := create(t, cs, "Mistake")
	call(t, cs, "edit_document", map[string]any{"documentId": docID, "text": "kept"})
	if saved := callResult(t, cs, "save_document", map[string]any{"documentId": docID}); saved.IsError {
		t.Fatalf("save: %v", texts(saved))
	}

	archived := callResult(t, cs, "archive_document", map[string]any{"documentId": docID})
	if archived.IsError {
		t.Fatalf("archive: %v", texts(archived))
	}
	if ok, _ := structured(t, archived)["archived"].(bool); !ok {
		t.Errorf("archive result is not marked archived: %#v", archived.StructuredContent)
	}

	if body, _ := call(t, cs, "list_documents", map[string]any{}); strings.Contains(body, docID) {
		t.Errorf("an archived document is still listed: %q", body)
	}
	for _, tool := range []string{"read_document", "document_history", "archive_document"} {
		body, isErr := call(t, cs, tool, map[string]any{"documentId": docID})
		if !isErr || body != "no document "+docID {
			t.Errorf("%s on an archived document = %q, isErr=%v; want %q", tool, body, isErr, "no document "+docID)
		}
	}
	if artifact, err := st.Artifact(t.Context(), docID, 1); err != nil || artifact != "kept" {
		t.Errorf("saved version after archive = %q, %v; history must be kept", artifact, err)
	}
}

// A non-member archiving somebody else's document is told the same thing as
// somebody naming an id that does not exist, and the document stays.
func TestOutsiderCannotArchive(t *testing.T) {
	ownerSession, st, engine, _ := session(t, owner)
	docID := create(t, ownerSession, "Private")
	cs := connect(t, st, engine, &injector{store: st}, outsider)

	body, isErr := call(t, cs, "archive_document", map[string]any{"documentId": docID})
	if !isErr || body != "no document "+docID {
		t.Errorf("outsider archiving = %q, isErr=%v; want %q", body, isErr, "no document "+docID)
	}
	const missing = "00000000-0000-4000-8000-000000000404"
	if body, isErr := call(t, ownerSession, "archive_document", map[string]any{"documentId": missing}); !isErr || body != "no document "+missing {
		t.Errorf("archiving an unknown id = %q, isErr=%v", body, isErr)
	}

	if listed, _ := call(t, ownerSession, "list_documents", map[string]any{}); !strings.Contains(listed, docID) {
		t.Fatalf("the outsider archived the document: %q", listed)
	}
	if _, err := st.Doc(t.Context(), docID); err != nil {
		t.Fatalf("the document is gone from the store: %v", err)
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
		{"archive_document", map[string]any{"documentId": docID}},
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
