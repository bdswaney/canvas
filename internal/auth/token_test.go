package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/bdswaney/canvas/internal/migrate"
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
	// Migrate here rather than rely on another package's tests having done it:
	// go test runs packages in parallel, so on a fresh database this package
	// can start before anything has created the schema.
	if err := migrate.Run(url); err != nil {
		t.Fatal(err)
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

// recordingTransport keeps the session id assigned to account A so the test
// can deliberately present it with account B's otherwise valid token.
type recordingTransport struct {
	base http.RoundTripper
	last atomic.Value
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if id := r.Header.Get("Mcp-Session-Id"); id != "" {
		t.last.Store(id)
	}
	return t.base.RoundTrip(r)
}

type staticOAuthHandler struct{ token string }

func (h staticOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: h.token}), nil
}

func (staticOAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return nil
}

func TestStatefulMCPBindsSessionsToBearerUsers(t *testing.T) {
	const (
		accountA = "00000000-0000-4000-8000-0000000000a1"
		accountB = "00000000-0000-4000-8000-0000000000b2"
	)
	var calls atomic.Int32
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(mcpServer, &mcpsdk.Tool{Name: "probe", Description: "record a tool call"},
		func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			calls.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "ok"},
			}}, nil, nil
		})

	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return mcpServer
	}, nil)
	verifier := func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		var id string
		switch token {
		case "account-a":
			id = accountA
		case "account-b":
			id = accountB
		default:
			return nil, fmt.Errorf("%w: unknown test token", mcpauth.ErrInvalidToken)
		}
		return &mcpauth.TokenInfo{UserID: id}, nil
	}
	httpServer := httptest.NewServer(requireBearerToken(verifier, streamable))
	t.Cleanup(httpServer.Close)

	newSession := func(token string, transport *recordingTransport) *mcpsdk.ClientSession {
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1"}, nil)
		cs, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
			Endpoint:             httpServer.URL,
			HTTPClient:           &http.Client{Transport: transport},
			OAuthHandler:         staticOAuthHandler{token: token},
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
		}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", token, err)
		}
		return cs
	}

	aTransport := &recordingTransport{base: http.DefaultTransport}
	aSession := newSession("account-a", aTransport)
	t.Cleanup(func() { aSession.Close() })
	if _, err := aSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "probe"}); err != nil {
		t.Fatalf("account A's own session: %v", err)
	}

	bSession := newSession("account-b", &recordingTransport{base: http.DefaultTransport})
	t.Cleanup(func() { bSession.Close() })
	if _, err := bSession.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "probe"}); err != nil {
		t.Fatalf("account B's own session: %v", err)
	}

	sessionID, ok := aTransport.last.Load().(string)
	if !ok || sessionID == "" {
		t.Fatal("account A's session id was not observed")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, httpServer.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"probe","arguments":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer account-b")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Mcp-Protocol-Version", "2025-03-26")
	request.Header.Set("Mcp-Session-Id", sessionID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("account B using account A's session = %d, want %d (%s; read error %v)", response.StatusCode, http.StatusForbidden, body, readErr)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("tool calls = %d, want 2; mismatched session must be rejected before execution", got)
	}
}

// This integration test uses PasswordAuth.RequireToken itself, rather than
// only the synthetic verifier above, so SQL revocation and expiry checks are
// exercised on stateful HTTP MCP requests too.
func TestPasswordAuthBindsStatefulMCPSessions(t *testing.T) {
	authn, username := tokenFixture(t)
	ctx := t.Context()
	otherUsername := fmt.Sprintf("token-other-%d", time.Now().UnixNano())
	if err := authn.CreateUser(ctx, otherUsername, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authn.DeleteUser(context.Background(), otherUsername) })

	accountA1, a1Token, err := authn.CreateToken(ctx, username, "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	accountA2, _, err := authn.CreateToken(ctx, username, "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	accountB, _, err := authn.CreateToken(ctx, otherUsername, "other", 0)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(mcpServer, &mcpsdk.Tool{Name: "probe", Description: "record a tool call"},
		func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			calls.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "ok"},
			}}, nil, nil
		})
	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return mcpServer
	}, nil)
	httpServer := httptest.NewServer(authn.RequireToken(streamable))
	t.Cleanup(httpServer.Close)

	newSession := func(token string, transport *recordingTransport) *mcpsdk.ClientSession {
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1"}, nil)
		cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
			Endpoint:             httpServer.URL,
			HTTPClient:           &http.Client{Transport: transport},
			OAuthHandler:         staticOAuthHandler{token: token},
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
		}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", token, err)
		}
		return cs
	}

	aTransport := &recordingTransport{base: http.DefaultTransport}
	aSession := newSession(accountA1, aTransport)
	t.Cleanup(func() { aSession.Close() })
	if _, err := aSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "probe"}); err != nil {
		t.Fatalf("account A's own session: %v", err)
	}

	bSession := newSession(accountB, &recordingTransport{base: http.DefaultTransport})
	t.Cleanup(func() { bSession.Close() })
	if _, err := bSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "probe"}); err != nil {
		t.Fatalf("account B's own session: %v", err)
	}

	sessionID, ok := aTransport.last.Load().(string)
	if !ok || sessionID == "" {
		t.Fatal("account A's session id was not observed")
	}
	request := func(method, token string) int {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"probe","arguments":{}}}`)
		}
		req, err := http.NewRequestWithContext(ctx, method, httpServer.URL, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Protocol-Version", "2025-03-26")
		req.Header.Set("Mcp-Session-Id", sessionID)
		if method == http.MethodGet {
			req.Header.Set("Accept", "text/event-stream")
		} else {
			req.Header.Set("Accept", "application/json, text/event-stream")
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}

	if got := request(http.MethodPost, accountA2); got != http.StatusOK {
		t.Fatalf("second valid token for account A = %d, want %d", got, http.StatusOK)
	}
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if got := request(method, accountB); got != http.StatusForbidden {
			t.Fatalf("account B using account A's session with %s = %d, want %d", method, got, http.StatusForbidden)
		}
	}
	if err := authn.RevokeToken(ctx, a1Token.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, accountA1); got != http.StatusUnauthorized {
		t.Fatalf("revoked account A token = %d, want %d", got, http.StatusUnauthorized)
	}

	expired, expiredToken, err := authn.CreateToken(ctx, username, "expired", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authn.pool.Exec(ctx,
		"UPDATE access_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid",
		expiredToken.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, expired); got != http.StatusUnauthorized {
		t.Fatalf("expired account A token = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("tool calls = %d, want 3; rejected session reuse must not execute tools", got)
	}
}
