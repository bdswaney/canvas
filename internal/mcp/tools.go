package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/store"
)

type noInput struct{}

type docRef struct {
	DocumentID string `json:"documentId" jsonschema:"the document's id, from list_documents"`
}

type projectsOutput struct {
	Projects []store.Project `json:"projects"`
}

type projectOutput struct {
	Project store.Project `json:"project"`
}

type documentView struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	Name           string    `json:"name"`
	SourceKey      string    `json:"sourceKey,omitempty"`
	CurrentVersion int       `json:"currentVersion"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type documentsOutput struct {
	Documents []documentView `json:"documents"`
}

type documentOutput struct {
	Document documentView `json:"document"`
	Created  bool         `json:"created"`
}

type readOutput struct {
	DocumentID  string `json:"documentId"`
	Version     int    `json:"version"`
	Text        string `json:"text"`
	BaseVersion string `json:"baseVersion,omitempty"`
}

type versionView struct {
	Version   int       `json:"version"`
	AuthorID  string    `json:"authorId"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

type historyOutput struct {
	DocumentID string        `json:"documentId"`
	Versions   []versionView `json:"versions"`
}

type editOutput struct {
	DocumentID  string `json:"documentId"`
	Saved       bool   `json:"saved"`
	BaseVersion string `json:"baseVersion"`
	Conflict    bool   `json:"conflict,omitempty"`
}

type saveOutput struct {
	DocumentID string `json:"documentId"`
	Version    int    `json:"version"`
	Saved      bool   `json:"saved"`
}

type upsertOutput struct {
	Document     documentView `json:"document"`
	Created      bool         `json:"created"`
	SourceKey    string       `json:"sourceKey"`
	Saved        bool         `json:"saved"`
	SavedVersion int          `json:"savedVersion"`
}

type archiveOutput struct {
	DocumentID string `json:"documentId"`
	Name       string `json:"name"`
	Archived   bool   `json:"archived"`
}

func documentViewOf(doc store.Doc) documentView {
	return documentView{
		ID: doc.ID, ProjectID: doc.ProjectID, Name: doc.Name, SourceKey: doc.SourceKey,
		CurrentVersion: doc.CurrentVersion, UpdatedAt: doc.UpdatedAt,
	}
}

func versionViewsOf(versions []store.Version) []versionView {
	views := make([]versionView, 0, len(versions))
	for _, version := range versions {
		views = append(views, versionView{Version: version.Version, AuthorID: version.AuthorID, Author: version.Author, CreatedAt: version.CreatedAt})
	}
	return views
}

