package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/metrics"
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
	cfg       *config.Config
	store     *postgres.Store
	qdrant    *qdrant.Client
	embedder  *embedding.VoyageClient
	summarGen *summary.Generator
	logger    *log.Logger
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

func (s *Server) initBackends(ctx context.Context) error {
	env, err := s.cfg.ResolveEnv()
	if err != nil {
		s.logger.Printf("warning: env resolution failed (backends may be unavailable): %v", err)
		return nil
	}

	// PostgreSQL
	dsn := s.cfg.PostgresDSN(env)
	store, err := postgres.NewStore(ctx, dsn, s.cfg.Metadata.MaxConnections)
	if err != nil {
		s.logger.Printf("warning: postgres unavailable: %v", err)
	} else {
		s.store = store
	}

	// Qdrant
	s.qdrant = qdrant.NewClient(s.cfg.Vector.URL, s.cfg.Vector.CollectionPrefix, env.VectorAPIKey)

	// Embedder
	s.embedder = embedding.NewVoyageClient(
		env.EmbeddingAPIKey,
		s.cfg.Embedding.Model,
		s.cfg.Embedding.Dimensions,
		s.cfg.Indexing.ConcurrentReqs,
	)

	// Summary generator
	if s.cfg.Summaries.Enabled && env.SummaryAPIKey != "" && s.store != nil {
		s.summarGen = summary.NewGenerator(
			env.SummaryAPIKey,
			s.cfg.Summaries.Model,
			s.store,
			s.cfg.Codebase.Name,
			s.logger,
		)
	}

	return nil
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
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req *jsonrpcRequest) *jsonrpcResponse {
	tools := []map[string]interface{}{
		{
			"name":        "search_code",
			"description": "Search the indexed codebase using natural language or code patterns. Uses hybrid semantic + keyword search.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":         map[string]interface{}{"type": "string", "description": "Natural language query or code pattern to search for"},
					"module_filter": map[string]interface{}{"type": "string", "description": "Restrict search to a specific module"},
					"kind_filter":   map[string]interface{}{"type": "string", "enum": []string{"function", "class", "struct", "enum", "macro", "any"}},
					"limit":         map[string]interface{}{"type": "integer", "default": 10},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "get_symbol",
			"description": "Look up a specific symbol (class, function, struct, enum) by name. Uses fuzzy matching.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string", "description": "Symbol name, can be short or qualified"},
					"kind": map[string]interface{}{"type": "string", "enum": []string{"function", "class", "struct", "enum", "macro", "any"}, "default": "any"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "list_modules",
			"description": "List all top-level modules in the codebase with file counts and statistics.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"parent": map[string]interface{}{"type": "string", "description": "List submodules under a parent module"},
				},
			},
		},
		{
			"name":        "get_references",
			"description": "Find all symbols that call or reference a given symbol. Returns callers, type users, or inheritance references.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol":   map[string]interface{}{"type": "string", "description": "Symbol name to find references for (short or qualified)"},
					"ref_kind": map[string]interface{}{"type": "string", "enum": []string{"calls", "inherits", "references", "any"}, "default": "any", "description": "Filter by relationship kind"},
					"limit":    map[string]interface{}{"type": "integer", "default": 20},
				},
				"required": []string{"symbol"},
			},
		},
		{
			"name":        "get_class_hierarchy",
			"description": "Traverse inheritance hierarchy for a class or struct. Shows parent types and child types.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"class_name": map[string]interface{}{"type": "string", "description": "Class or struct name to start from"},
					"direction":  map[string]interface{}{"type": "string", "enum": []string{"parents", "children", "both"}, "default": "both"},
					"depth":      map[string]interface{}{"type": "integer", "default": 5, "description": "Maximum traversal depth"},
				},
				"required": []string{"class_name"},
			},
		},
		{
			"name":        "get_file_context",
			"description": "Get a file's content with architectural context: defined symbols, what depends on it, and module info.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"filepath":   map[string]interface{}{"type": "string", "description": "Relative filepath within the codebase"},
					"line_start": map[string]interface{}{"type": "integer", "description": "Start line (optional, returns range instead of full file)"},
					"line_end":   map[string]interface{}{"type": "integer", "description": "End line (optional)"},
				},
				"required": []string{"filepath"},
			},
		},
		{
			"name":        "get_module_summary",
			"description": "Get a high-level architectural summary of a module. Returns purpose, key classes, dependencies, and statistics. Falls back to on-demand generation if no cached summary exists.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"module": map[string]interface{}{"type": "string", "description": "Module name (e.g., 'Chaos', 'GameplayAbilities', 'parser')"},
				},
				"required": []string{"module"},
			},
		},
		{
			"name":        "explain_subsystem",
			"description": "Get a detailed architectural explanation of how a subsystem or feature works, including data flow, key extension points, and common patterns. Uses semantic search to find relevant code, then generates an explanation.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{"type": "string", "description": "The subsystem or topic to explain (e.g., 'character movement', 'replication', 'indexing pipeline')"},
				},
				"required": []string{"topic"},
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

	switch params.Name {
	case "search_code":
		result, err = s.toolSearchCode(ctx, params.Arguments)
	case "get_symbol":
		result, err = s.toolGetSymbol(ctx, params.Arguments)
	case "list_modules":
		result, err = s.toolListModules(ctx, params.Arguments)
	case "get_references":
		result, err = s.toolGetReferences(ctx, params.Arguments)
	case "get_class_hierarchy":
		result, err = s.toolGetClassHierarchy(ctx, params.Arguments)
	case "get_file_context":
		result, err = s.toolGetFileContext(ctx, params.Arguments)
	case "get_module_summary":
		result, err = s.toolGetModuleSummary(ctx, params.Arguments)
	case "explain_subsystem":
		result, err = s.toolExplainSubsystem(ctx, params.Arguments)
	default:
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)},
		}
	}

	// Record metrics for the tool call
	metrics.RecordToolCall(params.Name, time.Since(start))

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

