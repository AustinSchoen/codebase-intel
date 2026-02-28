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
		INSERT INTO file_state (codebase_id, filepath, content_hash, structural_hash, chunk_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (codebase_id, filepath) DO UPDATE SET
			content_hash = $3, structural_hash = $4, chunk_count = $5, indexed_at = now()
	`, state.CodebaseID, state.Filepath, state.ContentHash, nilIfEmpty(state.StructuralHash), state.ChunkCount)
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

// GetAllFilePaths returns all tracked file paths for a codebase.
func (s *Store) GetAllFilePaths(ctx context.Context, codebaseID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT filepath FROM file_state WHERE codebase_id = $1
	`, codebaseID)
	if err != nil {
		return nil, fmt.Errorf("get all file paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		paths = append(paths, fp)
	}
	return paths, rows.Err()
}

// GetFileStructuralHash returns the stored structural hash for a file, or empty string if not set.
func (s *Store) GetFileStructuralHash(ctx context.Context, codebaseID, filepath string) (string, error) {
	var hash *string
	err := s.pool.QueryRow(ctx, `
		SELECT structural_hash FROM file_state
		WHERE codebase_id = $1 AND filepath = $2
	`, codebaseID, filepath).Scan(&hash)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if hash == nil {
		return "", nil
	}
	return *hash, nil
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

// UpsertRelationships bulk-inserts relationships using COPY protocol.
func (s *Store) UpsertRelationships(ctx context.Context, rels []Relationship) error {
	if len(rels) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE tmp_relationships (
			codebase_id TEXT,
			source_id TEXT,
			target_id TEXT,
			kind relationship_kind,
			filepath TEXT,
			line INTEGER
		) ON COMMIT DROP
	`); err != nil {
		return fmt.Errorf("create temp table: %w", err)
	}

	rows := make([][]interface{}, len(rels))
	for i, r := range rels {
		rows[i] = []interface{}{
			r.CodebaseID, r.SourceID, r.TargetID, r.Kind,
			nilIfEmpty(r.Filepath), nilIfZero(r.Line),
		}
	}

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"tmp_relationships"},
		[]string{"codebase_id", "source_id", "target_id", "kind", "filepath", "line"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy relationships: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO relationships (codebase_id, source_id, target_id, kind, filepath, line)
		SELECT codebase_id, source_id, target_id, kind, filepath, line
		FROM tmp_relationships
		ON CONFLICT (source_id, target_id, kind) DO UPDATE SET
			filepath = EXCLUDED.filepath, line = EXCLUDED.line
	`); err != nil {
		return fmt.Errorf("upsert from temp: %w", err)
	}

	return tx.Commit(ctx)
}

