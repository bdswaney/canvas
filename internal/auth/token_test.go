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

	auth, err := NewPasswordAuth(pool, "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
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

// TestStatelessMCPRequestsAuthenticateIndividually verifies that each request
// is authenticated and receives its own account context. There is no session
// ID to bind or hijack.
func TestStatelessMCPRequestsAuthenticateIndividually(t *testing.T) {
	const (
		accountA = "00000000-0000-4000-8000-0000000000a1"
		accountB = "00000000-0000-4000-8000-0000000000b2"
	)
	var calls atomic.Int32
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(mcpServer, &mcpsdk.Tool{Name: "probe", Description: "record a tool call"},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			calls.Add(1)
			info := mcpauth.TokenInfoFromContext(ctx)
			id := "missing-account"
			if info != nil {
				id = info.UserID
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: id},
			}}, nil, nil
		})
	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return mcpServer
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: 1 << 20})
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

	request := func(method, token, body string) (*http.Response, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, httpServer.URL, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json, text/event-stream")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		contents, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, contents
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	callTool := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{}}}`
	for _, tt := range []struct{ token, account string }{
		{token: "account-a", account: accountA},
		{token: "account-b", account: accountB},
	} {
		response, body := request(http.MethodPost, tt.token, initialize)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("initialize %s = %d: %s", tt.token, response.StatusCode, body)
		}
		if got := response.Header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("stateless initialize emitted session ID %q", got)
		}
		response, body = request(http.MethodPost, tt.token, callTool)
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), tt.account) {
			t.Fatalf("tool request %s = %d: %s", tt.token, response.StatusCode, body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response, _ := request(method, "account-a", "")
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("stateless %s = %d, want 405", method, response.StatusCode)
		}
	}
	response, _ := request(http.MethodPost, "unknown", callTool)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", response.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("tool calls after rejected requests = %d, want 2", got)
	}
}

// This integration test uses PasswordAuth.RequireToken itself, rather than
// only the synthetic verifier above, so SQL revocation and expiry checks are
// exercised on every stateless HTTP request.
func TestPasswordAuthAuthenticatesStatelessMCPRequests(t *testing.T) {
	authn, username := tokenFixture(t)
	ctx := t.Context()
	otherUsername := fmt.Sprintf("token-other-%d", time.Now().UnixNano())
	if err := authn.CreateUser(ctx, otherUsername, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authn.DeleteUser(context.Background(), otherUsername) })

	accountA, err := authn.UserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	accountB, err := authn.UserByUsername(ctx, otherUsername)
	if err != nil {
		t.Fatal(err)
	}
	tokenA, revokedToken, err := authn.CreateToken(ctx, username, "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	tokenA2, _, err := authn.CreateToken(ctx, username, "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, _, err := authn.CreateToken(ctx, otherUsername, "other", 0)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	mcpsdk.AddTool(mcpServer, &mcpsdk.Tool{Name: "probe", Description: "record a tool call"},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			calls.Add(1)
			info := mcpauth.TokenInfoFromContext(ctx)
			id := "missing-account"
			if info != nil {
				id = info.UserID
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: id},
			}}, nil, nil
		})
	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return mcpServer
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: 1 << 20})
	httpServer := httptest.NewServer(authn.RequireToken(streamable))
	t.Cleanup(httpServer.Close)

	request := func(method, token, body string) (*http.Response, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, httpServer.URL, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json, text/event-stream")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		contents, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, contents
	}

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	callTool := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{}}}`
	response, body := request(http.MethodPost, tokenA, initialize)
	if response.StatusCode != http.StatusOK || response.Header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("stateless account A initialize = %d, session=%q: %s", response.StatusCode, response.Header.Get("Mcp-Session-Id"), body)
	}
	response, body = request(http.MethodPost, tokenA2, callTool)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), accountA.ID) {
		t.Fatalf("account A request = %d: %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, tokenB, callTool)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), accountB.ID) {
		t.Fatalf("account B request = %d: %s", response.StatusCode, body)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response, _ = request(method, tokenA2, "")
		if response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("stateless %s = %d, want 405", method, response.StatusCode)
		}
	}

	if err := authn.RevokeToken(ctx, revokedToken.ID); err != nil {
		t.Fatal(err)
	}
	response, _ = request(http.MethodPost, tokenA, initialize)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d, want 401", response.StatusCode)
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
	response, _ = request(http.MethodPost, expired, initialize)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token = %d, want 401", response.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("tool calls after rejected requests = %d, want 2", got)
	}
}
