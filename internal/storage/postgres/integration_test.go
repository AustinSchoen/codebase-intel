//go:build integration

// Integration tests for the Postgres storage layer, run against a real pgvector
// container via testcontainers-go. Gated by the `integration` build tag so the
// default `go test ./...` stays fast and dependency-free; run with:
//
//   go test -tags=integration ./internal/storage/postgres/...
//
// Requires a working Docker (or Podman with the Docker socket shim) on the
// host. CI configuration lives in .github/workflows/integration-tests.yml.

package postgres

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupTestStore spins up a fresh pgvector container, runs migrations against
// it, and returns a Store wired to that database. The container is torn down
// via t.Cleanup, so each test gets an isolated database.
//
// Container startup takes a few seconds. If you're iterating on a single test,
// `go test -run TestX -tags=integration` is the fastest loop.
func setupTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()

	pgC, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:pg17",
		tcpostgres.WithDatabase("codebase_intel"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(pgC); err != nil {
			t.Logf("terminating container: %v", err)
		}
	})

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("getting connection string: %v", err)
	}

	store, err := NewStore(ctx, dsn, 10)
	if err != nil {
		t.Fatalf("connecting store: %v", err)
	}
	t.Cleanup(store.Close)

	if err := store.RunMigrations(ctx, migrationsDir(t)); err != nil {
		t.Fatalf("running migrations: %v", err)
	}

	return store
}

// migrationsDir resolves the path to the repo's migrations directory from the
// test source file, so the test works regardless of the working directory it's
// invoked from.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine integration_test.go path via runtime.Caller")
	}
	// internal/storage/postgres/integration_test.go -> repo root is three up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
}

// ensureCodebase is a small helper to insert a codebase row so symbols that
// reference it via FK can be inserted.
func ensureCodebase(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.EnsureCodebase(context.Background(), id, "/tmp/"+id, id); err != nil {
		t.Fatalf("ensuring codebase %q: %v", id, err)
	}
}

