package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cccteam/ccc"
	"github.com/cccteam/session/sessioninfo"
	"github.com/jackc/pgx/v5"
)

// tokenPrefix marks a Canvas token wherever one turns up — in a log, a
// config file, a paste — so it can be recognised and revoked.
const tokenPrefix = "canvas_pat_"

// tokenBytes is the random part. 32 bytes is well beyond guessing, which is
// why the stored hash can be a plain sha256: there is no low-entropy secret
// here for a slow hash to protect.
const tokenBytes = 32

// Token describes an issued credential. The secret itself is never in here:
// it exists once, in the return value of CreateToken.
type Token struct {
	ID         string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// Active reports whether the token would authenticate right now.
func (t Token) Active() bool {
	if t.RevokedAt != nil {
		return false
	}
	return t.ExpiresAt == nil || t.ExpiresAt.After(time.Now())
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateToken issues a credential for an account and returns it. This is the
// only time the token exists in full: only its hash is stored, so a lost token
// is replaced rather than recovered.
func (a *PasswordAuth) CreateToken(ctx context.Context, username, name string, ttl time.Duration) (string, Token, error) {
	user, err := a.UserByUsername(ctx, username)
	if err != nil {
		return "", Token{}, err
	}
	if name == "" {
		return "", Token{}, errors.New("a token needs a name, so it can be recognised later")
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, fmt.Errorf("generate token: %w", err)
	}
	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	// Enough to tell two tokens apart in a list, far too little to use.
	prefix := secret[:len(tokenPrefix)+6]

	var expires *time.Time
	if ttl > 0 {
		at := time.Now().Add(ttl)
		expires = &at
	}

	var token Token
	err = a.pool.QueryRow(ctx,
		`INSERT INTO access_tokens (user_id, name, token_sha256, prefix, expires_at)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING id::text, name, prefix, created_at, expires_at`,
		user.ID, name, hashToken(secret), prefix, expires,
	).Scan(&token.ID, &token.Name, &token.Prefix, &token.CreatedAt, &token.ExpiresAt)
	if err != nil {
		return "", Token{}, fmt.Errorf("store token: %w", err)
	}
	return secret, token, nil
}

// Tokens lists an account's tokens, revoked and expired ones included, so
// somebody auditing can see what was issued rather than only what still works.
func (a *PasswordAuth) Tokens(ctx context.Context, username string) ([]Token, error) {
	user, err := a.UserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	rows, err := a.pool.Query(ctx,
		`SELECT id::text, name, prefix, created_at, expires_at, revoked_at, last_used_at
		 FROM access_tokens WHERE user_id = $1::uuid ORDER BY created_at DESC`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var tokens []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt,
			&t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeToken stops a token working. It is marked rather than deleted, so a
// token that was used stays accountable for what it did.
func (a *PasswordAuth) RevokeToken(ctx context.Context, id string) error {
	tag, err := a.pool.Exec(ctx,
		"UPDATE access_tokens SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL", id)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no active token %s", id)
	}
	return nil
}

// UserByToken resolves a presented token to an account.
//
// Expiry and revocation are checked in the query, so a token that has lapsed
// is indistinguishable from one that never existed — there is nothing to learn
// from the difference.
func (a *PasswordAuth) UserByToken(ctx context.Context, secret string) (User, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return User{}, errors.New("not a Canvas token")
	}
	var user User
	var tokenID string
	err := a.pool.QueryRow(ctx,
		`SELECT t.id::text, u."Id"::text, u."Username"
		 FROM access_tokens t
		 JOIN "SessionUsers" u ON u."Id" = t.user_id AND NOT u."Disabled"
		 WHERE t.token_sha256 = $1
		   AND t.revoked_at IS NULL
		   AND (t.expires_at IS NULL OR t.expires_at > now())`,
		hashToken(secret)).Scan(&tokenID, &user.ID, &user.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errors.New("unknown or expired token")
	}
	if err != nil {
		return User{}, fmt.Errorf("look up token: %w", err)
	}

	// Recording use is for hygiene — spotting a token nobody needs any more —
	// and must never fail the request it describes. Written at most once a
	// minute so a busy client does not turn every call into a write.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		a.pool.Exec(ctx,
			`UPDATE access_tokens SET last_used_at = now()
			 WHERE id = $1::uuid AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`,
			tokenID)
	}()
	return user, nil
}

// BearerToken pulls a token out of an Authorization header.
func BearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

// RequireToken authenticates by bearer token and puts the account in the
// context, in the same shape session validation uses — so everything
// downstream, including the membership checks, cannot tell the two apart.
//
// There is deliberately no XSRF check here. XSRF defends against a browser
// attaching a credential automatically; a token is only ever sent by a client
// that was told to send it, so there is nothing ambient to forge.
func (a *PasswordAuth) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := BearerToken(r)
		if secret == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="canvas"`)
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		user, err := a.UserByToken(r.Context(), secret)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="canvas"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		id, err := ccc.UUIDFromString(user.ID)
		if err != nil {
			http.Error(w, "invalid account", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), sessioninfo.CtxUserInfo, &sessioninfo.UserInfo{
			ID:       id,
			Username: user.Username,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