func (s *Server) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Title:       "List projects",
		Description: "List the projects this account belongs to. A project contains documents and the people who can reach them.",
	}, s.listProjects)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_project",
		Title:       "Create a project",
		Description: "Create a project and make this account its first member.",
	}, s.createProject)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_documents",
		Title:       "List documents",
		Description: "List documents. Give a projectId to list one project's, or omit it for every document this account can reach.",
	}, s.listDocuments)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "read_document",
		Title: "Read a document",
		Description: "Read a document's current text, including changes nobody has saved yet. " +
			"The result carries a baseVersion naming exactly the text that was read; pass it to edit_document " +
			"so the edit is refused, rather than undoing somebody else's work, if the document changes in the meantime. " +
			"Pass a version to read a specific saved version instead.",
	}, s.readDocument)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "document_history",
		Title:       "Document history",
		Description: "List a document's saved versions, newest first, with who saved each and when.",
	}, s.documentHistory)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_document",
		Title:       "Create a document",
		Description: "Create an empty document in a project.",
	}, s.createDocument)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "edit_document",
		Title: "Edit a document",
		Description: "Replace a document's live text without creating a saved history version. This is a collaborative edit, not an overwrite: " +
			"the new text is diffed against the current text and only the differences are applied, so somebody typing in text you left unchanged keeps their work. " +
			"Pass the baseVersion from read_document: if the document has changed since that read, nothing is written and the current text comes back to redo the edit against. " +
			"Without baseVersion, anything somebody changed after your read is reverted to the text you send.",
	}, s.editDocument)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_document",
		Title:       "Save a document",
		Description: "Save the current live document text as a version. Use this when the edit should appear in document history.",
	}, s.saveDocument)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "upsert_document",
		Title:       "Import or update a document",
		Description: "Create or update a document using a stable sourceKey. Repeating an import with the same project and sourceKey updates the existing document instead of creating a duplicate. Set save to true to create a history version after the live edit.",
	}, s.upsertDocument)

	destructive := true
	mcp.AddTool(server, &mcp.Tool{
		Name:  "archive_document",
		Title: "Archive a document",
		Description: "Archive a document. It stops appearing in list_documents and can no longer be read, edited, or saved through these tools, " +
			"but it is not deleted: its saved versions are kept, the same as archiving it from the web app. There is no unarchive tool.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, s.archiveDocument)
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, projectsOutput, error) {
	projects, err := s.store.Projects(ctx, s.user.ID)
	if err != nil {
		return nil, projectsOutput{}, err
	}
	if len(projects) == 0 {
		return text("This account belongs to no projects."), projectsOutput{Projects: []store.Project{}}, nil
	}
	var b strings.Builder
	for _, project := range projects {
		fmt.Fprintf(&b, "%s\t%s\n", project.ID, project.Name)
	}
	return text(b.String()), projectsOutput{Projects: projects}, nil
}

type createProjectInput struct {
	Name string `json:"name" jsonschema:"the project's display name"`
}

func (s *Server) createProject(ctx context.Context, _ *mcp.CallToolRequest, in createProjectInput) (*mcp.CallToolResult, projectOutput, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, projectOutput{}, fmt.Errorf("name is required")
	}
	project, err := s.store.CreateProject(ctx, in.Name, s.user.ID)
	if err != nil {
		return nil, projectOutput{}, err
	}
	return text(fmt.Sprintf("Created %s\t%s", project.ID, project.Name)), projectOutput{Project: project}, nil
}

type listDocumentsInput struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"limit to one project; omit for every reachable document"`
}

func (s *Server) listDocuments(ctx context.Context, _ *mcp.CallToolRequest, in listDocumentsInput) (*mcp.CallToolResult, documentsOutput, error) {
	docs, err := s.store.Docs(ctx, in.ProjectID, s.user.ID)
	if err != nil {
		return nil, documentsOutput{}, err
	}
	if len(docs) == 0 {
		return text("No documents."), documentsOutput{Documents: []documentView{}}, nil
	}
	var b strings.Builder
	for _, doc := range docs {
		saved := "never saved"
		if doc.CurrentVersion > 0 {
			saved = fmt.Sprintf("version %d, saved %s",
				doc.CurrentVersion, doc.UpdatedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "%s\t%s\t(%s)\n", doc.ID, doc.Name, saved)
	}
	views := make([]documentView, 0, len(docs))
	for _, doc := range docs {
		views = append(views, documentViewOf(doc))
	}
	return text(b.String()), documentsOutput{Documents: views}, nil
}

type readDocumentInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document's id, from list_documents"`
	Version    int    `json:"version,omitempty" jsonschema:"read this saved version instead of the current text"`
}

func (s *Server) readDocument(ctx context.Context, _ *mcp.CallToolRequest, in readDocumentInput) (*mcp.CallToolResult, readOutput, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, readOutput{}, err
	}
	if in.Version > 0 {
		artifact, err := s.store.Artifact(ctx, in.DocumentID, in.Version)
		if err != nil {
			return nil, readOutput{}, fmt.Errorf("no version %d of %s", in.Version, in.DocumentID)
		}
		return text(artifact), readOutput{DocumentID: in.DocumentID, Version: in.Version, Text: artifact}, nil
	}
	_, body, err := s.liveText(ctx, in.DocumentID)
	if err != nil {
		return nil, readOutput{}, err
	}
	version := versionOf(body)
	// The text is a block of its own, exactly as the document reads, so a
	// caller that edits it and sends it back cannot pick up the version too.
	// The version is repeated in text because not every client shows the model
	// structured content.
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: body},
		&mcp.TextContent{Text: fmt.Sprintf("baseVersion: %s (pass this to edit_document)", version)},
	}}, readOutput{DocumentID: in.DocumentID, Text: body, BaseVersion: version}, nil
}