func (s *Server) toolSearchCode(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query        string `json:"query"`
		ModuleFilter string `json:"module_filter"`
		KindFilter   string `json:"kind_filter"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 25 {
		params.Limit = 25
	}

	if s.embedder == nil || s.qdrant == nil {
		return nil, fmt.Errorf("search backends not available")
	}

	// Generate embedding for query
	vec, err := s.embedder.Embed(ctx, params.Query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	searchReq := qdrant.SearchRequest{
		DenseVector:  vec,
		ModuleFilter: params.ModuleFilter,
		KindFilter:   params.KindFilter,
		Limit:        params.Limit,
	}

	results, err := s.qdrant.HybridSearch(ctx, s.cfg.Codebase.Name, searchReq)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	type searchResult struct {
		Score         float64 `json:"score"`
		Filepath      string  `json:"filepath"`
		QualifiedName string  `json:"qualified_name"`
		Kind          string  `json:"kind"`
		Module        string  `json:"module"`
		LineStart     int     `json:"line_start"`
		LineEnd       int     `json:"line_end"`
		Content       string  `json:"content"`
	}

	var out []searchResult
	for _, r := range results {
		sr := searchResult{Score: r.Score}
		if v, ok := r.Payload["filepath"].(string); ok {
			sr.Filepath = v
		}
		if v, ok := r.Payload["qualified_name"].(string); ok {
			sr.QualifiedName = v
		}
		if v, ok := r.Payload["kind"].(string); ok {
			sr.Kind = v
		}
		if v, ok := r.Payload["module"].(string); ok {
			sr.Module = v
		}
		if v, ok := r.Payload["line_start"].(float64); ok {
			sr.LineStart = int(v)
		}
		if v, ok := r.Payload["line_end"].(float64); ok {
			sr.LineEnd = int(v)
		}
		if v, ok := r.Payload["content"].(string); ok {
			sr.Content = v
		}
		out = append(out, sr)
	}

	return map[string]interface{}{"results": out}, nil
}

func (s *Server) toolGetSymbol(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}
	if params.Kind == "" {
		params.Kind = "any"
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	symbols, err := s.store.FuzzySearchSymbols(ctx, s.cfg.Codebase.Name, params.Name, params.Kind, 5)
	if err != nil {
		return nil, fmt.Errorf("symbol lookup: %w", err)
	}

	type symbolResult struct {
		Name       string `json:"name"`
		Qualified  string `json:"qualified"`
		Kind       string `json:"kind"`
		Filepath   string `json:"filepath"`
		LineStart  int    `json:"line_start"`
		LineEnd    int    `json:"line_end"`
		Module     string `json:"module"`
		Signature  string `json:"signature,omitempty"`
		DocComment string `json:"doc_comment,omitempty"`
	}

	var out []symbolResult
	for _, sym := range symbols {
		out = append(out, symbolResult{
			Name:       sym.Name,
			Qualified:  sym.Qualified,
			Kind:       sym.Kind,
			Filepath:   sym.Filepath,
			LineStart:  sym.LineStart,
			LineEnd:    sym.LineEnd,
			Module:     sym.Module,
			Signature:  sym.Signature,
			DocComment: sym.DocComment,
		})
	}

	return map[string]interface{}{"symbols": out}, nil
}

func (s *Server) toolListModules(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Parent string `json:"parent"`
	}
	if args != nil {
		json.Unmarshal(args, &params)
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	modules, err := s.store.ListModules(ctx, s.cfg.Codebase.Name, params.Parent)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}

	type moduleResult struct {
		Module        string `json:"module"`
		ClassCount    int    `json:"class_count"`
		FunctionCount int    `json:"function_count"`
		StructCount   int    `json:"struct_count"`
		FileCount     int    `json:"file_count"`
		EstimatedLOC  int    `json:"estimated_loc"`
	}

	var out []moduleResult
	for _, m := range modules {
		out = append(out, moduleResult{
			Module:        m.Module,
			ClassCount:    m.ClassCount,
			FunctionCount: m.FunctionCount,
			StructCount:   m.StructCount,
			FileCount:     m.FileCount,
			EstimatedLOC:  m.EstimatedLOC,
		})
	}

	return map[string]interface{}{"modules": out}, nil
}

func (s *Server) toolGetReferences(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Symbol  string `json:"symbol"`
		RefKind string `json:"ref_kind"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}
	if params.RefKind == "" || params.RefKind == "any" {
		params.RefKind = ""
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	refs, err := s.store.GetReferences(ctx, s.cfg.Codebase.Name, params.Symbol, params.RefKind, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("get references: %w", err)
	}

	type refResult struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Filepath  string `json:"filepath"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Module    string `json:"module,omitempty"`
		RefKind   string `json:"ref_kind"`
		RefLine   int    `json:"ref_line"`
	}

	var out []refResult
	for _, r := range refs {
		out = append(out, refResult{
			Name:      r.SymbolName,
			Qualified: r.SymbolQualified,
			Kind:      r.SymbolKind,
			Filepath:  r.Filepath,
			LineStart: r.LineStart,
			LineEnd:   r.LineEnd,
			Module:    r.Module,
			RefKind:   r.RefKind,
			RefLine:   r.RefLine,
		})
	}

	return map[string]interface{}{"references": out, "symbol": params.Symbol}, nil
}

func (s *Server) toolGetClassHierarchy(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		ClassName string `json:"class_name"`
		Direction string `json:"direction"`
		Depth     int    `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}
	if params.Direction == "" {
		params.Direction = "both"
	}
	if params.Depth <= 0 {
		params.Depth = 5
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	nodes, err := s.store.GetClassHierarchy(ctx, s.cfg.Codebase.Name, params.ClassName, params.Direction, params.Depth)
	if err != nil {
		return nil, fmt.Errorf("get class hierarchy: %w", err)
	}

	type hierarchyResult struct {
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Depth     int    `json:"depth"`
		Direction string `json:"direction"`
	}

	var out []hierarchyResult
	for _, n := range nodes {
		out = append(out, hierarchyResult{
			Qualified: n.Qualified,
			Kind:      n.Kind,
			Depth:     n.Depth,
			Direction: n.Direction,
		})
	}

	return map[string]interface{}{"class_name": params.ClassName, "hierarchy": out}, nil
}

