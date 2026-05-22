package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/chunker"
	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/parser"
	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
	"github.com/AustinSchoen/codebase-intel/internal/summary"
)

// rawRelationship holds an unresolved relationship from parsing.
type rawRelationship struct {
	SourceQualified string
	TargetName      string
	Kind            string
	Line            int
	Filepath        string
}

// Indexer walks a codebase, parses, chunks, embeds, and stores.
type Indexer struct {
	cfg       *config.Config
	parser    *parser.Parser
	chunker   *chunker.Chunker
	embedder  *embedding.VoyageClient
	qdrant    *qdrant.Client
	store     *postgres.Store
	summarGen *summary.Generator
	logger    *log.Logger
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

	idx := &Indexer{
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
	}

	// Initialize summary generator if enabled
	if cfg.Summaries.Enabled && env.SummaryAPIKey != "" {
		idx.summarGen = summary.NewGenerator(
			env.SummaryAPIKey,
			cfg.Summaries.Model,
			store,
			cfg.Codebase.Name,
			idx.logger,
		)
	}

	return idx, nil
}

// Run performs a full index then watches for changes.
func (idx *Indexer) Run() error {
	ctx := context.Background()
	defer idx.store.Close()

	idx.logger.Printf("indexing codebase: %s at %s", idx.cfg.Codebase.Name, idx.cfg.Codebase.Path)

	if err := idx.fullIndex(ctx); err != nil {
		return fmt.Errorf("full index: %w", err)
	}

	idx.logger.Println("full index complete, starting smart watcher")
	return idx.watchSmart(ctx)
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

	totalFiles := len(files)
	idx.logger.Printf("found %d files to index", totalFiles)
	startTime := time.Now()

	// Concurrent indexing pipeline
	concurrency := idx.cfg.Indexing.ConcurrentFiles
	if concurrency <= 0 {
		concurrency = 4
	}

	type fileResult struct {
		rels    []rawRelationship
		changed bool
	}

	var (
		mu          sync.Mutex
		allRawRels  []rawRelationship
		indexed     int
		skipped     int
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, file := range files {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore
		go func(f string) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			changed, fileRels, err := idx.indexFile(ctx, f)
			if err != nil {
				idx.logger.Printf("error indexing %s: %v", f, err)
				return
			}

			mu.Lock()
			if changed {
				indexed++
				allRawRels = append(allRawRels, fileRels...)
			} else {
				skipped++
			}
			processed := indexed + skipped
			if processed%50 == 0 || processed == totalFiles {
				pct := float64(processed) / float64(totalFiles) * 100
				idx.logger.Printf("progress: %d/%d files (%.0f%%)", processed, totalFiles, pct)
			}
			mu.Unlock()
		}(file)
	}
	wg.Wait()

	elapsed := time.Since(startTime)
	rate := float64(totalFiles) / elapsed.Seconds()
	idx.logger.Printf("indexing complete: %d files in %.1fs (%.1f files/sec) — %d indexed, %d unchanged",
		totalFiles, elapsed.Seconds(), rate, indexed, skipped)

	// Resolve and store relationships
	if len(allRawRels) > 0 {
		if err := idx.resolveAndStoreRelationships(ctx, allRawRels); err != nil {
			idx.logger.Printf("warning: storing relationships: %v", err)
		}
	}

	// Refresh materialized views
	if err := idx.store.RefreshMaterializedViews(ctx); err != nil {
		idx.logger.Printf("warning: refresh materialized views: %v", err)
	}

	// Generate summaries for changed modules
	if idx.summarGen != nil && indexed > 0 {
		changedModules := make(map[string]bool)
		for _, rels := range allRawRels {
			if rels.Filepath != "" {
				parts := strings.SplitN(rels.Filepath, "/", 2)
				if len(parts) > 0 {
					changedModules[parts[0]] = true
				}
			}
		}
		if len(changedModules) > 0 {
			idx.logger.Printf("generating summaries for %d changed modules", len(changedModules))
			if err := idx.summarGen.GenerateModuleSummaries(ctx, changedModules); err != nil {
				idx.logger.Printf("warning: generating summaries: %v", err)
			}
		}
	}

	// Sweep deleted files (garbage collection)
	if err := idx.sweepDeletedFiles(ctx); err != nil {
		idx.logger.Printf("warning: gc sweep: %v", err)
	}

	return nil
}

