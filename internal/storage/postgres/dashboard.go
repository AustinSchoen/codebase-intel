package postgres

import (
	"context"
	"fmt"
	"time"
)

// CodebaseStats holds detailed statistics for a single codebase.
type CodebaseStats struct {
	ID               string     `json:"id"`
	DisplayName      string     `json:"display_name"`
	RootPath         string     `json:"root_path"`
	FileCount        int64      `json:"file_count"`
	SymbolCount      int64      `json:"symbol_count"`
	RelationshipCount int64     `json:"relationship_count"`
	ChunkCount       int64      `json:"chunk_count"`
	LastIndexedAt    *time.Time `json:"last_indexed_at"`
}

// GetCodebaseStats returns detailed stats for a single codebase.
func (s *Store) GetCodebaseStats(ctx context.Context, codebaseID string) (*CodebaseStats, error) {
	cs := &CodebaseStats{ID: codebaseID}
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(c.display_name, c.id), COALESCE(c.root_path, '')
		FROM codebases c WHERE c.id = $1
	`, codebaseID).Scan(&cs.DisplayName, &cs.RootPath)
	if err != nil {
		return nil, fmt.Errorf("get codebase %s: %w", codebaseID, err)
	}

	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM file_state WHERE codebase_id = $1`, codebaseID).Scan(&cs.FileCount)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM symbols WHERE codebase_id = $1`, codebaseID).Scan(&cs.SymbolCount)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relationships WHERE codebase_id = $1`, codebaseID).Scan(&cs.RelationshipCount)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE codebase_id = $1`, codebaseID).Scan(&cs.ChunkCount)

	var lastIndexed *time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT MAX(indexed_at) FROM file_state WHERE codebase_id = $1
	`, codebaseID).Scan(&lastIndexed)
	if err == nil {
		cs.LastIndexedAt = lastIndexed
	}

	return cs, nil
}

// GetAllCodebaseStats returns stats for all codebases.
func (s *Store) GetAllCodebaseStats(ctx context.Context) ([]CodebaseStats, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM codebases ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list codebases: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []CodebaseStats
	for _, id := range ids {
		cs, err := s.GetCodebaseStats(ctx, id)
		if err != nil {
			continue
		}
		results = append(results, *cs)
	}
	return results, nil
}

// GlobalStats holds aggregate counts across all codebases.
type GlobalStats struct {
	TotalFiles         int64 `json:"total_files"`
	TotalSymbols       int64 `json:"total_symbols"`
	TotalRelationships int64 `json:"total_relationships"`
	TotalChunks        int64 `json:"total_chunks"`
	CodebaseCount      int64 `json:"codebase_count"`
}

// GetGlobalStats returns aggregate counts across all codebases.
func (s *Store) GetGlobalStats(ctx context.Context) (*GlobalStats, error) {
	gs := &GlobalStats{}
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM file_state`).Scan(&gs.TotalFiles)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM symbols`).Scan(&gs.TotalSymbols)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relationships`).Scan(&gs.TotalRelationships)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&gs.TotalChunks)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM codebases`).Scan(&gs.CodebaseCount)
	return gs, nil
}
