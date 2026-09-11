package ydoc

import (
	"context"
	"fmt"
	"strings"
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

// longDocument has a word near the top and the same word near the bottom,
// with about 10 kB between, and non-ASCII throughout so a unit mistake would
// land somewhere visible.
func longDocument() string {
	var b strings.Builder
	b.WriteString("# Plan\n\nRun `canvas-old` to start. Café ☕ and 👍 are here.\n\n")
	for i := range 110 {
		fmt.Fprintf(&b, "Paragraph %d explains one step of the rollout in some detail — naïve résumé 🎉.\n", i)
	}
	b.WriteString("\nFinally, run `canvas-old` again.\n")
	return b.String()
}

// mustSetText applies SetText and returns the update.
func mustSetText(t *testing.T, e *Engine, state []byte, next string) []byte {
	t.Helper()
	update, err := e.SetText(t.Context(), state, "notes", next)
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func mustText(t *testing.T, e *Engine, updates ...[]byte) string {
	t.Helper()
	merged, err := e.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Text(t.Context(), merged, "notes")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Two small edits far apart must be two small operations. Trimming only the
// shared prefix and suffix deletes and reinserts everything between them,
// which is kilobytes of journal and, worse, new Yjs items for text nobody
// changed.
//
// The items are checked through their effect rather than their ids: peers
// edit the middle against the same starting state. Their changes are anchored
// to the original items, so they land where they were made only if those
// items are still the live ones.
func TestDistantEditsLeaveTheTextBetweenThemAlone(t *testing.T) {
	e := engine(t)
	base := longDocument()
	state := mustSetText(t, e, nil, base)

	next := strings.ReplaceAll(base, "canvas-old", "canvas-new")
	update := mustSetText(t, e, state, next)
	t.Logf("document %d bytes, state %d bytes; two distant edits produced %d bytes",
		len(base), len(state), len(update))
	if len(update) > 300 {
		t.Errorf("two small edits produced %d bytes; the text between them was rewritten", len(update))
	}

	// Concurrently, one peer types in the middle and another deletes a word
	// further on.
	typed := mustSetText(t, e, state, strings.Replace(base, "Paragraph 40 explains", "Paragraph 40 [typed] explains", 1))
	deleted := mustSetText(t, e, state, strings.Replace(base, "Paragraph 70 explains", "Paragraph 70", 1))

	want := strings.Replace(next, "Paragraph 40 explains", "Paragraph 40 [typed] explains", 1)
	want = strings.Replace(want, "Paragraph 70 explains", "Paragraph 70", 1)
	if got := mustText(t, e, state, update, typed, deleted); got != want {
		t.Errorf("a concurrent edit between the two changes was misplaced or lost")
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Logf("first difference at byte %d:\n  got:  %q\n  want: %q", i, got[i:min(i+80, len(got))], want[i:min(i+80, len(want))])
				break
			}
		}
	}
}

// Whatever the diff decides, the result has to read exactly as asked. These
// are the shapes a line-then-character diff could get wrong: lines appearing
// and vanishing, a missing final newline, CRLF, surrogate pairs and ZWJ
// sequences, and a rewrite too large to refine.
func TestSetTextReachesExactlyTheRequestedText(t *testing.T) {
	e := engine(t)
	for _, tt := range []struct{ name, from, to string }{
		{"from empty", "", "x"},
		{"to empty", "x\ny\n", ""},
		{"change a line", "a\nb\nc\n", "a\nB\nc\n"},
		{"drop a line", "a\nb\nc", "a\nc"},
		{"add a line", "a\nc", "a\nb\nc"},
		{"final newline", "no newline", "no newline\n"},
		{"crlf", "line\r\nnext\r\n", "line\r\nNEXT\r\n"},
		{"surrogates move", "👍👍 middle", "👍 middle 👍"},
		{"zwj", "emoji: 👩‍💻 family 👨‍👩‍👧‍👦 flag 🇬🇧", "emoji: 👩‍💻 FAMILY 👨‍👩‍👧 flag 🇺🇸"},
		{"accents across lines", "café\nnaïve\nrésumé", "cafe\nnaïve!\nrésumé 🎉"},
		{"scattered", "the quick brown fox\njumps over\nthe lazy dog\n", "the quack brown fix\njumps over\nthe lazy cog!\n"},
		{"rewrite past the refine limit", strings.Repeat("abc ", 3000), strings.Repeat("xyz\n", 3000)},
		{"rewrite under the refine limit", strings.Repeat("ab", 4500), strings.Repeat("cd", 4500)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := mustSetText(t, e, nil, tt.from)
			update := mustSetText(t, e, state, tt.to)
			if got := mustText(t, e, state, update); got != tt.to {
				t.Errorf("SetText produced %q, want %q", truncate(got), truncate(tt.to))
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
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

// Folding an update into a document that does not exist yet is the ordinary
// first step of building one, so an empty state must not need special-casing.
func TestMergeIgnoresEmptyUpdates(t *testing.T) {
	e := engine(t)
	update, err := e.SetText(t.Context(), nil, "notes", "first")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := e.Merge(t.Context(), [][]byte{nil, update, {}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.Text(t.Context(), merged, "notes"); err != nil || got != "first" {
		t.Fatalf("merged with empty entries = %q, %v; want %q", got, err, "first")
	}
}
