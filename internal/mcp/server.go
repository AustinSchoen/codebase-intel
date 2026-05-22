package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/metrics"
	"github.com/AustinSchoen/codebase-intel/internal/migrations"
	"github.com/AustinSchoen/codebase-intel/internal/pipeline"
	"github.com/AustinSchoen/codebase-intel/internal/rerank"
	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
	"github.com/AustinSchoen/codebase-intel/internal/summary"
)

// JSON-RPC 2.0 types

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Server implements the MCP protocol over stdio.
type Server struct {
	cfg           *config.Config
	store         *postgres.Store
	qdrant        *qdrant.Client
	embedder      *embedding.VoyageClient
	reranker      rerank.Reranker
	logger        *log.Logger
	summarAPIKey  string // stored for per-codebase generator creation
	summarModel   string
	summarEnabled bool
	indexerMgr    *IndexerManager  // set in HTTP mode for reindex tools
	pipeline      *pipeline.Pipeline // server-side indexing pipeline (thin-client architecture, issue #18)

	// indexerRelBuffers groups raw relationships from in-flight indexing
	// requests by request_id, so cross-file rels can resolve against symbols
	// added later in the same pass.
	indexerRelBuffersMu sync.Mutex
	indexerRelBuffers   map[string]*pipeline.RelBuffer
}

// NewServer creates a new MCP server.
func NewServer(cfg *config.Config) *Server {
	return &Server{
		cfg:    cfg,
		logger: log.New(os.Stderr, "[mcp] ", log.LstdFlags),
	}
}

// Run starts the MCP server, reading JSON-RPC from stdin and writing to stdout.
func (s *Server) Run() error {
	ctx := context.Background()

	// Connect to backends
	if err := s.initBackends(ctx); err != nil {
		return fmt.Errorf("init backends: %w", err)
	}
	if s.store != nil {
		defer s.store.Close()
	}

	s.logger.Println("MCP server started, reading from stdin")

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				s.logger.Println("stdin closed, shutting down")
				return nil
			}
			return fmt.Errorf("reading stdin: %w", err)
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "Parse error", err.Error())
			continue
		}

		resp := s.handleRequest(ctx, &req)
		if resp != nil {
			s.writeResponse(resp)
		}
	}
}

// initBackends connects to and validates the required backends. It returns an
// error for any failure that would leave the server unable to serve its
// metadata-dependent tools (search_code, get_symbol, list_modules, etc.).
//
// Fail-fast is intentional. The previous behavior was to log warnings and
// continue with `s.store == nil`, which led to silent partial outages — the
// server returned 200 on /health while half the MCP tools were broken (issue
// #2). systemd's Restart=on-failure and Docker's restart policy will handle
// the transient case where Postgres comes up slightly after the server.
func (s *Server) initBackends(ctx context.Context) error {
	env, err := s.cfg.ResolveEnv()
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}

	// PostgreSQL — required. Without it the metadata-store-backed MCP tools
	// can't function, so we refuse to start.
	dsn := s.cfg.PostgresDSN(env)
	store, err := postgres.NewStore(ctx, dsn, s.cfg.Metadata.MaxConnections)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	s.store = store

	// Apply schema migrations from the embedded migrations dir. Idempotent —
	// safe to run on every startup. The indexer used to do this via a
	// -migrate flag (now removed in #18), but it can't anymore in the
	// thin-client world since it has no DB connection.
	if err := migrations.Run(ctx, s.store); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}

	// Qdrant — NewClient only constructs the HTTP client; connectivity is
	// validated lazily on first request.
	s.qdrant = qdrant.NewClient(s.cfg.Vector.URL, s.cfg.Vector.CollectionPrefix, env.VectorAPIKey)

	// Embedder — Voyage client construction never fails; requests are lazy.
	s.embedder = embedding.NewVoyageClient(
		env.EmbeddingAPIKey,
		s.cfg.Embedding.Model,
		s.cfg.Embedding.Dimensions,
		s.cfg.Indexing.ConcurrentReqs,
	)

	// Summary config (optional — only enabled if both flag and key are set).
	if s.cfg.Summaries.Enabled && env.SummaryAPIKey != "" {
		s.summarAPIKey = env.SummaryAPIKey
		s.summarModel = s.cfg.Summaries.Model
		s.summarEnabled = true
	}

	// Reranker (optional).
	if s.cfg.Reranking.Enabled && env.CohereAPIKey != "" {
		s.reranker = rerank.NewCohereReranker(env.CohereAPIKey, s.cfg.Reranking.Model)
		s.logger.Println("Cohere reranker enabled")
	}

	// Indexing pipeline — the server now owns parse/chunk/embed/store and
	// exposes it via /mcp/indexer/* for thin-client daemons (issue #18).
	pl, err := pipeline.New(s.cfg.Indexing, s.cfg.Embedding, s.store, s.qdrant, s.embedder, s.logger)
	if err != nil {
		return fmt.Errorf("constructing indexing pipeline: %w", err)
	}
	s.pipeline = pl
	s.indexerRelBuffers = make(map[string]*pipeline.RelBuffer)

	return nil
}

