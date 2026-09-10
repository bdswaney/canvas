package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tokens are a credential, so these run against the real database rather than
// a stub: the checks that matter — expiry, revocation, a disabled account —
// are in SQL, and a fake would only prove the fake works.
func tokenFixture(t *testing.T) (*PasswordAuth, string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	auth, err := NewPasswordAuth(pool, "")
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("token-%d", time.Now().UnixNano())
	if err := auth.CreateUser(ctx, username, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean := context.Background()
		pool.Exec(clean, `DELETE FROM access_tokens WHERE user_id = (SELECT "Id" FROM "SessionUsers" WHERE "Username" = $1)`, username)
		auth.DeleteUser(clean, username)
	})
	return auth, username
}

func TestTokenAuthenticatesItsAccount(t *testing.T) {
	auth, username := tokenFixture(t)
	ctx := context.Background()

	secret, token, err := auth.CreateToken(ctx, username, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, tokenPrefix) {
		t.Errorf("token %q has no recognisable prefix", secret)
	}
	if strings.Contains(secret, token.Prefix) && len(token.Prefix) >= len(secret) {
		t.Error("the stored prefix is the whole token")
	}

	user, err := auth.UserByToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != username {
		t.Errorf("token resolved to %q, want %q", user.Username, username)
	}
}

// Only a hash is stored, so the database cannot hand anybody a usable
// credential.
func TestTokenIsNotRecoverableFromTheDatabase(t *testing.T) {
	auth, username := tokenFixture(t)
	ctx := context.Background()
	secret, _, err := auth.CreateToken(ctx, username, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := auth.pool.Query(ctx, "SELECT prefix, token_sha256::text FROM access_tokens")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var prefix, hash string
		if err := rows.Scan(&prefix, &hash); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(hash, secret) || secret == prefix {
			t.Fatal("the token itself is in the database")
		}
	}
}

func TestRevokedAndExpiredTokensAreRefused(t *testing.T) {
	auth, username := tokenFixture(t)
	ctx := context.Background()

	revoked, token, err := auth.CreateToken(ctx, username, "revoke me", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeToken(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UserByToken(ctx, revoked); err == nil {
		t.Error("a revoked token still authenticates")
	}
	// Revoking twice is not a second event.
	if err := auth.RevokeToken(ctx, token.ID); err == nil {
		t.Error("revoking twice should fail")
	}

	// A positive ttl is the only way to ask for expiry, so a token that has
	// already lapsed is made by moving its expiry into the past. That also
	// tests the thing that matters — the expires_at check in the lookup
	// query — rather than the arithmetic in CreateToken.
	expired, expiredToken, err := auth.CreateToken(ctx, username, "expire me", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UserByToken(ctx, expired); err != nil {
		t.Fatalf("a token with an hour left was refused: %v", err)
	}
	if _, err := auth.pool.Exec(ctx,
		"UPDATE access_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid",
		expiredToken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UserByToken(ctx, expired); err == nil {
		t.Error("an expired token still authenticates")
	}

	// A revoked token is still listed, so what was issued stays auditable.
	tokens, err := auth.Tokens(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("listed %d tokens, want both", len(tokens))
	}
	for _, token := range tokens {
		if token.Active() {
			t.Errorf("%s reports active", token.Name)
		}
	}
}

func TestGarbageTokensAreRefused(t *testing.T) {
	auth, username := tokenFixture(t)
	ctx := context.Background()
	secret, _, err := auth.CreateToken(ctx, username, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "nonsense", tokenPrefix, tokenPrefix + "wrong",
		secret + "x",           // a suffix must not pass
		secret[:len(secret)-1], // nor a prefix
	} {
		if _, err := auth.UserByToken(ctx, bad); err == nil {
			t.Errorf("%q authenticated", bad)
		}
	}
}

// The middleware has to put the account in the context in the shape the rest
// of the server reads, or every membership check downstream sees a stranger.
func TestRequireTokenPopulatesTheContext(t *testing.T) {
	auth, username := tokenFixture(t)
	ctx := context.Background()
	secret, _, err := auth.CreateToken(ctx, username, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}

	var seen User
	handler := auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = auth.UserFromCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	for _, tt := range []struct {
		name, header string
		status       int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"not bearer", "Basic " + secret, http.StatusUnauthorized},
		{"bad token", "Bearer " + tokenPrefix + "nope", http.StatusUnauthorized},
		{"good token", "Bearer " + secret, http.StatusOK},
		{"case insensitive scheme", "bearer " + secret, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen = User{}
			r := httptest.NewRequest("POST", "/api/mcp", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if tt.status == http.StatusOK && seen.Username != username {
				t.Errorf("context carried %q, want %q", seen.Username, username)
			}
			if tt.status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
				t.Error("a refusal should say what it wanted")
			}
		})
	}
}
