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
  server/        MCP server entry point (stdio + HTTP)
  indexer/       CLI indexer (one-shot + daemon mode)
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
  indexer/       File watcher (fsnotify), indexing pipeline, GC
  mcp/           MCP server, HTTP transport, tool handlers, dashboard
  summary/       Claude-based module/subsystem summaries
  claudemd/      CLAUDE.md generation
  rerank/        Optional Cohere reranker
  metrics/       Prometheus metrics
migrations/      PostgreSQL schema
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
