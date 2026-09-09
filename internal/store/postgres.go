package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// nullableUUID turns an empty id into a NULL, so that "no project filter" and
// "no author" are one representation rather than an invalid uuid literal.
func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// isForeignKeyViolation reports whether err is Postgres rejecting a row that
// points at something that does not exist, which callers surface as not-found
// rather than as an internal error.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
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

func (s *PostgresStore) Projects(ctx context.Context, userID string) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.name, p.created_at FROM projects p
		 JOIN project_members m ON m.project_id = p.id AND m.user_id = $1::uuid
		 WHERE p.deleted_at IS NULL
		 ORDER BY p.name`, nullableUUID(userID))
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// CreateProject makes the creator a member in the same transaction. Without
// that they could not see what they had just made, so it is not a policy
// choice but a consequence of membership being the only way in.
func (s *PostgresStore) CreateProject(ctx context.Context, name, createdBy string) (Project, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("begin create project: %w", err)
	}
	defer tx.Rollback(ctx)

	var p Project
	if err := tx.QueryRow(ctx,
		"INSERT INTO projects (name) VALUES ($1) RETURNING id, name, created_at",
		name).Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	if createdBy != "" {
		if _, err := tx.Exec(ctx,
			"INSERT INTO project_members (project_id, user_id) VALUES ($1::uuid, $2::uuid)",
			p.ID, createdBy); err != nil {
			return Project{}, fmt.Errorf("add creator to project: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Project{}, fmt.Errorf("commit create project: %w", err)
	}
	return p, nil
}

func (s *PostgresStore) ProjectMember(ctx context.Context, projectID, userID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM project_members m
		   JOIN projects p ON p.id = m.project_id AND p.deleted_at IS NULL
		   WHERE m.project_id = $1::uuid AND m.user_id = $2::uuid)`,
		projectID, nullableUUID(userID)).Scan(&exists); err != nil {
		return false, fmt.Errorf("check project membership: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) ProjectMembers(ctx context.Context, projectID string) ([]Member, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT m.user_id::text, COALESCE(u."Username", ''), m.added_at
		 FROM project_members m
		 LEFT JOIN "SessionUsers" u ON u."Id" = m.user_id
		 WHERE m.project_id = $1::uuid
		 ORDER BY u."Username"`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project members: %w", err)
	}
	defer rows.Close()
	var members []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Username, &m.AddedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// AddProjectMember is idempotent: already a member is the state the caller
// asked for, not an error.
func (s *PostgresStore) AddProjectMember(ctx context.Context, projectID, userID, addedBy string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, added_by)
		 VALUES ($1::uuid, $2::uuid, $3::uuid) ON CONFLICT DO NOTHING`,
		projectID, userID, nullableUUID(addedBy))
	if isForeignKeyViolation(err) {
		return ErrNotFound // No such project or account.
	}
	if err != nil {
		return fmt.Errorf("add project member: %w", err)
	}
	return nil
}

func (s *PostgresStore) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	if _, err := s.pool.Exec(ctx,
		"DELETE FROM project_members WHERE project_id = $1::uuid AND user_id = $2::uuid",
		projectID, userID); err != nil {
		return fmt.Errorf("remove project member: %w", err)
	}
	return nil
}

// scanDocs drains a query that selected docColumns.
func scanDocs(rows pgx.Rows) ([]Doc, error) {
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

func (s *PostgresStore) Docs(ctx context.Context, projectID, userID string) ([]Doc, error) {
	// An empty projectID means every project this person belongs to; the cast
	// makes the comparison well typed while NULL turns that clause off. The
	// membership join is what bounds the answer either way.
	rows, err := s.pool.Query(ctx,
		`SELECT `+docColumns+` FROM docs d
		 JOIN projects p ON p.id = d.project_id AND p.deleted_at IS NULL
		 JOIN project_members m ON m.project_id = d.project_id AND m.user_id = $2::uuid
		 LEFT JOIN doc_state s ON s.doc_id = d.id
		 WHERE d.deleted_at IS NULL AND ($1::uuid IS NULL OR d.project_id = $1::uuid)
		 ORDER BY d.name`, nullableUUID(projectID), nullableUUID(userID))
	if err != nil {
		return nil, fmt.Errorf("list docs: %w", err)
	}
	return scanDocs(rows)
}

func (s *PostgresStore) Doc(ctx context.Context, docID string) (Doc, error) {
	// Archived documents are absent here on purpose: this is what the socket
	// handler checks before accepting a connection, so an archived document
	// must not be something people can keep editing live.
	doc, err := scanDoc(s.pool.QueryRow(ctx,
		`SELECT `+docColumns+` FROM docs d
		 JOIN projects p ON p.id = d.project_id AND p.deleted_at IS NULL
		 LEFT JOIN doc_state s ON s.doc_id = d.id
		 WHERE d.id = $1::uuid AND d.deleted_at IS NULL`, docID))
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
	// The deleted_at guard is what stops a save from writing history into an
	// archived document, which nothing downstream would notice.
	err = tx.QueryRow(ctx,
		`UPDATE docs SET current_version = current_version + 1, updated_at = now()
		 WHERE id = $1::uuid AND deleted_at IS NULL
		 RETURNING current_version`,
		docID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("claim version: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO doc_versions (doc_id, version, artifact, artifact_sha256, author_id)
		 VALUES ($1::uuid, $2, $3, $4, $5::uuid)`,
		docID, version, save.Artifact, save.SHA256, nullableUUID(save.AuthorID)); err != nil {
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
		`SELECT v.version, v.artifact_sha256, COALESCE(v.author_id::text, ''), COALESCE(u."Username", ''), v.created_at
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
		if err := rows.Scan(&v.Version, &v.SHA256, &v.AuthorID, &v.Author, &v.CreatedAt); err != nil {
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

// archive hides one row. Nothing is deleted: a real DELETE of a project would
// cascade into doc_versions, and that history is the only layer of this system
// that cannot be rebuilt.
func (s *PostgresStore) archive(ctx context.Context, table, id string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE "+table+" SET deleted_at = now() WHERE id = $1::uuid AND deleted_at IS NULL", id)
	if err != nil {
		return fmt.Errorf("archive %s: %w", table, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// The table names are constants at every call site, never caller input.
func (s *PostgresStore) ArchiveProject(ctx context.Context, projectID string) error {
	return s.archive(ctx, "projects", projectID)
}

func (s *PostgresStore) ArchiveDoc(ctx context.Context, docID string) error {
	return s.archive(ctx, "docs", docID)
}

func (s *PostgresStore) Close() { s.pool.Close() }

// Pool exposes the connection pool so other subsystems, notably session
// storage, can share this one connection pool.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }
