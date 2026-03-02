package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"testing"

	"github.com/AustinSchoen/codebase-intel/internal/config"
)

// newTestServer creates a minimal Server with nil backends for testing
// request routing and error paths.
func newTestServer() *Server {
	cfg := &config.Config{}
	return &Server{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}
}

func TestHandleRequest_Initialize(t *testing.T) {
	s := newTestServer()

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "initialize",
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response for initialize")
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("expected JSONRPC '2.0', got %q", resp.JSONRPC)
	}
	if resp.ID != float64(1) {
		t.Errorf("expected ID 1, got %v", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("expected no error, got %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map[string]interface{}, got %T", resp.Result)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion '2024-11-05', got %v", result["protocolVersion"])
	}

	caps, ok := result["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("expected capabilities to be a map")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("expected 'tools' in capabilities")
	}

	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatal("expected serverInfo to be a map")
	}
	if serverInfo["name"] != "codebase-intel" {
		t.Errorf("expected server name 'codebase-intel', got %v", serverInfo["name"])
	}
}

func TestHandleRequest_NotificationsInitialized(t *testing.T) {
	s := newTestServer()

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	resp := s.handleRequest(context.Background(), req)
	if resp != nil {
		t.Errorf("expected nil response for notifications/initialized, got %+v", resp)
	}
}

func TestHandleRequest_MethodNotFound(t *testing.T) {
	s := newTestServer()

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(2),
		Method:  "nonexistent/method",
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleRequest_ToolsCall_UnknownTool(t *testing.T) {
	s := newTestServer()

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "nonexistent_tool",
		"arguments": map[string]interface{}{},
	})

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(3),
		Method:  "tools/call",
		Params:  json.RawMessage(params),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error == nil {
		t.Fatal("expected error response for unknown tool")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleRequest_ToolsCall_InvalidParams(t *testing.T) {
	s := newTestServer()

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(4),
		Method:  "tools/call",
		Params:  json.RawMessage(`{invalid json`),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error == nil {
		t.Fatal("expected error response for invalid params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected error code -32602, got %d", resp.Error.Code)
	}
}

func TestHandleToolsList(t *testing.T) {
	s := newTestServer()

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(5),
		Method:  "tools/list",
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map[string]interface{}, got %T", resp.Result)
	}

	toolsRaw, ok := result["tools"]
	if !ok {
		t.Fatal("expected 'tools' key in result")
	}

	tools, ok := toolsRaw.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected tools to be []map[string]interface{}, got %T", toolsRaw)
	}

	// Collect tool names
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		name, ok := tool["name"].(string)
		if !ok {
			t.Errorf("expected tool name to be string, got %T", tool["name"])
			continue
		}
		toolNames[name] = true
	}

	// Verify all expected tools are present
	expectedTools := []string{
		"list_codebases",
		"search_code",
		"get_symbol",
		"list_modules",
		"get_references",
		"get_class_hierarchy",
		"get_file_context",
		"get_module_summary",
		"explain_subsystem",
		"generate_claude_md",
		"reindex",
		"reindex_status",
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q to be in tools list", name)
		}
	}

	if len(tools) != len(expectedTools) {
		t.Errorf("expected %d tools, got %d", len(expectedTools), len(tools))
	}

	// Verify tools with required params have inputSchema with required field
	for _, tool := range tools {
		name := tool["name"].(string)
		schema, ok := tool["inputSchema"].(map[string]interface{})
		if !ok {
			t.Errorf("tool %q: expected inputSchema to be a map", name)
			continue
		}

		// Check that search_code has required codebase and query
		if name == "search_code" {
			required, ok := schema["required"].([]string)
			if !ok {
				// JSON unmarshaling may produce []interface{}
				reqRaw, ok := schema["required"].([]interface{})
				if !ok {
					t.Errorf("tool %q: expected required to be an array", name)
					continue
				}
				required = make([]string, len(reqRaw))
				for i, v := range reqRaw {
					required[i] = v.(string)
				}
			}
			reqMap := make(map[string]bool)
			for _, r := range required {
				reqMap[r] = true
			}
			if !reqMap["codebase"] {
				t.Errorf("tool %q: expected 'codebase' in required params", name)
			}
			if !reqMap["query"] {
				t.Errorf("tool %q: expected 'query' in required params", name)
			}
		}
	}
}

func TestToolListCodebases_NoStore(t *testing.T) {
	s := newTestServer()
	// s.store is nil by default

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "list_codebases",
		"arguments": map[string]interface{}{},
	})

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(6),
		Method:  "tools/call",
		Params:  json.RawMessage(params),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// Tool errors are returned as isError results, not JSON-RPC errors
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map, got %T", resp.Result)
	}

	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatal("expected isError=true for list_codebases with no store")
	}

	content, ok := result["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected content to be []map[string]interface{}, got %T", result["content"])
	}
	if len(content) == 0 {
		t.Fatal("expected at least one content entry")
	}
	text, _ := content[0]["text"].(string)
	if text == "" {
		t.Fatal("expected non-empty error text")
	}
	if !containsString(text, "postgres not available") {
		t.Errorf("expected error about postgres not available, got %q", text)
	}
}

func TestToolSearchCode_NoBackends(t *testing.T) {
	s := newTestServer()
	// s.embedder and s.qdrant are nil

	args, _ := json.Marshal(map[string]interface{}{
		"codebase": "test-project",
		"query":    "hello world",
	})

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "search_code",
		"arguments": json.RawMessage(args),
	})

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(7),
		Method:  "tools/call",
		Params:  json.RawMessage(params),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map, got %T", resp.Result)
	}

	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatal("expected isError=true for search_code with no backends")
	}

	content, ok := result["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected content array, got %T", result["content"])
	}
	text, _ := content[0]["text"].(string)
	if !containsString(text, "search backends not available") {
		t.Errorf("expected error about search backends, got %q", text)
	}
}

func TestToolReindex_NoIndexerMgr(t *testing.T) {
	s := newTestServer()
	// s.indexerMgr is nil

	args, _ := json.Marshal(map[string]interface{}{
		"codebase": "test-project",
	})

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "reindex",
		"arguments": json.RawMessage(args),
	})

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(8),
		Method:  "tools/call",
		Params:  json.RawMessage(params),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map, got %T", resp.Result)
	}

	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatal("expected isError=true for reindex with no indexer manager")
	}

	content, ok := result["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected content array, got %T", result["content"])
	}
	text, _ := content[0]["text"].(string)
	if !containsString(text, "HTTP mode") {
		t.Errorf("expected error about HTTP mode, got %q", text)
	}
}

func TestToolReindexStatus_NoIndexerMgr(t *testing.T) {
	s := newTestServer()
	// s.indexerMgr is nil

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "reindex_status",
		"arguments": map[string]interface{}{},
	})

	req := &jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      float64(9),
		Method:  "tools/call",
		Params:  json.RawMessage(params),
	}

	resp := s.handleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Result to be map, got %T", resp.Result)
	}

	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatal("expected isError=true for reindex_status with no indexer manager")
	}

	content, ok := result["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected content array, got %T", result["content"])
	}
	text, _ := content[0]["text"].(string)
	if !containsString(text, "HTTP mode") {
		t.Errorf("expected error about HTTP mode, got %q", text)
	}
}

// containsString checks if s contains substr (case-insensitive is not needed here).
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
