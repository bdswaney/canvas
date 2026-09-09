// Package authtest is a stand-in for the session package, so routing and
// socket behaviour can be tested without a database.
package authtest

import (
	"context"
	"errors"
	"net/http"

	"github.com/bdswaney/canvas/internal/auth"
)

// OutsiderID is a signed-in account that belongs to no project.
const OutsiderID = "00000000-0000-4000-8000-0000000000aa"

// Stub implements auth.Authenticator. Sessions are either always valid or
// never valid, which is the only distinction the routing makes. UserID
// overrides who is signed in, so a test can act as somebody who belongs to
// nothing.
type Stub struct {
	Valid  bool
	UserID string
}

func (Stub) StartSession(next http.Handler) http.Handler { return next }

func (Stub) SetXSRFToken(next http.Handler) http.Handler { return next }

func (s Stub) ValidateSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s Stub) ValidateXSRFToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Valid {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s Stub) Login() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}

func (s Stub) Logout() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}

func (s Stub) Authenticated() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.Valid {
			w.Write([]byte(`{"authenticated":true}`))
			return
		}
		w.Write([]byte(`{"authenticated":false}`))
	}
}

func (s Stub) ValidateSessionCtx(ctx context.Context) (context.Context, error) {
	if !s.Valid {
		return ctx, errors.New("session is not valid")
	}
	return ctx, nil
}

func (s Stub) UserFromCtx(context.Context) (auth.User, bool) {
	if !s.Valid {
		return auth.User{}, false
	}
	if s.UserID != "" {
		return auth.User{ID: s.UserID, Username: "outsider"}, true
	}
	return auth.User{ID: "00000000-0000-4000-8000-00000000000f", Username: "tester"}, true
}

func (s Stub) Users(context.Context) ([]auth.User, error) {
	return []auth.User{
		{ID: "00000000-0000-4000-8000-00000000000f", Username: "tester"},
		{ID: OutsiderID, Username: "outsider"},
	}, nil
}

// User is the identity Stub authenticates as by default.
func User() auth.User {
	user, _ := Stub{Valid: true}.UserFromCtx(context.Background())
	return user
}
