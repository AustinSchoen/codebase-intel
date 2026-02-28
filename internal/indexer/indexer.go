package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"

	"github.com/AustinSchoen/codebase-intel/internal/chunker"
	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/parser"
	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
)

// Indexer walks a codebase, parses, chunks, embeds, and stores.
type Indexer struct {
	cfg      *config.Config
	parser   *parser.Parser
	chunker  *chunker.Chunker
	embedder *embedding.VoyageClient
	qdrant   *qdrant.Client
	store    *postgres.Store
	logger   *log.Logger
}

// New creates an Indexer with all pipeline components.
func New(cfg *config.Config) (*Indexer, error) {
	env, err := cfg.ResolveEnv()
	if err != nil {
		return nil, fmt.Errorf("resolving env: %w", err)
	}

	ctx := context.Background()

	store, err := postgres.NewStore(ctx, cfg.PostgresDSN(env), cfg.Metadata.MaxConnections)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}

	return &Indexer{
		cfg:     cfg,
		parser:  parser.New(),
		chunker: chunker.New(cfg.Indexing.ChunkMaxLines, cfg.Indexing.ChunkOverlapLines),
		embedder: embedding.NewVoyageClient(
			env.EmbeddingAPIKey,
			cfg.Embedding.Model,
			cfg.Embedding.Dimensions,
			cfg.Indexing.ConcurrentReqs,
		),
		qdrant: qdrant.NewClient(cfg.Vector.URL, cfg.Vector.CollectionPrefix, env.VectorAPIKey),
		store:  store,
		logger: log.New(os.Stderr, "[indexer] ", log.LstdFlags),
	}, nil
}

// Run performs a full index then watches for changes.
func (idx *Indexer) Run() error {
	ctx := context.Background()
	defer idx.store.Close()

	idx.logger.Printf("indexing codebase: %s at %s", idx.cfg.Codebase.Name, idx.cfg.Codebase.Path)

	if err := idx.fullIndex(ctx); err != nil {
		return fmt.Errorf("full index: %w", err)
	}

	idx.logger.Println("full index complete, starting file watcher")
	return idx.watch(ctx)
}

// RunOnce performs a full index without watching.
func (idx *Indexer) RunOnce() error {
	ctx := context.Background()
	defer idx.store.Close()

	idx.logger.Printf("indexing codebase: %s at %s", idx.cfg.Codebase.Name, idx.cfg.Codebase.Path)
	return idx.fullIndex(ctx)
}

// Reindex clears all data and performs a full re-index.
func (idx *Indexer) Reindex() error {
	ctx := context.Background()
	defer idx.store.Close()

	idx.logger.Printf("re-indexing codebase: %s", idx.cfg.Codebase.Name)
	// Full index handles change detection; with reindex we skip hash checks
	return idx.fullIndex(ctx)
}