// indexFile processes a single file through the pipeline.
// Returns true if the file was indexed (changed), plus any raw relationships extracted.
func (idx *Indexer) indexFile(ctx context.Context, path string) (bool, []rawRelationship, error) {
	relPath, err := filepath.Rel(idx.cfg.Codebase.Path, path)
	if err != nil {
		relPath = path
	}

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return false, nil, fmt.Errorf("reading file: %w", err)
	}

	// Check content hash for incremental indexing
	hash := chunker.ContentHash(content)
	if idx.cfg.Indexing.Incremental {
		existingHash, err := idx.store.GetFileHash(ctx, idx.cfg.Codebase.Name, relPath)
		if err != nil {
			return false, nil, fmt.Errorf("getting file hash: %w", err)
		}
		if existingHash == hash {
			return false, nil, nil // unchanged
		}
	}

	lang := idx.detectLanguage(path)
	if lang == "" {
		return false, nil, nil
	}

	// Parse
	result, err := idx.parser.ParseFile(ctx, relPath, content, lang)
	if err != nil {
		return false, nil, fmt.Errorf("parsing: %w", err)
	}

	// Chunk
	chunks := idx.chunker.ChunkFile(result)
	if len(chunks) == 0 {
		return false, nil, nil
	}

	// Build symbols for postgres. IDs are deterministic (chunkID derived from
	// filepath + qualified name + line), so re-indexing an unchanged symbol
	// produces the same ID and UpsertSymbols becomes a no-op DO UPDATE — which
	// is what keeps the relationships FK from cascade-deleting rows that other
	// files reference (issue #7).
	var symbols []postgres.Symbol
	keepIDs := make([]string, 0, len(chunks))
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
		keepIDs = append(keepIDs, ch.ID)
	}

	if err := idx.store.UpsertSymbols(ctx, symbols); err != nil {
		return false, nil, fmt.Errorf("upserting symbols: %w", err)
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
		return false, nil, fmt.Errorf("embedding: %w", err)
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

	// Refresh Qdrant: clear out vectors for this filepath, then upsert the new
	// set. Qdrant has no FK relationships, so a delete+upsert here is safe and
	// keeps vectors aligned with the current chunk set even if line ranges shift.
	if err := idx.qdrant.DeleteByFilter(ctx, idx.cfg.Codebase.Name, relPath); err != nil {
		idx.logger.Printf("warning: deleting old vectors for %s: %v", relPath, err)
	}
	if err := idx.qdrant.Upsert(ctx, idx.cfg.Codebase.Name, points); err != nil {
		return false, nil, fmt.Errorf("upserting vectors: %w", err)
	}

	// Upsert chunk records to Postgres
	if err := idx.store.UpsertChunks(ctx, chunkRecords); err != nil {
		return false, nil, fmt.Errorf("upserting chunk records: %w", err)
	}

	// Remove any chunk/symbol rows for this file that no longer exist in the
	// new parse result (symbols that were renamed, moved, or deleted). The
	// relationships FK cascade fires only for these genuine orphans — symbols
	// that still exist with the same ID survive, preserving incoming
	// relationships from other unchanged files. See issue #7.
	if err := idx.store.CleanupStaleFileData(ctx, idx.cfg.Codebase.Name, relPath, keepIDs); err != nil {
		return false, nil, fmt.Errorf("cleaning up stale rows: %w", err)
	}

	// Compute structural AST hash (best-effort, non-fatal)
	structuralHash, _ := computeASTHash(content, lang)

	// Update file state
	if err := idx.store.SetFileState(ctx, postgres.FileState{
		CodebaseID:     idx.cfg.Codebase.Name,
		Filepath:       relPath,
		ContentHash:    hash,
		StructuralHash: structuralHash,
		ChunkCount:     len(chunks),
	}); err != nil {
		return false, nil, fmt.Errorf("setting file state: %w", err)
	}

	// Collect raw relationships from parse result
	var rels []rawRelationship
	for _, r := range result.Relationships {
		rels = append(rels, rawRelationship{
			SourceQualified: r.SourceQualified,
			TargetName:      r.TargetName,
			Kind:            r.Kind,
			Line:            r.Line,
			Filepath:        relPath,
		})
	}

	return true, rels, nil
}

// watchSmart uses the smart watcher with event batching and branch switch detection.
func (idx *Indexer) watchSmart(ctx context.Context) error {
	sw, err := newSmartWatcher(idx)
	if err != nil {
		return fmt.Errorf("creating smart watcher: %w", err)
	}
	defer sw.close()
	return sw.run(ctx)
}

