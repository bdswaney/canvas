package main

import (
	"context"
	"errors"
	"net/http"
)

// stubAuth stands in for the session package so routing and socket behavior
// can be tested without a database. Sessions are either always valid or never
// valid, which is the only distinction the routing makes.
type stubAuth struct{ valid bool }

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

func (s stubAuth) ValidateSessionCtx(context.Context) error {
	if !s.valid {
		return errors.New("session is not valid")
	}
	return nil
}
