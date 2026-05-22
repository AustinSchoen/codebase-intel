// Package pipeline runs the per-file indexing pipeline (parse → chunk → embed
// → store) on the server side. It used to live in internal/indexer.Indexer,
// where it ran on the host that owned the source files; under the thin-client
// architecture (issue #18) it runs on the central server and is driven by
// HTTP uploads from indexer daemons that ship file content.
//
// The pipeline operates on file content passed in by the caller, not on
// paths it reads from disk — that's the key thin-client move. Callers
// (the MCP server's /mcp/indexer/files handler, or the in-process one-shot
// indexer running on the server host) are responsible for collecting content
// and feeding it in.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AustinSchoen/codebase-intel/internal/chunker"
	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/parser"
	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
)

// Pipeline owns the per-file indexing stages and the backends they write to.
// One instance is shared across all incoming indexer requests on a server.
type Pipeline struct {
	parser   *parser.Parser
	chunker  *chunker.Chunker
	embedder *embedding.VoyageClient
	qdrant   *qdrant.Client
	store    *postgres.Store

	// indexingCfg holds the chunking + concurrency knobs that used to live
	// in each codebase config. With the thin client they're a server-wide
	// concern — incoming files use the server's chunking parameters.
	indexingCfg config.IndexingConfig

	// embeddingCfg carries the embedding model + dimensions; the latter is
	// needed when creating new qdrant collections.
	embeddingCfg config.EmbeddingConfig

	logger *log.Logger
}

// New constructs a Pipeline. Backends are passed in (rather than built here)
// so the server can construct them once at startup and share them.
func New(
	indexingCfg config.IndexingConfig,
	embeddingCfg config.EmbeddingConfig,
	store *postgres.Store,
	qdr *qdrant.Client,
	embedder *embedding.VoyageClient,
	logger *log.Logger,
) (*Pipeline, error) {
	return &Pipeline{
		parser:       parser.New(),
		chunker:      chunker.New(indexingCfg.ChunkMaxLines, indexingCfg.ChunkOverlapLines),
		embedder:     embedder,
		qdrant:       qdr,
		store:        store,
		indexingCfg:  indexingCfg,
		embeddingCfg: embeddingCfg,
		logger:       logger,
	}, nil
}

// FileInput is the per-file payload IndexFile operates on. Filepath is the
// path relative to the codebase root (the daemon strips the prefix before
// sending).
type FileInput struct {
	Codebase string
	Filepath string
	Content  []byte
}

// RawRelationship is an unresolved relationship extracted from a parse
// result. The pipeline collects these per-file and the caller buffers them
// across a request — relationships across files need to wait until both
// endpoints exist in the symbols table before they can resolve.
type RawRelationship struct {
	SourceQualified string
	TargetName      string
	Kind            string
	Line            int
	Filepath        string
}

