package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/lib0"
	mcpserver "github.com/bdswaney/canvas/internal/mcp"
	"github.com/bdswaney/canvas/internal/migrate"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type staticOAuthHandler struct{ token string }

func (h staticOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: h.token}), nil
}

func (staticOAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return nil
}

// TestRealSessionReachesTheSocket runs the whole path with the actual session
// package rather than the stub: log in, then open a collaboration socket.
//
// It exists because the stub hid a real bug. stubAuth.UserFromCtx returns a
// user whatever the context holds, so every membership check passed in tests
// while the running server refused every socket — the package's API form of
// ValidateSession validates the session but, unlike its middleware, does not
// attach the account. Nothing that mocks the auth.Authenticator can catch that.
func TestRealSessionReachesTheSocket(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	if err := migrate.Run(url); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	authn, err := auth.NewPasswordAuth(st.Pool(), "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	username := fmt.Sprintf("socket-%d", stamp)
	if err := authn.CreateUser(ctx, username, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authn.DeleteUser(context.Background(), username) })

	users, err := authn.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, user := range users {
		if user.Username == username {
			userID = user.ID
		}
	}
	if userID == "" {
		t.Fatal("the account just created is not in the user list")
	}

	project, err := st.CreateProject(ctx, fmt.Sprintf("socket-%d", stamp), userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Pool().Exec(context.Background(), "DELETE FROM projects WHERE id = $1::uuid", project.ID)
	})
	doc, err := st.CreateDoc(ctx, project.ID, "Notes")
	if err != nil {
		t.Fatal(err)
	}

	handler, err := New(
		fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		relay.NewHub(st, nil), nil, authn, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	// GET first, the way the app does, so the XSRF cookie exists before a write.
	primed, err := client.Get(server.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	primed.Body.Close()

	var xsrf string
	for _, cookie := range jar.Cookies(primed.Request.URL) {
		if cookie.Name == "XSRF-TOKEN" {
			xsrf = cookie.Value
		}
	}
	if xsrf == "" {
		t.Fatal("no XSRF cookie was issued")
	}

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, "correct horse battery staple")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server.URL+"/api/session", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-XSRF-TOKEN", xsrf)
	login, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK && login.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d", login.StatusCode)
	}

	// The REST path and the socket path resolve the user differently, so both
	// are checked: a green REST call is not evidence the socket works.
	whoami, err := client.Get(server.URL + "/api/docs/" + doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer whoami.Body.Close()
	if whoami.StatusCode != http.StatusOK {
		t.Fatalf("reading the doc over REST = %d", whoami.StatusCode)
	}
	var got store.Doc
	if err := json.NewDecoder(whoami.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != doc.ID {
		t.Fatalf("doc = %s, want %s", got.ID, doc.ID)
	}

	socket := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/sync/doc/" + doc.ID
	conn, _, err := websocket.Dial(ctx, socket, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Origin": {server.URL}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// A member's socket is served, not closed. Before the fix this arrived as
	// close code 4405.
	read, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	kind, frame, err := conn.Read(read)
	if err != nil {
		t.Fatalf("read = %v (close status %d); a member's socket must be served",
			err, websocket.CloseStatus(err))
	}
	if kind != websocket.MessageBinary {
		t.Fatalf("first frame is %v, want binary", kind)
	}
	r := lib0.NewReader(frame)
	if messageType, err := r.VarUint(); err != nil || messageType != relay.MessageSync {
		t.Fatalf("first frame type = %d, %v; want sync", messageType, err)
	}
}

// TestRealMCPEditIsRevokedByRESTMemberRemoval uses the production session,
// PostgreSQL store, REST router, MCP bearer transport, and one Hub. The MCP
// session is authenticated and reads the document before REST removes its
// account, then its already-open edit must fail without a journal row while
// the member's browser socket is physically closed asynchronously.
func TestRealMCPEditIsRevokedByRESTMemberRemoval(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	if err := migrate.Run(url); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	st, err := store.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	authn, err := auth.NewPasswordAuth(st.Pool(), "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	ownerName := fmt.Sprintf("revoke-owner-%d", stamp)
	memberName := fmt.Sprintf("revoke-member-%d", stamp)
	password := "correct horse battery staple"
	if err := authn.CreateUser(ctx, ownerName, password); err != nil {
		t.Fatal(err)
	}
	if err := authn.CreateUser(ctx, memberName, password); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = authn.DeleteUser(context.Background(), ownerName)
		_ = authn.DeleteUser(context.Background(), memberName)
	})

	owner, err := authn.UserByUsername(ctx, ownerName)
	if err != nil {
		t.Fatal(err)
	}
	member, err := authn.UserByUsername(ctx, memberName)
	if err != nil {
		t.Fatal(err)
	}
	project, err := st.CreateProject(ctx, fmt.Sprintf("revoke-project-%d", stamp), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddProjectMember(ctx, project.ID, member.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	doc, err := st.CreateDoc(ctx, project.ID, "revocation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), "DELETE FROM projects WHERE id = $1::uuid", project.ID)
	})

	engine, err := ydoc.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	hub := relay.NewHub(st, engine)
	mcpHandler := authn.RequireToken(mcpsdk.NewStreamableHTTPHandler(
		func(r *http.Request) *mcpsdk.Server {
			user, _ := authn.UserFromCtx(r.Context())
			return mcpserver.New(st, engine, hub, user)
		}, nil))
	handler, err := New(
		fstest.MapFS{"index.html": {Data: []byte(`<div id="root"></div>`)}},
		hub, nil, authn, mcpHandler,
	)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	login := func(username string) (*http.Client, string) {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar}
		primed, err := client.Get(httpServer.URL + "/api/session")
		if err != nil {
			t.Fatal(err)
		}
		primed.Body.Close()
		var xsrf string
		for _, cookie := range jar.Cookies(primed.Request.URL) {
			if cookie.Name == "XSRF-TOKEN" {
				xsrf = cookie.Value
			}
		}
		if xsrf == "" {
			t.Fatal("no XSRF cookie was issued")
		}
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			httpServer.URL+"/api/session", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-XSRF-TOKEN", xsrf)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
			t.Fatalf("login %s = %d", username, response.StatusCode)
		}
		for _, cookie := range jar.Cookies(response.Request.URL) {
			if cookie.Name == "XSRF-TOKEN" {
				xsrf = cookie.Value
			}
		}
		if xsrf == "" {
			t.Fatal("login did not retain an XSRF cookie")
		}
		return client, xsrf
	}

	ownerClient, ownerXSRF := login(ownerName)
	memberClient, _ := login(memberName)
	dial := func(client *http.Client) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx,
			"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sync/doc/"+doc.ID,
			&websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Origin": {httpServer.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		readCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		kind, _, err := conn.Read(readCtx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("socket handshake = %v, %v", kind, err)
		}
		return conn
	}
	memberSocket := dial(memberClient)
	dial(ownerClient)

	mcpToken, _, err := authn.CreateToken(ctx, memberName, "revocation-e2e", 0)
	if err != nil {
		t.Fatal(err)
	}
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "revocation-test", Version: "1"}, nil)
	mcpSession, err := mcpClient.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/api/mcp",
		OAuthHandler:         staticOAuthHandler{token: mcpToken},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mcpSession.Close() })
	if result, err := mcpSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "read_document", Arguments: map[string]any{"documentId": doc.ID},
	}); err != nil || result.IsError {
		t.Fatalf("authenticated MCP read = %v, %v", result, err)
	}

	removeRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		httpServer.URL+"/api/projects/"+project.ID+"/members/"+member.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	removeRequest.Header.Set("X-XSRF-TOKEN", ownerXSRF)
	removed, err := ownerClient.Do(removeRequest)
	if err != nil {
		t.Fatal(err)
	}
	removed.Body.Close()
	if removed.StatusCode != http.StatusNoContent {
		t.Fatalf("REST member removal = %d", removed.StatusCode)
	}
	memberDocs, err := memberClient.Get(httpServer.URL + "/api/docs")
	if err != nil {
		t.Fatal(err)
	}
	memberDocs.Body.Close()
	if memberDocs.StatusCode != http.StatusOK {
		t.Fatalf("removed member's REST document list = %d, want 200 with no docs", memberDocs.StatusCode)
	}

	edit, err := mcpSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "edit_document", Arguments: map[string]any{
			"documentId": doc.ID, "text": "must not be journalled",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !edit.IsError {
		t.Fatalf("already-authenticated MCP edit after removal was accepted: %#v", edit)
	}
	updates, err := st.Load(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Fatalf("revoked MCP edit appended %d updates", len(updates))
	}

	closeCtx, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	_, _, closeErr := memberSocket.Read(closeCtx)
	if got := websocket.CloseStatus(closeErr); got != relay.StatusNotAMember {
		t.Fatalf("revoked member socket close = %d (%v), want %d", got, closeErr, relay.StatusNotAMember)
	}
}
