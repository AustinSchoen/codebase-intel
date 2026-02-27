package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
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

// RunMigrations executes all .sql files in migrationsDir in lexical order.
func (s *Store) RunMigrations(ctx context.Context, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("reading migrations dir %s: %w", migrationsDir, err)
	}

	// Sort by filename for ordered execution
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
	CodebaseID  string
	Filepath    string
	ContentHash string
	ChunkCount  int
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

// EnsureCodebase creates or updates a codebase entry.
func (s *Store) EnsureCodebase(ctx context.Context, id, rootPath, displayName string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO codebases (id, root_path, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET root_path = $2, display_name = $3
	`, id, rootPath, displayName)
	return err
}

// UpsertSymbols bulk-inserts symbols using COPY protocol.
func (s *Store) UpsertSymbols(ctx context.Context, symbols []Symbol) error {
	if len(symbols) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE tmp_symbols (LIKE symbols INCLUDING DEFAULTS) ON COMMIT DROP
	`); err != nil {
		return fmt.Errorf("create temp table: %w", err)
	}

	rows := make([][]interface{}, len(symbols))
	for i, sym := range symbols {
		rows[i] = []interface{}{
			sym.ID, sym.CodebaseID, sym.Name, sym.Qualified, sym.Kind,
			sym.Filepath, sym.LineStart, sym.LineEnd, sym.Module,
			nilIfEmpty(sym.ParentID), nilIfEmpty(sym.Signature), nilIfEmpty(sym.DocComment),
		}
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"tmp_symbols"},
		[]string{"id", "codebase_id", "name", "qualified", "kind",
			"filepath", "line_start", "line_end", "module",
			"parent_id", "signature", "doc_comment"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy symbols: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO symbols (id, codebase_id, name, qualified, kind, filepath,
			line_start, line_end, module, parent_id, signature, doc_comment)
		SELECT id, codebase_id, name, qualified, kind, filepath,
			line_start, line_end, module, parent_id, signature, doc_comment
		FROM tmp_symbols
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, qualified = EXCLUDED.qualified, kind = EXCLUDED.kind,
			filepath = EXCLUDED.filepath, line_start = EXCLUDED.line_start,
			line_end = EXCLUDED.line_end, module = EXCLUDED.module,
			parent_id = EXCLUDED.parent_id, signature = EXCLUDED.signature,
			doc_comment = EXCLUDED.doc_comment, updated_at = now()
	`); err != nil {
		return fmt.Errorf("upsert from temp: %w", err)
	}

	return tx.Commit(ctx)
}

// UpsertChunks bulk-inserts chunk records using COPY protocol.
func (s *Store) UpsertChunks(ctx context.Context, chunks []ChunkRecord) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE tmp_chunks (LIKE chunks INCLUDING DEFAULTS) ON COMMIT DROP
	`); err != nil {
		return fmt.Errorf("create temp table: %w", err)
	}

	rows := make([][]interface{}, len(chunks))
	for i, c := range chunks {
		rows[i] = []interface{}{
			c.ID, c.CodebaseID, nilIfEmpty(c.SymbolID), c.Filepath,
			c.LineStart, c.LineEnd, nilIfEmpty(c.Kind), nilIfEmpty(c.QualifiedName),
			nilIfEmpty(c.Module), c.TokenCount,
		}
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"tmp_chunks"},
		[]string{"id", "codebase_id", "symbol_id", "filepath",
			"line_start", "line_end", "kind", "qualified_name", "module", "token_count"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy chunks: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO chunks (id, codebase_id, symbol_id, filepath,
			line_start, line_end, kind, qualified_name, module, token_count)
		SELECT id, codebase_id, symbol_id, filepath,
			line_start, line_end, kind, qualified_name, module, token_count
		FROM tmp_chunks
		ON CONFLICT (id) DO UPDATE SET
			symbol_id = EXCLUDED.symbol_id, filepath = EXCLUDED.filepath,
			line_start = EXCLUDED.line_start, line_end = EXCLUDED.line_end,
			kind = EXCLUDED.kind, qualified_name = EXCLUDED.qualified_name,
			module = EXCLUDED.module, token_count = EXCLUDED.token_count
	`); err != nil {
		return fmt.Errorf("upsert from temp: %w", err)
	}

	return tx.Commit(ctx)
}

// SetFileState records that a file has been indexed.
func (s *Store) SetFileState(ctx context.Context, state FileState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO file_state (codebase_id, filepath, content_hash, chunk_count)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (codebase_id, filepath) DO UPDATE SET
			content_hash = $3, chunk_count = $4, indexed_at = now()
	`, state.CodebaseID, state.Filepath, state.ContentHash, state.ChunkCount)
	return err
}

// GetFileHash returns the stored content hash for a file, or empty string if not indexed.
func (s *Store) GetFileHash(ctx context.Context, codebaseID, filepath string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `
		SELECT content_hash FROM file_state
		WHERE codebase_id = $1 AND filepath = $2
	`, codebaseID, filepath).Scan(&hash)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// DeleteFileData removes all data for a specific file (chunks, symbols, file state).
func (s *Store) DeleteFileData(ctx context.Context, codebaseID, filepath string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE codebase_id = $1 AND filepath = $2`, codebaseID, filepath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM symbols WHERE codebase_id = $1 AND filepath = $2`, codebaseID, filepath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM file_state WHERE codebase_id = $1 AND filepath = $2`, codebaseID, filepath); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FuzzySearchSymbols finds symbols matching a query using pg_trgm similarity.
func (s *Store) FuzzySearchSymbols(ctx context.Context, codebaseID, query string, kind string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 5
	}

	q := `
		SELECT id, codebase_id, name, qualified, kind::text, filepath,
			COALESCE(line_start, 0), COALESCE(line_end, 0),
			COALESCE(module, ''), COALESCE(parent_id, ''),
			COALESCE(signature, ''), COALESCE(doc_comment, '')
		FROM symbols
		WHERE codebase_id = $1
			AND (qualified % $2 OR name % $2)
	`
	args := []interface{}{codebaseID, query}
	if kind != "" && kind != "any" {
		q += ` AND kind = $3::symbol_kind`
		args = append(args, kind)
	}
	q += fmt.Sprintf(` ORDER BY similarity(qualified, $2) DESC LIMIT %d`, limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fuzzy search: %w", err)
	}
	defer rows.Close()

	var results []Symbol
	for rows.Next() {
		var sym Symbol
		if err := rows.Scan(
			&sym.ID, &sym.CodebaseID, &sym.Name, &sym.Qualified, &sym.Kind,
			&sym.Filepath, &sym.LineStart, &sym.LineEnd,
			&sym.Module, &sym.ParentID, &sym.Signature, &sym.DocComment,
		); err != nil {
			return nil, err
		}
		results = append(results, sym)
	}
	return results, rows.Err()
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
