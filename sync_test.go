package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

// testRelay starts the server and returns a dialer for one of its rooms.
func testRelay(t *testing.T, store Store) func(t *testing.T, room string) *websocket.Conn {
	t.Helper()
	return testRelayWithAuth(t, store, stubAuth{valid: true})
}

func testRelayWithAuth(t *testing.T, store Store, auth authenticator) func(t *testing.T, room string) *websocket.Conn {
	t.Helper()
	handler, err := newHandler(fstest.MapFS{"index.html": {Data: []byte("<div id=\"root\"></div>")}}, newHub(store), nil, auth)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/sync/"

	return func(t *testing.T, room string) *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, url+room, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", room, err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
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
	r := &reader{buf: read(t, conn)}
	messageType, err := r.varUint()
	if err != nil || messageType != messageSync {
		t.Fatalf("message type = %d, %v; want sync", messageType, err)
	}
	got, err := r.varUint()
	if err != nil || got != subType {
		t.Fatalf("sync sub-type = %d, %v; want %d", got, err, subType)
	}
	payload, err := r.varBytes()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload
}

// A joining client is asked for its state and, after asking for the
// document, is told the room is empty so that it flips to synced.
func TestHandshakeInEmptyRoom(t *testing.T) {
	dial := testRelay(t, NewMemoryStore())
	conn := dial(t, "empty")

	if payload := expectSync(t, conn, syncStep1); !bytes.Equal(payload, emptyStateVector) {
		t.Errorf("server state vector = % x, want % x", payload, emptyStateVector)
	}
	write(t, conn, syncFrame(syncStep1, emptyStateVector))
	if payload := expectSync(t, conn, syncStep2); !bytes.Equal(payload, emptyUpdate) {
		t.Errorf("step 2 payload = % x, want % x", payload, emptyUpdate)
	}
}

func TestUpdateReachesOtherClients(t *testing.T) {
	dial := testRelay(t, NewMemoryStore())
	sender, receiver, bystander := dial(t, "shared"), dial(t, "shared"), dial(t, "other")
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
		t.Errorf("other room saw % x", payload)
	}
}

// A client answers the server's step 1 with a step 2 carrying state it
// already had. That state must be logged, not just relayed.
func TestStep2FromClientIsPersisted(t *testing.T) {
	store := NewMemoryStore()
	dial := testRelay(t, store)
	first := dial(t, "restored")
	expectSync(t, first, syncStep1)

	existing := []byte{0x0a, 0x0b}
	write(t, first, syncFrame(syncStep2, existing))
	// Round-trip through the relay to be sure the frame was processed.
	write(t, first, syncFrame(syncStep1, emptyStateVector))
	expectSync(t, first, syncStep2)

	updates, err := store.Load(context.Background(), "restored")
	if err != nil || len(updates) != 1 || !bytes.Equal(updates[0], existing) {
		t.Fatalf("stored %v, %v; want one copy of % x", updates, err, existing)
	}
}

func TestAwarenessIsRelayedButNotStored(t *testing.T) {
	store := NewMemoryStore()
	dial := testRelay(t, store)
	sender, receiver := dial(t, "presence"), dial(t, "presence")
	expectSync(t, sender, syncStep1)
	expectSync(t, receiver, syncStep1)

	awareness := appendVarBytes(appendVarUint(nil, messageAwareness), []byte{0x07})
	write(t, sender, awareness)
	if frame := read(t, receiver); !bytes.Equal(frame, awareness) {
		t.Errorf("relayed % x, want % x", frame, awareness)
	}
	if updates, err := store.Load(context.Background(), "presence"); err != nil || len(updates) != 0 {
		t.Errorf("awareness was stored: %v, %v", updates, err)
	}
}

func TestInvalidRoomName(t *testing.T) {
	handler, err := newHandler(fstest.MapFS{"index.html": {Data: []byte("<div id=\"root\"></div>")}}, newHub(NewMemoryStore()), nil, stubAuth{valid: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/sync/not%20valid", nil)
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
	dial := testRelayWithAuth(t, NewMemoryStore(), stubAuth{valid: false})
	conn := dial(t, "private")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != statusUnauthenticated {
		t.Fatalf("close status = %d (%v), want %d", got, err, statusUnauthenticated)
	}
}

// A valid session still gets the normal handshake.
func TestAuthenticatedSocketHandshakes(t *testing.T) {
	dial := testRelayWithAuth(t, NewMemoryStore(), stubAuth{valid: true})
	conn := dial(t, "private")
	if payload := expectSync(t, conn, syncStep1); !bytes.Equal(payload, emptyStateVector) {
		t.Errorf("state vector = % x, want % x", payload, emptyStateVector)
	}
}
