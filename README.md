# Codebase Intelligence MCP Server

An MCP (Model Context Protocol) server that indexes codebases for AI agent consumption. Provides semantic code search, symbol resolution, and codebase understanding tools.

## Features (planned)

- **Tree-sitter parsing** for Go, Python, TypeScript (extensible)
- **Voyage AI embeddings** (voyage-code-3, 1024-dim)
- **Qdrant hybrid search** (dense + sparse vectors)
- **PostgreSQL metadata** with pg_trgm fuzzy matching
- **Incremental indexing** via file watcher (fsnotify)
- **MCP tools**: search_code, get_symbol, get_references, list_modules, and more

## Quick Start

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your paths and credentials

# Index a codebase
go run ./cmd/indexer --path /path/to/codebase

# Start MCP server (stdio)
go run ./cmd/server
```

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design document.

## License

MIT
