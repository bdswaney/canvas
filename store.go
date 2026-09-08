package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps the ordered update log for each room. Yjs updates are
// idempotent and commutative, so replaying the whole log to a joining client
// reconstructs the document without the server understanding its contents.
type Store interface {
	Append(ctx context.Context, room string, update []byte) error
	Load(ctx context.Context, room string) ([][]byte, error)
	Close()
}

// MemoryStore holds updates for the process lifetime. Used by tests and by
// development runs started without a database.
type MemoryStore struct {
	mu   sync.Mutex
	logs map[string][][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{logs: map[string][][]byte{}}
}

func (s *MemoryStore) Append(_ context.Context, room string, update []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs[room] = append(s.logs[room], append([]byte(nil), update...))
	return nil
}

func (s *MemoryStore) Load(_ context.Context, room string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.logs[room]...), nil
}

func (s *MemoryStore) Close() {}

const schema = `
CREATE TABLE IF NOT EXISTS room_updates (
	id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	room   text NOT NULL,
	update bytea NOT NULL,
	added  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS room_updates_room_id_idx ON room_updates (room, id);
`

// PostgresStore persists the update log so rooms survive a restart.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, url string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Append(ctx context.Context, room string, update []byte) error {
	_, err := s.pool.Exec(ctx, "INSERT INTO room_updates (room, update) VALUES ($1, $2)", room, update)
	if err != nil {
		return fmt.Errorf("append update: %w", err)
	}
	return nil
}

func (s *PostgresStore) Load(ctx context.Context, room string) ([][]byte, error) {
	rows, err := s.pool.Query(ctx, "SELECT update FROM room_updates WHERE room = $1 ORDER BY id", room)
	if err != nil {
		return nil, fmt.Errorf("load updates: %w", err)
	}
	defer rows.Close()
	var updates [][]byte
	for rows.Next() {
		var update []byte
		if err := rows.Scan(&update); err != nil {
			return nil, fmt.Errorf("scan update: %w", err)
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

func (s *PostgresStore) Close() { s.pool.Close() }
