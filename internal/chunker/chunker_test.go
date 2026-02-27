package chunker

import (
	"strings"
	"testing"

	"github.com/AustinSchoen/codebase-intel/internal/parser"
)

func TestChunkFile_SingleSmallFunction(t *testing.T) {
	c := New(200, 20)

	result := &parser.ParseResult{
		Filepath: "main.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "Hello",
				Qualified: "Hello",
				Kind:      "function",
				Content:   "func Hello() string {\n\treturn \"hello\"\n}",
				Language:  "go",
				LineStart: 1,
				LineEnd:   3,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	ch := chunks[0]
	if ch.Kind != "function" {
		t.Errorf("expected kind=function, got %s", ch.Kind)
	}
	if ch.QualifiedName != "Hello" {
		t.Errorf("expected qualified=Hello, got %s", ch.QualifiedName)
	}
	if ch.Filepath != "main.go" {
		t.Errorf("expected filepath=main.go, got %s", ch.Filepath)
	}
	if ch.ID == "" {
		t.Error("chunk ID should not be empty")
	}
	if !strings.Contains(ch.Content, "Hello") {
		t.Error("chunk content should contain function body")
	}
}

func TestChunkFile_LargeFunctionSplit(t *testing.T) {
	c := New(50, 10)

	// Generate a function with 100 lines
	var lines []string
	lines = append(lines, "func BigFunc() {")
	for i := 0; i < 98; i++ {
		lines = append(lines, "\t// line "+string(rune('A'+i%26)))
	}
	lines = append(lines, "}")
	content := strings.Join(lines, "\n")

	result := &parser.ParseResult{
		Filepath: "big.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "BigFunc",
				Qualified: "BigFunc",
				Kind:      "function",
				Content:   content,
				Language:  "go",
				LineStart: 1,
				LineEnd:   100,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for 100-line function with maxLines=50, got %d", len(chunks))
	}

	// Verify no chunk exceeds maxLines
	for i, ch := range chunks {
		chunkLines := strings.Count(ch.Content, "\n") + 1
		if chunkLines > 50 {
			t.Errorf("chunk %d has %d lines, exceeds maxLines=50", i, chunkLines)
		}
	}

	// Verify IDs are unique
	ids := make(map[string]bool)
	for _, ch := range chunks {
		if ids[ch.ID] {
			t.Error("duplicate chunk ID found")
		}
		ids[ch.ID] = true
	}
}

func TestChunkFile_MethodWithParentClass(t *testing.T) {
	c := New(200, 20)

	result := &parser.ParseResult{
		Filepath: "server.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "Server",
				Qualified: "Server",
				Kind:      "struct",
				Content:   "type Server struct {\n\tport int\n}",
				Language:  "go",
				LineStart: 1,
				LineEnd:   3,
			},
			{
				Name:        "Start",
				Qualified:   "Server.Start",
				Kind:        "method",
				Content:     "func (s *Server) Start() error {\n\treturn nil\n}",
				Language:    "go",
				LineStart:   5,
				LineEnd:     7,
				ParentClass: "Server",
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	methodChunk := chunks[1]
	if methodChunk.ParentClass != "Server" {
		t.Errorf("expected parentClass=Server, got %s", methodChunk.ParentClass)
	}
	if !strings.Contains(methodChunk.ContextPrefix, "Server") {
		t.Error("method context prefix should reference parent class")
	}
}

func TestChunkFile_EmptyResult(t *testing.T) {
	c := New(200, 20)

	chunks := c.ChunkFile(nil)
	if chunks != nil {
		t.Errorf("expected nil for nil input, got %v", chunks)
	}

	chunks = c.ChunkFile(&parser.ParseResult{})
	if chunks != nil {
		t.Errorf("expected nil for empty parse result, got %v", chunks)
	}
}

func TestChunkFile_ContextPrefix(t *testing.T) {
	c := New(200, 20)

	result := &parser.ParseResult{
		Filepath: "internal/server/handler.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "HandleRequest",
				Qualified: "HandleRequest",
				Kind:      "function",
				Content:   "func HandleRequest() {}",
				Language:  "go",
				LineStart: 10,
				LineEnd:   10,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	prefix := chunks[0].ContextPrefix
	if !strings.Contains(prefix, "handler.go") {
		t.Errorf("context prefix should contain filepath, got: %s", prefix)
	}
	if !strings.Contains(prefix, "function") {
		t.Errorf("context prefix should contain kind, got: %s", prefix)
	}
}

func TestContentHash(t *testing.T) {
	hash1 := ContentHash([]byte("hello world"))
	hash2 := ContentHash([]byte("hello world"))
	hash3 := ContentHash([]byte("different content"))

	if hash1 != hash2 {
		t.Error("same content should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different content should produce different hash")
	}
	if len(hash1) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d", len(hash1))
	}
}

func TestEstimateTokens(t *testing.T) {
	tokens := EstimateTokens("hello world") // 11 chars
	if tokens < 1 {
		t.Error("should estimate at least 1 token")
	}
	if tokens > 11 {
		t.Errorf("should not overestimate: got %d for 11 chars", tokens)
	}
}

func TestInferModule(t *testing.T) {
	tests := []struct {
		path   string
		expect string
	}{
		{"cmd/server/main.go", "cmd"},
		{"internal/config/config.go", "config"},
		{"pkg/utils/helper.go", "utils"},
		{"main.go", ""},
	}

	for _, tt := range tests {
		got := inferModule(tt.path)
		if got != tt.expect {
			t.Errorf("inferModule(%q) = %q, want %q", tt.path, got, tt.expect)
		}
	}
}

func TestChunkID_Deterministic(t *testing.T) {
	id1 := chunkID("file.go", "Func", 10)
	id2 := chunkID("file.go", "Func", 10)
	id3 := chunkID("file.go", "Func", 11)

	if id1 != id2 {
		t.Error("same inputs should produce same ID")
	}
	if id1 == id3 {
		t.Error("different inputs should produce different ID")
	}
}
