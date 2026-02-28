package indexer

import (
	"context"
	"os"
	"path/filepath"
)

// sweepDeletedFiles queries all known file paths from Postgres file_state,
// compares against what exists on disk, and removes stale entries.
func (idx *Indexer) sweepDeletedFiles(ctx context.Context) error {
	files, err := idx.store.GetAllFilePaths(ctx, idx.cfg.Codebase.Name)
	if err != nil {
		return err
	}

	var deleted []string
	for _, relPath := range files {
		absPath := filepath.Join(idx.cfg.Codebase.Path, relPath)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			deleted = append(deleted, relPath)
		}
	}

	if len(deleted) == 0 {
		return nil
	}

	idx.logger.Printf("gc: sweeping %d deleted files", len(deleted))

	for _, relPath := range deleted {
		// Delete from Postgres (symbols, chunks, file_state)
		if err := idx.store.DeleteFileData(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
			idx.logger.Printf("gc: error deleting postgres data for %s: %v", relPath, err)
			continue
		}
		// Delete vectors from Qdrant
		if err := idx.qdrant.DeleteByFilter(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
			idx.logger.Printf("gc: error deleting qdrant vectors for %s: %v", relPath, err)
		}
	}

	idx.logger.Printf("gc: cleaned up %d deleted files", len(deleted))
	return nil
}
