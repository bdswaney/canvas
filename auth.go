package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cccteam/session"
	"github.com/cccteam/session/sessionstorage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// authenticator is the slice of the session package this app uses. Routing
// depends on the interface rather than the concrete generic type so tests can
// substitute a stub and exercise the wiring without a database.
type authenticator interface {
	// Middleware. StartSession reads the cookie; everything else needs it to
	// have run first.
	StartSession(next http.Handler) http.Handler
	SetXSRFToken(next http.Handler) http.Handler
	ValidateSession(next http.Handler) http.Handler
	ValidateXSRFToken(next http.Handler) http.Handler

	// Endpoints, mounted under /api/auth/session.
	Login() http.HandlerFunc
	Logout() http.HandlerFunc
	Authenticated() http.HandlerFunc

	// ValidateSessionCtx reports whether the session on this request is still
	// valid. The WebSocket handler checks it itself instead of sitting behind
	// ValidateSession, so that it can reject with a close code the client
	// understands rather than a failed handshake it will retry forever.
	ValidateSessionCtx(ctx context.Context) error
}

// passwordAuth adapts the session package's PasswordAuth to authenticator.
type passwordAuth struct {
	*session.PasswordAuth[session.NoCustomData, session.NoCustomData]
}

// newPasswordAuth builds username/password authentication over the app's own
// connection pool. cookieKey is base64 of at least 32 random bytes; empty
// makes the session package generate one and print it, which is fine for
// development but invalidates every session on restart.
func newPasswordAuth(pool *pgxpool.Pool, cookieKey string) (*passwordAuth, error) {
	auth, err := session.NewPasswordAuth[session.NoCustomData, session.NoCustomData](
		sessionstorage.NewPostgresPassword(pool),
		cookieKey,
	)
	if err != nil {
		return nil, fmt.Errorf("configure password authentication: %w", err)
	}
	return &passwordAuth{PasswordAuth: auth}, nil
}

func (a *passwordAuth) ValidateSessionCtx(ctx context.Context) error {
	if _, err := a.API().ValidateSession(ctx); err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	return nil
}

// createUser adds a user account. The HTTP handler for this sits behind
// authentication, so the first account has to be made from the command line.
func (a *passwordAuth) createUser(ctx context.Context, username, password string) error {
	if _, err := a.API().CreateSessionUser(ctx, &session.CreateUserRequest{
		Username: username,
		Password: &password,
	}); err != nil {
		return fmt.Errorf("create user %q: %w", username, err)
	}
	return nil
}
