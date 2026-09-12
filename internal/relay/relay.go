// Package relay is the Yjs WebSocket relay. It never interprets document
// contents: every update is appended to a journal and replayed to joiners,
// and the hub only routes bytes between the clients of one document.
package relay

import (
	"context"
	"errors"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/lib0"
	"github.com/bdswaney/canvas/internal/store"
	"log"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Top-level message types from the y-websocket protocol.
const (
	// MessageSync is exported so a test outside this package can recognise
	// the first frame a served socket sends.
	MessageSync           = 0
	messageAwareness      = 1
	messageQueryAwareness = 3
)

// Sub-types of a MessageSync frame.
const (
	syncStep1  = 0
	syncStep2  = 1
	syncUpdate = 2
)

const (
	// y-websocket treats close codes in 4400-4499 as permanent and stops
	// reconnecting, which is what an expired session or a missing document
	// should mean.
	StatusUnauthenticated = 4401
	StatusUnknownDoc      = 4404
	StatusNotAMember      = 4405

	// A joining client replays the whole doc log, so allow generous frames.
	readLimit = 32 << 20
	// Slow clients are disconnected rather than allowed to stall a broadcast.
	sendBuffer = 64
)

// emptyStateVector asks a peer for everything it has: a client-clock map with
// no entries. emptyUpdate applies nothing and is used to mark a client synced.
var (
	emptyStateVector = []byte{0x00}
	emptyUpdate      = []byte{0x00, 0x00}
)

// idPattern is the shape of a document id arriving in a socket URL. It only
// has to be tight enough to keep a malformed value out of a query; the store
// decides what exists.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type client struct {
	conn      *websocket.Conn
	send      chan []byte
	userID    string
	projectID string
	room      *docSession

	// revoked is protected by room.mu. It is deliberately tied to the
	// authenticated account and project, never to a Yjs client id or an
	// awareness name.
	revoked bool
}

func (c *client) isRevoked() bool {
	c.room.mu.Lock()
	defer c.room.mu.Unlock()
	return c.revoked
}

