package relay

import (
	"context"
	"testing"

	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
)

func mergerFor(t *testing.T) *ydoc.Engine {
	t.Helper()
	e, err := ydoc.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(context.Background()) })
	return e
}

// journalOf builds a document one edit at a time, exactly as the relay would
// journal it, and returns the store plus the text it should read as.
func journalOf(t *testing.T, e *ydoc.Engine, st store.Store, docID string, edits []string) string {
	t.Helper()
	var state []byte
	for _, text := range edits {
		update, err := e.SetText(t.Context(), state, "notes", text)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Append(t.Context(), docID, update); err != nil {
			t.Fatal(err)
		}
		state, err = e.Merge(t.Context(), [][]byte{state, update})
		if err != nil {
			t.Fatal(err)
		}
	}
	return edits[len(edits)-1]
}

// replay is what a joining client does: apply every live journal row in order.
func replay(t *testing.T, e *ydoc.Engine, st store.Store, docID string) string {
	t.Helper()
	updates, err := st.Load(t.Context(), docID)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := e.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}
	text, err := e.Text(t.Context(), merged, "notes")
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// The whole point: a client joining after a compaction must see exactly the
// document a client joining before it would have seen.
func TestCompactionPreservesTheDocument(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	hub := NewHub(st, e)
	const docID = "doc"

	// Non-ASCII on purpose: an offset that means different things on the two
	// sides of the merge would corrupt here and nowhere else.
	edits := make([]string, 0, compactThreshold+8)
	body := "café ☕ "
	for i := 0; i < compactThreshold+8; i++ {
		body += "👍word "
		edits = append(edits, body)
	}
	want := journalOf(t, e, st, docID, edits)

	before := replay(t, e, st, docID)
	if before != want {
		t.Fatalf("replay before compaction = %q, want %q", before, want)
	}

	if err := hub.Compact(t.Context(), docID); err != nil {
		t.Fatal(err)
	}

	entries, err := st.Journal(t.Context(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal has %d live rows after compaction, want 1", len(entries))
	}
	if after := replay(t, e, st, docID); after != before {
		t.Fatalf("compaction changed the document:\n  before: %q\n  after:  %q", before, after)
	}
}

// An update that lands while a merge is being computed must survive it.
//
// This covers the ordinary ordering — the late update takes a higher id than
// anything read — which a range retire would also survive. The dangerous case
// is a row with a *lower* id committing late, which needs real transactions:
// see TestSupersedeKeepsARowThatCommitsLate in internal/store.
func TestLaterUpdateSurvivesCompaction(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	hub := NewHub(st, e)
	const docID = "doc"

	edits := make([]string, 0, compactThreshold)
	body := ""
	for i := 0; i < compactThreshold; i++ {
		body += "a"
		edits = append(edits, body)
	}
	journalOf(t, e, st, docID, edits)

	// Read the journal the way Compact does, then let a late update land
	// before the supersede runs.
	entries, err := st.Journal(t.Context(), docID)
	if err != nil {
		t.Fatal(err)
	}
	updates := make([][]byte, 0, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		updates = append(updates, entry.Update)
		ids = append(ids, entry.ID)
	}
	merged, err := e.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}

	// The late arrival: a client typing while the merge was in flight.
	full, err := e.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}
	late, err := e.SetText(t.Context(), full, "notes", body+"LATE")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(t.Context(), docID, late); err != nil {
		t.Fatal(err)
	}

	// Only the rows read before the late arrival are retired.
	if err := st.Supersede(t.Context(), docID, ids, merged); err != nil {
		t.Fatal(err)
	}

	got := replay(t, e, st, docID)
	if got != body+"LATE" {
		t.Fatalf("replay after compaction = %q, want %q — the late update was lost", got, body+"LATE")
	}
	_ = hub
}

// Retiring a row twice must fail rather than leave a merged update alongside
// rows it does not account for.
func TestSupersedeRefusesToRunTwice(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	const docID = "doc"
	journalOf(t, e, st, docID, []string{"one", "one two"})

	entries, _ := st.Journal(t.Context(), docID)
	ids := []int64{entries[0].ID, entries[1].ID}
	merged, err := e.Merge(t.Context(), [][]byte{entries[0].Update, entries[1].Update})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Supersede(t.Context(), docID, ids, merged); err != nil {
		t.Fatal(err)
	}
	if err := st.Supersede(t.Context(), docID, ids, merged); err == nil {
		t.Fatal("superseding the same rows twice should fail")
	}
}

// A short journal is not worth rewriting.
func TestCompactionLeavesShortJournalsAlone(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	hub := NewHub(st, e)
	const docID = "doc"
	journalOf(t, e, st, docID, []string{"one", "one two", "one two three"})

	if err := hub.Compact(t.Context(), docID); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Journal(t.Context(), docID)
	if len(entries) != 3 {
		t.Fatalf("journal has %d rows, want the original 3", len(entries))
	}
}

// Compacting under a live session would leave Hub.history serving rows the
// store has retired.
func TestCompactionSkipsLiveSessions(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	hub := NewHub(st, e)
	const docID = "doc"

	edits := make([]string, 0, compactThreshold)
	body := ""
	for i := 0; i < compactThreshold; i++ {
		body += "a"
		edits = append(edits, body)
	}
	journalOf(t, e, st, docID, edits)

	c := &client{send: make(chan []byte, 1)}
	hub.join(docID, c)

	if err := hub.Compact(t.Context(), docID); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Journal(t.Context(), docID)
	if len(entries) != compactThreshold {
		t.Fatalf("compacted under a live session: %d rows left", len(entries))
	}
}

// Without a merger the relay behaves exactly as it did before.
func TestNoMergerMeansNoCompaction(t *testing.T) {
	e := mergerFor(t)
	st := store.NewMemoryStore()
	hub := NewHub(st, nil)
	const docID = "doc"

	edits := make([]string, 0, compactThreshold)
	body := ""
	for i := 0; i < compactThreshold; i++ {
		body += "a"
		edits = append(edits, body)
	}
	journalOf(t, e, st, docID, edits)

	if err := hub.Compact(t.Context(), docID); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Journal(t.Context(), docID)
	if len(entries) != compactThreshold {
		t.Fatalf("journal changed with no merger: %d rows", len(entries))
	}
}