// IndexFile runs the per-file pipeline on a single file. Returns true if the
// file was actually re-indexed (false means it was unchanged and skipped via
// content-hash dedup when incremental is true), plus any raw relationships
// extracted by the parser that the caller should buffer and resolve later via
// FinalizeRelationships.
func (p *Pipeline) IndexFile(ctx context.Context, in FileInput, incremental bool) (bool, []RawRelationship, error) {
	hash := chunker.ContentHash(in.Content)
	if incremental {
		existingHash, err := p.store.GetFileHash(ctx, in.Codebase, in.Filepath)
		if err != nil {
			return false, nil, fmt.Errorf("getting file hash: %w", err)
		}
		if existingHash == hash {
			return false, nil, nil
		}
	}

	lang := DetectLanguage(in.Filepath)
	if lang == "" {
		return false, nil, nil
	}

	result, err := p.parser.ParseFile(ctx, in.Filepath, in.Content, lang)
	if err != nil {
		return false, nil, fmt.Errorf("parsing: %w", err)
	}

	chunks := p.chunker.ChunkFile(result)
	if len(chunks) == 0 {
		return false, nil, nil
	}

	// Symbol IDs are content-derived (chunkID = sha256(filepath, qualified,
	// line_start)). Re-indexing an unchanged symbol produces the same ID and
	// UpsertSymbols becomes a no-op DO UPDATE, which preserves cross-file
	// relationship rows that reference it (issue #7).
	symbols := make([]postgres.Symbol, 0, len(chunks))
	keepIDs := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		symbols = append(symbols, postgres.Symbol{
			ID:         ch.ID,
			CodebaseID: in.Codebase,
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
	if err := p.store.UpsertSymbols(ctx, symbols); err != nil {
		return false, nil, fmt.Errorf("upserting symbols: %w", err)
	}

	// Embed chunks.
	texts := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		embeddingText := ch.Content
		if ch.ContextPrefix != "" {
			embeddingText = ch.ContextPrefix + "\n\n" + ch.Content
		}
		texts = append(texts, embeddingText)
	}
	vectors, err := p.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return false, nil, fmt.Errorf("embedding: %w", err)
	}

	// Build qdrant points + chunk records.
	points := make([]qdrant.Point, 0, len(chunks))
	chunkRecords := make([]postgres.ChunkRecord, 0, len(chunks))
	for i, ch := range chunks {
		points = append(points, qdrant.Point{
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
		})
		chunkRecords = append(chunkRecords, postgres.ChunkRecord{
			ID:            ch.ID,
			CodebaseID:    in.Codebase,
			Filepath:      ch.Filepath,
			LineStart:     ch.LineStart,
			LineEnd:       ch.LineEnd,
			Kind:          ch.Kind,
			QualifiedName: ch.QualifiedName,
			Module:        ch.Module,
			TokenCount:    chunker.EstimateTokens(ch.Content),
		})
	}

	// Qdrant has no FK relationships, so delete+upsert is safe and keeps
	// vectors aligned even if a chunk's line range shifts.
	if err := p.qdrant.DeleteByFilter(ctx, in.Codebase, in.Filepath); err != nil {
		p.logger.Printf("warning: deleting old vectors for %s: %v", in.Filepath, err)
	}
	if err := p.qdrant.Upsert(ctx, in.Codebase, points); err != nil {
		return false, nil, fmt.Errorf("upserting vectors: %w", err)
	}

	if err := p.store.UpsertChunks(ctx, chunkRecords); err != nil {
		return false, nil, fmt.Errorf("upserting chunk records: %w", err)
	}

	// Drop chunks/symbols for this file whose IDs aren't in the new parse
	// result — handles renames, moves, and removals. Cascade through the
	// relationships FK fires only for these genuine orphans (issue #7).
	if err := p.store.CleanupStaleFileData(ctx, in.Codebase, in.Filepath, keepIDs); err != nil {
		return false, nil, fmt.Errorf("cleaning up stale rows: %w", err)
	}

	structuralHash, _ := computeASTHash(in.Content, lang)

	if err := p.store.SetFileState(ctx, postgres.FileState{
		CodebaseID:     in.Codebase,
		Filepath:       in.Filepath,
		ContentHash:    hash,
		StructuralHash: structuralHash,
		ChunkCount:     len(chunks),
	}); err != nil {
		return false, nil, fmt.Errorf("setting file state: %w", err)
	}

	rels := make([]RawRelationship, 0, len(result.Relationships))
	for _, r := range result.Relationships {
		rels = append(rels, RawRelationship{
			SourceQualified: r.SourceQualified,
			TargetName:      r.TargetName,
			Kind:            r.Kind,
			Line:            r.Line,
			Filepath:        in.Filepath,
		})
	}
	return true, rels, nil
}

// FinalizeRelationships resolves a batch of raw relationships against the
// codebase's symbol table and stores the resolved ones. Callers should buffer
// raw relationships across IndexFile calls (so cross-file rels can resolve
// against symbols added later in the same indexing pass) and invoke this once
// at the end.
//
// Relationships whose source can't be found are dropped silently — that's the
// expected outcome for calls into external packages. Relationships whose
// target can't be found are also dropped (they targeted code outside the
// codebase). Self-loops (source == target) are dropped.
func (p *Pipeline) FinalizeRelationships(ctx context.Context, codebase string, raw []RawRelationship) error {
	if len(raw) == 0 {
		return nil
	}

	refs, err := p.store.GetAllSymbolRefs(ctx, codebase)
	if err != nil {
		return fmt.Errorf("getting symbol refs: %w", err)
	}

	qualifiedToID := make(map[string]string, len(refs))
	nameToIDs := make(map[string][]string, len(refs))
	for _, ref := range refs {
		qualifiedToID[ref.Qualified] = ref.ID
		nameToIDs[ref.Name] = append(nameToIDs[ref.Name], ref.ID)
	}

	resolved := make([]postgres.Relationship, 0, len(raw))
	seen := make(map[string]bool, len(raw))

	for _, r := range raw {
		sourceID, ok := qualifiedToID[r.SourceQualified]
		if !ok {
			continue
		}
		targetID := resolveTarget(r.TargetName, qualifiedToID, nameToIDs)
		if targetID == "" || targetID == sourceID {
			continue
		}
		key := sourceID + ":" + targetID + ":" + r.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved = append(resolved, postgres.Relationship{
			CodebaseID: codebase,
			SourceID:   sourceID,
			TargetID:   targetID,
			Kind:       r.Kind,
			Filepath:   r.Filepath,
			Line:       r.Line,
		})
	}

	if len(resolved) == 0 {
		return nil
	}
	p.logger.Printf("storing %d resolved relationships (from %d raw) for codebase %s", len(resolved), len(raw), codebase)
	return p.store.UpsertRelationships(ctx, resolved)
}