// getSummarGen creates a summary generator for a specific codebase.
func (s *Server) getSummarGen(codebaseID string) *summary.Generator {
	if !s.summarEnabled {
		return nil
	}
	return summary.NewGenerator(s.summarAPIKey, s.summarModel, s.store, codebaseID, s.logger)
}

func (s *Server) handleRequest(ctx context.Context, req *jsonrpcRequest) *jsonrpcResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil // notification, no response
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func (s *Server) handleInitialize(req *jsonrpcRequest) *jsonrpcResponse {
	return &jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "codebase-intel",
				"version": "0.2.0",
			},
		},
	}
}

// codebaseParam is the shared schema for the codebase parameter across all tools.
var codebaseParam = map[string]interface{}{
	"type":        "string",
	"description": "Codebase identifier (use list_codebases to discover available codebases)",
}

func (s *Server) handleToolsList(req *jsonrpcRequest) *jsonrpcResponse {
	tools := []map[string]interface{}{
		{
			"name":        "list_codebases",
			"description": "List all indexed codebases with file counts, symbol counts, and metadata. Use this to discover available codebases before querying.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "search_code",
			"description": "Search an indexed codebase using natural language or code patterns. Uses hybrid semantic + keyword search.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase":      codebaseParam,
					"query":         map[string]interface{}{"type": "string", "description": "Natural language query or code pattern to search for"},
					"module_filter": map[string]interface{}{"type": "string", "description": "Restrict search to a specific module"},
					"kind_filter":   map[string]interface{}{"type": "string", "enum": []string{"function", "class", "struct", "enum", "macro", "any"}},
					"limit":         map[string]interface{}{"type": "integer", "default": 10},
				},
				"required": []string{"codebase", "query"},
			},
		},
		{
			"name":        "get_symbol",
			"description": "Look up a specific symbol (class, function, struct, enum) by name. Uses fuzzy matching.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"name":     map[string]interface{}{"type": "string", "description": "Symbol name, can be short or qualified"},
					"kind":     map[string]interface{}{"type": "string", "enum": []string{"function", "class", "struct", "enum", "macro", "any"}, "default": "any"},
				},
				"required": []string{"codebase", "name"},
			},
		},
		{
			"name":        "list_modules",
			"description": "List all top-level modules in a codebase with file counts and statistics.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"parent":   map[string]interface{}{"type": "string", "description": "List submodules under a parent module"},
				},
				"required": []string{"codebase"},
			},
		},
		{
			"name":        "get_references",
			"description": "Find all symbols that call or reference a given symbol. Returns callers, type users, or inheritance references.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"symbol":   map[string]interface{}{"type": "string", "description": "Symbol name to find references for (short or qualified)"},
					"ref_kind": map[string]interface{}{"type": "string", "enum": []string{"calls", "inherits", "references", "any"}, "default": "any", "description": "Filter by relationship kind"},
					"limit":    map[string]interface{}{"type": "integer", "default": 20},
				},
				"required": []string{"codebase", "symbol"},
			},
		},
		{
			"name":        "get_class_hierarchy",
			"description": "Traverse inheritance hierarchy for a class or struct. Shows parent types and child types.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase":   codebaseParam,
					"class_name": map[string]interface{}{"type": "string", "description": "Class or struct name to start from"},
					"direction":  map[string]interface{}{"type": "string", "enum": []string{"parents", "children", "both"}, "default": "both"},
					"depth":      map[string]interface{}{"type": "integer", "default": 5, "description": "Maximum traversal depth"},
				},
				"required": []string{"codebase", "class_name"},
			},
		},
		{
			"name":        "get_file_context",
			"description": "Get a file's architectural context: defined symbols, what depends on it, and module info. File content is included when the server has local access to the codebase.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase":   codebaseParam,
					"filepath":   map[string]interface{}{"type": "string", "description": "Relative filepath within the codebase"},
					"line_start": map[string]interface{}{"type": "integer", "description": "Start line (optional, returns range instead of full file)"},
					"line_end":   map[string]interface{}{"type": "integer", "description": "End line (optional)"},
				},
				"required": []string{"codebase", "filepath"},
			},
		},
		{
			"name":        "get_module_summary",
			"description": "Get a high-level architectural summary of a module. Returns purpose, key classes, dependencies, and statistics. Falls back to on-demand generation if no cached summary exists.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"module":   map[string]interface{}{"type": "string", "description": "Module name (e.g., 'Chaos', 'GameplayAbilities', 'parser')"},
				},
				"required": []string{"codebase", "module"},
			},
		},
		{
			"name":        "explain_subsystem",
			"description": "Get a detailed architectural explanation of how a subsystem or feature works, including data flow, key extension points, and common patterns. Uses semantic search to find relevant code, then generates an explanation.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"topic":    map[string]interface{}{"type": "string", "description": "The subsystem or topic to explain (e.g., 'character movement', 'replication', 'indexing pipeline')"},
				},
				"required": []string{"codebase", "topic"},
			},
		},
		{
			"name":        "generate_claude_md",
			"description": "Generate a CLAUDE.md file with project overview, key modules, important symbols, and file structure. Suitable for writing to CLAUDE.md to give Claude Code context about the codebase.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase":    codebaseParam,
					"top_symbols": map[string]interface{}{"type": "integer", "default": 20, "description": "Number of top symbols to include (by reference count)"},
				},
				"required": []string{"codebase"},
			},
		},
		{
			"name":        "reindex",
			"description": "Trigger a reindex of a codebase. Sends a command to the connected indexer daemon. Returns immediately with a request ID that can be tracked with reindex_status.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codebase": codebaseParam,
					"full":     map[string]interface{}{"type": "boolean", "default": false, "description": "Force full re-index (ignore content hashes). Default is incremental."},
				},
				"required": []string{"codebase"},
			},
		},
		{
			"name":        "reindex_status",
			"description": "Check the status of a reindex operation. Can query by request_id, codebase, or return all recent statuses.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"request_id": map[string]interface{}{"type": "string", "description": "Specific reindex request ID to check"},
					"codebase":   map[string]interface{}{"type": "string", "description": "Return latest reindex status for this codebase"},
				},
			},
		},
	}

	return &jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"tools": tools},
	}
}