func TestStore_RoundTripSymbol(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	ensureCodebase(t, store, "demo")

	sym := Symbol{
		ID:         "sym-1",
		CodebaseID: "demo",
		Name:       "Hello",
		Qualified:  "main.Hello",
		Kind:       "function",
		Filepath:   "main.go",
		LineStart:  1,
		LineEnd:    3,
		Module:     "main",
	}
	if err := store.UpsertSymbols(ctx, []Symbol{sym}); err != nil {
		t.Fatalf("upserting symbol: %v", err)
	}

	got, err := store.FuzzySearchSymbols(ctx, "demo", "Hello", "", 5)
	if err != nil {
		t.Fatalf("fuzzy search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("FuzzySearchSymbols returned %d results, want 1", len(got))
	}
	if got[0].Qualified != "main.Hello" {
		t.Errorf("qualified = %q, want main.Hello", got[0].Qualified)
	}
}

// TestCleanupStaleFileData_PreservesUnchangedRelationships locks in the fix
// for issue #7. The pre-fix flow ran a blanket DELETE of all symbols for a
// file before re-inserting them, which cascaded through the relationships FK
// and silently wiped every relationship pointing to any symbol in the file —
// including relationships emitted by *unchanged* files that the indexer
// would not reprocess.
//
// The post-fix flow upserts symbols first (no-op DO UPDATE for stable IDs,
// no cascade) and then calls CleanupStaleFileData to drop only the IDs no
// longer in the new parse result. This test reproduces the original failure
// shape: a relationship from file B to a symbol in file A must survive a
// re-index of file A that doesn't change A's symbols.
func TestCleanupStaleFileData_PreservesUnchangedRelationships(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	const codebase = "issue-7-repro"
	ensureCodebase(t, store, codebase)

	// File A: two symbols.
	symA1 := Symbol{ID: "a1", CodebaseID: codebase, Name: "Foo", Qualified: "a.Foo", Kind: "function", Filepath: "a.go", LineStart: 1, LineEnd: 5}
	symA2 := Symbol{ID: "a2", CodebaseID: codebase, Name: "Bar", Qualified: "a.Bar", Kind: "function", Filepath: "a.go", LineStart: 10, LineEnd: 15}

	// File B: one symbol that calls into file A.
	symB1 := Symbol{ID: "b1", CodebaseID: codebase, Name: "Baz", Qualified: "b.Baz", Kind: "function", Filepath: "b.go", LineStart: 1, LineEnd: 8}

	if err := store.UpsertSymbols(ctx, []Symbol{symA1, symA2, symB1}); err != nil {
		t.Fatalf("initial UpsertSymbols: %v", err)
	}

	// Two relationships:
	//   crossFile: b.Baz calls a.Foo  — the canary; depends on a.Foo's id
	//   sameFile:  a.Bar calls a.Foo  — emitted by file A itself
	rels := []Relationship{
		{CodebaseID: codebase, SourceID: "b1", TargetID: "a1", Kind: "calls", Filepath: "b.go", Line: 4},
		{CodebaseID: codebase, SourceID: "a2", TargetID: "a1", Kind: "calls", Filepath: "a.go", Line: 12},
	}
	if err := store.UpsertRelationships(ctx, rels); err != nil {
		t.Fatalf("UpsertRelationships: %v", err)
	}

	if got := countRelationships(t, store, codebase); got != 2 {
		t.Fatalf("after initial insert: relationship count = %d, want 2", got)
	}

	// Simulate the new incremental-reindex path for file A: symbols are upserted
	// with the same IDs (deterministic chunkID), then CleanupStaleFileData runs
	// to remove orphans. Both IDs are still present, so cleanup is a no-op for
	// this file.
	if err := store.UpsertSymbols(ctx, []Symbol{symA1, symA2}); err != nil {
		t.Fatalf("re-UpsertSymbols: %v", err)
	}
	if err := store.CleanupStaleFileData(ctx, codebase, "a.go", []string{"a1", "a2"}); err != nil {
		t.Fatalf("CleanupStaleFileData: %v", err)
	}

	// Both relationships must still exist — the regression invariant.
	if got := countRelationships(t, store, codebase); got != 2 {
		t.Fatalf("after re-index of file A: relationship count = %d, want 2 "+
			"(this is the #7 regression: cross-file relationships getting wiped)", got)
	}
}

// TestCleanupStaleFileData_RemovesOrphanedSymbols verifies the cascade still
// fires correctly for genuinely-removed symbols: deleting a symbol from file A
// must wipe relationships that point at it, including those emitted by other
// files. This is the complementary correctness property to the test above.
func TestCleanupStaleFileData_RemovesOrphanedSymbols(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	const codebase = "issue-7-orphan"
	ensureCodebase(t, store, codebase)

	symA1 := Symbol{ID: "a1", CodebaseID: codebase, Name: "Foo", Qualified: "a.Foo", Kind: "function", Filepath: "a.go", LineStart: 1, LineEnd: 5}
	symA2 := Symbol{ID: "a2", CodebaseID: codebase, Name: "Bar", Qualified: "a.Bar", Kind: "function", Filepath: "a.go", LineStart: 10, LineEnd: 15}
	symB1 := Symbol{ID: "b1", CodebaseID: codebase, Name: "Baz", Qualified: "b.Baz", Kind: "function", Filepath: "b.go", LineStart: 1, LineEnd: 8}
	if err := store.UpsertSymbols(ctx, []Symbol{symA1, symA2, symB1}); err != nil {
		t.Fatalf("initial UpsertSymbols: %v", err)
	}

	rels := []Relationship{
		{CodebaseID: codebase, SourceID: "b1", TargetID: "a1", Kind: "calls", Filepath: "b.go", Line: 4},
		{CodebaseID: codebase, SourceID: "a2", TargetID: "a1", Kind: "calls", Filepath: "a.go", Line: 12},
	}
	if err := store.UpsertRelationships(ctx, rels); err != nil {
		t.Fatalf("UpsertRelationships: %v", err)
	}

	// Re-index file A *removing* symbol a1. The cleanup call should cascade
	// and remove both relationships, since both target a1.
	if err := store.CleanupStaleFileData(ctx, codebase, "a.go", []string{"a2"}); err != nil {
		t.Fatalf("CleanupStaleFileData: %v", err)
	}

	if got := countRelationships(t, store, codebase); got != 0 {
		t.Errorf("after removing symbol a1: relationship count = %d, want 0 "+
			"(both relationships targeted a1 and should have cascaded)", got)
	}
}

// TestCleanupStaleFileData_ScopedToFilepath ensures cleanup affects only the
// given file. A symbol with the same ID-not-in-keep but living in a different
// file must not be touched (defense in depth — the WHERE clause should already
// scope by filepath, but tests are the documentation for that intent).
func TestCleanupStaleFileData_ScopedToFilepath(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	const codebase = "issue-7-scope"
	ensureCodebase(t, store, codebase)

	symA := Symbol{ID: "a1", CodebaseID: codebase, Name: "F", Qualified: "a.F", Kind: "function", Filepath: "a.go", LineStart: 1, LineEnd: 5}
	symB := Symbol{ID: "b1", CodebaseID: codebase, Name: "G", Qualified: "b.G", Kind: "function", Filepath: "b.go", LineStart: 1, LineEnd: 5}
	if err := store.UpsertSymbols(ctx, []Symbol{symA, symB}); err != nil {
		t.Fatalf("UpsertSymbols: %v", err)
	}

	// Cleanup for a.go with an empty keep set should drop a1, leave b1 alone.
	if err := store.CleanupStaleFileData(ctx, codebase, "a.go", nil); err != nil {
		t.Fatalf("CleanupStaleFileData: %v", err)
	}

	got, err := store.FuzzySearchSymbols(ctx, codebase, "G", "", 5)
	if err != nil {
		t.Fatalf("FuzzySearchSymbols: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b1" {
		t.Errorf("b1 should survive cleanup of a.go (got %d results: %+v)", len(got), got)
	}
}

func countRelationships(t *testing.T, store *Store, codebase string) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM relationships WHERE codebase_id = $1`, codebase,
	).Scan(&n); err != nil {
		t.Fatalf("counting relationships: %v", err)
	}
	return n
}
