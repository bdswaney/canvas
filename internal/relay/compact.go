package relay

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Merger collapses a sequence of updates into one carrying the same document.
// internal/ydoc implements it; the hub takes the interface so a relay without
// a CRDT engine simply does not compact.
type Merger interface {
	Merge(ctx context.Context, updates [][]byte) ([]byte, error)
}

// compactThreshold is how many live journal rows a document needs before
// compacting it is worth the work. Every reconnect appends a full copy of the
// document, so this is reached by ordinary use rather than only by heavy
// editing.
const compactThreshold = 32

// compactTimeout bounds the work. Compaction is maintenance: if it cannot
// finish promptly it is better to leave the journal alone and try after the
// next session.
const compactTimeout = 30 * time.Second

// Compact folds a document's live journal into a single update.
//
// It runs only for documents with nobody connected. That is not an
// optimisation: Hub.history caches the journal for the life of a session, so
// compacting underneath a live one would leave the cache serving rows the
// store has already retired. Waiting for the session to end sidesteps the
// cache and most of the concurrency at once.
//
// Nothing is deleted. Supersede marks the rows it folded in, so a merge that
// ever proves wrong is one UPDATE away from being undone.
func (h *Hub) Compact(ctx context.Context, docID string) error {
	if h.merger == nil {
		return nil
	}
	// A session opening between here and the store read would rebuild its
	// cache from the journal as it stands, which is consistent either way;
	// what matters is not compacting under a session that already cached it.
	h.mu.Lock()
	_, live := h.sessions[docID]
	h.mu.Unlock()
	if live {
		return nil
	}

	entries, err := h.store.Journal(ctx, docID)
	if err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	if len(entries) < compactThreshold {
		return nil
	}

	updates := make([][]byte, 0, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		updates = append(updates, entry.Update)
		ids = append(ids, entry.ID)
	}

	merged, err := h.merger.Merge(ctx, updates)
	if err != nil {
		return fmt.Errorf("merge updates: %w", err)
	}

	// Only the rows read above are retired. An update that arrived while the
	// merge was running is not among them and stays in the journal, which is
	// what keeps a late-committing insert from being lost.
	if err := h.store.Supersede(ctx, docID, ids, merged); err != nil {
		return fmt.Errorf("supersede journal: %w", err)
	}
	log.Printf("compacted %s: %d updates into 1", docID, len(entries))
	return nil
}

// compactAfter runs a compaction in the background once a document's last
// client has gone. Failure is logged and otherwise ignored: the journal is
// left exactly as it was, and the next session will have another chance.
func (h *Hub) compactAfter(docID string) {
	if h.merger == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
		defer cancel()
		if err := h.Compact(ctx, docID); err != nil {
			log.Printf("compact %s: %v", docID, err)
		}
	}()
}

// Inject journals an update that did not come from a connected client and
// hands it to everyone editing the document.
//
// Without the broadcast, a write from outside the relay — an MCP client, say —
// would be invisible to anybody with the document open until they next
// reconnected: the journal would hold it, but no live session would have seen
// it. Appending through the session also keeps Hub.history's cache in step, so
// the next client to join is not served a journal missing the change.
func (h *Hub) Inject(ctx context.Context, docID string, update []byte) error {
	if len(update) == 0 {
		return nil
	}
	h.mu.Lock()
	session := h.sessions[docID]
	h.mu.Unlock()

	if session == nil {
		// Nobody is editing, so there is nothing to broadcast and no cache to
		// keep current.
		if err := h.store.Append(ctx, docID, update); err != nil {
			return fmt.Errorf("journal update: %w", err)
		}
		return nil
	}

	if err := h.append(ctx, session, update); err != nil {
		return fmt.Errorf("journal update: %w", err)
	}
	// A nil sender means every client receives it; this update belongs to none
	// of them.
	session.broadcast(nil, syncFrame(syncUpdate, update))
	return nil
}
