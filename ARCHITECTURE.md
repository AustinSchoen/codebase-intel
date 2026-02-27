# Codebase Intelligence MCP Server — Architecture Plan

## Project Overview

A self-hosted MCP server that provides AI agents (primarily Claude Code) with deep, persistent, contextual understanding of large codebases. Designed to scale from typical projects (~50K LOC) to massive codebases like Unreal Engine (~30-40M LOC C++).

**Core Principle:** The agent should never have to read raw code to orient itself. It asks questions, gets precise answers, and only pulls actual source when it needs to reason about specific implementations.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Claude Code / Agent                    │
│                     (MCP Client)                         │
└──────────────────────┬──────────────────────────────────┘
                       │ MCP Protocol (stdio)
┌──────────────────────▼──────────────────────────────────┐
│                  MCP Server (Go)                         │
│                                                          │
│  Tools:                                                  │
│  ├── search_code        (semantic + keyword hybrid)      │
│  ├── get_symbol          (exact symbol lookup)           │
│  ├── get_references      (who calls/uses this?)         │
│  ├── get_class_hierarchy (inheritance tree)              │
│  ├── get_module_summary  (high-level module overview)    │
│  ├── explain_subsystem   (architectural narrative)       │
│  ├── get_file_context    (file with surrounding context) │
│  └── list_modules        (top-level module index)        │
└──────────┬───────────────────────┬──────────────────────┘
           │                       │
     ┌─────▼─────┐         ┌──────▼──────┐
     │  Qdrant   │         │  PostgreSQL │
     │  (vectors │         │  (metadata, │
     │  + hybrid │         │  summaries, │
     │   search) │         │  graph,     │
     │           │         │  pg_trgm)   │
     └───────────┘         └─────────────┘
```

---

## Component Breakdown

### 1. Indexing Pipeline

The indexer runs as a separate process (or goroutine pool) that watches the codebase and maintains the index incrementally.

```
Source Files
    │
    ▼
┌──────────────┐     ┌───────────────┐     ┌──────────────┐
│  File Watcher │────▶│  Tree-sitter  │────▶│   Chunker    │
│  (fsnotify)  │     │  C++ Parser   │     │              │
└──────────────┘     └───────────────┘     └──────┬───────┘
                                                   │
                     ┌─────────────────────────────┤
                     │                             │
                     ▼                             ▼
              ┌──────────────┐            ┌───────────────┐
              │  Voyage AI   │            │  Metadata     │
              │  Embeddings  │            │  Extractor    │
              │  (voyage-    │            │  (symbols,    │
              │   code-3)    │            │  refs, UE     │
              └──────┬───────┘            │  macros)      │
                     │                    └───────┬───────┘
                     ▼                            ▼
              ┌──────────────┐            ┌───────────────┐
              │   Qdrant     │            │  PostgreSQL   │
              │  (vectors)   │            │  (structured) │
              └──────────────┘            └───────────────┘
```

#### Tree-sitter Parsing Strategy

Use `go-tree-sitter` with the C++ grammar (and grammars for other languages as needed). Parse each file into an AST and extract semantic units:

- **Functions/Methods** — full body with signature
- **Class/Struct declarations** — declaration + member list (without method bodies)
- **Namespace blocks** — just the namespace path for context
- **Includes** — dependency graph edges
- **Macros/Defines** — especially UE reflection macros

#### Chunking Strategy

This is the most critical design decision. Bad chunks = bad retrieval = useless agent.

```
Chunk = {
    id:               string       // hash of content + location
    filepath:         string       // relative to repo root
    module:           string       // inferred module (e.g., "Engine/Source/Runtime/Chaos")
    qualified_name:   string       // e.g., "UWorld::Tick" or "FVector::Normalize"
    kind:             enum         // function, class, struct, enum, macro, file_header
    language:         string       // "cpp", "h", "py", etc.
    content:          string       // the actual code
    context_prefix:   string       // class decl + includes (prepended for embedding)
    line_start:       int
    line_end:         int
    parent_class:     string?      // if this is a method
    dependencies:     []string     // symbols referenced
    ue_metadata:      UEMetadata?  // UCLASS/UPROPERTY/UFUNCTION annotations
}
```

**Chunking rules:**

| Source Element | Chunk Strategy |
|---|---|
| Small function (<50 lines) | Single chunk, include class decl as context prefix |
| Large function (50-200 lines) | Single chunk (preserve wholeness), truncate if >200 |
| Very large function (>200 lines) | Split at logical blocks (if/else, loops), overlap 20 lines |
| Class declaration | One chunk for the decl + member signatures (no bodies) |
| Each method | Separate chunk with class decl as context prefix |
| Header file | One chunk for includes + forward decls, then per-class/function |
| Enum | Single chunk |
| UE Macros (UCLASS etc.) | Extracted as structured metadata, also embedded as chunks |

**Context prefix pattern** (critical for retrieval quality):

```cpp
// Context: Engine/Source/Runtime/Engine/Classes/GameFramework/Actor.h
// Class: AActor : public UObject
// Module: Engine (Runtime)
// UE: UCLASS(BlueprintType, Blueprintable)

