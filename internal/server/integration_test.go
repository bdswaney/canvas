package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/lib0"
	"github.com/bdswaney/canvas/internal/migrate"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/store"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

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

	authn, err := auth.NewPasswordAuth(st.Pool(), "")
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
		relay.NewHub(st, nil), nil, authn,
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