// push queues a frame for the writer goroutine, waiting for room in the
// buffer rather than dropping frames this connection asked for.
func (c *client) push(ctx context.Context, frame []byte) error {
	// Queueing and revocation are one room-level linearization point. A
	// revoked client may still have frames queued from before revocation, but
	// it cannot acquire the room lock and queue anything after the mark.
	for {
		c.room.mu.Lock()
		if c.revoked {
			c.room.mu.Unlock()
			return errors.New("client access revoked")
		}
		select {
		case c.send <- frame:
			c.room.mu.Unlock()
			return nil
		default:
			c.room.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// docSession relays updates between the clients of one document and keeps
// the update log in memory, mirroring every append into the store. It lives
// only as long as somebody is connected; the durable log outlives it.
type docSession struct {
	docID string

	mu      sync.Mutex
	clients map[*client]struct{}
	updates [][]byte
	loaded  bool
}

// Hub owns the live document sessions. A session is dropped when its last
// client leaves; the durable log in the store outlives it.
type Hub struct {
	store  store.Store
	merger Merger

	mu       sync.Mutex
	sessions map[string]*docSession

	// access serializes membership and archive changes with frame handling.
	// A removal therefore has one clear point: frames already holding the
	// read lock finish before the store change and live-socket revocation;
	// later frames cannot append or broadcast as the removed account.
	access sync.RWMutex
}

// NewHub returns a Hub over the given store. Without a Merger it relays and
// journals exactly as before; with one it also compacts a document's journal
// once the last client leaves.
func NewHub(store store.Store, merger Merger) *Hub {
	return &Hub{store: store, merger: merger, sessions: map[string]*docSession{}}
}

func (h *Hub) session(docID string) *docSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.sessions[docID]
	if !ok {
		r = &docSession{docID: docID, clients: map[*client]struct{}{}}
		h.sessions[docID] = r
	}
	return r
}

func (h *Hub) join(docID string, c *client) *docSession {
	r := h.session(docID)
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	return r
}

func (h *Hub) leave(r *docSession, c *client) {
	r.mu.Lock()
	delete(r.clients, c)
	empty := len(r.clients) == 0
	r.mu.Unlock()
	if !empty {
		return
	}
	h.mu.Lock()
	// Another client may have joined while the session lock was released.
	dropped := false
	if current, ok := h.sessions[r.docID]; ok && current == r {
		r.mu.Lock()
		if len(r.clients) == 0 {
			delete(h.sessions, r.docID)
			dropped = true
		}
		r.mu.Unlock()
	}
	h.mu.Unlock()

	// With the session gone there is no cached journal to go stale, which is
	// what makes this the safe moment to compact.
	if dropped {
		h.compactAfter(r.docID)
	}
}

// history returns the session's updates, loading them from the store once.
func (h *Hub) history(ctx context.Context, r *docSession) ([][]byte, error) {
	r.mu.Lock()
	if r.loaded {
		updates := append([][]byte(nil), r.updates...)
		r.mu.Unlock()
		return updates, nil
	}
	r.mu.Unlock()

	stored, err := h.store.Load(ctx, r.docID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loaded {
		// Updates appended during the load are kept after the stored ones.
		r.updates = append(stored, r.updates...)
		r.loaded = true
	}
	return append([][]byte(nil), r.updates...), nil
}

func (h *Hub) append(ctx context.Context, r *docSession, update []byte) error {
	r.mu.Lock()
	r.updates = append(r.updates, update)
	r.mu.Unlock()
	return h.store.Append(ctx, r.docID, update)
}

// broadcast sends a frame to every client in the session except the sender.
// A client whose buffer is full is closed instead of blocking the session.
func (r *docSession) broadcast(sender *client, frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.clients {
		if c == sender || c.revoked {
			continue
		}
		select {
		case c.send <- frame:
		default:
			c.conn.Close(websocket.StatusPolicyViolation, "client too slow")
		}
	}
}

func syncFrame(subType uint64, payload []byte) []byte {
	return lib0.AppendVarBytes(lib0.AppendVarUint(lib0.AppendVarUint(nil, MessageSync), subType), payload)
}

// Store is the store the hub journals into, shared with the HTTP API.
func (h *Hub) Store() store.Store { return h.store }

// revokeClients marks matching live clients while the access write lock is
// held, then initiates physical socket closure asynchronously. The logical
// mark happens before Close so a reader racing with the close cannot append or
// enqueue another frame.
func (h *Hub) revokeClients(match func(*client) bool, status websocket.StatusCode, reason string) {
	var revoked []*client

	h.mu.Lock()
	for _, session := range h.sessions {
		session.mu.Lock()
		for c := range session.clients {
			if c.revoked || !match(c) {
				continue
			}
			c.revoked = true
			revoked = append(revoked, c)
		}
		session.mu.Unlock()
	}
	h.mu.Unlock()

	for _, c := range revoked {
		// Close, rather than CloseNow, gives y-websocket the permanent status
		// code. It can wait for the peer's WebSocket lock, so do not hold the
		// membership operation hostage to a client that stopped reading: the
		// room-level mark is already authoritative and the close is initiated
		// immediately in this goroutine.
		go func(c *client) {
			if err := c.conn.Close(status, reason); err != nil {
				c.conn.CloseNow()
			}
		}(c)
	}
}

// RemoveProjectMember changes durable membership and logically revokes that
// account's live document sockets as one in-process operation. Physical socket
// closure is initiated asynchronously before the caller returns. The access
// lock is the boundary between an update that was already in flight and one
// that must be refused; it is intentionally not a distributed invalidation
// mechanism.
func (h *Hub) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	h.access.Lock()
	defer h.access.Unlock()

	if err := h.store.RemoveProjectMember(ctx, projectID, userID); err != nil {
		return err
	}
	h.revokeClients(func(c *client) bool {
		return c.projectID == projectID && c.userID == userID
	}, StatusNotAMember, "not a member of this project")
	return nil
}

// ArchiveProject hides the project and logically revokes every socket serving
// one of its documents; physical closure is initiated asynchronously before
// the caller returns. New connections are rejected by the store's Doc lookup.
func (h *Hub) ArchiveProject(ctx context.Context, projectID string) error {
	h.access.Lock()
	defer h.access.Unlock()

	if err := h.store.ArchiveProject(ctx, projectID); err != nil {
		return err
	}
	h.revokeClients(func(c *client) bool { return c.projectID == projectID }, StatusUnknownDoc, "project archived")
	return nil
}

// ArchiveDoc hides a document and logically revokes its existing sockets;
// physical closure is initiated asynchronously before the caller returns.
// This preserves the durable journal and saved history while preventing a
// live socket from continuing to edit an archived row.
func (h *Hub) ArchiveDoc(ctx context.Context, docID string) error {
	h.access.Lock()
	defer h.access.Unlock()

	if err := h.store.ArchiveDoc(ctx, docID); err != nil {
		return err
	}
	h.revokeClients(func(c *client) bool {
		return c.room != nil && c.room.docID == docID
	}, StatusUnknownDoc, "document archived")
	return nil
}

// Handler serves the collaboration socket for one document, expected to be
// mounted at a route with a {docID} parameter.
func (h *Hub) Handler(originPatterns []string, authn auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "docID")
		if !idPattern.MatchString(name) {
			http.Error(w, "invalid document id", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns})
		if err != nil {
			// Accept has already written a response.
			log.Printf("sync %s: accept: %v", name, err)
			return
		}
		conn.SetReadLimit(readLimit)
		var joined *client
		defer func() {
			// A revoked client is being closed with a permanent status by
			// revokeClients. CloseNow here would race that frame and turn the
			// revocation into an abnormal close that y-websocket retries.
			if joined == nil || !joined.isRevoked() {
				conn.CloseNow()
			}
		}()

		// The session is checked after the upgrade rather than by middleware
		// so that an expired session closes the socket with a code the client
		// treats as final. A rejected handshake looks like a network failure,
		// and y-websocket would reconnect against it forever.
		ctx, err := authn.ValidateSessionCtx(r.Context())
		if err != nil {
			conn.Close(StatusUnauthenticated, "session expired")
			return
		}

		// Keep the in-process access read lock from the existence check through
		// joining. A concurrent archive/removal therefore cannot pass this
		// check and then become a live client after its write-side revocation.
		h.access.RLock()

		// A socket for a document that does not exist would otherwise create a
		// session out of thin air and journal updates nothing can ever read.
		doc, err := h.store.Doc(ctx, name)
		if err != nil {
			h.access.RUnlock()
			conn.Close(StatusUnknownDoc, "unknown document")
			return
		}

		// The real gate: a document is reachable only by members of its
		// project. This is the same rule the REST handlers apply, checked
		// here because a live socket bypasses them entirely.
		user, ok := authn.UserFromCtx(ctx)
		if !ok {
			h.access.RUnlock()
			conn.Close(StatusUnauthenticated, "session expired")
			return
		}
		switch member, err := h.store.ProjectMember(ctx, doc.ProjectID, user.ID); {
		case err != nil:
			h.access.RUnlock()
			log.Printf("sync %s: membership check: %v", name, err)
			conn.Close(websocket.StatusInternalError, "membership check failed")
			return
		case !member:
			h.access.RUnlock()
			conn.Close(StatusNotAMember, "not a member of this project")
			return
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		c := &client{
			conn: conn, send: make(chan []byte, sendBuffer),
			userID: user.ID, projectID: doc.ProjectID,
		}
		rm := h.join(name, c)
		c.room = rm
		joined = c
		h.access.RUnlock()
		defer h.leave(rm, c)

		go writeLoop(ctx, cancel, c, rm)

		// Ask the client for state it already holds; without this a client
		// with local history never pushes it to the server.
		if err := c.push(ctx, syncFrame(syncStep1, emptyStateVector)); err != nil {
			return
		}

		if err := h.readLoop(ctx, rm, c); err != nil {
			log.Printf("sync %s: %v", name, err)
		}
		if !c.isRevoked() {
			conn.Close(websocket.StatusNormalClosure, "")
		}
	}
}

func writeLoop(ctx context.Context, cancel context.CancelFunc, c *client, rm *docSession) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-c.send:
			// Serialize the final revoked check with the close mark. A frame
			// already being written may finish before revocation; no queued
			// frame starts after the write-side lock marks this client.
			rm.mu.Lock()
			if c.revoked {
				rm.mu.Unlock()
				return
			}
			writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(writeCtx, websocket.MessageBinary, frame)
			done()
			rm.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (h *Hub) readLoop(ctx context.Context, rm *docSession, c *client) error {
	for {
		kind, frame, err := c.conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != -1 || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if err := h.handleFrame(ctx, rm, c, frame); err != nil {
			return err
		}
	}
}

func (h *Hub) handleFrame(ctx context.Context, rm *docSession, c *client, frame []byte) error {
	// Membership/archive revocation takes the write side of this lock. Holding
	// its read side across journal append and broadcast gives every frame a
	// deterministic before-or-after relationship with removal.
	h.access.RLock()
	defer h.access.RUnlock()

	rm.mu.Lock()
	revoked := c.revoked
	rm.mu.Unlock()
	if revoked {
		return nil
	}

	reader := lib0.NewReader(frame)
	messageType, err := reader.VarUint()
	if err != nil {
		return nil // Ignore frames this server does not understand.
	}

	switch messageType {
	case messageAwareness, messageQueryAwareness:
		// Awareness is ephemeral presence state: relay it, never store it.
		rm.broadcast(c, frame)
		return nil
	case MessageSync:
	default:
		return nil
	}

	subType, err := reader.VarUint()
	if err != nil {
		return nil
	}
	payload, err := reader.VarBytes()
	if err != nil {
		return nil
	}

	if subType == syncStep1 {
		// The client asked for the document. Replay the log, then send an
		// empty step 2 so the client flips to synced even when nothing is stored.
		updates, err := h.history(ctx, rm)
		if err != nil {
			return err
		}
		for _, update := range updates {
			if err := c.push(ctx, syncFrame(syncStep2, update)); err != nil {
				return err
			}
		}
		return c.push(ctx, syncFrame(syncStep2, emptyUpdate))
	}

	if subType != syncStep2 && subType != syncUpdate {
		return nil
	}
	// A reply to our step 1 arrives as step 2 and carries real document
	// state, so it is logged exactly like a live update.
	update := append([]byte(nil), payload...)
	if err := h.append(ctx, rm, update); err != nil {
		return err
	}
	rm.broadcast(c, syncFrame(syncUpdate, update))
	return nil
}
