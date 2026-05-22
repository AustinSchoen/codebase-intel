// Package migrations bundles the SQL files under repo-root /migrations/ into
// the server binary via go:embed. The server runs them on startup so a fresh
// install needs only one binary, not the binary plus a separate migrations
// directory.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
)

//go:embed *.sql
var fsys embed.FS

// Run applies every embedded .sql file in lexical order. The Store's
// underlying pool handles each file as a single statement bundle; migrations
// must be idempotent on their own (the schema files use CREATE TABLE IF NOT
// EXISTS, etc.) since there's no migrations-tracking table.
func Run(ctx context.Context, store *postgres.Store) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("listing embedded migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		sql, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if err := store.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
	}
	return nil
}
