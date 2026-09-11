package ydoc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// This suite is the reason the WASM approach is defensible at all.
//
// yrs is not yjs. It is a second implementation of the same format, and a
// disagreement between them does not surface as a failed request — it
// silently corrupts a document that browsers and the server no longer read the
// same way. So every claim of compatibility here is demonstrated against the
// real yjs from node_modules, in both directions, rather than assumed.

type yjsResult struct {
	Update string `json:"update"`
	State  string `json:"state"`
	Text   string `json:"text"`
	Error  string `json:"error"`
}

// yjs runs one command through the real library and returns what it said.
func yjs(t *testing.T, command map[string]any) yjsResult {
	t.Helper()
	script := filepath.Join("testdata", "yjs.mjs")
	if _, err := os.Stat(filepath.Join("..", "..", "node_modules", "yjs")); err != nil {
		t.Skip("node_modules/yjs is missing; run npm ci")
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", script)
	cmd.Stdin = bytes.NewReader(body)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("run yjs helper: %v\n%s", err, errs.String())
	}
	var result yjsResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode yjs helper output %q: %v", out.String(), err)
	}
	if result.Error != "" {
		t.Fatalf("yjs helper: %s", result.Error)
	}
	return result
}

func b64(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(body)
}

func unb64(t *testing.T, encoded string) []byte {
	t.Helper()
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Text the server writes has to be readable by every browser already
// connected to the document.
func TestServerWritesAreReadableByYjs(t *testing.T) {
	e := engine(t)
	for _, want := range []string{
		"plain ascii",
		"# Heading\n\nA paragraph with *emphasis*.\n",
		"accented: café naïve résumé",
		// Surrogate pairs are where a UTF-16 offset disagreement would show
		// up first: yrs counts bytes unless told otherwise, yjs counts UTF-16
		// code units.
		"emoji: 👩‍💻 family 👨‍👩‍👧‍👦 flag 🇬🇧",
		"mixed 👍 café 🎉 end",
	} {
		t.Run(want, func(t *testing.T) {
			update, err := e.SetText(t.Context(), nil, "notes", want)
			if err != nil {
				t.Fatal(err)
			}
			got := yjs(t, map[string]any{"op": "text", "state": b64(update)})
			if got.Text != want {
				t.Errorf("yjs read %q from the server's update, want %q", got.Text, want)
			}
		})
	}
}

// And the reverse: whatever a browser sends, the server has to read the same
// way, or its idea of the document silently diverges.
func TestYjsWritesAreReadableByTheServer(t *testing.T) {
	e := engine(t)
	written := yjs(t, map[string]any{
		"op":    "edit",
		"state": nil,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 0, "text": "hello world"}},
		},
	})
	got, err := e.Text(t.Context(), unb64(t, written.State), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if got != written.Text {
		t.Fatalf("server read %q, yjs says %q", got, written.Text)
	}
}

// Offsets are the most likely place for the two to disagree, and the failure
// is silent, so edit at an index that sits after a surrogate pair.
func TestOffsetsAgreeAcrossSurrogatePairs(t *testing.T) {
	e := engine(t)
	// "👍" is one code point but two UTF-16 code units. Anything counting
	// bytes or code points lands somewhere else entirely.
	base := yjs(t, map[string]any{
		"op":    "edit",
		"state": nil,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 0, "text": "👍ab"}},
		},
	})
	// Insert between 'a' and 'b': index 3 in UTF-16 units.
	edited := yjs(t, map[string]any{
		"op":    "edit",
		"state": base.State,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 3, "text": "X"}},
		},
	})
	got, err := e.Text(t.Context(), unb64(t, edited.State), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if got != edited.Text {
		t.Fatalf("server read %q, yjs says %q — offsets disagree", got, edited.Text)
	}
	if edited.Text != "👍aXb" {
		t.Fatalf("yjs itself produced %q, want %q", edited.Text, "👍aXb")
	}
}

