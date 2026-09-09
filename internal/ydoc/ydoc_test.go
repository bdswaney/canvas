package ydoc

import (
	"context"
	"testing"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(context.Background()) })
	return e
}

func TestEmptyDocumentHasEmptyText(t *testing.T) {
	got, err := engine(t).Text(t.Context(), nil, "notes")
	if err != nil || got != "" {
		t.Fatalf("text of an empty document = %q, %v", got, err)
	}
}

func TestSetTextThenReadItBack(t *testing.T) {
	e := engine(t)
	update, err := e.SetText(t.Context(), nil, "notes", "# Hello")
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Text(t.Context(), update, "notes")
	if err != nil || got != "# Hello" {
		t.Fatalf("round trip = %q, %v; want %q", got, err, "# Hello")
	}
}

// An edit must leave the untouched parts of the document alone, because the
// update it returns is what other clients merge. Replacing everything would
// clobber a peer editing elsewhere.
func TestSetTextOnlyRewritesWhatChanged(t *testing.T) {
	e := engine(t)
	state, err := e.SetText(t.Context(), nil, "notes", "alpha bravo charlie")
	if err != nil {
		t.Fatal(err)
	}
	// The returned update carries only the change, so it is much smaller than
	// the document when the edit is small.
	small, err := e.SetText(t.Context(), state, "notes", "alpha BRAVO charlie")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := e.SetText(t.Context(), state, "notes", "completely different text here")
	if err != nil {
		t.Fatal(err)
	}
	if len(small) >= len(whole) {
		t.Errorf("a one-word edit produced %d bytes and a whole rewrite %d; "+
			"the small edit should touch less", len(small), len(whole))
	}
}

func TestMergeCollapsesUpdates(t *testing.T) {
	e := engine(t)
	first, err := e.SetText(t.Context(), nil, "notes", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.SetText(t.Context(), first, "notes", "one two")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := e.Merge(t.Context(), [][]byte{first, second})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Text(t.Context(), merged, "notes")
	if err != nil || got != "one two" {
		t.Fatalf("merged text = %q, %v; want %q", got, err, "one two")
	}
}

// Malformed input must come back as an error rather than trapping, or one bad
// journal row would take the server down.
func TestGarbageIsAnErrorNotACrash(t *testing.T) {
	if _, err := engine(t).Text(t.Context(), []byte{0xff, 0xfe, 0xfd}, "notes"); err == nil {
		t.Fatal("expected an error for a malformed state")
	}
}
