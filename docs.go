package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	store Store
	auth  authenticator
}

func (a *docAPI) routes(r chi.Router) {
	r.Get("/", a.list)
	r.Post("/", a.create)
	r.Route("/{docID}", func(r chi.Router) {
		r.Get("/", a.get)
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

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
}

func (a *docAPI) list(w http.ResponseWriter, r *http.Request) {
	docs, err := a.store.Docs(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if docs == nil {
		docs = []Doc{}
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
		body.ProjectID = defaultProjectID
	}
	doc, err := a.store.CreateDoc(r.Context(), body.ProjectID, body.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (a *docAPI) get(w http.ResponseWriter, r *http.Request) {
	doc, err := a.store.Doc(r.Context(), chi.URLParam(r, "docID"))
	if err != nil {
		writeError(w, err)
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

	user, _ := a.auth.UserFromCtx(r.Context())
	sum := sha256.Sum256([]byte(body.Artifact))
	version, err := a.store.SaveDoc(r.Context(), chi.URLParam(r, "docID"), Save{
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

func (a *docAPI) versions(w http.ResponseWriter, r *http.Request) {
	versions, err := a.store.Versions(r.Context(), chi.URLParam(r, "docID"))
	if err != nil {
		writeError(w, err)
		return
	}
	if versions == nil {
		versions = []Version{}
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
	docID := chi.URLParam(r, "docID")
	artifact, err := a.store.Artifact(r.Context(), docID, version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "artifact": artifact})
}
