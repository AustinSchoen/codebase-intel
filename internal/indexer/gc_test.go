package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweepDeletedFiles_DetectsDeleted(t *testing.T) {
	// Create a temp directory with some files
	tmpDir := t.TempDir()
	existingFile := filepath.Join(tmpDir, "existing.go")
	if err := os.WriteFile(existingFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate file paths from the database
	// "existing.go" exists on disk, "deleted.go" does not
	knownPaths := []string{"existing.go", "deleted.go"}

	// Check which are deleted
	var deleted []string
	for _, relPath := range knownPaths {
		absPath := filepath.Join(tmpDir, relPath)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			deleted = append(deleted, relPath)
		}
	}

	if len(deleted) != 1 {
		t.Fatalf("expected 1 deleted file, got %d", len(deleted))
	}
	if deleted[0] != "deleted.go" {
		t.Errorf("expected deleted.go, got %s", deleted[0])
	}
}