// GetReferences finds all symbols that reference a given symbol.
func (s *Store) GetReferences(ctx context.Context, codebaseID, symbolName, kind string, limit int) ([]ReferenceResult, error) {
	if limit <= 0 {
		limit = 20
	}

	q := `
		WITH target_symbols AS (
			SELECT id FROM symbols
			WHERE codebase_id = $1 AND (qualified % $2 OR name % $2)
			ORDER BY similarity(qualified, $2) DESC
			LIMIT 5
		)
		SELECT s.name, s.qualified, s.kind::text, s.filepath,
			COALESCE(s.line_start, 0), COALESCE(s.line_end, 0),
			COALESCE(s.module, ''), r.kind::text, COALESCE(r.line, 0)
		FROM relationships r
		JOIN symbols s ON s.id = r.source_id
		WHERE r.target_id IN (SELECT id FROM target_symbols)
	`
	args := []interface{}{codebaseID, symbolName}
	if kind != "" && kind != "any" {
		q += ` AND r.kind = $3::relationship_kind`
		args = append(args, kind)
	}
	q += fmt.Sprintf(` ORDER BY s.qualified LIMIT %d`, limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get references: %w", err)
	}
	defer rows.Close()

	var results []ReferenceResult
	for rows.Next() {
		var r ReferenceResult
		if err := rows.Scan(
			&r.SymbolName, &r.SymbolQualified, &r.SymbolKind, &r.Filepath,
			&r.LineStart, &r.LineEnd, &r.Module, &r.RefKind, &r.RefLine,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetClassHierarchy traverses inheritance relationships up and/or down.
func (s *Store) GetClassHierarchy(ctx context.Context, codebaseID, className, direction string, maxDepth int) ([]HierarchyNode, error) {
	if maxDepth <= 0 {
		maxDepth = 5
	}
	if direction == "" {
		direction = "both"
	}

	// Build direction filter for recursive term
	var dirFilter string
	switch direction {
	case "parents":
		dirFilter = "r.source_id = h.id"
	case "children":
		dirFilter = "r.target_id = h.id"
	default:
		dirFilter = "(r.source_id = h.id OR r.target_id = h.id)"
	}

	q := fmt.Sprintf(`
		WITH RECURSIVE hierarchy AS (
			SELECT s.id, s.qualified, s.kind::text, 0 as depth, 'self'::text as direction
			FROM symbols s
			WHERE s.codebase_id = $1
				AND (s.qualified %% $2 OR s.name = $2)
				AND s.kind IN ('class', 'struct')
			ORDER BY similarity(s.qualified, $2) DESC
			LIMIT 1

			UNION ALL

			SELECT s2.id, s2.qualified, s2.kind::text, h.depth + 1,
				CASE WHEN r.source_id = h.id THEN 'parent' ELSE 'child' END::text
			FROM hierarchy h
			JOIN relationships r ON %s AND r.kind = 'inherits'
			JOIN symbols s2 ON s2.id = CASE
				WHEN r.source_id = h.id THEN r.target_id
				ELSE r.source_id
			END
			WHERE h.depth < $3
			AND s2.id != h.id
		)
		SELECT DISTINCT ON (id) id, qualified, kind, depth, direction
		FROM hierarchy
		ORDER BY id, depth
	`, dirFilter)

	rows, err := s.pool.Query(ctx, q, codebaseID, className, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("get class hierarchy: %w", err)
	}
	defer rows.Close()

	var results []HierarchyNode
	for rows.Next() {
		var n HierarchyNode
		if err := rows.Scan(&n.ID, &n.Qualified, &n.Kind, &n.Depth, &n.Direction); err != nil {
			return nil, err
		}
		results = append(results, n)
	}
	return results, rows.Err()
}

// GetFileSymbols returns all symbols defined in a specific file.
func (s *Store) GetFileSymbols(ctx context.Context, codebaseID, filepath string) ([]Symbol, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, codebase_id, name, qualified, kind::text, filepath,
			COALESCE(line_start, 0), COALESCE(line_end, 0),
			COALESCE(module, ''), COALESCE(parent_id, ''),
			COALESCE(signature, ''), COALESCE(doc_comment, '')
		FROM symbols
		WHERE codebase_id = $1 AND filepath = $2
		ORDER BY line_start
	`, codebaseID, filepath)
	if err != nil {
		return nil, fmt.Errorf("get file symbols: %w", err)
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

// GetFileDependents finds symbols from other files that reference symbols in the given file.
func (s *Store) GetFileDependents(ctx context.Context, codebaseID, filepath string, limit int) ([]ReferenceResult, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT s.name, s.qualified, s.kind::text, s.filepath,
			COALESCE(s.line_start, 0), COALESCE(s.line_end, 0),
			COALESCE(s.module, ''), r.kind::text, COALESCE(r.line, 0)
		FROM relationships r
		JOIN symbols s ON s.id = r.source_id
		JOIN symbols target ON target.id = r.target_id
		WHERE target.codebase_id = $1 AND target.filepath = $2
		AND s.filepath != $2
		ORDER BY s.qualified
		LIMIT %d
	`, limit), codebaseID, filepath)
	if err != nil {
		return nil, fmt.Errorf("get file dependents: %w", err)
	}
	defer rows.Close()

	var results []ReferenceResult
	for rows.Next() {
		var r ReferenceResult
		if err := rows.Scan(
			&r.SymbolName, &r.SymbolQualified, &r.SymbolKind, &r.Filepath,
			&r.LineStart, &r.LineEnd, &r.Module, &r.RefKind, &r.RefLine,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetAllSymbolRefs returns lightweight references for all symbols in a codebase,
// used for resolving relationship names to IDs.
func (s *Store) GetAllSymbolRefs(ctx context.Context, codebaseID string) ([]SymbolRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, qualified FROM symbols WHERE codebase_id = $1
	`, codebaseID)
	if err != nil {
		return nil, fmt.Errorf("get symbol refs: %w", err)
	}
	defer rows.Close()

	var results []SymbolRef
	for rows.Next() {
		var ref SymbolRef
		if err := rows.Scan(&ref.ID, &ref.Name, &ref.Qualified); err != nil {
			return nil, err
		}
		results = append(results, ref)
	}
	return results, rows.Err()
}

// Summary represents a pre-computed LLM-generated summary.
type Summary struct {
	ID           string
	CodebaseID   string
	Scope        string // module path or class qualified name
	Level        string // module, class, subsystem
	SummaryText  string
	KeyClasses   string // JSON array
	Dependencies string // JSON array
	SourceHash   string
}

// UpsertSummary inserts or updates a summary record.
func (s *Store) UpsertSummary(ctx context.Context, sum Summary) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO summaries (id, codebase_id, scope, level, summary, key_classes, dependencies, source_hash)
		VALUES ($1, $2, $3, $4::summary_level, $5, $6::jsonb, $7::jsonb, $8)
		ON CONFLICT (codebase_id, scope, level) DO UPDATE SET
			summary = EXCLUDED.summary,
			key_classes = EXCLUDED.key_classes,
			dependencies = EXCLUDED.dependencies,
			source_hash = EXCLUDED.source_hash,
			updated_at = now()
	`, sum.ID, sum.CodebaseID, sum.Scope, sum.Level, sum.SummaryText,
		sum.KeyClasses, sum.Dependencies, nilIfEmpty(sum.SourceHash))
	return err
}

// GetSummary retrieves a summary by codebase, scope, and level.
func (s *Store) GetSummary(ctx context.Context, codebaseID, scope, level string) (*Summary, error) {
	var sum Summary
	err := s.pool.QueryRow(ctx, `
		SELECT id, codebase_id, scope, level::text, summary,
			COALESCE(key_classes::text, '[]'), COALESCE(dependencies::text, '[]'),
			COALESCE(source_hash, '')
		FROM summaries
		WHERE codebase_id = $1 AND scope = $2 AND level = $3::summary_level
	`, codebaseID, scope, level).Scan(
		&sum.ID, &sum.CodebaseID, &sum.Scope, &sum.Level,
		&sum.SummaryText, &sum.KeyClasses, &sum.Dependencies, &sum.SourceHash,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}
	return &sum, nil
}

// ListSummaries returns all summaries for a codebase, optionally filtered by level.
func (s *Store) ListSummaries(ctx context.Context, codebaseID, level string) ([]Summary, error) {
	q := `
		SELECT id, codebase_id, scope, level::text, summary,
			COALESCE(key_classes::text, '[]'), COALESCE(dependencies::text, '[]'),
			COALESCE(source_hash, '')
		FROM summaries
		WHERE codebase_id = $1
	`
	args := []interface{}{codebaseID}
	if level != "" {
		q += ` AND level = $2::summary_level`
		args = append(args, level)
	}
	q += ` ORDER BY scope`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list summaries: %w", err)
	}
	defer rows.Close()

	var results []Summary
	for rows.Next() {
		var sum Summary
		if err := rows.Scan(
			&sum.ID, &sum.CodebaseID, &sum.Scope, &sum.Level,
			&sum.SummaryText, &sum.KeyClasses, &sum.Dependencies, &sum.SourceHash,
		); err != nil {
			return nil, err
		}
		results = append(results, sum)
	}
	return results, rows.Err()
}

// GetModuleFiles returns all distinct filepaths for symbols in a given module.
func (s *Store) GetModuleFiles(ctx context.Context, codebaseID, module string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT filepath FROM symbols
		WHERE codebase_id = $1 AND module = $2
		ORDER BY filepath
	`, codebaseID, module)
	if err != nil {
		return nil, fmt.Errorf("get module files: %w", err)
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		files = append(files, fp)
	}
	return files, rows.Err()
}

// GetModuleSymbolSummary returns top symbols in a module for summary generation.
func (s *Store) GetModuleSymbolSummary(ctx context.Context, codebaseID, module string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, codebase_id, name, qualified, kind::text, filepath,
			COALESCE(line_start, 0), COALESCE(line_end, 0),
			COALESCE(module, ''), COALESCE(parent_id, ''),
			COALESCE(signature, ''), COALESCE(doc_comment, '')
		FROM symbols
		WHERE codebase_id = $1 AND module = $2
			AND kind IN ('class', 'struct', 'function')
		ORDER BY kind, qualified
		LIMIT %d
	`, limit), codebaseID, module)
	if err != nil {
		return nil, fmt.Errorf("get module symbol summary: %w", err)
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

// ImportantSymbol holds a symbol with its importance ranking.
type ImportantSymbol struct {
	Qualified    string
	Kind         string
	Module       string
	Signature    string
	IncomingRefs int
}

// GetTopSymbolsByImportance returns the most-referenced symbols from the symbol_importance view.
func (s *Store) GetTopSymbolsByImportance(ctx context.Context, codebaseID string, limit int) ([]ImportantSymbol, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT si.qualified, si.kind, COALESCE(si.module, ''),
			COALESCE(sym.signature, ''), COALESCE(si.incoming_refs, 0)
		FROM symbol_importance si
		LEFT JOIN symbols sym ON sym.id = si.id
		WHERE si.codebase_id = $1 AND si.incoming_refs > 0
		ORDER BY si.incoming_refs DESC
		LIMIT %d
	`, limit), codebaseID)
	if err != nil {
		return nil, fmt.Errorf("get top symbols: %w", err)
	}
	defer rows.Close()

	var results []ImportantSymbol
	for rows.Next() {
		var sym ImportantSymbol
		if err := rows.Scan(&sym.Qualified, &sym.Kind, &sym.Module, &sym.Signature, &sym.IncomingRefs); err != nil {
			return nil, err
		}
		results = append(results, sym)
	}
	return results, rows.Err()
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

// GetIndexCounts returns counts of symbols, chunks (file_state entries), and relationships for a codebase.
func (s *Store) GetIndexCounts(ctx context.Context, codebaseName string) (symbols, files, relationships int64, err error) {
	row := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM symbols WHERE codebase_id = $1`, codebaseName)
	if err = row.Scan(&symbols); err != nil {
		return
	}
	row = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM file_state WHERE codebase_id = $1`, codebaseName)
	if err = row.Scan(&files); err != nil {
		return
	}
	row = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relationships WHERE codebase_id = $1`, codebaseName)
	if err = row.Scan(&relationships); err != nil {
		return
	}
	return
}