func (s *Server) documentHistory(ctx context.Context, _ *mcp.CallToolRequest, in docRef) (*mcp.CallToolResult, historyOutput, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, historyOutput{}, err
	}
	versions, err := s.store.Versions(ctx, in.DocumentID)
	if err != nil {
		return nil, historyOutput{}, err
	}
	if len(versions) == 0 {
		return text("This document has never been saved."), historyOutput{DocumentID: in.DocumentID, Versions: []versionView{}}, nil
	}
	var b strings.Builder
	for _, version := range versions {
		who := version.Author
		if who == "" {
			who = "unknown"
		}
		fmt.Fprintf(&b, "version %d\t%s\t%s\n", version.Version, who, version.CreatedAt.Format(time.RFC3339))
	}
	return text(b.String()), historyOutput{DocumentID: in.DocumentID, Versions: versionViewsOf(versions)}, nil
}

type createDocumentInput struct {
	ProjectID string `json:"projectId" jsonschema:"the project to create it in, from list_projects"`
	Name      string `json:"name" jsonschema:"what to call the document"`
}

func (s *Server) createDocument(ctx context.Context, _ *mcp.CallToolRequest, in createDocumentInput) (*mcp.CallToolResult, documentOutput, error) {
	if in.Name == "" {
		return nil, documentOutput{}, fmt.Errorf("name is required")
	}
	if in.ProjectID == "" {
		in.ProjectID = store.DefaultProjectID
	}
	member, err := s.store.ProjectMember(ctx, in.ProjectID, s.user.ID)
	if err != nil {
		return nil, documentOutput{}, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return nil, documentOutput{}, fmt.Errorf("no project %s", in.ProjectID)
	}
	doc, err := s.store.CreateDoc(ctx, in.ProjectID, in.Name)
	if err != nil {
		return nil, documentOutput{}, err
	}
	return text(fmt.Sprintf("Created %s\t%s", doc.ID, doc.Name)), documentOutput{Document: documentViewOf(doc), Created: true}, nil
}

type editDocumentInput struct {
	DocumentID  string `json:"documentId" jsonschema:"the document's id, from list_documents"`
	Text        string `json:"text" jsonschema:"the document's full new text"`
	BaseVersion string `json:"baseVersion,omitempty" jsonschema:"the baseVersion read_document returned for the text this edit starts from; if the document has changed since, nothing is written"`
}

// editLive turns a desired text into a CRDT edit against the document as it
// is now and hands it to everyone editing the document.
func (s *Server) editLive(ctx context.Context, docID, next string) (bool, error) {
	state, current, err := s.liveText(ctx, docID)
	if err != nil {
		return false, err
	}
	return s.apply(ctx, docID, state, current, next)
}

// apply writes next over the state current was read from.
//
// The whole text is the input rather than a position and a span because that
// is how a caller reasoning about a document thinks. What reaches the journal
// is still a minimal edit: SetText diffs current against next and applies each
// hunk separately, so a person typing anywhere the caller did not change keeps
// their work. Replacing the text outright is what restore does, and restore is
// documented as unable to merge for exactly this reason.
func (s *Server) apply(ctx context.Context, docID string, state []byte, current, next string) (bool, error) {
	if current == next {
		return false, nil
	}
	update, err := s.docs.SetText(ctx, state, textName, next)
	if err != nil {
		return false, fmt.Errorf("apply edit: %w", err)
	}
	if s.relay == nil {
		return false, fmt.Errorf("this server cannot write: no relay attached")
	}
	if err := s.relay.InjectFor(ctx, docID, s.user.ID, update); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) liveText(ctx context.Context, docID string) ([]byte, string, error) {
	state, err := s.state(ctx, docID)
	if err != nil {
		return nil, "", err
	}
	if len(state) == 0 {
		return state, "", nil
	}
	body, err := s.docs.Text(ctx, state, textName)
	if err != nil {
		return nil, "", fmt.Errorf("read live text: %w", err)
	}
	return state, body, nil
}

