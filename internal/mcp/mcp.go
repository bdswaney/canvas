// Package mcp exposes Canvas over the Model Context Protocol, so an assistant
// can work with a person's projects and documents.
//
// Everything here runs as one signed-in account and goes through the same
// membership checks as the REST API: store.ProjectMember is the single place
// authorization is decided, and a second copy of that rule living here is
// exactly the drift the package layout exists to prevent.
//
// Reads return the *live* document, not the last save. The server can
// interpret the journal now, so "what does my document say" answers with what
// the author has on screen rather than what they last committed. Saved
// versions remain reachable by asking for one explicitly.
package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/store"
)

// Engine is the slice of internal/ydoc this package needs: reading a document
// out of its journal, and turning a desired text into an update that merges
// with whatever else is happening.
type Engine interface {
	Merge(ctx context.Context, updates [][]byte) ([]byte, error)
	Text(ctx context.Context, state []byte, name string) (string, error)
	SetText(ctx context.Context, state []byte, name, next string) ([]byte, error)
}

// Broadcaster hands an update to the clients editing a document. relay.Hub
// implements it; without one a write would be invisible to anybody with the
// document open until they reconnected.
type Broadcaster interface {
	Inject(ctx context.Context, docID string, update []byte) error
}

// textName is the shared Y.Text the editor binds to; see src/pages/Workspace.tsx.
const textName = "notes"

// Server serves one account's view of Canvas.
type Server struct {
	store store.Store
	docs  Engine
	relay Broadcaster
	user  auth.User
}

// New builds the MCP server for a signed-in account.
func New(st store.Store, docs Engine, relay Broadcaster, user auth.User) *mcp.Server {
	s := &Server{store: st, docs: docs, relay: relay, user: user}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "canvas",
		Version: "0.1.0",
		Title:   "Canvas",
	}, nil)
	s.register(server)
	return server
}

// allowed reports whether this account may reach a document, and returns it.
// A document in a project the account does not belong to is reported as
// missing rather than forbidden, matching the REST API: distinguishing them
// would let somebody probe for which ids are real.
func (s *Server) allowed(ctx context.Context, docID string) (store.Doc, error) {
	doc, err := s.store.Doc(ctx, docID)
	if err != nil {
		return store.Doc{}, fmt.Errorf("no document %s", docID)
	}
	member, err := s.store.ProjectMember(ctx, doc.ProjectID, s.user.ID)
	if err != nil {
		return store.Doc{}, fmt.Errorf("check membership: %w", err)
	}
	if !member {
		return store.Doc{}, fmt.Errorf("no document %s", docID)
	}
	return doc, nil
}

// state replays a document's journal into a single update, which is the live
// document as the server understands it.
func (s *Server) state(ctx context.Context, docID string) ([]byte, error) {
	updates, err := s.store.Load(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	if len(updates) == 0 {
		return nil, nil
	}
	return s.docs.Merge(ctx, updates)
}

func text(body string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}}
}