// Each SetText call runs in a fresh Wasm instance. It must still get a
// distinct Yjs client ID, or two edits based on the same state can share an
// (client, clock) pair and one update is silently discarded.
func TestConcurrentServerEditsUseDistinctClientIDs(t *testing.T) {
	e := engine(t)

	base, err := e.SetText(t.Context(), nil, "notes", "base")
	if err != nil {
		t.Fatal(err)
	}

	// Identical edits make the client-ID distinction observable in the update
	// bytes themselves: with the old implicit ID, fresh Wasm instances emitted
	// the same update for both calls.
	identicalA, err := e.SetText(t.Context(), base, "notes", "base X")
	if err != nil {
		t.Fatal(err)
	}
	identicalB, err := e.SetText(t.Context(), base, "notes", "base X")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(identicalA, identicalB) {
		t.Fatal("same-base edits unexpectedly have identical update bytes")
	}

	left, err := e.SetText(t.Context(), base, "notes", "base left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := e.SetText(t.Context(), base, "notes", "base right")
	if err != nil {
		t.Fatal(err)
	}

	for _, updates := range [][][]byte{
		{base, left, right},
		{base, right, left},
	} {
		merged, err := e.Merge(t.Context(), updates)
		if err != nil {
			t.Fatal(err)
		}
		serverText, err := e.Text(t.Context(), merged, "notes")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(serverText, "left") || !strings.Contains(serverText, "right") {
			t.Fatalf("independent edits were lost in merge: %q", serverText)
		}

		browserUpdates := make([]string, 0, len(updates))
		for _, update := range updates {
			browserUpdates = append(browserUpdates, b64(update))
		}
		browser := yjs(t, map[string]any{"op": "merge", "updates": browserUpdates})
		if browser.Text != serverText {
			t.Fatalf("browser and server disagree for merge order:\n  server: %q\n  browser: %q", serverText, browser.Text)
		}
	}
}

// The point of doing this as a CRDT rather than a text replace: an edit from
// the server and an edit from a browser, made against the same starting
// state, must both survive and both sides must land on the same text.
func TestConcurrentEditsConverge(t *testing.T) {
	e := engine(t)

	base := yjs(t, map[string]any{
		"op":    "edit",
		"state": nil,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 0, "text": "alpha bravo charlie"}},
		},
	})
	state := unb64(t, base.State)

	// The server rewrites the middle word.
	serverUpdate, err := e.SetText(t.Context(), state, "notes", "alpha BRAVO charlie")
	if err != nil {
		t.Fatal(err)
	}

	// Concurrently, a browser appends to the end.
	browser := yjs(t, map[string]any{
		"op":    "edit",
		"state": base.State,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 19, "text": " delta"}},
		},
	})

	// Each side receives the other's update.
	serverFinal, err := e.Merge(t.Context(), [][]byte{state, serverUpdate, unb64(t, browser.Update)})
	if err != nil {
		t.Fatal(err)
	}
	serverText, err := e.Text(t.Context(), serverFinal, "notes")
	if err != nil {
		t.Fatal(err)
	}
	browserFinal := yjs(t, map[string]any{
		"op":      "merge",
		"updates": []string{base.State, browser.Update, b64(serverUpdate)},
	})

	if serverText != browserFinal.Text {
		t.Fatalf("diverged:\n  server:  %q\n  browser: %q", serverText, browserFinal.Text)
	}
	// Both edits have to be present: this is what a replace would have lost.
	if !bytes.Contains([]byte(serverText), []byte("BRAVO")) ||
		!bytes.Contains([]byte(serverText), []byte("delta")) {
		t.Fatalf("an edit was lost in the merge: %q", serverText)
	}
}

// A browser typing between two distant server edits, both made against the
// same state. The browser's characters are anchored to the items around the
// cursor, so they land where they were typed only if the server left those
// items alone — and yjs has to agree about it, not just yrs.
func TestABrowserTypingBetweenDistantServerEditsKeepsItsPlace(t *testing.T) {
	e := engine(t)
	start := "👍 top line\n" + strings.Repeat("café middle line\n", 40) + "bottom 🎉 line\n"
	base := yjs(t, map[string]any{
		"op":    "edit",
		"state": nil,
		"ops": []any{
			map[string]any{"insert": map[string]any{"index": 0, "text": start}},
		},
	})
	state := unb64(t, base.State)

	next := strings.Replace(strings.Replace(start, "top", "TOP", 1), "bottom", "BOTTOM", 1)
	serverUpdate, err := e.SetText(t.Context(), state, "notes", next)
	if err != nil {
		t.Fatal(err)
	}

	// Twenty lines in; the browser counts UTF-16 units.
	at := len("👍 top line\n") + 20*len("café middle line\n")
	browser := yjs(t, map[string]any{
		"op":    "edit",
		"state": base.State,
		"ops": []any{
			map[string]any{"insert": map[string]any{
				"index": len(utf16.Encode([]rune(start[:at]))), "text": "[typed]",
			}},
		},
	})

	serverFinal, err := e.Merge(t.Context(), [][]byte{state, serverUpdate, unb64(t, browser.Update)})
	if err != nil {
		t.Fatal(err)
	}
	serverText, err := e.Text(t.Context(), serverFinal, "notes")
	if err != nil {
		t.Fatal(err)
	}
	browserFinal := yjs(t, map[string]any{
		"op":      "merge",
		"updates": []string{base.State, browser.Update, b64(serverUpdate)},
	})

	if serverText != browserFinal.Text {
		t.Fatalf("diverged:\n  server:  %q\n  browser: %q", serverText, browserFinal.Text)
	}
	// "TOP" and "BOTTOM" are the same length as what they replace, so the
	// offset is unchanged.
	if want := next[:at] + "[typed]" + next[at:]; serverText != want {
		t.Fatalf("the browser's typing moved:\n  got:  %q\n  want: %q", serverText, want)
	}
}