// editDocument applies a caller's full desired text.
//
// A diff cannot tell the caller's changes from changes somebody made after the
// caller read the document: both are differences from the current text, and
// the second kind would be silently reverted. baseVersion closes that gap by
// refusing the edit when the text has moved on since the read. The check and
// the write use the same state, so a keystroke that lands after the check is an
// ordinary concurrent edit, which the CRDT merges.
//
// Refusing was chosen over rebasing (diffing the read text against the new one
// and replaying that onto the current document). A rebase needs the text or
// state that was read, and the server keeps neither: it cannot be rebuilt from
// the journal, because compaction garbage-collects deleted content, and
// carrying it in the token would make baseVersion kilobytes a caller has to
// echo back exactly. Holding it in memory would make the tool rebase or refuse
// depending on which process answered and whether it had restarted. A refusal
// hands back the current text, so the caller can redo the edit against it.
func (s *Server) editDocument(ctx context.Context, _ *mcp.CallToolRequest, in editDocumentInput) (*mcp.CallToolResult, editOutput, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, editOutput{}, err
	}
	state, current, err := s.liveText(ctx, in.DocumentID)
	if err != nil {
		return nil, editOutput{}, err
	}
	if in.BaseVersion != "" {
		if now := versionOf(current); now != in.BaseVersion {
			return staleEdit(in.DocumentID, in.BaseVersion, current), editOutput{
				DocumentID:  in.DocumentID,
				BaseVersion: now,
				Conflict:    true,
			}, nil
		}
	}
	if _, err := s.apply(ctx, in.DocumentID, state, current, in.Text); err != nil {
		return nil, editOutput{}, err
	}
	// The version of the text the caller asked for, so a chain of edits needs
	// no re-read. If somebody typed in the same instant it will not match, and
	// the next edit is refused, which is the safe direction to be wrong in.
	version := versionOf(in.Text)
	return text(fmt.Sprintf("Edited %s; baseVersion is now %s. The change is live for anyone with it open, and is not in saved history until somebody saves.", in.DocumentID, version)), editOutput{
		DocumentID:  in.DocumentID,
		Saved:       false,
		BaseVersion: version,
	}, nil
}

// staleEdit is the refusal for an edit based on text that has since changed.
// It is an error result that still carries the current text, because the
// caller's next step is to redo the edit against it.
func staleEdit(docID, base, current string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Nothing was written: %s has changed since baseVersion %s, "+
				"and applying this text would undo those changes. Its current text follows, at baseVersion %s. "+
				"Make the edit again against it.", docID, base, versionOf(current))},
			&mcp.TextContent{Text: current},
		},
	}
}

// versionOf names a text for baseVersion. It is a hash of the text rather than
// of the CRDT state, so it changes exactly when what a reader would see
// changes: somebody typing a word and deleting it again leaves the version
// alone, because an edit based on the earlier read would revert nothing.
func versionOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

type saveDocumentInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document's id, from list_documents"`
}

