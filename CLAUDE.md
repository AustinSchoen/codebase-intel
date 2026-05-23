# Codebase Intelligence

Go MCP server that indexes codebases and exposes them to AI agents via the Model Context Protocol. Hybrid vector + keyword search over symbols, references, class hierarchies, and module summaries.

## Tech stack

- **Go 1.24+**
- **MCP protocol** — stdio and HTTP transports (see `internal/mcp`)
- **Tree-sitter** via `go-tree-sitter`, with grammars for Go, Python, TypeScript/JS, Rust, C, C++, Kotlin, Swift, Dart
- **Voyage AI** `voyage-code-3` for embeddings (1024-dim)
- **Qdrant** for hybrid dense + sparse vector search
- **PostgreSQL 17** with `pg_trgm` + `pgvector` for symbols, references, and metadata

## Layout

```
cmd/
  server/        MCP server entry point (stdio + HTTP). Owns the
                 indexing pipeline and all backend credentials.
  indexer/       Thin-client CLI (one-shot + daemon mode). Walks
                 the codebase, ships file content to the server.
  benchmark/     Retrieval-quality benchmark harness
internal/
  config/        YAML config loading
  parser/        Tree-sitter parsing, per-language extractors,
                 Unreal Engine reflection (UCLASS/UPROPERTY/UFUNCTION)
  chunker/       Code chunking
  embedding/     Voyage AI client
  storage/
    qdrant/      Vector store
    postgres/    Symbols, references, summaries
  pipeline/      Server-side parse → chunk → embed → store pipeline.
                 Called by the /mcp/indexer/files handler; was moved
                 here from internal/indexer in the thin-client refactor.
  indexer/       Thin client + daemon (file watcher, SSE, HTTP uploads).
                 No parser/chunker/embedder/backend imports.
  mcp/           MCP server, HTTP transport, tool handlers, dashboard.
                 Hosts the /mcp/indexer/{files,gc,delete} endpoints.
  migrations/    Embedded PostgreSQL schema (go:embed); applied on
                 server startup.
  discovery/     mDNS / Zeroconf service advertise + discover for
                 frictionless multi-host indexer setup.
  httpretry/     Retry helper with backoff + Retry-After; wraps
                 Voyage / Qdrant / server HTTP calls.
  summary/       Claude-based module/subsystem summaries
  claudemd/      CLAUDE.md generation
  rerank/        Optional Cohere reranker
  metrics/       Prometheus metrics
```

Full architecture in [ARCHITECTURE.md](ARCHITECTURE.md). Contributor guide in [CONTRIBUTING.md](CONTRIBUTING.md).

## Dev commands

```bash
go build ./...
go test ./...
go run ./cmd/server
go run ./cmd/indexer -config configs/my-project.yaml
./setup.sh                              # bootstrap Qdrant + Postgres + server
./scripts/index.sh configs/my-project.yaml
```

## MCP tools exposed

`list_codebases`, `search_code`, `get_symbol`, `list_modules`, `get_references`, `get_class_hierarchy`, `get_file_context`, `get_module_summary`, `explain_subsystem`, `generate_claude_md`, `reindex`, `reindex_status`.
