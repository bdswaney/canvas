package main

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// projectAPI serves projects and their membership. A project is the container
// above a document and the boundary that decides who can reach one.
type projectAPI struct {
	store Store
	auth  authenticator
}

func (a *projectAPI) projectRoutes(r chi.Router) {
	r.Get("/", a.listProjects)
	r.Post("/", a.createProject)
	r.Delete("/{projectID}", a.archiveProject)
	r.Route("/{projectID}/members", func(r chi.Router) {
		r.Get("/", a.listMembers)
		r.Post("/", a.addMember)
		r.Delete("/{userID}", a.removeMember)
	})
}

// decodeBody reads a small JSON body, reporting whether it succeeded. Request
// bodies here are a name and an id or two; the cap keeps a stray upload from
// being buffered.
func decodeBody(w http.ResponseWriter, r *http.Request, body any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid request body"})
		return false
	}
	return true
}

// allowedProject reports whether the caller belongs to the project named in
// the URL. A project they do not belong to is answered as not found, so that
// membership cannot be probed by id.
func (a *projectAPI) allowed(w http.ResponseWriter, r *http.Request, projectID string) bool {
	user, _ := a.auth.UserFromCtx(r.Context())
	member, err := a.store.ProjectMember(r.Context(), projectID, user.ID)
	if err != nil {
		writeError(w, err)
		return false
	}
	if !member {
		notFound(w)
		return false
	}
	return true
}

// listUsers backs the member picker. Every signed-in person can see it; see
// authenticator.Users for why.
func (a *projectAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.auth.Users(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	people := make([]Member, 0, len(users))
	for _, user := range users {
		people = append(people, Member{UserID: user.ID, Username: user.Username})
	}
	writeJSON(w, http.StatusOK, people)
}

func (a *projectAPI) listProjects(w http.ResponseWriter, r *http.Request) {
	user, _ := a.auth.UserFromCtx(r.Context())
	projects, err := a.store.Projects(r.Context(), user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (a *projectAPI) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "name is required"})
		return
	}
	user, _ := a.auth.UserFromCtx(r.Context())
	project, err := a.store.CreateProject(r.Context(), body.Name, user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

// Archiving hides a project and deletes nothing. Its documents and their
// whole saved history survive, hidden along with it, because that history is
// the only layer here that cannot be rebuilt. There is no un-archive yet;
// restoring one is an UPDATE away, but it needs a decision about what
// restoring a document inside a still-archived project should mean.
func (a *projectAPI) archiveProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if !a.allowed(w, r, projectID) {
		return
	}
	if err := a.store.ArchiveProject(r.Context(), projectID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// Members are managed by any member of the project. There are no roles yet:
// the people in a project are trusted equally, which is the same assumption
// the save endpoint already makes.
func (a *projectAPI) listMembers(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if !a.allowed(w, r, projectID) {
		return
	}
	members, err := a.store.ProjectMembers(r.Context(), projectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if members == nil {
		members = []Member{}
	}
	writeJSON(w, http.StatusOK, members)
}

func (a *projectAPI) addMember(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if !a.allowed(w, r, projectID) {
		return
	}
	var body struct {
		UserID string `json:"userId"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "userId is required"})
		return
	}
	user, _ := a.auth.UserFromCtx(r.Context())
	if err := a.store.AddProjectMember(r.Context(), projectID, body.UserID, user.ID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// removeMember refuses to empty a project. A project with no members is
// unreachable by anyone, including whoever would have to put it right.
func (a *projectAPI) removeMember(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if !a.allowed(w, r, projectID) {
		return
	}
	members, err := a.store.ProjectMembers(r.Context(), projectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(members) <= 1 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"message": "a project must keep at least one member",
		})
		return
	}
	if err := a.store.RemoveProjectMember(r.Context(), projectID, chi.URLParam(r, "userID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
