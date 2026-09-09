package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/store"
)

// mount builds the API behind the same middleware chain main uses for it, so
// these tests exercise the wiring and not only the handlers.
func mount(st store.Store, authn auth.Authenticator) http.Handler {
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		r.Use(authn.StartSession)
		r.Use(authn.SetXSRFToken)
		r.Group(func(r chi.Router) {
			r.Use(authn.ValidateSession)
			r.Use(authn.ValidateXSRFToken)
			Mount(r, st, authn)
		})
	})
	return router
}

// newTestStore returns a memory store with the stub user already in the
// default project, which is what the membership migration's seed does for a
// database that existed before membership. Tests that want to exercise a
// non-member use a different id instead.
func newTestStore(t *testing.T) *store.MemoryStore {
	t.Helper()
	st := store.NewMemoryStore()
	if err := st.AddProjectMember(context.Background(), store.DefaultProjectID, authtest.User().ID, ""); err != nil {
		t.Fatal(err)
	}
	return st
}
