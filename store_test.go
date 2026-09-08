package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMemoryStoreCopiesUpdates(t *testing.T) {
	store := NewMemoryStore()
	update := []byte{0x01, 0x02}
	if err := store.Append(context.Background(), "room", update); err != nil {
		t.Fatal(err)
	}
	update[0] = 0xff // The caller's buffer must not alias the stored copy.
	stored, err := store.Load(context.Background(), "room")
	if err != nil || len(stored) != 1 || !bytes.Equal(stored[0], []byte{0x01, 0x02}) {
		t.Fatalf("stored %v, %v", stored, err)
	}
	if other, err := store.Load(context.Background(), "elsewhere"); err != nil || len(other) != 0 {
		t.Fatalf("unknown room returned %v, %v", other, err)
	}
}

// Runs only when DATABASE_URL points at a scratch database, for example the
// container started by mise run db.
func TestPostgresStore(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	room := fmt.Sprintf("test-%d", time.Now().UnixNano())
	updates := [][]byte{{0x01}, {0x02, 0x03}}
	for _, update := range updates {
		if err := store.Append(ctx, room, update); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := store.Load(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(updates) {
		t.Fatalf("loaded %d updates, want %d", len(stored), len(updates))
	}
	for i, update := range updates {
		if !bytes.Equal(stored[i], update) {
			t.Errorf("update %d = % x, want % x", i, stored[i], update)
		}
	}
	if _, err := store.pool.Exec(ctx, "DELETE FROM room_updates WHERE room = $1", room); err != nil {
		t.Fatal(err)
	}
}
