# Contributing

Thanks for your interest in Codebase Intelligence! This document covers what you need to get a working dev environment and the conventions used in the codebase.

## Development setup

Requirements:

- Go 1.24 or newer
- Docker + Docker Compose v2 (for Qdrant and Postgres)
- A [Voyage AI API key](https://dash.voyageai.com/api-keys) for code embeddings
- *(Optional)* An Anthropic API key if you want to exercise the summary tools

```bash
git clone https://github.com/AustinSchoen/codebase-intel.git
cd codebase-intel
./setup.sh        # starts Qdrant + Postgres, generates .env, builds the indexer
go build ./...    # build everything
go test ./...     # run the test suite
```

`setup.sh` prompts for your Voyage key once and auto-generates the rest. See [`.env.example`](.env.example) for the full list of environment variables.

## Project layout

The high-level structure is documented in the [README](README.md#architecture) and in detail in [ARCHITECTURE.md](ARCHITECTURE.md). Briefly:

| Path | Purpose |
|------|---------|
| `cmd/server` | MCP server entry point (stdio + HTTP transports) |
| `cmd/indexer` | One-shot and daemon-mode indexer CLI |
| `cmd/benchmark` | Retrieval-quality benchmark harness |
| `internal/parser` | Tree-sitter integration and per-language extractors |
| `internal/chunker` | Code chunking |
| `internal/embedding` | Voyage AI client |
| `internal/storage/{qdrant,postgres}` | Vector and metadata stores |
| `internal/indexer` | File watcher, indexing pipeline, GC |
| `internal/mcp` | MCP protocol, HTTP transport, tool handlers |
| `migrations` | PostgreSQL schema |

## Code style

- **Format with `gofmt`.** CI does not currently enforce this, but PRs should land formatted.
- **Keep packages focused.** New cross-cutting concerns usually belong in a new `internal/` package, not in `mcp` or `indexer`.
- **No `interface{}` / `any` at package boundaries** unless there's a concrete reason. Tree-sitter nodes are the main exception.
- **Errors travel up.** Wrap with `fmt.Errorf("doing X: %w", err)` and let the top-level handler decide how to surface them.
- **Tests live next to the code they test** (`foo.go` ↔ `foo_test.go`). Table-driven where it fits.

## Adding a new language

Most contributions land here. The pattern:

1. Add the tree-sitter grammar to `go.mod` and register it in `internal/parser/parser.go` (the `languages` map and the `ExtractSymbols` switch).
2. Add per-language symbol extraction. Small grammars can extend `parser.go` directly; larger ones get their own `lang_<name>.go`.
3. Add relationship extraction in `internal/parser/relationships_<name>.go`, leaning on the helpers in `relationships_common.go` to avoid duplication.
4. Add a fixture and tests under `internal/parser/`.

## Pull requests

- Branch from `main`. Keep PRs focused — one feature or fix per PR.
- Run `go build ./... && go test ./...` before pushing.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`).
- If the change affects the MCP tool surface or the on-disk schema, update README and ARCHITECTURE.md in the same PR.

## Reporting issues

Bug reports are most useful with:

- The command you ran (or MCP tool call)
- The codebase config (`configs/*.yaml`) and language(s) involved
- Relevant server logs (`docker compose logs server`)
- Go version and OS

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