func (idx *Indexer) fullIndex(ctx context.Context) error {
	// Ensure codebase exists in DB
	if err := idx.store.EnsureCodebase(ctx, idx.cfg.Codebase.Name, idx.cfg.Codebase.Path, idx.cfg.Codebase.Name); err != nil {
		return fmt.Errorf("ensure codebase: %w", err)
	}

	// Ensure Qdrant collection exists
	if err := idx.qdrant.EnsureCollection(ctx, idx.cfg.Codebase.Name, idx.cfg.Embedding.Dimensions); err != nil {
		return fmt.Errorf("ensure collection: %w", err)
	}

	// Walk codebase
	var files []string
	err := filepath.WalkDir(idx.cfg.Codebase.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			// Check exclude patterns
			relPath, _ := filepath.Rel(idx.cfg.Codebase.Path, path)
			for _, pattern := range idx.cfg.Codebase.ExcludePatterns {
				if matched, _ := filepath.Match(pattern, relPath); matched {
					return filepath.SkipDir
				}
				// Also check with ** prefix stripped
				trimmed := strings.TrimPrefix(pattern, "**/")
				if matched, _ := filepath.Match(trimmed, filepath.Base(path)); matched {
					return filepath.SkipDir
				}
			}
			return nil
		}

		lang := idx.detectLanguage(path)
		if lang == "" {
			return nil
		}

		// Check exclude patterns for files
		relPath, _ := filepath.Rel(idx.cfg.Codebase.Path, path)
		for _, pattern := range idx.cfg.Codebase.ExcludePatterns {
			if matched, _ := filepath.Match(pattern, relPath); matched {
				return nil
			}
		}

		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking codebase: %w", err)
	}

	idx.logger.Printf("found %d files to index", len(files))

	indexed := 0
	skipped := 0
	for _, file := range files {
		changed, err := idx.indexFile(ctx, file)
		if err != nil {
			idx.logger.Printf("error indexing %s: %v", file, err)
			continue
		}
		if changed {
			indexed++
		} else {
			skipped++
		}
		if (indexed+skipped)%100 == 0 {
			idx.logger.Printf("progress: %d indexed, %d skipped of %d total", indexed, skipped, len(files))
		}
	}

	idx.logger.Printf("indexing complete: %d indexed, %d unchanged", indexed, skipped)

	// Refresh materialized views
	if err := idx.store.RefreshMaterializedViews(ctx); err != nil {
		idx.logger.Printf("warning: refresh materialized views: %v", err)
	}

	return nil
}

// indexFile processes a single file through the pipeline. Returns true if the file was indexed (changed).
func (idx *Indexer) indexFile(ctx context.Context, path string) (bool, error) {
	relPath, err := filepath.Rel(idx.cfg.Codebase.Path, path)
	if err != nil {
		relPath = path
	}

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading file: %w", err)
	}

	// Check content hash for incremental indexing
	hash := chunker.ContentHash(content)
	if idx.cfg.Indexing.Incremental {
		existingHash, err := idx.store.GetFileHash(ctx, idx.cfg.Codebase.Name, relPath)
		if err != nil {
			return false, fmt.Errorf("getting file hash: %w", err)
		}
		if existingHash == hash {
			return false, nil // unchanged
		}
	}

	lang := idx.detectLanguage(path)
	if lang == "" {
		return false, nil
	}

	// Parse
	result, err := idx.parser.ParseFile(ctx, relPath, content, lang)
	if err != nil {
		return false, fmt.Errorf("parsing: %w", err)
	}

	// Chunk
	chunks := idx.chunker.ChunkFile(result)
	if len(chunks) == 0 {
		return false, nil
	}

	// Delete old data for this file
	if err := idx.store.DeleteFileData(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
		return false, fmt.Errorf("deleting old data: %w", err)
	}
	if err := idx.qdrant.DeleteByFilter(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
		idx.logger.Printf("warning: deleting old vectors for %s: %v", relPath, err)
	}

	// Build symbols for postgres
	var symbols []postgres.Symbol
	for _, ch := range chunks {
		symbols = append(symbols, postgres.Symbol{
			ID:         ch.ID,
			CodebaseID: idx.cfg.Codebase.Name,
			Name:       extractName(ch.QualifiedName),
			Qualified:  ch.QualifiedName,
			Kind:       ch.Kind,
			Filepath:   ch.Filepath,
			LineStart:  ch.LineStart,
			LineEnd:    ch.LineEnd,
			Module:     ch.Module,
		})
	}

	if err := idx.store.UpsertSymbols(ctx, symbols); err != nil {
		return false, fmt.Errorf("upserting symbols: %w", err)
	}

	// Embed chunks
	var texts []string
	for _, ch := range chunks {
		embeddingText := ch.Content
		if ch.ContextPrefix != "" {
			embeddingText = ch.ContextPrefix + "\n\n" + ch.Content
		}
		texts = append(texts, embeddingText)
	}

	vectors, err := idx.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return false, fmt.Errorf("embedding: %w", err)
	}

	// Build Qdrant points
	var points []qdrant.Point
	var chunkRecords []postgres.ChunkRecord
	for i, ch := range chunks {
		point := qdrant.Point{
			ID: ch.ID,
			Vector: map[string]interface{}{
				"dense": vectors[i],
			},
			Payload: map[string]interface{}{
				"filepath":       ch.Filepath,
				"qualified_name": ch.QualifiedName,
				"kind":           ch.Kind,
				"module":         ch.Module,
				"language":       ch.Language,
				"line_start":     ch.LineStart,
				"line_end":       ch.LineEnd,
				"content":        ch.Content,
			},
		}
		points = append(points, point)

		chunkRecords = append(chunkRecords, postgres.ChunkRecord{
			ID:            ch.ID,
			CodebaseID:    idx.cfg.Codebase.Name,
			Filepath:      ch.Filepath,
			LineStart:     ch.LineStart,
			LineEnd:       ch.LineEnd,
			Kind:          ch.Kind,
			QualifiedName: ch.QualifiedName,
			Module:        ch.Module,
			TokenCount:    chunker.EstimateTokens(ch.Content),
		})
	}

	// Upsert to Qdrant
	if err := idx.qdrant.Upsert(ctx, idx.cfg.Codebase.Name, points); err != nil {
		return false, fmt.Errorf("upserting vectors: %w", err)
	}

	// Upsert chunk records to Postgres
	if err := idx.store.UpsertChunks(ctx, chunkRecords); err != nil {
		return false, fmt.Errorf("upserting chunk records: %w", err)
	}

	// Update file state
	if err := idx.store.SetFileState(ctx, postgres.FileState{
		CodebaseID:  idx.cfg.Codebase.Name,
		Filepath:    relPath,
		ContentHash: hash,
		ChunkCount:  len(chunks),
	}); err != nil {
		return false, fmt.Errorf("setting file state: %w", err)
	}

	return true, nil
}

