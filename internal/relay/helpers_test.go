package relay

import (
	"context"
	"testing"

	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/store"
)

// newTestStore returns a memory store with the stub user already in the
// default project, which is what the membership migration's seed does for a
// database that existed before membership. Tests that want to exercise a
// non-member use a different id instead.
func newTestStore(t *testing.T) *store.MemoryStore {
	t.Helper()
	st := store.NewMemoryStore()
	if err := st.AddProjectMember(context.Background(), store.DefaultProjectID, authtest.User().ID, ""); err != nil {
		t.Fatal(err)
	}
	return st
}
