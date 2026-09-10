package mcp

import (
	"context"
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

func (s *Server) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Title:       "List projects",
		Description: "List the projects this account belongs to. A project contains documents and the people who can reach them.",
	}, s.listProjects)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_documents",
		Title:       "List documents",
		Description: "List documents. Give a projectId to list one project's, or omit it for every document this account can reach.",
	}, s.listDocuments)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "read_document",
		Title: "Read a document",
		Description: "Read a document's current text, including changes the author has not saved yet. " +
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
		Description: "Replace a document's text. This is a collaborative edit, not an overwrite: " +
			"the unchanged start and end are left alone, so somebody typing elsewhere in the document keeps their work. " +
			"It is not saved to history — a person does that from the editor. " +
			"Note: when this server runs as a separate process from the web server (the canvas mcp subcommand), " +
			"somebody with the document already open will not see the change until they reload, " +
			"and their editor may overwrite it. Prefer editing documents nobody is currently in.",
	}, s.editDocument)
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	projects, err := s.store.Projects(ctx, s.user.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(projects) == 0 {
		return text("This account belongs to no projects."), nil, nil
	}
	var b strings.Builder
	for _, project := range projects {
		fmt.Fprintf(&b, "%s\t%s\n", project.ID, project.Name)
	}
	return text(b.String()), nil, nil
}

type listDocumentsInput struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"limit to one project; omit for every reachable document"`
}

func (s *Server) listDocuments(ctx context.Context, _ *mcp.CallToolRequest, in listDocumentsInput) (*mcp.CallToolResult, any, error) {
	docs, err := s.store.Docs(ctx, in.ProjectID, s.user.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(docs) == 0 {
		return text("No documents."), nil, nil
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
	return text(b.String()), nil, nil
}

type readDocumentInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document's id, from list_documents"`
	Version    int    `json:"version,omitempty" jsonschema:"read this saved version instead of the current text"`
}

func (s *Server) readDocument(ctx context.Context, _ *mcp.CallToolRequest, in readDocumentInput) (*mcp.CallToolResult, any, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, nil, err
	}
	if in.Version > 0 {
		artifact, err := s.store.Artifact(ctx, in.DocumentID, in.Version)
		if err != nil {
			return nil, nil, fmt.Errorf("no version %d of %s", in.Version, in.DocumentID)
		}
		return text(artifact), nil, nil
	}
	state, err := s.state(ctx, in.DocumentID)
	if err != nil {
		return nil, nil, err
	}
	if len(state) == 0 {
		return text(""), nil, nil
	}
	body, err := s.docs.Text(ctx, state, textName)
	if err != nil {
		return nil, nil, err
	}
	return text(body), nil, nil
}

func (s *Server) documentHistory(ctx context.Context, _ *mcp.CallToolRequest, in docRef) (*mcp.CallToolResult, any, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, nil, err
	}
	versions, err := s.store.Versions(ctx, in.DocumentID)
	if err != nil {
		return nil, nil, err
	}
	if len(versions) == 0 {
		return text("This document has never been saved."), nil, nil
	}
	var b strings.Builder
	for _, version := range versions {
		who := version.Author
		if who == "" {
			who = "unknown"
		}
		fmt.Fprintf(&b, "version %d\t%s\t%s\n", version.Version, who, version.CreatedAt.Format(time.RFC3339))
	}
	return text(b.String()), nil, nil
}

type createDocumentInput struct {
	ProjectID string `json:"projectId" jsonschema:"the project to create it in, from list_projects"`
	Name      string `json:"name" jsonschema:"what to call the document"`
}

func (s *Server) createDocument(ctx context.Context, _ *mcp.CallToolRequest, in createDocumentInput) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if in.ProjectID == "" {
		in.ProjectID = store.DefaultProjectID
	}
	member, err := s.store.ProjectMember(ctx, in.ProjectID, s.user.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return nil, nil, fmt.Errorf("no project %s", in.ProjectID)
	}
	doc, err := s.store.CreateDoc(ctx, in.ProjectID, in.Name)
	if err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Created %s\t%s", doc.ID, doc.Name)), nil, nil
}

type editDocumentInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document's id, from list_documents"`
	Text       string `json:"text" jsonschema:"the document's full new text"`
}

// editDocument turns a desired text into a CRDT edit and hands it to everyone
// editing the document.
//
// The whole text is the input rather than a position and a span because that
// is how a caller reasoning about a document thinks. What reaches the journal
// is still a minimal edit: SetText leaves the shared prefix and suffix alone,
// so a person typing in another paragraph keeps their work. Replacing the text
// outright is what restore does, and restore is documented as unable to merge
// for exactly this reason.
func (s *Server) editDocument(ctx context.Context, _ *mcp.CallToolRequest, in editDocumentInput) (*mcp.CallToolResult, any, error) {
	if _, err := s.allowed(ctx, in.DocumentID); err != nil {
		return nil, nil, err
	}
	state, err := s.state(ctx, in.DocumentID)
	if err != nil {
		return nil, nil, err
	}
	update, err := s.docs.SetText(ctx, state, textName, in.Text)
	if err != nil {
		return nil, nil, fmt.Errorf("apply edit: %w", err)
	}
	if s.relay == nil {
		return nil, nil, fmt.Errorf("this server cannot write: no relay attached")
	}
	if err := s.relay.Inject(ctx, in.DocumentID, update); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Edited %s. The change is live for anyone with it open, "+
		"and is not in saved history until somebody saves.", in.DocumentID)), nil, nil
}
