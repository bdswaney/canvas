package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore holds everything for the process lifetime. Tests use it so most
// of them need no database.
type MemoryStore struct {
	mu       sync.Mutex
	journals map[string][][]byte
	projects map[string]*Project
	docs     map[string]*Doc
	versions map[string][]Version
	saved    map[string][]string // doc id -> artifact per version, 1-based
	members  map[string][]Member // project id -> members
	archived map[string]bool     // ids of archived projects and documents
	next     int
}

func NewMemoryStore() *MemoryStore {
	// The default project is seeded by the migrations in Postgres, so seed it
	// here too or the two stores disagree about what an empty database holds.
	return &MemoryStore{
		journals: map[string][][]byte{},
		projects: map[string]*Project{
			DefaultProjectID: {ID: DefaultProjectID, Name: "Default", CreatedAt: time.Now()},
		},
		docs:     map[string]*Doc{},
		versions: map[string][]Version{},
		saved:    map[string][]string{},
		members:  map[string][]Member{},
		archived: map[string]bool{},
	}
}

// nextID hands out distinguishable uuid-shaped ids. The caller holds the lock.
func (s *MemoryStore) nextID(kind int) string {
	s.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%04d%08d", kind, s.next)
}

// live reports whether an id is neither archived itself nor inside an
// archived project. The caller holds the lock.
func (s *MemoryStore) live(id, projectID string) bool {
	return !s.archived[id] && !s.archived[projectID]
}

// archive is not idempotent: archiving something already archived is not a
// second event, and Postgres reports it the same way through RowsAffected.
func (s *MemoryStore) archive(id string, exists bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !exists || s.archived[id] {
		return ErrNotFound
	}
	s.archived[id] = true
	return nil
}

// member reports membership. The caller holds the lock.
func (s *MemoryStore) member(projectID, userID string) bool {
	for _, m := range s.members[projectID] {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) Projects(_ context.Context, userID string) ([]Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects := make([]Project, 0, len(s.projects))
	for _, project := range s.projects {
		if s.archived[project.ID] || !s.member(project.ID, userID) {
			continue
		}
		projects = append(projects, *project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// CreateProject makes the creator a member. Without that they could not see
// what they had just made, so it is not a policy choice but a consequence of
// membership being the only way in.
func (s *MemoryStore) CreateProject(_ context.Context, name, createdBy string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := &Project{ID: s.nextID(1), Name: name, CreatedAt: time.Now()}
	s.projects[project.ID] = project
	s.members[project.ID] = []Member{{UserID: createdBy, AddedAt: project.CreatedAt}}
	return *project, nil
}

func (s *MemoryStore) ProjectMember(_ context.Context, projectID, userID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archived[projectID] {
		return false, nil
	}
	return s.member(projectID, userID), nil
}

func (s *MemoryStore) ProjectMembers(_ context.Context, projectID string) ([]Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrNotFound
	}
	// Postgres resolves the username by joining "SessionUsers"; there is no
	// such table here, so the name stays empty rather than being invented.
	return append([]Member(nil), s.members[projectID]...), nil
}

func (s *MemoryStore) AddProjectMember(_ context.Context, projectID, userID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return ErrNotFound
	}
	if s.member(projectID, userID) {
		return nil // Already a member is the state the caller asked for.
	}
	s.members[projectID] = append(s.members[projectID], Member{UserID: userID, AddedAt: time.Now()})
	return nil
}

func (s *MemoryStore) RemoveProjectMember(_ context.Context, projectID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Member, 0, len(s.members[projectID]))
	for _, m := range s.members[projectID] {
		if m.UserID != userID {
			kept = append(kept, m)
		}
	}
	s.members[projectID] = kept
	return nil
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

func (s *MemoryStore) Docs(_ context.Context, projectID, userID string) ([]Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs := make([]Doc, 0, len(s.docs))
	for _, doc := range s.docs {
		if projectID != "" && doc.ProjectID != projectID {
			continue
		}
		if !s.live(doc.ID, doc.ProjectID) || !s.member(doc.ProjectID, userID) {
			continue
		}
		docs = append(docs, *doc)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

func (s *MemoryStore) Doc(_ context.Context, docID string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[docID]
	if !ok || !s.live(docID, doc.ProjectID) {
		return Doc{}, ErrNotFound
	}
	return *doc, nil
}

func (s *MemoryStore) CreateDoc(_ context.Context, projectID, name string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok || s.archived[projectID] {
		return Doc{}, ErrNotFound
	}
	doc := &Doc{
		ID:        s.nextID(2),
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
	if !ok || !s.live(docID, doc.ProjectID) {
		return 0, ErrNotFound
	}
	doc.CurrentVersion++
	doc.SavedSHA256 = save.SHA256
	doc.UpdatedAt = time.Now()
	s.versions[docID] = append(s.versions[docID], Version{
		Version:   doc.CurrentVersion,
		SHA256:    save.SHA256,
		AuthorID:  save.AuthorID,
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

func (s *MemoryStore) ArchiveProject(_ context.Context, projectID string) error {
	s.mu.Lock()
	_, ok := s.projects[projectID]
	s.mu.Unlock()
	return s.archive(projectID, ok)
}

func (s *MemoryStore) ArchiveDoc(_ context.Context, docID string) error {
	s.mu.Lock()
	_, ok := s.docs[docID]
	s.mu.Unlock()
	return s.archive(docID, ok)
}

func (s *MemoryStore) Close() {}
