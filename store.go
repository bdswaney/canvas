package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned for a doc or version that does not exist.
var ErrNotFound = errors.New("not found")

// Doc is a document's metadata. The live text is not here: it lives in the
// journal and in the clients editing it.
type Doc struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	Name           string    `json:"name"`
	CurrentVersion int       `json:"currentVersion"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// SavedSHA256 is the hash of the artifact stored by the last save, so a
	// client can tell whether what it holds has been saved. Empty when the doc
	// has never been saved.
	SavedSHA256 []byte `json:"savedSha256,omitempty"`
}

// Version is one saved artifact. The author is a SessionUsers id resolved to
// a name for display; usernames are mutable, so only the id is stored.
type Version struct {
	Version   int       `json:"version"`
	SHA256    []byte    `json:"sha256"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

// Save is one client-authored commit: the readable artifact plus the CRDT
// state it was taken from. The server cannot derive either one.
type Save struct {
	Artifact string
	Snapshot []byte
	SHA256   []byte
	AuthorID string
}

// Store keeps the ordered update journal for each doc, and the docs
// themselves. Yjs updates are idempotent and commutative, so replaying a
// journal reconstructs the document without the server understanding it.
type Store interface {
	Append(ctx context.Context, docID string, update []byte) error
	Load(ctx context.Context, docID string) ([][]byte, error)

	Docs(ctx context.Context) ([]Doc, error)
	Doc(ctx context.Context, docID string) (Doc, error)
	CreateDoc(ctx context.Context, projectID, name string) (Doc, error)

	SaveDoc(ctx context.Context, docID string, save Save) (int, error)
	Versions(ctx context.Context, docID string) ([]Version, error)
	Artifact(ctx context.Context, docID string, version int) (string, error)

	Close()
}

// defaultProjectID is seeded by the migrations. Projects get their own UI
// later; until then new docs land here.
const defaultProjectID = "00000000-0000-4000-8000-000000000001"

// MemoryStore holds everything for the process lifetime. Tests use it so most
// of them need no database.
type MemoryStore struct {
	mu       sync.Mutex
	journals map[string][][]byte
	docs     map[string]*Doc
	versions map[string][]Version
	saved    map[string][]string // doc id -> artifact per version, 1-based
	next     int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		journals: map[string][][]byte{},
		docs:     map[string]*Doc{},
		versions: map[string][]Version{},
		saved:    map[string][]string{},
	}
}

func (s *MemoryStore) Append(_ context.Context, docID string, update []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journals[docID] = append(s.journals[docID], append([]byte(nil), update...))
	return nil
}

func (s *MemoryStore) Load(_ context.Context, docID string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.journals[docID]...), nil
}

func (s *MemoryStore) Docs(_ context.Context) ([]Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs := make([]Doc, 0, len(s.docs))
	for _, doc := range s.docs {
		docs = append(docs, *doc)
	}
	return docs, nil
}

func (s *MemoryStore) Doc(_ context.Context, docID string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[docID]
	if !ok {
		return Doc{}, ErrNotFound
	}
	return *doc, nil
}

func (s *MemoryStore) CreateDoc(_ context.Context, projectID, name string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	doc := &Doc{
		ID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", s.next),
		ProjectID: projectID,
		Name:      name,
		UpdatedAt: time.Now(),
	}
	s.docs[doc.ID] = doc
	return *doc, nil
}

func (s *MemoryStore) SaveDoc(_ context.Context, docID string, save Save) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[docID]
	if !ok {
		return 0, ErrNotFound
	}
	doc.CurrentVersion++
	doc.SavedSHA256 = save.SHA256
	doc.UpdatedAt = time.Now()
	s.versions[docID] = append(s.versions[docID], Version{
		Version:   doc.CurrentVersion,
		SHA256:    save.SHA256,
		Author:    save.AuthorID,
		CreatedAt: doc.UpdatedAt,
	})
	s.saved[docID] = append(s.saved[docID], save.Artifact)
	return doc.CurrentVersion, nil
}

func (s *MemoryStore) Versions(_ context.Context, docID string) ([]Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[docID]; !ok {
		return nil, ErrNotFound
	}
	return append([]Version(nil), s.versions[docID]...), nil
}

func (s *MemoryStore) Artifact(_ context.Context, docID string, version int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artifacts := s.saved[docID]
	if version < 1 || version > len(artifacts) {
		return "", ErrNotFound
	}
	return artifacts[version-1], nil
}

func (s *MemoryStore) Close() {}

// PostgresStore persists docs and their journals.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, url string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	// The schema is owned by the migrations in migrations/, applied before
	// the pool is opened.
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Append(ctx context.Context, docID string, update []byte) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO doc_updates (doc_id, update) VALUES ($1::uuid, $2)", docID, update)
	if err != nil {
		return fmt.Errorf("append update: %w", err)
	}
	return nil
}