// GC removes file_state rows (and their chunks/symbols/vectors) for files no
// longer present on the indexer host. Daemons walk their codebase tree, build
// a "keep" list, and post it to the server — anything not in keep gets dropped
// here.
func (p *Pipeline) GC(ctx context.Context, codebase string, keepFiles []string) error {
	currentFiles, err := p.store.GetAllFilePaths(ctx, codebase)
	if err != nil {
		return fmt.Errorf("listing current files: %w", err)
	}
	keep := make(map[string]bool, len(keepFiles))
	for _, f := range keepFiles {
		keep[f] = true
	}
	var orphans []string
	for _, f := range currentFiles {
		if !keep[f] {
			orphans = append(orphans, f)
		}
	}
	for _, orphan := range orphans {
		if err := p.store.DeleteFileData(ctx, codebase, orphan); err != nil {
			p.logger.Printf("warning: deleting orphan postgres rows for %s: %v", orphan, err)
		}
		if err := p.qdrant.DeleteByFilter(ctx, codebase, orphan); err != nil {
			p.logger.Printf("warning: deleting orphan vectors for %s: %v", orphan, err)
		}
	}
	if len(orphans) > 0 {
		p.logger.Printf("gc: dropped %d orphan files from codebase %s", len(orphans), codebase)
	}
	return nil
}

// DeleteFile handles a single file removal (typically from a watcher event).
// Wipes postgres rows + qdrant vectors for that file.
func (p *Pipeline) DeleteFile(ctx context.Context, codebase, filepath string) error {
	if err := p.store.DeleteFileData(ctx, codebase, filepath); err != nil {
		return fmt.Errorf("deleting postgres rows: %w", err)
	}
	if err := p.qdrant.DeleteByFilter(ctx, codebase, filepath); err != nil {
		p.logger.Printf("warning: deleting vectors for %s: %v", filepath, err)
	}
	return nil
}

// EnsureCodebase registers a codebase row before any files are indexed AND
// creates the corresponding qdrant collection. The latter used to live in
// indexer.fullIndex; when the indexer became a thin client it stopped being
// called for new codebases, which caused 404s on vector upsert until a
// collection was created out-of-band. Both operations are idempotent.
func (p *Pipeline) EnsureCodebase(ctx context.Context, codebase, displayName, rootPath string) error {
	if err := p.store.EnsureCodebase(ctx, codebase, rootPath, displayName); err != nil {
		return err
	}
	if err := p.qdrant.EnsureCollection(ctx, codebase, p.embeddingCfg.Dimensions); err != nil {
		return fmt.Errorf("ensuring qdrant collection: %w", err)
	}
	return nil
}

// DetectLanguage maps a file path's extension to a language tag the parser
// recognizes. Returns the empty string for unsupported extensions; callers
// should treat that as "skip this file."
func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescript"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascript"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".hpp":
		return "cpp"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".dart":
		return "dart"
	}
	return ""
}

// extractName pulls the unqualified symbol name out of a qualified name like
// "Server.Run" -> "Run" or "pkg.Type.Method" -> "Method".
func extractName(qualified string) string {
	if idx := strings.LastIndex(qualified, "."); idx >= 0 {
		return qualified[idx+1:]
	}
	return qualified
}

// resolveTarget attempts to map a target name to a symbol ID. Prefers a
// qualified-name match (which is unique) and falls back to a name-only match
// (potentially ambiguous; picks the first).
func resolveTarget(target string, qualifiedToID map[string]string, nameToIDs map[string][]string) string {
	if id, ok := qualifiedToID[target]; ok {
		return id
	}
	// Strip pointer prefix (Go) and try again.
	if strings.HasPrefix(target, "*") {
		if id, ok := qualifiedToID[target[1:]]; ok {
			return id
		}
	}
	if ids, ok := nameToIDs[target]; ok && len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// RelBuffer accumulates raw relationships from multiple IndexFile calls so
// the caller can flush them all at once. Safe for concurrent IndexFile
// invocations from the same logical request.
type RelBuffer struct {
	mu   sync.Mutex
	rels []RawRelationship
}

func NewRelBuffer() *RelBuffer { return &RelBuffer{} }

func (b *RelBuffer) Append(rels []RawRelationship) {
	if len(rels) == 0 {
		return
	}
	b.mu.Lock()
	b.rels = append(b.rels, rels...)
	b.mu.Unlock()
}

// Drain returns the accumulated relationships and resets the buffer.
func (b *RelBuffer) Drain() []RawRelationship {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.rels
	b.rels = nil
	return out
}