void AActor::BeginPlay()
{
    // ... actual method body
}
```

The context prefix is prepended to the code content BEFORE embedding, so that semantic search understands the chunk's place in the architecture. It is NOT stored as part of the content returned to the agent (to save tokens).

### 2. Embedding Pipeline

**Model:** Voyage AI `voyage-code-3`
- 1024-dimensional embeddings
- Optimized for code retrieval
- Supports up to 16K token input (generous for code chunks)
- Batch API available (crucial for initial indexing of large codebases)

**Batching strategy for UE-scale:**

```
Estimated chunks for UE: ~500K-800K (at avg ~40-60 lines per chunk)
Voyage batch size: 128 inputs per request
Estimated API calls: ~4,000-6,500
At ~$0.05/1M tokens: probably $15-30 for full index

Throughput target: batch with 10 concurrent requests
Estimated time: 20-40 minutes for full reindex
Incremental: <1 second per changed file
```

**Embedding what gets embedded:**
- `context_prefix + content` → main embedding (stored in Qdrant)
- `qualified_name + kind + module` → lightweight keyword/metadata for filtering

### 3. Storage Layer

#### Qdrant (Vector + Hybrid Search)

Self-hosted on Rocky Linux. Single node is fine for this scale. Colocate with the Postgres instance or run on a separate server — either works since the MCP server talks to both over the LAN.

```yaml
# docker-compose.yml or direct binary
# Qdrant collection config
collection:
  name: "codebase_{repo_hash}"
  vectors:
    size: 1024              # voyage-code-3 dimensions
    distance: Cosine
  sparse_vectors:
    text:
      modifier: idf         # BM25-style sparse vectors for keyword search
  optimizers:
    indexing_threshold: 20000
  # Enable payload indexing for filtered search
  payload_indexes:
    - field: module
      type: keyword
    - field: kind
      type: keyword  
    - field: qualified_name
      type: keyword
    - field: language
      type: keyword
    - field: filepath
      type: keyword
```

**Hybrid search** (this is where Qdrant shines over simpler setups):
- Dense vector search (semantic similarity via Voyage embeddings)
- Sparse vector search (BM25 keyword matching — finds exact symbol names, error messages, etc.)
- Reciprocal Rank Fusion (RRF) to merge results
- Payload filtering (restrict to module, file type, symbol kind)

#### PostgreSQL (Structured Metadata + Graph)

Existing Postgres instance on the LAN. Create a dedicated database (`codebase_intel`) with extensions for fuzzy matching and full-text search.

```sql
-- Extensions
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- fuzzy symbol name matching
CREATE EXTENSION IF NOT EXISTS btree_gin;  -- composite GIN indexes

-- Enum types for type safety
CREATE TYPE symbol_kind AS ENUM (
    'function', 'method', 'class', 'struct', 'enum', 
    'macro', 'typedef', 'namespace', 'variable', 'field'
);