// Compaction replaces many journal rows with one update. The server's merge
// has to produce something a browser reads identically, or joining a document
// after a compaction shows different text.
func TestMergeMatchesYjs(t *testing.T) {
	e := engine(t)

	var updates [][]byte
	var encoded []string
	state := ""
	for _, chunk := range []string{"one ", "two ", "three ", "four"} {
		step := yjs(t, map[string]any{
			"op":    "edit",
			"state": state,
			"ops": []any{
				map[string]any{"insert": map[string]any{"index": 0, "text": chunk}},
			},
		})
		updates = append(updates, unb64(t, step.Update))
		encoded = append(encoded, step.Update)
		state = step.State
	}

	merged, err := e.Merge(t.Context(), updates)
	if err != nil {
		t.Fatal(err)
	}
	serverText, err := e.Text(t.Context(), merged, "notes")
	if err != nil {
		t.Fatal(err)
	}

	// The server's merged update, read by yjs.
	viaServer := yjs(t, map[string]any{"op": "text", "state": b64(merged)})
	// And yjs merging the same updates itself.
	viaYjs := yjs(t, map[string]any{"op": "merge", "updates": encoded})

	if serverText != viaYjs.Text || viaServer.Text != viaYjs.Text {
		t.Fatalf("merge disagrees:\n  server:          %q\n  yjs reading it:  %q\n  yjs merging:     %q",
			serverText, viaServer.Text, viaYjs.Text)
	}
}

// The server editing a document that already contains non-ASCII is where an
// offset disagreement actually bites: the index it computes has to mean the
// same thing to yrs as it would to yjs. Every other test here edits from an
// empty document, where the index is always zero and the question never
// arises — this one exists because that gap let a real divergence pass.
func TestServerEditsAfterNonASCIIStayAligned(t *testing.T) {
	e := engine(t)
	for _, tt := range []struct{ base, next string }{
		// "é" is two bytes and one UTF-16 unit; "☕" is three bytes and one
		// unit; "👍" is four bytes and two units. Anything counting the wrong
		// thing lands in the middle of a character.
		{"café ☕ x", "café ☕ xY"},
		{"👍👍 middle", "👍👍 middle end"},
		{"naïve résumé", "naïve résumé!"},
		{"a👍b", "a👍bc"},
		// Edits in the middle and in several places at once, where the diff
		// emits more than one operation and each index depends on the last.
		{"emoji: 👩‍💻 family 👨‍👩‍👧‍👦 flag 🇬🇧", "emoji: 👩‍💻 FAMILY 👨‍👩‍👧 flag 🇺🇸!"},
		{"mixed 👍 café 🎉 end", "mixed 👎 cafe 🎉 fin"},
		{"naïve résumé\nsecond 👍 line\nthird", "naive résumé\nsecond 👍👍 line\nthird café"},
	} {
		t.Run(tt.base, func(t *testing.T) {
			base := yjs(t, map[string]any{
				"op":    "edit",
				"state": nil,
				"ops": []any{
					map[string]any{"insert": map[string]any{"index": 0, "text": tt.base}},
				},
			})
			update, err := e.SetText(t.Context(), unb64(t, base.State), "notes", tt.next)
			if err != nil {
				t.Fatal(err)
			}
			merged, err := e.Merge(t.Context(), [][]byte{unb64(t, base.State), update})
			if err != nil {
				t.Fatal(err)
			}
			got := yjs(t, map[string]any{"op": "text", "state": b64(merged)})
			if got.Text != tt.next {
				t.Errorf("yjs read %q after the server's edit, want %q", got.Text, tt.next)
			}
		})
	}
}