func (idx *Indexer) watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}
	defer watcher.Close()

	// Add all directories
	err = filepath.WalkDir(idx.cfg.Codebase.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			for _, pattern := range idx.cfg.Codebase.ExcludePatterns {
				trimmed := strings.TrimPrefix(pattern, "**/")
				if matched, _ := filepath.Match(trimmed, filepath.Base(path)); matched {
					return filepath.SkipDir
				}
			}
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("adding watch paths: %w", err)
	}

	idx.logger.Println("watching for file changes...")

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				lang := idx.detectLanguage(event.Name)
				if lang != "" {
					idx.logger.Printf("file changed: %s", event.Name)
					if _, err := idx.indexFile(ctx, event.Name); err != nil {
						idx.logger.Printf("error re-indexing %s: %v", event.Name, err)
					}
				}
			}
			if event.Op&fsnotify.Remove != 0 {
				relPath, _ := filepath.Rel(idx.cfg.Codebase.Path, event.Name)
				idx.logger.Printf("file removed: %s", relPath)
				if err := idx.store.DeleteFileData(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
					idx.logger.Printf("error cleaning up %s: %v", relPath, err)
				}
				if err := idx.qdrant.DeleteByFilter(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
					idx.logger.Printf("error deleting vectors for %s: %v", relPath, err)
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			idx.logger.Printf("watcher error: %v", err)
		}
	}
}

// detectLanguage returns the language for a file based on extension.
func (idx *Indexer) detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	langMap := map[string]string{
		".go": "go",
		".py": "python",
		".ts": "typescript",
	}

	lang, ok := langMap[ext]
	if !ok {
		return ""
	}

	// Check if this language is in the configured languages
	for _, configured := range idx.cfg.Codebase.Languages {
		if configured == lang || configured == ext[1:] {
			return lang
		}
	}
	return ""
}

// extractName gets the short name from a qualified name (e.g. "Foo.Bar" -> "Bar").
func extractName(qualified string) string {
	parts := strings.Split(qualified, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return qualified
}

// RunMigrations runs database migrations.
func (idx *Indexer) RunMigrations(ctx context.Context, migrationsDir string) error {
	return idx.store.RunMigrations(ctx, migrationsDir)
}