// resolveAndStoreRelationships resolves raw relationship names to symbol IDs and stores them.
func (idx *Indexer) resolveAndStoreRelationships(ctx context.Context, rawRels []rawRelationship) error {
	refs, err := idx.store.GetAllSymbolRefs(ctx, idx.cfg.Codebase.Name)
	if err != nil {
		return fmt.Errorf("getting symbol refs: %w", err)
	}

	// Build lookup maps
	qualifiedToID := make(map[string]string)
	nameToIDs := make(map[string][]string)
	for _, ref := range refs {
		qualifiedToID[ref.Qualified] = ref.ID
		nameToIDs[ref.Name] = append(nameToIDs[ref.Name], ref.ID)
	}

	var resolved []postgres.Relationship
	seen := make(map[string]bool) // dedup key: source_id:target_id:kind

	for _, raw := range rawRels {
		sourceID, ok := qualifiedToID[raw.SourceQualified]
		if !ok {
			continue
		}

		targetID := resolveTarget(raw.TargetName, qualifiedToID, nameToIDs)
		if targetID == "" || targetID == sourceID {
			continue
		}

		key := sourceID + ":" + targetID + ":" + raw.Kind
		if seen[key] {
			continue
		}
		seen[key] = true

		resolved = append(resolved, postgres.Relationship{
			CodebaseID: idx.cfg.Codebase.Name,
			SourceID:   sourceID,
			TargetID:   targetID,
			Kind:       raw.Kind,
			Filepath:   raw.Filepath,
			Line:       raw.Line,
		})
	}

	if len(resolved) == 0 {
		return nil
	}

	idx.logger.Printf("storing %d resolved relationships (from %d raw)", len(resolved), len(rawRels))
	return idx.store.UpsertRelationships(ctx, resolved)
}

// resolveTarget tries to match a target name to a symbol ID.
func resolveTarget(name string, qualifiedToID map[string]string, nameToIDs map[string][]string) string {
	// 1. Exact qualified match
	if id, ok := qualifiedToID[name]; ok {
		return id
	}

	// 2. Extract last component for suffix matching
	lastDot := strings.LastIndex(name, ".")
	shortName := name
	if lastDot >= 0 {
		shortName = name[lastDot+1:]
	}

	// 3. Try short name as qualified name
	if id, ok := qualifiedToID[shortName]; ok {
		return id
	}

	// 4. Try suffix match: "Type.Method" might be in qualifiedToID
	// For selector calls like "variable.Method", try to match "*.Method"
	if lastDot >= 0 {
		for qualified, id := range qualifiedToID {
			if strings.HasSuffix(qualified, "."+shortName) {
				return id // first match wins
			}
		}
	}

	// 5. Unambiguous short name match
	if ids, ok := nameToIDs[shortName]; ok && len(ids) == 1 {
		return ids[0]
	}

	return ""
}

// detectLanguage returns the language for a file based on extension.
func (idx *Indexer) detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	langMap := map[string]string{
		".go":    "go",
		".py":    "python",
		".ts":    "typescript",
		".tsx":   "tsx",
		".js":    "javascript",
		".jsx":   "jsx",
		".rs":    "rust",
		".c":     "c",
		".h":     "h",
		".cpp":   "cpp",
		".cc":    "cc",
		".hpp":   "hpp",
		".kt":    "kotlin",
		".kts":   "kts",
		".swift": "swift",
		".dart":  "dart",
	}

	lang, ok := langMap[ext]
	if !ok {
		return ""
	}

	// Check if this language is in the configured languages
	// Normalize language families for matching
	family := langFamily(lang)
	for _, configured := range idx.cfg.Codebase.Languages {
		if configured == lang || configured == ext[1:] || configured == family {
			return lang
		}
	}
	return ""
}

// langFamily maps language variants to their family for config matching.
func langFamily(lang string) string {
	switch lang {
	case "tsx":
		return "typescript"
	case "jsx":
		return "javascript"
	case "cc", "hpp":
		return "cpp"
	case "h":
		return "c"
	case "rs":
		return "rust"
	case "kt", "kts":
		return "kotlin"
	default:
		return lang
	}
}

// relPath returns the path relative to the codebase root.
func (idx *Indexer) relPath(path string) (string, error) {
	return filepath.Rel(idx.cfg.Codebase.Path, path)
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
