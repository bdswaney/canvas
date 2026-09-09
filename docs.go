package main

import (
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

// docAPI serves the document endpoints.
type docAPI struct {
	store store.Store
	auth  auth.Authenticator
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
func (a *docAPI) archive(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.allowedDoc(w, r); !ok {
		return
	}
	if err := a.store.ArchiveDoc(r.Context(), chi.URLParam(r, "docID")); err != nil {
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
