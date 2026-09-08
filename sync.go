package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Top-level message types from the y-websocket protocol.
const (
	messageSync           = 0
	messageAwareness      = 1
	messageQueryAwareness = 3
)

// Sub-types of a messageSync frame.
const (
	syncStep1  = 0
	syncStep2  = 1
	syncUpdate = 2
)

const (
	// A joining client replays the whole room log, so allow generous frames.
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

var roomNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type client struct {
	conn *websocket.Conn
	send chan []byte
}

// push queues a frame for the writer goroutine, waiting for room in the
// buffer rather than dropping frames this connection asked for.
func (c *client) push(ctx context.Context, frame []byte) error {
	select {
	case c.send <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// room relays updates between the clients of one document and keeps the
// update log in memory, mirroring every append into the store.
type room struct {
	name string

	mu      sync.Mutex
	clients map[*client]struct{}
	updates [][]byte
	loaded  bool
}

// hub owns the live rooms. Rooms are dropped when their last client leaves;
// the durable log in the store outlives them.
type hub struct {
	store Store

	mu    sync.Mutex
	rooms map[string]*room
}

func newHub(store Store) *hub {
	return &hub{store: store, rooms: map[string]*room{}}
}

func (h *hub) room(name string) *room {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[name]
	if !ok {
		r = &room{name: name, clients: map[*client]struct{}{}}
		h.rooms[name] = r
	}
	return r
}

func (h *hub) join(name string, c *client) *room {
	r := h.room(name)
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.mu.Unlock()
	return r
}

func (h *hub) leave(r *room, c *client) {
	r.mu.Lock()
	delete(r.clients, c)
	empty := len(r.clients) == 0
	r.mu.Unlock()
	if !empty {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Another client may have joined while the room lock was released.
	if current, ok := h.rooms[r.name]; ok && current == r {
		r.mu.Lock()
		if len(r.clients) == 0 {
			delete(h.rooms, r.name)
		}
		r.mu.Unlock()
	}
}

// history returns the room's updates, loading them from the store once.
func (h *hub) history(ctx context.Context, r *room) ([][]byte, error) {
	r.mu.Lock()
	if r.loaded {
		updates := append([][]byte(nil), r.updates...)
		r.mu.Unlock()
		return updates, nil
	}
	r.mu.Unlock()

	stored, err := h.store.Load(ctx, r.name)
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

func (h *hub) append(ctx context.Context, r *room, update []byte) error {
	r.mu.Lock()
	r.updates = append(r.updates, update)
	r.mu.Unlock()
	return h.store.Append(ctx, r.name, update)
}

// broadcast sends a frame to every client in the room except the sender.
// A client whose buffer is full is closed instead of blocking the room.
func (r *room) broadcast(sender *client, frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.clients {
		if c == sender {
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
	return appendVarBytes(appendVarUint(appendVarUint(nil, messageSync), subType), payload)
}

// syncHandler serves the collaboration socket for one room.
func (h *hub) syncHandler(originPatterns []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := roomName(r)
		if !roomNamePattern.MatchString(name) {
			http.Error(w, "invalid room name", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns})
		if err != nil {
			// Accept has already written a response.
			log.Printf("sync %s: accept: %v", name, err)
			return
		}
		conn.SetReadLimit(readLimit)
		defer conn.CloseNow()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		c := &client{conn: conn, send: make(chan []byte, sendBuffer)}
		rm := h.join(name, c)
		defer h.leave(rm, c)

		go writeLoop(ctx, cancel, c)

		// Ask the client for state it already holds; without this a client
		// with local history never pushes it to the server.
		if err := c.push(ctx, syncFrame(syncStep1, emptyStateVector)); err != nil {
			return
		}

		if err := h.readLoop(ctx, rm, c); err != nil {
			log.Printf("sync %s: %v", name, err)
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}
}

func writeLoop(ctx context.Context, cancel context.CancelFunc, c *client) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-c.send:
			writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(writeCtx, websocket.MessageBinary, frame)
			done()
			if err != nil {
				return
			}
		}
	}
}

func (h *hub) readLoop(ctx context.Context, rm *room, c *client) error {
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

func (h *hub) handleFrame(ctx context.Context, rm *room, c *client, frame []byte) error {
	reader := &reader{buf: frame}
	messageType, err := reader.varUint()
	if err != nil {
		return nil // Ignore frames this server does not understand.
	}

	switch messageType {
	case messageAwareness, messageQueryAwareness:
		// Awareness is ephemeral presence state: relay it, never store it.
		rm.broadcast(c, frame)
		return nil
	case messageSync:
	default:
		return nil
	}

	subType, err := reader.varUint()
	if err != nil {
		return nil
	}
	payload, err := reader.varBytes()
	if err != nil {
		return nil
	}

	if subType == syncStep1 {
		// The client asked for the document. Replay the log, then send an
		// empty step 2 so the client flips to synced even in an empty room.
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