func (s *Server) saveLive(ctx context.Context, docID string) (int, error) {
	state, body, err := s.liveText(ctx, docID)
	if err != nil {
		return 0, err
	}
	if len(state) == 0 {
		// An empty document has no journal state yet. SetText gives SaveDoc a
		// valid Yjs snapshot without changing the live document.
		state, err = s.docs.SetText(ctx, nil, textName, body)
		if err != nil {
			return 0, fmt.Errorf("create empty snapshot: %w", err)
		}
	}
	hash := sha256.Sum256([]byte(body))
	version, err := s.store.SaveDoc(ctx, docID, store.Save{
		Artifact: body,
		Snapshot: state,
		SHA256:   hash[:],
		AuthorID: s.user.ID,
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Server) saveDocument(ctx context.Context, _ *mcp.CallToolRequest, in saveDocumentInput) (*mcp.CallToolResult, saveOutput, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, saveOutput{}, err
	}
	version, err := s.saveLive(ctx, in.DocumentID)
	if err != nil {
		return nil, saveOutput{}, err
	}
	return text(fmt.Sprintf("Saved %s as version %d.", in.DocumentID, version)), saveOutput{
		DocumentID: in.DocumentID,
		Version:    version,
		Saved:      true,
	}, nil
}

type upsertDocumentInput struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"the project to import into; omit for the default project"`
	SourceKey string `json:"sourceKey" jsonschema:"stable identifier from the source system, such as github:issue:42"`
	Name      string `json:"name" jsonschema:"document display name"`
	Text      string `json:"text" jsonschema:"the document's full live text"`
	Save      bool   `json:"save,omitempty" jsonschema:"also create a saved history version after updating the live text"`
}

func (s *Server) upsertDocument(ctx context.Context, _ *mcp.CallToolRequest, in upsertDocumentInput) (*mcp.CallToolResult, upsertOutput, error) {
	if strings.TrimSpace(in.SourceKey) == "" {
		return nil, upsertOutput{}, fmt.Errorf("sourceKey is required")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, upsertOutput{}, fmt.Errorf("name is required")
	}
	if in.ProjectID == "" {
		in.ProjectID = store.DefaultProjectID
	}
	member, err := s.store.ProjectMember(ctx, in.ProjectID, s.user.ID)
	if err != nil {
		return nil, upsertOutput{}, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return nil, upsertOutput{}, fmt.Errorf("no project %s", in.ProjectID)
	}
	doc, created, err := s.store.UpsertDoc(ctx, in.ProjectID, in.SourceKey, in.Name)
	if err != nil {
		return nil, upsertOutput{}, err
	}
	changed, err := s.editLive(ctx, doc.ID, in.Text)
	if err != nil {
		return nil, upsertOutput{}, err
	}
	version := 0
	if in.Save {
		// A retry of an identical saved import must not create another history
		// row. An unsaved document (including a newly created empty one) still
		// needs its first version when the caller explicitly asks to save it.
		desiredHash := sha256.Sum256([]byte(in.Text))
		if changed || created || doc.CurrentVersion == 0 || !bytes.Equal(doc.SavedSHA256, desiredHash[:]) {
			version, err = s.saveLive(ctx, doc.ID)
			if err != nil {
				return nil, upsertOutput{}, err
			}
		} else {
			version = doc.CurrentVersion
		}
	}
	doc, err = s.store.Doc(ctx, doc.ID)
	if err != nil {
		return nil, upsertOutput{}, err
	}
	action := "Created"
	if !created {
		action = "Updated"
	}
	persistence := "live only"
	if in.Save {
		persistence = "saved"
	}
	return text(fmt.Sprintf("%s document %s (%s).", action, doc.ID, persistence)), upsertOutput{
		Document:     documentViewOf(doc),
		Created:      created,
		SourceKey:    in.SourceKey,
		Saved:        in.Save,
		SavedVersion: version,
	}, nil
}

// archiveDocument hides a document the way the web app's archive does. Saved
// versions are kept: ArchiveDoc marks the row rather than deleting it. When
// the broadcaster is the browser-serving Hub, it also closes this process's
// live sockets under the same access boundary. An archived document fails the
// membership check like a missing one, so archiving it again is answered the
// same way as an unknown id.
func (s *Server) archiveDocument(ctx context.Context, _ *mcp.CallToolRequest, in docRef) (*mcp.CallToolResult, archiveOutput, error) {
	doc, err := s.allowed(ctx, in.DocumentID)
	if err != nil {
		return nil, archiveOutput{}, err
	}
	archive := s.store.ArchiveDoc
	if archiver, ok := s.relay.(Archiver); ok {
		archive = archiver.ArchiveDoc
	}
	if err := archive(ctx, in.DocumentID); err != nil {
		// Archived by somebody else since the check above: still missing, and
		// still answered the same way.
		if errors.Is(err, store.ErrNotFound) {
			return nil, archiveOutput{}, fmt.Errorf("no document %s", in.DocumentID)
		}
		return nil, archiveOutput{}, fmt.Errorf("archive: %w", err)
	}
	return text(fmt.Sprintf("Archived %s\t%s. It no longer appears in list_documents; its saved versions are kept.", doc.ID, doc.Name)), archiveOutput{
		DocumentID: doc.ID,
		Name:       doc.Name,
		Archived:   true,
	}, nil
}
