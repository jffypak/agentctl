package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRepoUpsertTakesTheWriteLockThroughWithTx: Repos.Upsert was the last
// autocommit write on SQLite's own busy handler (sparse back-off probes that
// a busy writer can starve; docs/notes/m1-flaky-tests.md). It must take the
// write lock like every other store write: through withTx, which begins on
// the write pool and stamps the release time that drives the writer yield.
func TestRepoUpsertTakesTheWriteLockThroughWithTx(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "agentctl.db"), Options{Now: newFakeClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st.released.Load() != 0 || st.wdb.Stats().OpenConnections != 0 {
		t.Fatal("fresh store already wrote through withTx")
	}

	root := filepath.Join(t.TempDir(), "repo")
	first, err := st.Repos().Upsert(ctx, root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if st.released.Load() == 0 {
		t.Fatal("Upsert did not release the write lock through withTx")
	}
	if st.wdb.Stats().OpenConnections == 0 {
		t.Fatal("Upsert did not begin on the write pool")
	}

	// Behaviour is unchanged: the second upsert renames and keeps the row.
	second, err := st.Repos().Upsert(ctx, root, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Name != "renamed" || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("re-upsert = %+v, first = %+v", second, first)
	}
	if got, err := st.Repos().ByPath(ctx, root); err != nil || got != second {
		t.Fatalf("ByPath = %+v, %v; want %+v", got, err, second)
	}
	if _, err := st.Repos().Upsert(ctx, "relative/repo", "x"); err == nil {
		t.Fatal("relative root accepted")
	}
}
