package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

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

	return scanReferenceResults(rows)
}

// GetClassHierarchy traverses inheritance relationships up and/or down.
func (s *Store) GetClassHierarchy(ctx context.Context, codebaseID, className, direction string, maxDepth int) ([]HierarchyNode, error) {
	if maxDepth <= 0 {
		maxDepth = 5
	}
	if direction == "" {
		direction = "both"
	}

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
				AND s.id = (
					SELECT s2.id FROM symbols s2
					WHERE s2.codebase_id = $1
						AND (s2.qualified %% $2 OR s2.name = $2)
						AND s2.kind IN ('class', 'struct')
					ORDER BY similarity(s2.qualified, $2) DESC
					LIMIT 1
				)

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

	return scanReferenceResults(rows)
}

// scanReferenceResults is a helper to scan rows of reference results.
func scanReferenceResults(rows pgx.Rows) ([]ReferenceResult, error) {
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
