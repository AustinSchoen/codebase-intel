package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

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