CREATE TYPE relationship_kind AS ENUM (
    'calls', 'inherits', 'includes', 'overrides', 
    'references', 'implements', 'instantiates', 'contains'
);

CREATE TYPE summary_level AS ENUM ('module', 'class', 'subsystem');

-- ============================================================
-- Core tables
-- ============================================================

-- Codebase registry (multi-repo support)
CREATE TABLE codebases (
    id          TEXT PRIMARY KEY,          -- short name ("ue5", "my-game")
    root_path   TEXT NOT NULL,
    display_name TEXT,
    created_at  TIMESTAMPTZ DEFAULT now(),
    config      JSONB DEFAULT '{}'         -- per-repo overrides
);

-- Symbol table (fast exact lookups)
CREATE TABLE symbols (
    id          TEXT PRIMARY KEY,          -- hash(codebase + qualified_name)
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,             -- short name (e.g., "Tick")
    qualified   TEXT NOT NULL,             -- full name (e.g., "UWorld::Tick")
    kind        symbol_kind NOT NULL,
    filepath    TEXT NOT NULL,
    line_start  INTEGER,
    line_end    INTEGER,
    module      TEXT,
    parent_id   TEXT REFERENCES symbols(id) ON DELETE SET NULL,
    signature   TEXT,                      -- function signature
    doc_comment TEXT,                      -- extracted doc comments
    ue_meta     JSONB,                     -- UE reflection metadata (UCLASS, UPROPERTY, etc.)
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

-- Relationship graph (edges)
CREATE TABLE relationships (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    source_id   TEXT NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    target_id   TEXT NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    kind        relationship_kind NOT NULL,
    filepath    TEXT,                      -- where the relationship occurs
    line        INTEGER,
    UNIQUE(source_id, target_id, kind)
);

-- Module summaries (pre-computed by LLM)
CREATE TABLE summaries (
    id          TEXT PRIMARY KEY,          -- hash(codebase + scope)
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    scope       TEXT NOT NULL,             -- module path or class qualified name
    level       summary_level NOT NULL,
    summary     TEXT NOT NULL,             -- LLM-generated summary
    key_classes JSONB DEFAULT '[]',        -- important classes in this scope
    dependencies JSONB DEFAULT '[]',       -- modules/classes this depends on
    source_hash TEXT,                      -- hash of source files used to generate
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(codebase_id, scope, level)
);

-- Indexing state (for incremental updates)
CREATE TABLE file_state (
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    filepath    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    indexed_at  TIMESTAMPTZ DEFAULT now(),
    chunk_count INTEGER DEFAULT 0,
    PRIMARY KEY (codebase_id, filepath)
);

-- Chunk tracking (maps chunks in Qdrant back to source)
CREATE TABLE chunks (
    id          TEXT PRIMARY KEY,          -- same ID used in Qdrant point
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    symbol_id   TEXT REFERENCES symbols(id) ON DELETE SET NULL,
    filepath    TEXT NOT NULL,
    line_start  INTEGER,
    line_end    INTEGER,
    kind        symbol_kind,
    qualified_name TEXT,
    module      TEXT,
    token_count INTEGER                    -- track embedding costs
);

-- ============================================================
-- Indexes
-- ============================================================

-- Symbol lookups (the most common query path)
CREATE INDEX idx_symbols_codebase ON symbols(codebase_id);
CREATE INDEX idx_symbols_module ON symbols(codebase_id, module);
CREATE INDEX idx_symbols_kind ON symbols(codebase_id, kind);
CREATE INDEX idx_symbols_parent ON symbols(parent_id);
CREATE INDEX idx_symbols_qualified ON symbols(qualified);

-- Trigram index for fuzzy symbol name search
-- Lets agent search for "BeginPla" and still find "AActor::BeginPlay"
CREATE INDEX idx_symbols_name_trgm ON symbols USING gin (name gin_trgm_ops);
CREATE INDEX idx_symbols_qualified_trgm ON symbols USING gin (qualified gin_trgm_ops);

-- Full-text search on doc comments
CREATE INDEX idx_symbols_doc_fts ON symbols USING gin (
    to_tsvector('english', COALESCE(doc_comment, ''))
);

-- JSONB index on UE metadata (search by specifier, category, etc.)
CREATE INDEX idx_symbols_ue_meta ON symbols USING gin (ue_meta jsonb_path_ops);

-- Relationship graph traversal
CREATE INDEX idx_rel_source ON relationships(source_id, kind);
CREATE INDEX idx_rel_target ON relationships(target_id, kind);
CREATE INDEX idx_rel_codebase ON relationships(codebase_id);

-- File state lookups
CREATE INDEX idx_file_state_hash ON file_state(content_hash);

-- Chunk lookups
CREATE INDEX idx_chunks_codebase ON chunks(codebase_id);
CREATE INDEX idx_chunks_filepath ON chunks(codebase_id, filepath);
CREATE INDEX idx_chunks_symbol ON chunks(symbol_id);

-- ============================================================
-- Useful views
-- ============================================================

-- Symbol reference counts (for identifying "important" classes)
CREATE MATERIALIZED VIEW symbol_importance AS
SELECT 
    s.id,
    s.qualified,
    s.kind,
    s.module,
    s.codebase_id,
    COUNT(DISTINCT r.source_id) AS incoming_refs,
    COUNT(DISTINCT r2.target_id) AS outgoing_refs
FROM symbols s
LEFT JOIN relationships r ON r.target_id = s.id
LEFT JOIN relationships r2 ON r2.source_id = s.id
GROUP BY s.id, s.qualified, s.kind, s.module, s.codebase_id
ORDER BY incoming_refs DESC;

CREATE UNIQUE INDEX idx_symbol_importance_id ON symbol_importance(id);

-- Module overview (for list_modules tool)
CREATE MATERIALIZED VIEW module_stats AS
SELECT
    codebase_id,
    module,
    COUNT(*) FILTER (WHERE kind = 'class') AS class_count,
    COUNT(*) FILTER (WHERE kind = 'function' OR kind = 'method') AS function_count,
    COUNT(*) FILTER (WHERE kind = 'struct') AS struct_count,
    COUNT(DISTINCT filepath) AS file_count,
    SUM(COALESCE(line_end - line_start, 0)) AS estimated_loc
FROM symbols
WHERE module IS NOT NULL
GROUP BY codebase_id, module;

CREATE UNIQUE INDEX idx_module_stats ON module_stats(codebase_id, module);
```

**Useful queries the MCP tools will run:**

```sql
-- Fuzzy symbol lookup (get_symbol with typo tolerance)
SELECT * FROM symbols 
WHERE codebase_id = $1 
  AND qualified % $2        -- trigram similarity
ORDER BY similarity(qualified, $2) DESC
LIMIT 5;

-- Class hierarchy traversal (get_class_hierarchy)
WITH RECURSIVE hierarchy AS (
    SELECT s.id, s.qualified, s.kind, 0 AS depth
    FROM symbols s WHERE s.qualified = $1
    UNION ALL
    SELECT s2.id, s2.qualified, s2.kind, h.depth + 1
    FROM hierarchy h
    JOIN relationships r ON r.source_id = h.id AND r.kind = 'inherits'
    JOIN symbols s2 ON s2.id = r.target_id
    WHERE h.depth < $2  -- max depth parameter
)
SELECT * FROM hierarchy;

-- Find all callers of a function (get_references)
SELECT s.qualified, s.filepath, r.line
FROM relationships r
JOIN symbols s ON s.id = r.source_id
WHERE r.target_id = (SELECT id FROM symbols WHERE qualified = $1)
  AND r.kind = 'calls'
ORDER BY s.module, s.filepath;

-- UE metadata search (e.g., "all BlueprintCallable functions in module X")
SELECT qualified, filepath, ue_meta
FROM symbols
WHERE codebase_id = $1
  AND module = $2
  AND ue_meta @> '{"functions": [{"specifiers": ["BlueprintCallable"]}]}';

-- Refresh materialized views (run after indexing)
REFRESH MATERIALIZED VIEW CONCURRENTLY symbol_importance;
REFRESH MATERIALIZED VIEW CONCURRENTLY module_stats;
```

### 4. MCP Tool Definitions

These are the tools the agent sees and calls. Each is designed for a specific retrieval pattern.

#### `search_code`
**Purpose:** Semantic + keyword hybrid search across the codebase.
```json
{
  "name": "search_code",
  "description": "Search the indexed codebase using natural language or code patterns. Uses hybrid semantic + keyword search. Returns ranked code chunks with file locations.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "query": {
        "type": "string",
        "description": "Natural language query or code pattern to search for"
      },
      "module_filter": {
        "type": "string",
        "description": "Optional: restrict search to a specific module (e.g., 'Engine/Source/Runtime/Chaos')"
      },
      "kind_filter": {
        "type": "string",
        "enum": ["function", "class", "struct", "enum", "macro", "any"],
        "description": "Optional: restrict to specific symbol kinds"
      },
      "limit": {
        "type": "integer",
        "default": 10,
        "description": "Max results to return (1-25)"
      }
    },
    "required": ["query"]
  }
}
```

**Returns:**
```json
{
  "results": [
    {
      "score": 0.89,
      "filepath": "Engine/Source/Runtime/Engine/Private/World.cpp",
      "qualified_name": "UWorld::Tick",
      "kind": "function",
      "module": "Engine",
      "line_start": 1247,
      "line_end": 1312,
      "content": "void UWorld::Tick(ELevelTick TickType, float DeltaSeconds)\n{\n    ...",
      "context": "// Class: UWorld : public UObject\n// Module: Engine (Runtime)"
    }
  ]
}
```

#### `get_symbol`
**Purpose:** Exact symbol lookup by name. Fast path — no embedding search needed.
```json
{
  "name": "get_symbol",
  "description": "Look up a specific symbol (class, function, struct, enum) by name. Returns the definition with full source code and metadata.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "name": {
        "type": "string",
        "description": "Symbol name, can be short ('AActor') or qualified ('AActor::BeginPlay')"
      },
      "kind": {
        "type": "string",
        "enum": ["function", "class", "struct", "enum", "macro", "any"],
        "default": "any"
      }
    },
    "required": ["name"]
  }
}
```

#### `get_references`
**Purpose:** Find all references to a symbol — who calls this, who uses this type, etc.
```json
{
  "name": "get_references",
  "description": "Find all references to a symbol: callers, implementors, users. Answers 'who uses this?' and 'what depends on this?'",
  "inputSchema": {
    "type": "object",
    "properties": {
      "symbol": {
        "type": "string",
        "description": "Symbol name to find references for"
      },
      "ref_kind": {
        "type": "string",
        "enum": ["calls", "inherits", "references", "overrides", "all"],
        "default": "all"
      },
      "limit": { "type": "integer", "default": 20 }
    },
    "required": ["symbol"]
  }
}
```

#### `get_class_hierarchy`
**Purpose:** Traverse inheritance chains up or down.
```json
{
  "name": "get_class_hierarchy",
  "description": "Get the inheritance hierarchy for a class. Can traverse up (parents) or down (children/subclasses).",
  "inputSchema": {
    "type": "object",
    "properties": {
      "class_name": { "type": "string" },
      "direction": {
        "type": "string",
        "enum": ["parents", "children", "both"],
        "default": "both"
      },
      "depth": { "type": "integer", "default": 3 }
    },
    "required": ["class_name"]
  }
}
```

#### `get_module_summary`
**Purpose:** High-level overview of a module without reading any source code.
```json
{
  "name": "get_module_summary",
  "description": "Get a high-level architectural summary of a module or subsystem. Includes purpose, key classes, dependencies, and patterns.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "module": {
        "type": "string",
        "description": "Module path (e.g., 'Chaos', 'GameplayAbilities', 'Niagara')"
      }
    },
    "required": ["module"]
  }
}
```

**Returns pre-computed summary:**
```json
{
  "module": "Engine/Source/Runtime/Chaos",
  "summary": "Chaos is UE5's physics and destruction system, replacing PhysX for rigid body simulation. It provides a unified solver for rigid bodies, cloth, and particles...",
  "key_classes": ["FPBDRigidsSolver", "FChaosSolverConfiguration", "UChaosPhysicalMaterial"],
  "submodules": ["Chaos/Core", "Chaos/Experimental", "Chaos/Framework"],
  "dependencies": ["Core", "PhysicsCore", "GeometryCore"],
  "file_count": 847,
  "estimated_loc": 312000
}
```

#### `explain_subsystem`
**Purpose:** Generate or retrieve an architectural narrative about how a subsystem works.
```json
{
  "name": "explain_subsystem",
  "description": "Get a detailed architectural explanation of how a subsystem or feature works, including data flow, key extension points, and common patterns.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "topic": {
        "type": "string",
        "description": "The subsystem or architectural topic (e.g., 'character movement', 'replication', 'garbage collection', 'shader compilation pipeline')"
      }
    },
    "required": ["topic"]
  }
}
```

#### `get_file_context`
**Purpose:** Get a specific file's content with surrounding architectural context.
```json
{
  "name": "get_file_context",
  "description": "Retrieve a file's content (or a range of lines) along with its role in the architecture — what module it belongs to, what it depends on, and what depends on it.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "filepath": { "type": "string" },
      "line_start": { "type": "integer" },
      "line_end": { "type": "integer" }
    },
    "required": ["filepath"]
  }
}
```

#### `list_modules`
**Purpose:** Orientation tool — show the agent what modules exist.
```json
{
  "name": "list_modules",
  "description": "List all top-level modules in the codebase with file counts and brief descriptions. Use this to orient yourself before diving into specific areas.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "parent": {
        "type": "string",
        "description": "Optional: list submodules under a parent (e.g., 'Engine/Source/Runtime')"
      }
    }
  }
}
```

---

## UE-Specific Extensions

### Reflection Macro Parser

UE's reflection system encodes massive architectural information. Parse these into structured metadata:

```cpp
// Source
UCLASS(BlueprintType, Blueprintable, meta=(DisplayName="My Actor"))
class ENGINE_API AActor : public UObject
{
    GENERATED_BODY()

    UPROPERTY(EditAnywhere, BlueprintReadWrite, Category="Transform")
    FTransform ActorTransform;

    UFUNCTION(BlueprintCallable, Category="Gameplay")
    void DoSomething();
};
```

```json
// Extracted metadata
{
  "class": "AActor",
  "ue_specifiers": {
    "class": ["BlueprintType", "Blueprintable"],
    "meta": {"DisplayName": "My Actor"},
    "api_macro": "ENGINE_API"
  },
  "properties": [
    {
      "name": "ActorTransform",
      "type": "FTransform",
      "specifiers": ["EditAnywhere", "BlueprintReadWrite"],
      "category": "Transform"
    }
  ],
  "functions": [
    {
      "name": "DoSomething",
      "specifiers": ["BlueprintCallable"],
      "category": "Gameplay"
    }
  ]
}
```

This metadata is:
1. Stored in the `ue_meta` column of the symbols table
2. Included in the chunk's context prefix for embedding
3. Searchable via `search_code` (e.g., "BlueprintCallable functions in the movement system")

### Module Discovery

UE uses `.Build.cs` and `.uproject`/`.uplugin` files to define modules. Parse these to automatically discover module boundaries, dependencies, and public/private API surfaces:

```
Engine/Source/Runtime/Engine/Engine.Build.cs
    → Module: "Engine"
    → Dependencies: ["Core", "CoreUObject", "PhysicsCore", ...]
    → PublicIncludePaths: [...]
```

---

## Summary Generation Pipeline

Summaries are pre-computed offline and stored in SQLite. They're regenerated when the underlying code changes significantly.

### Hierarchy

```
Codebase Summary (1)
  └── Module Summaries (~50-200)
       └── Subsystem/Feature Summaries (~200-500)
            └── Class Summaries (top ~1000 most important)
```

### Generation Strategy

1. **Module summaries:** Feed the module's file list, public headers, Build.cs dependencies, and a sample of key class declarations to Claude. Ask for a 200-300 word architectural summary.

2. **Class summaries:** For the top N most-referenced classes (determined by incoming edges in the relationship graph), generate summaries including purpose, key methods, inheritance context, and common usage patterns.

3. **Subsystem narratives:** For known architectural topics (rendering pipeline, replication, GC, etc.), generate longer-form explanations by pulling summaries of involved modules + key class summaries + selected code samples.

**Cost estimate for UE:**
- ~200 module summaries × ~2K tokens each = ~400K tokens
- ~1000 class summaries × ~1K tokens each = ~1M tokens  
- ~50 subsystem narratives × ~4K tokens each = ~200K tokens
- Total: ~1.6M tokens ≈ $5-12 depending on model
- Regeneration: only changed modules, so incremental cost is minimal

---

## Implementation Plan

### Phase 1: Core Infrastructure (Week 1-2)

**Goal:** Index a medium codebase, perform hybrid search via MCP.

- [ ] Go project scaffold with MCP server (stdio transport)
- [ ] Tree-sitter integration (`go-tree-sitter` + C++ grammar)
- [ ] Basic chunker: functions, classes, structs, enums
- [ ] Voyage AI embedding client (batch support)
- [ ] Qdrant client (collection management, upsert, hybrid search)
- [ ] PostgreSQL schema + migrations (pgx + pgxpool)
- [ ] Basic symbol table with pg_trgm indexes
- [ ] MCP tools: `search_code`, `get_symbol`, `list_modules`
- [ ] File watcher (fsnotify) for incremental updates
- [ ] Bulk insert pipeline (pgx COPY for initial indexing)
- [ ] Content hashing for change detection
- [ ] CLI for manual index/reindex

**Test target:** Index a medium Go or C++ project (~50K LOC), verify search quality.

### Phase 2: Relationships + Navigation (Week 3)

**Goal:** Graph-based navigation between symbols.

- [ ] Reference extraction from tree-sitter AST (calls, type usage)
- [ ] Include/import graph
- [ ] Inheritance hierarchy extraction
- [ ] MCP tools: `get_references`, `get_class_hierarchy`, `get_file_context`
- [ ] Payload-filtered search (by module, kind)

### Phase 3: Summaries + UE Extensions (Week 4)

**Goal:** High-level architectural intelligence, UE-specific awareness.

- [ ] UE reflection macro parser (UCLASS, UPROPERTY, UFUNCTION, USTRUCT, UENUM)
- [ ] UE module discovery (.Build.cs parser)
- [ ] Summary generation pipeline (module → class → subsystem)
- [ ] MCP tools: `get_module_summary`, `explain_subsystem`
- [ ] Summary invalidation on code changes

### Phase 4: Scale + Polish (Week 5+)

**Goal:** Handle UE-scale, optimize retrieval quality.

- [ ] Benchmark at UE scale (~30M+ LOC)
- [ ] Tune chunk sizes, overlap, context prefix content
- [ ] Reranking pipeline (initial retrieval → LLM rerank for complex queries)
- [ ] Qdrant HNSW tuning for collection size
- [ ] Concurrent indexing pipeline (goroutine pool)
- [ ] Materialized view refresh scheduling (symbol_importance, module_stats)
- [ ] Progress reporting during initial index
- [ ] Multi-language support (Python, Rust, Go, JS/TS)
- [ ] CLAUDE.md integration (auto-generate recommended context for Claude Code)

---

## Tech Stack Summary

| Component | Technology | Why |
|---|---|---|
| **Language** | Go | Performance, concurrency, your existing expertise |
| **MCP Transport** | stdio | Standard for Claude Code integration |
| **Parsing** | go-tree-sitter | Fast, incremental, 48+ language support |
| **Embeddings** | Voyage AI voyage-code-3 | Best-in-class code retrieval, you have an account |
| **Vector Store** | Qdrant (self-hosted) | Hybrid search, payload filtering, single binary |
| **Metadata Store** | PostgreSQL (existing) | Already running on LAN, concurrent writes, JSONB, pg_trgm |
| **File Watching** | fsnotify | Standard Go file watcher |
| **Summary Gen** | Claude API | Architectural narratives from code context |

---

## Configuration

```yaml
# config.yaml
codebase:
  path: "/path/to/UnrealEngine"
  name: "UnrealEngine"
  languages: ["cpp", "h"]
  exclude_patterns:
    - "**/ThirdParty/**"
    - "**/Intermediate/**"
    - "**/Binaries/**"
    - "**/*.generated.h"

indexing:
  chunk_max_lines: 200
  chunk_overlap_lines: 20
  context_prefix: true
  batch_size: 128
  concurrent_requests: 10
  incremental: true

embeddings:
  provider: "voyage"
  model: "voyage-code-3"
  api_key_env: "VOYAGE_API_KEY"
  dimensions: 1024

vector_store:
  provider: "qdrant"
  url: "http://localhost:6333"
  collection_prefix: "codebase"

metadata_store:
  provider: "postgres"
  host: "postgres.lan"           # your existing instance
  port: 5432
  database: "codebase_intel"
  user_env: "PGUSER"
  password_env: "PGPASSWORD"
  max_connections: 20            # pool size for concurrent indexing

summaries:
  enabled: true
  provider: "anthropic"
  model: "claude-sonnet-4-5-20250929"
  api_key_env: "ANTHROPIC_API_KEY"
  top_classes: 1000       # summarize top N most-referenced classes
  regenerate_on_change: true

ue_extensions:
  enabled: true
  parse_reflection_macros: true
  parse_build_cs: true
  module_discovery: true
```

---

## Open Questions / Decisions Needed

1. **~~SQLite vs Postgres for metadata?~~** → **Decided: PostgreSQL.** Existing instance on LAN, concurrent writes for parallel indexing, `pg_trgm` for fuzzy symbol matching, JSONB for UE metadata queries, materialized views for precomputed stats. Connection pooling via `pgxpool` in Go.

2. **Sparse vectors in Qdrant vs Postgres FTS for keyword search?** Qdrant supports sparse vectors for BM25-style keyword search natively, meaning one query can do hybrid search via RRF. Alternative: use Postgres `to_tsvector`/`to_tsquery` for keyword search and Qdrant for semantic only, then merge results in Go. The Qdrant-native approach is fewer moving parts; the Postgres approach gives you more control over keyword ranking and you already have the FTS index on doc comments. **Recommendation:** Start with Qdrant-native hybrid, add Postgres FTS as a fallback path for exact symbol name searches where trigram matching is better.

3. **Summary generation model?** Sonnet is fast and cheap for summaries. Opus would be better for complex subsystem narratives. Could use Sonnet for module/class summaries and Opus for subsystem explanations.

4. **Reranking strategy?** For Phase 1, just use Qdrant's hybrid search scoring. For Phase 4, add an LLM reranking step where the top-20 results are fed to Claude with the original query and asked to pick the top-5 most relevant. This is expensive but dramatically improves precision for complex queries.

5. **Multi-repo support?** The schema already supports multiple codebases via `codebase_id`. If you want to index both UE source and your game project simultaneously, the agent could search across both with a codebase filter. The `codebases` table config JSONB field can hold per-repo chunking/embedding overrides.

6. **go-tree-sitter vs cgo wrapper?** The pure Go tree-sitter bindings have improved but the CGO wrappers are more mature. Test both for C++ parsing correctness, especially with UE's macro-heavy code.

7. **Qdrant deployment?** Single-node Docker on the same LAN as Postgres, or colocated on one of your Rocky Linux servers? For UE-scale (~500K-800K vectors at 1024 dimensions), a single node with 4-8GB RAM dedicated to Qdrant is plenty. Docker compose with persistent volume is the simplest path.

8. **Go database driver?** `pgx` (jackc/pgx) is the standard for Go + Postgres. It supports connection pooling (`pgxpool`), COPY for bulk inserts during indexing, and native JSONB handling. The bulk insert path matters — during initial indexing you'll be inserting hundreds of thousands of symbols and relationships.
