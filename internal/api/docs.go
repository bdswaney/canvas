// Package api serves the REST endpoints for projects, documents, and
// membership. Every handler here sits behind session validation and the XSRF
// check; the collaboration socket is the relay's, not this package's.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/store"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

const (
	// A save carries the whole document twice, as text and as CRDT state.
	// These bound what one request can push into the database; the server
	// cannot validate the bytes, so it limits them instead.
	maxArtifactBytes = 8 << 20
	maxSnapshotBytes = 16 << 20
)

// LiveAccess is the in-process coordination boundary between REST changes
// and the collaboration hub. It changes durable state and logically revokes
// affected sockets under one linearization lock; physical closure is initiated
// asynchronously before the request returns. A nil value keeps the API useful
// in isolated tests and deployments without a live hub.
type LiveAccess interface {
	RemoveProjectMember(context.Context, string, string) error
	ArchiveProject(context.Context, string) error
	ArchiveDoc(context.Context, string) error
}

// Mount attaches every endpoint under r: /search, /docs, /projects, and /users.
// live is optional for callers that mount the API without a collaboration
// hub. The browser server always passes its hub.
func Mount(r chi.Router, st store.Store, authn auth.Authenticator, live ...LiveAccess) {
	var access LiveAccess
	if len(live) != 0 {
		access = live[0]
	}
	projects := &projectAPI{store: st, auth: authn, live: access}
	docs := &docAPI{store: st, auth: authn, live: access}
	r.Get("/search", docs.search)
	r.Route("/docs", docs.routes)
	r.Route("/projects", projects.projectRoutes)
	r.Get("/users", projects.listUsers)
}

// docAPI serves the document endpoints.
type docAPI struct {
	store store.Store
	auth  auth.Authenticator
	live  LiveAccess
}

func (a *docAPI) routes(r chi.Router) {
	r.Get("/", a.list)
	r.Post("/", a.create)
	r.Route("/{docID}", func(r chi.Router) {
		r.Get("/", a.get)
		r.Delete("/", a.archive)
		r.Post("/save", a.save)
		r.Get("/versions", a.versions)
		r.Post("/restore/{version}", a.restore)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		json.NewEncoder(w).Encode(body)
	}
}

// notFound answers anything the caller is not allowed to see the same way as
// something that does not exist. Distinguishing them would let a non-member
// probe for which document ids are real.
func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
}

// allowedDoc reads a document and reports whether the caller may have it. A
// document is reachable only by members of its project.
func (a *docAPI) allowedDoc(w http.ResponseWriter, r *http.Request) (store.Doc, bool) {
	doc, err := a.store.Doc(r.Context(), chi.URLParam(r, "docID"))
	if err != nil {
		writeError(w, err)
		return store.Doc{}, false
	}
	user, _ := a.auth.UserFromCtx(r.Context())
	member, err := a.store.ProjectMember(r.Context(), doc.ProjectID, user.ID)
	if err != nil {
		writeError(w, err)
		return store.Doc{}, false
	}
	if !member {
		notFound(w)
		return store.Doc{}, false
	}
	return doc, true
}

// search finds documents by their latest saved artifact. Live journal text is
// deliberately absent: unsaved edits and documents with no saved version do
// not enter the search contract.
func (a *docAPI) search(w http.ResponseWriter, r *http.Request) {
	user, _ := a.auth.UserFromCtx(r.Context())
	results, err := a.store.SearchDocuments(r.Context(), user.ID, r.URL.Query().Get("q"))
	if errors.Is(err, store.ErrSearchQueryEmpty) || errors.Is(err, store.ErrSearchQueryLong) || errors.Is(err, store.ErrSearchQueryInvalid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []store.SearchResult{}
	}
	writeJSON(w, http.StatusOK, results)
}

// list returns the caller's documents, or one project's when ?projectId= is
// given. Either way it is bounded by the projects they belong to.
func (a *docAPI) list(w http.ResponseWriter, r *http.Request) {
	user, _ := a.auth.UserFromCtx(r.Context())
	docs, err := a.store.Docs(r.Context(), r.URL.Query().Get("projectId"), user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if docs == nil {
		docs = []store.Doc{}
	}
	writeJSON(w, http.StatusOK, docs)
}

func (a *docAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid request body"})
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "name is required"})
		return
	}
	if body.ProjectID == "" {
		body.ProjectID = store.DefaultProjectID
	}
	user, _ := a.auth.UserFromCtx(r.Context())
	switch member, err := a.store.ProjectMember(r.Context(), body.ProjectID, user.ID); {
	case err != nil:
		writeError(w, err)
		return
	case !member:
		notFound(w)
		return
	}
	doc, err := a.store.CreateDoc(r.Context(), body.ProjectID, body.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (a *docAPI) get(w http.ResponseWriter, r *http.Request) {
	doc, ok := a.allowedDoc(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// save commits one version. The client sends both the readable artifact and
// the CRDT state it was encoded from, because the server has no Yjs and can
// derive neither from the other.
func (a *docAPI) save(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Artifact string `json:"artifact"`
		Snapshot string `json:"snapshot"` // base64
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxArtifactBytes+maxSnapshotBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid request body"})
		return
	}
	if len(body.Artifact) > maxArtifactBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "artifact is too large"})
		return
	}
	snapshot, err := base64.StdEncoding.DecodeString(body.Snapshot)
	if err != nil || len(snapshot) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "snapshot must be base64 encoded Yjs state"})
		return
	}
	if len(snapshot) > maxSnapshotBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "snapshot is too large"})
		return
	}

	if _, ok := a.allowedDoc(w, r); !ok {
		return
	}
	user, _ := a.auth.UserFromCtx(r.Context())
	sum := sha256.Sum256([]byte(body.Artifact))
	version, err := a.store.SaveDoc(r.Context(), chi.URLParam(r, "docID"), store.Save{
		Artifact: body.Artifact,
		Snapshot: snapshot,
		SHA256:   sum[:],
		AuthorID: user.ID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version,
		"sha256":  sum[:],
	})
}

// archive hides a document. Its saved versions are kept: history is the one
// layer of this system that cannot be rebuilt, so nothing here deletes it.
// When mounted with a live Hub, existing sockets are logically revoked
// synchronously; physical socket closure is initiated asynchronously before
// the archive request returns.
func (a *docAPI) archive(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.allowedDoc(w, r); !ok {
		return
	}
	archive := a.store.ArchiveDoc
	if a.live != nil {
		archive = a.live.ArchiveDoc
	}
	if err := archive(r.Context(), chi.URLParam(r, "docID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (a *docAPI) versions(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.allowedDoc(w, r); !ok {
		return
	}
	versions, err := a.store.Versions(r.Context(), chi.URLParam(r, "docID"))
	if err != nil {
		writeError(w, err)
		return
	}
	if versions == nil {
		versions = []store.Version{}
	}
	writeJSON(w, http.StatusOK, versions)
}

// restore hands back a saved artifact for the client to write into the live
// document. The server cannot do it: turning text back into CRDT state needs
// a Yjs, so the caller applies the artifact and saves the result as a new
// version. History is never rewritten.
func (a *docAPI) restore(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid version"})
		return
	}
	if _, ok := a.allowedDoc(w, r); !ok {
		return
	}
	docID := chi.URLParam(r, "docID")
	artifact, err := a.store.Artifact(r.Context(), docID, version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "artifact": artifact})
}
