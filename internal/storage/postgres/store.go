package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store manages PostgreSQL operations for codebase metadata.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a connection pool and returns a Store.
func NewStore(ctx context.Context, dsn string, maxConns int) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing dsn: %w", err)
	}
	cfg.MaxConns = int32(maxConns)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close shuts down the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies the connection pool can reach Postgres. Used by the /ready
// HTTP endpoint as a runtime liveness check.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// RunMigrations executes all .sql files in migrationsDir in lexical order.
func (s *Store) RunMigrations(ctx context.Context, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("reading migrations dir %s: %w", migrationsDir, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		path := filepath.Join(migrationsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", entry.Name(), err)
		}
		if _, err := s.pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("executing migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// Symbol represents a code symbol stored in PostgreSQL.
type Symbol struct {
	ID         string
	CodebaseID string
	Name       string
	Qualified  string
	Kind       string
	Filepath   string
	LineStart  int
	LineEnd    int
	Module     string
	ParentID   string
	Signature  string
	DocComment string
}

// ChunkRecord tracks a chunk stored in Qdrant.
type ChunkRecord struct {
	ID            string
	CodebaseID    string
	SymbolID      string
	Filepath      string
	LineStart     int
	LineEnd       int
	Kind          string
	QualifiedName string
	Module        string
	TokenCount    int
}

// FileState tracks content hash for incremental indexing.
type FileState struct {
	CodebaseID     string
	Filepath       string
	ContentHash    string
	StructuralHash string
	ChunkCount     int
}

// ModuleStat holds aggregated module statistics.
type ModuleStat struct {
	CodebaseID    string
	Module        string
	ClassCount    int
	FunctionCount int
	StructCount   int
	FileCount     int
	EstimatedLOC  int
}

// Relationship represents a resolved relationship between two symbols.
type Relationship struct {
	CodebaseID string
	SourceID   string
	TargetID   string
	Kind       string
	Filepath   string
	Line       int
}

// ReferenceResult holds a symbol that references another symbol.
type ReferenceResult struct {
	SymbolName      string
	SymbolQualified string
	SymbolKind      string
	Filepath        string
	LineStart       int
	LineEnd         int
	Module          string
	RefKind         string
	RefLine         int
}

// HierarchyNode represents a node in an inheritance hierarchy.
type HierarchyNode struct {
	ID        string
	Qualified string
	Kind      string
	Depth     int
	Direction string // self, parent, child
}

// SymbolRef is a lightweight reference for name-to-ID resolution.
type SymbolRef struct {
	ID        string
	Name      string
	Qualified string
}

// ListModules returns module statistics for a codebase.
func (s *Store) ListModules(ctx context.Context, codebaseID string, parent string) ([]ModuleStat, error) {
	q := `
		SELECT codebase_id, module,
			COALESCE(class_count, 0), COALESCE(function_count, 0),
			COALESCE(struct_count, 0), COALESCE(file_count, 0),
			COALESCE(estimated_loc, 0)
		FROM module_stats
		WHERE codebase_id = $1
	`
	args := []interface{}{codebaseID}
	if parent != "" {
		q += ` AND module LIKE $2`
		args = append(args, parent+"%")
	}
	q += ` ORDER BY module`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	defer rows.Close()

	var results []ModuleStat
	for rows.Next() {
		var m ModuleStat
		if err := rows.Scan(
			&m.CodebaseID, &m.Module,
			&m.ClassCount, &m.FunctionCount, &m.StructCount,
			&m.FileCount, &m.EstimatedLOC,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// RefreshMaterializedViews refreshes the precomputed views.
func (s *Store) RefreshMaterializedViews(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY symbol_importance`); err != nil {
		return fmt.Errorf("refresh symbol_importance: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY module_stats`); err != nil {
		return fmt.Errorf("refresh module_stats: %w", err)
	}
	return nil
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}
