package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cccteam/ccc"
	"github.com/cccteam/session"
	"github.com/cccteam/session/sessioninfo"
	"github.com/cccteam/session/sessionstorage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is the authenticated person. ID is the SessionUsers id and is what
// other tables reference: usernames are mutable, so they are for display only.
type User struct {
	ID       string
	Username string
}

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
	// valid and returns a context carrying the user. The WebSocket handler
	// checks it itself instead of sitting behind ValidateSession, so that it
	// can reject with a close code the client understands rather than a failed
	// handshake it will retry forever.
	ValidateSessionCtx(ctx context.Context) (context.Context, error)

	// UserFromCtx returns the authenticated user, if the context has been
	// through session validation.
	UserFromCtx(ctx context.Context) (User, bool)

	// Users lists the accounts that exist, so a member can pick somebody to
	// add to a project. This deliberately shows every username to every
	// signed-in person: there is no way to grant access to someone you cannot
	// name, and the alternative is inviting by exact spelling with no
	// feedback. It is a real disclosure, and worth revisiting if Canvas ever
	// holds more than one organisation.
	Users(ctx context.Context) ([]User, error)
}

// passwordAuth adapts the session package's PasswordAuth to authenticator.
type passwordAuth struct {
	*session.PasswordAuth[session.NoCustomData, session.NoCustomData]
	// pool is the same one the session storage uses. Account administration
	// needs to look a user up by name, which the package's API does not
	// expose: everything there is keyed by id.
	pool *pgxpool.Pool
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
	return &passwordAuth{PasswordAuth: auth, pool: pool}, nil
}

// ValidateSessionCtx validates the session and returns a context carrying the
// user, which is what the WebSocket handler checks membership with.
//
// The package's API form of ValidateSession is not the same as its middleware:
// it validates the session and carries its username, but only the middleware
// looks the account up and attaches it to the context. So this has to do that
// part itself — without it UserFromCtx finds nothing, and the socket refuses
// every member as a stranger.
func (a *passwordAuth) ValidateSessionCtx(ctx context.Context) (context.Context, error) {
	ctx, err := a.API().ValidateSession(ctx)
	if err != nil {
		return ctx, fmt.Errorf("validate session: %w", err)
	}

	session, ok := ctx.Value(sessioninfo.CtxSessionInfo).(*sessioninfo.SessionData)
	if !ok || session.SessionInfo == nil {
		return ctx, errors.New("validated session carries no session info")
	}

	// Matched the way the session package matches it, so a login that differs
	// only by case or Unicode form resolves to the same account.
	var id, username string
	var disabled bool
	err = a.pool.QueryRow(ctx,
		`SELECT "Id"::text, "Username", "Disabled" FROM "SessionUsers"
		 WHERE "NormalizedUsername" = casefold(normalize($1))`, session.Username).Scan(&id, &username, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, fmt.Errorf("no account named %q", session.Username)
	}
	if err != nil {
		return ctx, fmt.Errorf("look up the session's account: %w", err)
	}
	if disabled {
		return ctx, fmt.Errorf("account %q is disabled", username)
	}
	userID, err := ccc.UUIDFromString(id)
	if err != nil {
		return ctx, fmt.Errorf("parse user id %q: %w", id, err)
	}

	return context.WithValue(ctx, sessioninfo.CtxUserInfo, &sessioninfo.UserInfo{
		ID:       userID,
		Username: username,
	}), nil
}

// UserFromCtx reads the context value directly rather than calling
// sessioninfo.UserFromCtx, which panics when the value is absent.
func (a *passwordAuth) UserFromCtx(ctx context.Context) (User, bool) {
	info, ok := ctx.Value(sessioninfo.CtxUserInfo).(*sessioninfo.UserInfo)
	if !ok {
		return User{}, false
	}
	return User{ID: info.ID.String(), Username: info.Username}, true
}

// Users lists every account, ordered by name. The session package keys
// everything by id and offers no listing, so this reads the table directly.
func (a *passwordAuth) Users(ctx context.Context) ([]User, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT "Id"::text, "Username" FROM "SessionUsers" WHERE NOT "Disabled" ORDER BY "Username"`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
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

// deleteUser removes an account by name and destroys its sessions. Rows that
// reference "SessionUsers" hold the id, so anything the account authored has
// to be dealt with first; the foreign key refuses the delete otherwise, which
// is the point of storing the id rather than the name.
func (a *passwordAuth) deleteUser(ctx context.Context, username string) error {
	var id string
	err := a.pool.QueryRow(ctx,
		`SELECT "Id" FROM "SessionUsers" WHERE "Username" = $1`, username).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no user named %q", username)
	}
	if err != nil {
		return fmt.Errorf("look up user %q: %w", username, err)
	}
	userID, err := ccc.UUIDFromString(id)
	if err != nil {
		return fmt.Errorf("parse user id %q: %w", id, err)
	}
	if err := a.API().DeleteSessionUser(ctx, userID); err != nil {
		return fmt.Errorf("delete user %q: %w", username, err)
	}
	return nil
}