func (s *Server) toolGetFileContext(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Filepath  string `json:"filepath"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	// Get symbols defined in this file
	symbols, err := s.store.GetFileSymbols(ctx, s.cfg.Codebase.Name, params.Filepath)
	if err != nil {
		return nil, fmt.Errorf("get file symbols: %w", err)
	}

	type symInfo struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Signature string `json:"signature,omitempty"`
	}
	var symOut []symInfo
	module := ""
	for _, sym := range symbols {
		symOut = append(symOut, symInfo{
			Name:      sym.Name,
			Qualified: sym.Qualified,
			Kind:      sym.Kind,
			LineStart: sym.LineStart,
			LineEnd:   sym.LineEnd,
			Signature: sym.Signature,
		})
		if module == "" && sym.Module != "" {
			module = sym.Module
		}
	}

	// Get dependents (other files that reference this file's symbols)
	dependents, err := s.store.GetFileDependents(ctx, s.cfg.Codebase.Name, params.Filepath, 20)
	if err != nil {
		return nil, fmt.Errorf("get file dependents: %w", err)
	}

	type depInfo struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Filepath  string `json:"filepath"`
		RefKind   string `json:"ref_kind"`
	}
	var depOut []depInfo
	for _, d := range dependents {
		depOut = append(depOut, depInfo{
			Name:      d.SymbolName,
			Qualified: d.SymbolQualified,
			Kind:      d.SymbolKind,
			Filepath:  d.Filepath,
			RefKind:   d.RefKind,
		})
	}

	result := map[string]interface{}{
		"filepath":   params.Filepath,
		"module":     module,
		"symbols":    symOut,
		"dependents": depOut,
	}

	// Read file content if requested (line range or full)
	if s.cfg.Codebase.Path != "" {
		fullPath := filepath.Join(s.cfg.Codebase.Path, params.Filepath)
		data, err := os.ReadFile(fullPath)
		if err == nil {
			content := string(data)
			if params.LineStart > 0 || params.LineEnd > 0 {
				lines := strings.Split(content, "\n")
				start := params.LineStart - 1
				if start < 0 {
					start = 0
				}
				end := params.LineEnd
				if end <= 0 || end > len(lines) {
					end = len(lines)
				}
				if start < len(lines) {
					content = strings.Join(lines[start:end], "\n")
				}
			}
			result["content"] = content
		}
	}

	return result, nil
}

func (s *Server) toolGetModuleSummary(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Module string `json:"module"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	// Try to get cached summary first
	sum, err := s.store.GetSummary(ctx, s.cfg.Codebase.Name, params.Module, "module")
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}

	// If no cached summary, try on-demand generation
	if sum == nil && s.summarGen != nil {
		sum, err = s.summarGen.GenerateModuleSummary(ctx, params.Module)
		if err != nil {
			s.logger.Printf("warning: on-demand summary generation failed: %v", err)
			// Fall through to return module stats without summary
		}
	}

	// Get module stats
	modules, err := s.store.ListModules(ctx, s.cfg.Codebase.Name, params.Module)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}

	// Build result
	result := map[string]interface{}{
		"module": params.Module,
	}

	if sum != nil {
		result["summary"] = sum.SummaryText

		var keyClasses []string
		json.Unmarshal([]byte(sum.KeyClasses), &keyClasses)
		result["key_classes"] = keyClasses

		var deps []string
		json.Unmarshal([]byte(sum.Dependencies), &deps)
		result["dependencies"] = deps
	}

	// Aggregate stats across matching modules
	var totalFiles, totalLOC int
	var submodules []string
	for _, m := range modules {
		totalFiles += m.FileCount
		totalLOC += m.EstimatedLOC
		if m.Module != params.Module {
			submodules = append(submodules, m.Module)
		}
	}
	result["file_count"] = totalFiles
	result["estimated_loc"] = totalLOC
	if len(submodules) > 0 {
		result["submodules"] = submodules
	}

	if sum == nil {
		result["summary"] = fmt.Sprintf("No summary available for module '%s'. Summary generation may be disabled or the module may not exist.", params.Module)
	}

	return result, nil
}

