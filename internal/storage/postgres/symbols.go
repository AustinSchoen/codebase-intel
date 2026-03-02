package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

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

	return scanSymbols(rows)
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

	return scanSymbols(rows)
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

	return scanSymbols(rows)
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

// scanSymbols is a helper to scan rows of symbols.
func scanSymbols(rows pgx.Rows) ([]Symbol, error) {
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
