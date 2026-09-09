package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// stubAuth stands in for the session package so routing and socket behavior
// can be tested without a database. Sessions are either always valid or never
// valid, which is the only distinction the routing makes. userID overrides who
// is signed in, so a test can act as somebody who belongs to nothing.
type stubAuth struct {
	valid  bool
	userID string
}

func (stubAuth) StartSession(next http.Handler) http.Handler { return next }

func (stubAuth) SetXSRFToken(next http.Handler) http.Handler { return next }

func (s stubAuth) ValidateSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s stubAuth) ValidateXSRFToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.valid {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s stubAuth) Login() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}

func (s stubAuth) Logout() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}

func (s stubAuth) Authenticated() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.valid {
			w.Write([]byte(`{"authenticated":true}`))
			return
		}
		w.Write([]byte(`{"authenticated":false}`))
	}
}

func (s stubAuth) ValidateSessionCtx(ctx context.Context) (context.Context, error) {
	if !s.valid {
		return ctx, errors.New("session is not valid")
	}
	return ctx, nil
}

func (s stubAuth) UserFromCtx(context.Context) (User, bool) {
	if !s.valid {
		return User{}, false
	}
	if s.userID != "" {
		return User{ID: s.userID, Username: "outsider"}, true
	}
	return User{ID: "00000000-0000-4000-8000-00000000000f", Username: "tester"}, true
}

func (s stubAuth) Users(context.Context) ([]User, error) {
	return []User{
		{ID: "00000000-0000-4000-8000-00000000000f", Username: "tester"},
		{ID: outsiderID, Username: "outsider"},
	}, nil
}

// stubUser is the identity stubAuth authenticates as.
func stubUser() User {
	user, _ := stubAuth{valid: true}.UserFromCtx(context.Background())
	return user
}

// newTestStore returns a memory store with the stub user already in the
// default project, which is what the membership migration's seed does for a
// database that existed before membership. Tests that want to exercise a
// non-member use a different id instead.
func newTestStore(t *testing.T) *MemoryStore {
	t.Helper()
	store := NewMemoryStore()
	if err := store.AddProjectMember(context.Background(), defaultProjectID, stubUser().ID, ""); err != nil {
		t.Fatal(err)
	}
	return store
}
