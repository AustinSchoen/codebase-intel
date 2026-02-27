# Codebase Intelligence MCP Server

A Go MCP server that indexes codebases for AI agent consumption.

## Architecture

See ARCHITECTURE.md for the full plan.

## Tech Stack
- Go 1.23+
- MCP protocol (stdio transport)
- Tree-sitter (go-tree-sitter) for parsing
- Voyage AI voyage-code-3 for embeddings (1024-dim)
- Qdrant for hybrid vector search
- PostgreSQL 17 + pg_trgm + pgvector for structured metadata

## Project Structure
```
cmd/server/     - MCP server entry point
cmd/indexer/    - CLI indexer entry point  
internal/
  config/       - YAML config loading
  mcp/          - MCP server + tool handlers
  indexer/      - File watcher + indexing pipeline
  parser/       - Tree-sitter parsing
  chunker/      - Code chunking logic
  embedding/    - Voyage AI client
  storage/
    qdrant/     - Qdrant vector store client
    postgres/   - PostgreSQL metadata store
```

## Phase 1 Scope
- Go project scaffold with MCP server (stdio)
- Tree-sitter integration (Go + Python + TypeScript grammars)
- Basic chunker: functions, classes, structs
- Voyage AI embedding client (batch)
- Qdrant client (collection mgmt, upsert, hybrid search)
- PostgreSQL schema + migrations
- Basic symbol table with pg_trgm indexes
- MCP tools: search_code, get_symbol, list_modules
- File watcher (fsnotify) for incremental updates
- CLI for manual index/reindex

## Dev Commands
```bash
go build ./...
go test ./...
go run ./cmd/server
go run ./cmd/indexer --path /path/to/codebase
```
