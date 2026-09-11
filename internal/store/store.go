// Package store holds the durable model: projects, documents, their saved
// versions, and the update journal. Store is the interface; MemoryStore and
// PostgresStore implement it, and one conformance suite runs against both.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned for a doc or version that does not exist.
var ErrNotFound = errors.New("not found")

// Doc is a document's metadata. The live text is not here: it lives in the
// journal and in the clients editing it.
type Doc struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	Name           string    `json:"name"`
	SourceKey      string    `json:"-"`
	CurrentVersion int       `json:"currentVersion"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// SavedSHA256 is the hash of the artifact stored by the last save, so a
	// client can tell whether what it holds has been saved. Empty when the doc
	// has never been saved.
	SavedSHA256 []byte `json:"savedSha256,omitempty"`
}

// Version is one saved artifact. AuthorID is what is stored; Author is that
// id resolved to a username for display, and is empty when the name cannot be
// resolved. Usernames are mutable, so only the id is durable.
type Version struct {
	Version   int       `json:"version"`
	SHA256    []byte    `json:"sha256"`
	AuthorID  string    `json:"authorId"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

// Project is the top of the hierarchy: a named container for documents and
// the people who work on them.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Member is a person's membership in a project. UserID is what is stored;
// Username is that id resolved for display, and is empty when it cannot be
// resolved. Usernames are mutable, so only the id is durable.
type Member struct {
	UserID   string    `json:"userId"`
	Username string    `json:"username"`
	AddedAt  time.Time `json:"addedAt"`
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
// JournalEntry is one stored update and the id it lives under. Compaction
// needs the ids so it can supersede exactly the rows it merged.
type JournalEntry struct {
	ID     int64
	Update []byte
}

type Store interface {
	Append(ctx context.Context, docID string, update []byte) error
	Load(ctx context.Context, docID string) ([][]byte, error)

	// Journal returns the live updates for a document with their ids, which
	// is what Load reads without them.
	Journal(ctx context.Context, docID string) ([]JournalEntry, error)

	// Supersede replaces the given journal rows with one merged update
	// carrying the same document. It takes the ids to retire rather than a
	// range on purpose: an identity value is assigned at INSERT and only
	// becomes visible at COMMIT, so a row with a lower id can appear after a
	// reader has seen a higher one. Retiring a range would drop it silently.
	//
	// Nothing is deleted. The retired rows are marked, so restoring them is
	// one UPDATE away if a merge ever proves wrong.
	Supersede(ctx context.Context, docID string, ids []int64, merged []byte) error

	// Projects lists the projects userID belongs to. Membership is the
	// authorization boundary, so the lists that could otherwise leak the
	// existence of other people's work are filtered in SQL rather than by the
	// caller. Single-row reads are gated by the handler instead, which keeps
	// one copy of that rule rather than one per store.
	Projects(ctx context.Context, userID string) ([]Project, error)
	CreateProject(ctx context.Context, name, createdBy string) (Project, error)

	// Archiving hides a row from every read path and deletes nothing. A real
	// delete would cascade into doc_versions, and history is the one durable
	// layer here — everything else can be rebuilt from it.
	ArchiveProject(ctx context.Context, projectID string) error
	ArchiveDoc(ctx context.Context, docID string) error

	// ProjectMember reports whether userID belongs to projectID. This is the
	// check every handler makes before touching a single row, so it is also
	// where an archived project stops being reachable: nobody belongs to one.
	ProjectMember(ctx context.Context, projectID, userID string) (bool, error)
	ProjectMembers(ctx context.Context, projectID string) ([]Member, error)
	AddProjectMember(ctx context.Context, projectID, userID, addedBy string) error
	RemoveProjectMember(ctx context.Context, projectID, userID string) error

	// Docs lists the documents in a project userID belongs to, or across all
	// of their projects when projectID is empty.
	Docs(ctx context.Context, projectID, userID string) ([]Doc, error)
	Doc(ctx context.Context, docID string) (Doc, error)
	CreateDoc(ctx context.Context, projectID, name string) (Doc, error)
	// UpsertDoc uses sourceKey as a stable external identity. It creates a
	// document when the key is new and updates its display name when it exists.
	// An empty sourceKey is rejected: callers that need an ordinary document
	// should use CreateDoc so imports cannot silently duplicate.
	UpsertDoc(ctx context.Context, projectID, sourceKey, name string) (doc Doc, created bool, err error)

	SaveDoc(ctx context.Context, docID string, save Save) (int, error)
	Versions(ctx context.Context, docID string) ([]Version, error)
	Artifact(ctx context.Context, docID string, version int) (string, error)

	Close()
}

// DefaultProjectID is seeded by the migrations. A request that names no
// project lands here.
const DefaultProjectID = "00000000-0000-4000-8000-000000000001"