func (s *Server) toolExplainSubsystem(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err)
	}

	if s.summarGen == nil {
		return nil, fmt.Errorf("summary generation not available (check summaries.enabled and API key)")
	}

	// Check for cached subsystem summary
	cached, err := s.store.GetSummary(ctx, s.cfg.Codebase.Name, params.Topic, "subsystem")
	if err != nil {
		return nil, fmt.Errorf("check cached summary: %w", err)
	}

	if cached != nil {
		var keyClasses, relatedModules []string
		json.Unmarshal([]byte(cached.KeyClasses), &keyClasses)
		json.Unmarshal([]byte(cached.Dependencies), &relatedModules)
		return map[string]interface{}{
			"topic":           params.Topic,
			"explanation":     cached.SummaryText,
			"key_classes":     keyClasses,
			"related_modules": relatedModules,
		}, nil
	}

	// Use semantic search to find relevant code chunks
	var relevantChunks []string
	if s.embedder != nil && s.qdrant != nil {
		vec, err := s.embedder.Embed(ctx, params.Topic)
		if err == nil {
			results, err := s.qdrant.HybridSearch(ctx, s.cfg.Codebase.Name, qdrant.SearchRequest{
				DenseVector: vec,
				Limit:       10,
			})
			if err == nil {
				for _, r := range results {
					if content, ok := r.Payload["content"].(string); ok {
						prefix := ""
						if fp, ok := r.Payload["filepath"].(string); ok {
							prefix = "// File: " + fp + "\n"
						}
						if qn, ok := r.Payload["qualified_name"].(string); ok {
							prefix += "// Symbol: " + qn + "\n"
						}
						relevantChunks = append(relevantChunks, prefix+content)
					}
				}
			}
		}
	}

	// Generate explanation
	explanation, keyClasses, relatedModules, err := s.summarGen.GenerateSubsystemExplanation(ctx, params.Topic, relevantChunks)
	if err != nil {
		return nil, fmt.Errorf("generate explanation: %w", err)
	}

	return map[string]interface{}{
		"topic":           params.Topic,
		"explanation":     explanation,
		"key_classes":     keyClasses,
		"related_modules": relatedModules,
	}, nil
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