func (s *Server) handleToolsCall(ctx context.Context, req *jsonrpcRequest) *jsonrpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: "invalid params"},
		}
	}

	start := time.Now()

	var result interface{}
	var err error
	var codebase string

	switch params.Name {
	case "list_codebases":
		result, err = s.toolListCodebases(ctx)
	case "search_code":
		result, err, codebase = s.toolSearchCode(ctx, params.Arguments)
	case "get_symbol":
		result, err, codebase = s.toolGetSymbol(ctx, params.Arguments)
	case "list_modules":
		result, err, codebase = s.toolListModules(ctx, params.Arguments)
	case "get_references":
		result, err, codebase = s.toolGetReferences(ctx, params.Arguments)
	case "get_class_hierarchy":
		result, err, codebase = s.toolGetClassHierarchy(ctx, params.Arguments)
	case "get_file_context":
		result, err, codebase = s.toolGetFileContext(ctx, params.Arguments)
	case "get_module_summary":
		result, err, codebase = s.toolGetModuleSummary(ctx, params.Arguments)
	case "explain_subsystem":
		result, err, codebase = s.toolExplainSubsystem(ctx, params.Arguments)
	case "generate_claude_md":
		result, err, codebase = s.toolGenerateClaudeMD(ctx, params.Arguments)
	case "reindex":
		result, err, codebase = s.toolReindex(ctx, params.Arguments)
	case "reindex_status":
		result, err = s.toolReindexStatus(ctx, params.Arguments)
	default:
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)},
		}
	}

	// Record metrics for the tool call
	if codebase == "" {
		codebase = "_global"
	}
	metrics.RecordToolCall(params.Name, codebase, time.Since(start))

	if err != nil {
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("Error: %v", err)},
				},
				"isError": true,
			},
		}
	}

	text, _ := json.MarshalIndent(result, "", "  ")
	return &jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": string(text)},
			},
		},
	}
}

// extractCodebase extracts and validates the codebase parameter from tool arguments.
func (s *Server) extractCodebase(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Codebase string `json:"codebase"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing args: %w", err)
	}
	if params.Codebase == "" {
		return "", s.missingCodebaseError(ctx)
	}
	return params.Codebase, nil
}

func (s *Server) missingCodebaseError(ctx context.Context) error {
	if s.store == nil {
		return fmt.Errorf("'codebase' parameter is required (postgres not available to list codebases)")
	}
	codebases, err := s.store.ListCodebases(ctx)
	if err != nil || len(codebases) == 0 {
		return fmt.Errorf("'codebase' parameter is required. Use list_codebases to discover available codebases")
	}
	var names []string
	for _, cb := range codebases {
		names = append(names, cb.ID)
	}
	return fmt.Errorf("'codebase' parameter is required. Available codebases: %s", strings.Join(names, ", "))
}

func (s *Server) writeResponse(resp *jsonrpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Printf("error marshaling response: %v", err)
		return
	}
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func (s *Server) writeError(id interface{}, code int, message, data string) {
	resp := &jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	}
	s.writeResponse(resp)
}