func (s *PostgresStore) Load(ctx context.Context, docID string) ([][]byte, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT update FROM doc_updates WHERE doc_id = $1::uuid ORDER BY id", docID)
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

const docColumns = `d.id, d.project_id, d.name, d.current_version, d.updated_at, s.artifact_sha256`

func scanDoc(row pgx.Row) (Doc, error) {
	var doc Doc
	var saved []byte
	if err := row.Scan(&doc.ID, &doc.ProjectID, &doc.Name, &doc.CurrentVersion, &doc.UpdatedAt, &saved); err != nil {
		return Doc{}, err
	}
	doc.SavedSHA256 = saved
	return doc, nil
}

func (s *PostgresStore) Docs(ctx context.Context) ([]Doc, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+docColumns+` FROM docs d
		 LEFT JOIN doc_state s ON s.doc_id = d.id
		 ORDER BY d.name`)
	if err != nil {
		return nil, fmt.Errorf("list docs: %w", err)
	}
	defer rows.Close()
	var docs []Doc
	for rows.Next() {
		doc, err := scanDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("scan doc: %w", err)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *PostgresStore) Doc(ctx context.Context, docID string) (Doc, error) {
	doc, err := scanDoc(s.pool.QueryRow(ctx,
		`SELECT `+docColumns+` FROM docs d
		 LEFT JOIN doc_state s ON s.doc_id = d.id
		 WHERE d.id = $1::uuid`, docID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, fmt.Errorf("read doc: %w", err)
	}
	return doc, nil
}

func (s *PostgresStore) CreateDoc(ctx context.Context, projectID, name string) (Doc, error) {
	var id string
	if err := s.pool.QueryRow(ctx,
		"INSERT INTO docs (project_id, name) VALUES ($1::uuid, $2) RETURNING id",
		projectID, name).Scan(&id); err != nil {
		return Doc{}, fmt.Errorf("create doc: %w", err)
	}
	return s.Doc(ctx, id)
}

// SaveDoc commits one version. The version number is assigned here, under a
// row lock on the doc, so two clients saving at once cannot land on the same
// number or overwrite each other's history.
//
// Nothing is deleted from doc_updates: replay is idempotent, so leaving the
// journal alone costs bytes, while trimming it wrongly loses edits.
func (s *PostgresStore) SaveDoc(ctx context.Context, docID string, save Save) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin save: %w", err)
	}
	defer tx.Rollback(ctx)

	var version int
	err = tx.QueryRow(ctx,
		"UPDATE docs SET current_version = current_version + 1, updated_at = now() WHERE id = $1::uuid RETURNING current_version",
		docID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("claim version: %w", err)
	}

	var authorID any
	if save.AuthorID != "" {
		authorID = save.AuthorID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO doc_versions (doc_id, version, artifact, artifact_sha256, author_id)
		 VALUES ($1::uuid, $2, $3, $4, $5::uuid)`,
		docID, version, save.Artifact, save.SHA256, authorID); err != nil {
		return 0, fmt.Errorf("write version: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO doc_state (doc_id, snapshot, saved_version, artifact_sha256, updated_at)
		 VALUES ($1::uuid, $2, $3, $4, now())
		 ON CONFLICT (doc_id) DO UPDATE
		 SET snapshot = EXCLUDED.snapshot,
		     saved_version = EXCLUDED.saved_version,
		     artifact_sha256 = EXCLUDED.artifact_sha256,
		     updated_at = EXCLUDED.updated_at`,
		docID, save.Snapshot, version, save.SHA256); err != nil {
		return 0, fmt.Errorf("write state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit save: %w", err)
	}
	return version, nil
}

func (s *PostgresStore) Versions(ctx context.Context, docID string) ([]Version, error) {
	if _, err := s.Doc(ctx, docID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT v.version, v.artifact_sha256, COALESCE(u."Username", ''), v.created_at
		 FROM doc_versions v
		 LEFT JOIN "SessionUsers" u ON u."Id" = v.author_id
		 WHERE v.doc_id = $1::uuid
		 ORDER BY v.version DESC`, docID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	var versions []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.SHA256, &v.Author, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *PostgresStore) Artifact(ctx context.Context, docID string, version int) (string, error) {
	var artifact string
	err := s.pool.QueryRow(ctx,
		"SELECT artifact FROM doc_versions WHERE doc_id = $1::uuid AND version = $2",
		docID, version).Scan(&artifact)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read artifact: %w", err)
	}
	return artifact, nil
}

func (s *PostgresStore) Close() { s.pool.Close() }

// Pool exposes the connection pool so other subsystems, notably session
// storage, can share this one connection pool.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }
