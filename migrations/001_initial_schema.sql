-- 001_initial_schema.sql
-- Core schema for Codebase Intelligence MCP Server

-- Extensions
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- Enum types
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

CREATE TABLE codebases (
    id          TEXT PRIMARY KEY,
    root_path   TEXT NOT NULL,
    display_name TEXT,
    created_at  TIMESTAMPTZ DEFAULT now(),
    config      JSONB DEFAULT '{}'
);

CREATE TABLE symbols (
    id          TEXT PRIMARY KEY,
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    qualified   TEXT NOT NULL,
    kind        symbol_kind NOT NULL,
    filepath    TEXT NOT NULL,
    line_start  INTEGER,
    line_end    INTEGER,
    module      TEXT,
    parent_id   TEXT REFERENCES symbols(id) ON DELETE SET NULL,
    signature   TEXT,
    doc_comment TEXT,
    ue_meta     JSONB,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE relationships (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    source_id   TEXT NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    target_id   TEXT NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
    kind        relationship_kind NOT NULL,
    filepath    TEXT,
    line        INTEGER,
    UNIQUE(source_id, target_id, kind)
);

CREATE TABLE summaries (
    id          TEXT PRIMARY KEY,
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    scope       TEXT NOT NULL,
    level       summary_level NOT NULL,
    summary     TEXT NOT NULL,
    key_classes JSONB DEFAULT '[]',
    dependencies JSONB DEFAULT '[]',
    source_hash TEXT,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(codebase_id, scope, level)
);

CREATE TABLE file_state (
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    filepath    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    indexed_at  TIMESTAMPTZ DEFAULT now(),
    chunk_count INTEGER DEFAULT 0,
    PRIMARY KEY (codebase_id, filepath)
);

CREATE TABLE chunks (
    id          TEXT PRIMARY KEY,
    codebase_id TEXT NOT NULL REFERENCES codebases(id) ON DELETE CASCADE,
    symbol_id   TEXT REFERENCES symbols(id) ON DELETE SET NULL,
    filepath    TEXT NOT NULL,
    line_start  INTEGER,
    line_end    INTEGER,
    kind        symbol_kind,
    qualified_name TEXT,
    module      TEXT,
    token_count INTEGER
);

-- ============================================================
-- Indexes
-- ============================================================

CREATE INDEX idx_symbols_codebase ON symbols(codebase_id);
CREATE INDEX idx_symbols_module ON symbols(codebase_id, module);
CREATE INDEX idx_symbols_kind ON symbols(codebase_id, kind);
CREATE INDEX idx_symbols_parent ON symbols(parent_id);
CREATE INDEX idx_symbols_qualified ON symbols(qualified);

-- Trigram indexes for fuzzy symbol name search
CREATE INDEX idx_symbols_name_trgm ON symbols USING gin (name gin_trgm_ops);
CREATE INDEX idx_symbols_qualified_trgm ON symbols USING gin (qualified gin_trgm_ops);

-- Full-text search on doc comments
CREATE INDEX idx_symbols_doc_fts ON symbols USING gin (
    to_tsvector('english', COALESCE(doc_comment, ''))
);

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
-- Materialized views
-- ============================================================

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
