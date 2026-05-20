package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// EnsureCodebase creates or updates a codebase entry.
func (s *Store) EnsureCodebase(ctx context.Context, id, rootPath, displayName string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO codebases (id, root_path, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET root_path = $2, display_name = $3
	`, id, rootPath, displayName)
	return err
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
//
// Use this only when the file is genuinely gone (deleted on disk, removed from
// the codebase, GC'd). It cascades through the relationships FK, which is the
// correct behavior in that case.
//
// For the common incremental-reindex path — where the file still exists but
// its content changed — use CleanupStaleFileData instead, which preserves
// symbol IDs that still exist and only drops genuine orphans. See issue #7.
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

// CleanupStaleFileData removes symbols and chunks for a file whose IDs are not
// in keepIDs — i.e., symbols that were renamed, moved, or removed since the last
// index of this file. Symbols whose IDs are still in keepIDs are preserved,
// which keeps the relationships FK cascade from firing on rows that other,
// unchanged files reference (the root cause of incremental reindex losing
// relationships — see issue #7).
//
// Pass keepIDs as the IDs of the symbols just upserted for this file. The
// cascade still fires for genuinely orphaned symbols, which is correct: those
// symbols no longer exist, so relationships referencing them should also be
// removed.
func (s *Store) CleanupStaleFileData(ctx context.Context, codebaseID, filepath string, keepIDs []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Chunks: delete those whose id isn't in the new set.
	if _, err := tx.Exec(ctx, `
		DELETE FROM chunks
		WHERE codebase_id = $1 AND filepath = $2 AND NOT (id = ANY($3::text[]))
	`, codebaseID, filepath, keepIDs); err != nil {
		return fmt.Errorf("cleanup stale chunks: %w", err)
	}

	// Symbols: same. Cascade fires only for orphans (correct behavior).
	if _, err := tx.Exec(ctx, `
		DELETE FROM symbols
		WHERE codebase_id = $1 AND filepath = $2 AND NOT (id = ANY($3::text[]))
	`, codebaseID, filepath, keepIDs); err != nil {
		return fmt.Errorf("cleanup stale symbols: %w", err)
	}

	return tx.Commit(ctx)
}

// CodebaseInfo holds metadata about an indexed codebase.
type CodebaseInfo struct {
	ID          string
	DisplayName string
	RootPath    string
	FileCount   int64
	SymbolCount int64
}

// ListCodebases returns all indexed codebases with stats.
func (s *Store) ListCodebases(ctx context.Context) ([]CodebaseInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, COALESCE(c.display_name, c.id), COALESCE(c.root_path, ''),
			(SELECT COUNT(*) FROM file_state WHERE codebase_id = c.id),
			(SELECT COUNT(*) FROM symbols WHERE codebase_id = c.id)
		FROM codebases c
		ORDER BY c.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list codebases: %w", err)
	}
	defer rows.Close()

	var results []CodebaseInfo
	for rows.Next() {
		var cb CodebaseInfo
		if err := rows.Scan(&cb.ID, &cb.DisplayName, &cb.RootPath, &cb.FileCount, &cb.SymbolCount); err != nil {
			return nil, err
		}
		results = append(results, cb)
	}
	return results, rows.Err()
}

// GetCodebaseRootPath returns the root path for a codebase, or empty string if not found.
func (s *Store) GetCodebaseRootPath(ctx context.Context, codebaseID string) (string, error) {
	var rootPath string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(root_path, '') FROM codebases WHERE id = $1
	`, codebaseID).Scan(&rootPath)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return rootPath, err
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
