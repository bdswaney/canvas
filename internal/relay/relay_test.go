package relay

import (
	"bytes"
	"context"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/lib0"
	"github.com/bdswaney/canvas/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// testRelay starts the server and hands out sockets for documents created on
// demand, so each test names its documents rather than juggling ids.
type testRelay struct {
	store store.Store
	url   string
	ids   map[string]string
}

func newTestRelay(t *testing.T, st store.Store) *testRelay {
	t.Helper()
	return newTestRelayWithAuth(t, st, authtest.Stub{Valid: true})
}

func newTestRelayWithAuth(t *testing.T, st store.Store, authn auth.Authenticator) *testRelay {
	t.Helper()
	router := chi.NewRouter()
	router.Get("/api/sync/doc/{docID}", NewHub(st).Handler(nil, authn))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &testRelay{
		store: st,
		url:   "ws" + strings.TrimPrefix(server.URL, "http") + "/api/sync/doc/",
		ids:   map[string]string{},
	}
}

// docID returns the id of a document with this name, creating it once.
func (r *testRelay) docID(t *testing.T, name string) string {
	t.Helper()
	if id, ok := r.ids[name]; ok {
		return id
	}
	doc, err := r.store.CreateDoc(context.Background(), store.DefaultProjectID, name)
	if err != nil {
		t.Fatal(err)
	}
	r.ids[name] = doc.ID
	return doc.ID
}

func (r *testRelay) dial(t *testing.T, name string) *websocket.Conn {
	t.Helper()
	return r.dialID(t, r.docID(t, name))
}

func (r *testRelay) dialID(t *testing.T, id string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, r.url+id, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", id, err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func read(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, frame, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return frame
}

func write(t *testing.T, conn *websocket.Conn, frame []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// expectSync asserts that the next frame is a sync message of subType and
// returns its payload.
func expectSync(t *testing.T, conn *websocket.Conn, subType uint64) []byte {
	t.Helper()
	r := lib0.NewReader(read(t, conn))
	messageType, err := r.VarUint()
	if err != nil || messageType != MessageSync {
		t.Fatalf("message type = %d, %v; want sync", messageType, err)
	}
	got, err := r.VarUint()
	if err != nil || got != subType {
		t.Fatalf("sync sub-type = %d, %v; want %d", got, err, subType)
	}
	payload, err := r.VarBytes()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload
}

// A joining client is asked for its state and, after asking for the
// document, is told there is nothing stored so that it flips to synced.
func TestHandshakeInEmptyRoom(t *testing.T) {
	relay := newTestRelay(t, newTestStore(t))
	conn := relay.dial(t, "empty")

	if payload := expectSync(t, conn, syncStep1); !bytes.Equal(payload, emptyStateVector) {
		t.Errorf("server state vector = % x, want % x", payload, emptyStateVector)
	}
	write(t, conn, syncFrame(syncStep1, emptyStateVector))
	if payload := expectSync(t, conn, syncStep2); !bytes.Equal(payload, emptyUpdate) {
		t.Errorf("step 2 payload = % x, want % x", payload, emptyUpdate)
	}
}

func TestUpdateReachesOtherClients(t *testing.T) {
	relay := newTestRelay(t, newTestStore(t))
	sender, receiver, bystander := relay.dial(t, "shared"), relay.dial(t, "shared"), relay.dial(t, "other")
	for _, conn := range []*websocket.Conn{sender, receiver, bystander} {
		expectSync(t, conn, syncStep1)
	}

	update := []byte{0x01, 0x02, 0x03}
	write(t, sender, syncFrame(syncUpdate, update))

	if payload := expectSync(t, receiver, syncUpdate); !bytes.Equal(payload, update) {
		t.Errorf("relayed update = % x, want % x", payload, update)
	}
	// The sender must not receive its own update back.
	write(t, sender, syncFrame(syncStep1, emptyStateVector))
	if payload := expectSync(t, sender, syncStep2); !bytes.Equal(payload, update) {
		t.Errorf("replayed update = % x, want % x", payload, update)
	}
	// Rooms are isolated: the bystander only ever sees its own handshake.
	write(t, bystander, syncFrame(syncStep1, emptyStateVector))
	if payload := expectSync(t, bystander, syncStep2); !bytes.Equal(payload, emptyUpdate) {
		t.Errorf("other document saw % x", payload)
	}
}

// A client answers the server's step 1 with a step 2 carrying state it
// already had. That state must be logged, not just relayed.
func TestStep2FromClientIsPersisted(t *testing.T) {
	st := newTestStore(t)
	relay := newTestRelay(t, st)
	first := relay.dial(t, "restored")
	expectSync(t, first, syncStep1)

	existing := []byte{0x0a, 0x0b}
	write(t, first, syncFrame(syncStep2, existing))
	// Round-trip through the relay to be sure the frame was processed.
	write(t, first, syncFrame(syncStep1, emptyStateVector))
	expectSync(t, first, syncStep2)

	updates, err := st.Load(context.Background(), relay.docID(t, "restored"))
	if err != nil || len(updates) != 1 || !bytes.Equal(updates[0], existing) {
		t.Fatalf("stored %v, %v; want one copy of % x", updates, err, existing)
	}
}

func TestAwarenessIsRelayedButNotStored(t *testing.T) {
	st := newTestStore(t)
	relay := newTestRelay(t, st)
	sender, receiver := relay.dial(t, "presence"), relay.dial(t, "presence")
	expectSync(t, sender, syncStep1)
	expectSync(t, receiver, syncStep1)

	awareness := lib0.AppendVarBytes(lib0.AppendVarUint(nil, messageAwareness), []byte{0x07})
	write(t, sender, awareness)
	if frame := read(t, receiver); !bytes.Equal(frame, awareness) {
		t.Errorf("relayed % x, want % x", frame, awareness)
	}
	if updates, err := st.Load(context.Background(), relay.docID(t, "presence")); err != nil || len(updates) != 0 {
		t.Errorf("awareness was stored: %v, %v", updates, err)
	}
}

func TestInvalidDocID(t *testing.T) {
	router := chi.NewRouter()
	router.Get("/api/sync/doc/{docID}", NewHub(newTestStore(t)).Handler(nil, authtest.Stub{Valid: true}))
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/sync/doc/not%20valid", nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("expected the dial to be rejected")
	}
	if resp == nil || resp.StatusCode != 400 {
		t.Fatalf("status = %v, want 400", resp)
	}
}

// An expired session must close the socket with a code y-websocket treats as
// permanent, rather than leaving the client to reconnect forever.
func TestUnauthenticatedSocketIsClosedPermanently(t *testing.T) {
	relay := newTestRelayWithAuth(t, newTestStore(t), authtest.Stub{Valid: false})
	conn := relay.dialID(t, "00000000-0000-4000-8000-000000000099")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != StatusUnauthenticated {
		t.Fatalf("close status = %d (%v), want %d", got, err, StatusUnauthenticated)
	}
}

// A valid session still gets the normal handshake.
func TestAuthenticatedSocketHandshakes(t *testing.T) {
	relay := newTestRelayWithAuth(t, newTestStore(t), authtest.Stub{Valid: true})
	conn := relay.dial(t, "private")
	if payload := expectSync(t, conn, syncStep1); !bytes.Equal(payload, emptyStateVector) {
		t.Errorf("state vector = % x, want % x", payload, emptyStateVector)
	}
}

// A socket for a document that does not exist is closed permanently rather
// than quietly creating a session whose journal nothing can read.
func TestUnknownDocumentIsClosedPermanently(t *testing.T) {
	relay := newTestRelay(t, newTestStore(t))
	conn := relay.dialID(t, "00000000-0000-4000-8000-0000000000ff")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != StatusUnknownDoc {
		t.Fatalf("close status = %d (%v), want %d", websocket.CloseStatus(err), err, StatusUnknownDoc)
	}
}

// The collaboration socket bypasses every REST handler, so it carries the
// membership check itself and closes with a code the client treats as final.
func TestNonMemberSocketIsClosed(t *testing.T) {
	st := newTestStore(t)
	relay := newTestRelayWithAuth(t, st, authtest.Stub{Valid: true, UserID: authtest.OutsiderID})
	doc, err := st.CreateDoc(context.Background(), store.DefaultProjectID, "Notes")
	if err != nil {
		t.Fatal(err)
	}

	conn := relay.dialID(t, doc.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != StatusNotAMember {
		t.Fatalf("close status = %d (%v), want %d", websocket.CloseStatus(err), err, StatusNotAMember)
	}
}
