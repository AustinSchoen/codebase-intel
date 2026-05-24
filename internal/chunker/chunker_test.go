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

// --- Additional edge case tests ---

func TestNew_Defaults(t *testing.T) {
	// Zero/negative values should get defaults
	c := New(0, 0)
	if c.maxLines != 200 {
		t.Errorf("expected default maxLines=200, got %d", c.maxLines)
	}
	if c.overlapLines != 20 {
		t.Errorf("expected default overlapLines=20, got %d", c.overlapLines)
	}

	c2 := New(-5, -10)
	if c2.maxLines != 200 {
		t.Errorf("expected default maxLines=200 for negative, got %d", c2.maxLines)
	}
}

func TestChunkFile_SymbolWithNoContent(t *testing.T) {
	c := New(200, 20)
	result := &parser.ParseResult{
		Filepath: "empty.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "Empty",
				Qualified: "Empty",
				Kind:      "function",
				Content:   "",
				Language:  "go",
				LineStart: 1,
				LineEnd:   1,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk even for empty content, got %d", len(chunks))
	}
	if chunks[0].Content != "" {
		t.Errorf("expected empty content, got %q", chunks[0].Content)
	}
}

func TestChunkFile_MaxLinesExactBoundary(t *testing.T) {
	// Symbol with exactly maxLines lines → should be 1 chunk (no split)
	c := New(10, 3)

	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "line")
	}
	content := strings.Join(lines, "\n")

	result := &parser.ParseResult{
		Filepath: "exact.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "ExactFunc",
				Qualified: "ExactFunc",
				Kind:      "function",
				Content:   content,
				Language:  "go",
				LineStart: 1,
				LineEnd:   10,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for exactly maxLines lines, got %d", len(chunks))
	}
}

func TestChunkFile_MaxLinesPlusOne(t *testing.T) {
	// Symbol with maxLines+1 lines → should split into 2 chunks
	c := New(10, 3)

	var lines []string
	for i := 0; i < 11; i++ {
		lines = append(lines, "line")
	}
	content := strings.Join(lines, "\n")

	result := &parser.ParseResult{
		Filepath: "split.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "SplitFunc",
				Qualified: "SplitFunc",
				Kind:      "function",
				Content:   content,
				Language:  "go",
				LineStart: 1,
				LineEnd:   11,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for maxLines+1 lines, got %d", len(chunks))
	}
}

func TestChunkFile_OverlapContent(t *testing.T) {
	// Verify overlap: with maxLines=5, overlapLines=2, stride=3
	// First chunk: lines 0-4, second: lines 3-7, etc.
	c := New(5, 2)

	lines := []string{"L0", "L1", "L2", "L3", "L4", "L5", "L6", "L7"}
	content := strings.Join(lines, "\n")

	result := &parser.ParseResult{
		Filepath: "overlap.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "OverlapFunc",
				Qualified: "OverlapFunc",
				Kind:      "function",
				Content:   content,
				Language:  "go",
				LineStart: 1,
				LineEnd:   8,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// First chunk should contain L0-L4
	if !strings.Contains(chunks[0].Content, "L0") {
		t.Error("first chunk should start with L0")
	}
	if !strings.Contains(chunks[0].Content, "L4") {
		t.Error("first chunk should include L4")
	}

	// Second chunk should contain L3 (overlap)
	if !strings.Contains(chunks[1].Content, "L3") {
		t.Error("second chunk should contain overlapping line L3")
	}
}

func TestChunkFile_LanguagePreserved(t *testing.T) {
	c := New(200, 20)
	result := &parser.ParseResult{
		Filepath: "test.py",
		Language: "python",
		Symbols: []parser.Symbol{
			{
				Name:      "hello",
				Qualified: "hello",
				Kind:      "function",
				Content:   "def hello(): pass",
				Language:  "python",
				LineStart: 1,
				LineEnd:   1,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Language != "python" {
		t.Errorf("expected language=python, got %s", chunks[0].Language)
	}
}

func TestChunkFile_ModuleInference(t *testing.T) {
	c := New(200, 20)
	result := &parser.ParseResult{
		Filepath: "src/controllers/auth.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{
				Name:      "Login",
				Qualified: "Login",
				Kind:      "function",
				Content:   "func Login() {}",
				Language:  "go",
				LineStart: 1,
				LineEnd:   1,
			},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Module != "controllers" {
		t.Errorf("expected module=controllers, got %s", chunks[0].Module)
	}
}

func TestEstimateTokens_EdgeCases(t *testing.T) {
	tests := []struct {
		input  string
		expect int
	}{
		{"", 0},
		{"abc", 0},                     // 3/4 = 0
		{"abcd", 1},                    // 4/4 = 1
		{"12345678", 2},                // 8/4 = 2
		{strings.Repeat("x", 100), 25}, // 100/4 = 25
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.expect {
			t.Errorf("EstimateTokens(%d chars) = %d, want %d", len(tt.input), got, tt.expect)
		}
	}
}

func TestInferModule_AdditionalCases(t *testing.T) {
	tests := []struct {
		path   string
		expect string
	}{
		{"src/utils/helper.go", "utils"},
		{"lib/core/engine.py", "core"},
		{".", ""},
		{"single.go", ""},
		{"deep/nested/dir/file.go", "deep"},
	}
	for _, tt := range tests {
		got := inferModule(tt.path)
		if got != tt.expect {
			t.Errorf("inferModule(%q) = %q, want %q", tt.path, got, tt.expect)
		}
	}
}

func TestExtractDeclarationHeader(t *testing.T) {
	tests := []struct {
		content string
		expect  string
	}{
		{"class Foo {\n  method() {}\n}", "class Foo {"},
		{"struct Bar", "struct Bar"},
		{"type MyStruct struct {\n\tField int\n}", "type MyStruct struct {"},
		{"", ""},
		{"single_line_only", "single_line_only"},
	}
	for _, tt := range tests {
		got := extractDeclarationHeader(tt.content)
		if got != tt.expect {
			t.Errorf("extractDeclarationHeader(%q) = %q, want %q", tt.content, got, tt.expect)
		}
	}
}

func TestContentHash_EmptyContent(t *testing.T) {
	hash := ContentHash([]byte{})
	if hash == "" {
		t.Error("hash of empty content should not be empty")
	}
	if len(hash) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d", len(hash))
	}
}

func TestChunkFile_MultipleSymbols(t *testing.T) {
	c := New(200, 20)
	result := &parser.ParseResult{
		Filepath: "multi.go",
		Language: "go",
		Symbols: []parser.Symbol{
			{Name: "A", Qualified: "A", Kind: "function", Content: "func A() {}", Language: "go", LineStart: 1, LineEnd: 1},
			{Name: "B", Qualified: "B", Kind: "function", Content: "func B() {}", Language: "go", LineStart: 3, LineEnd: 3},
			{Name: "C", Qualified: "C", Kind: "struct", Content: "type C struct{}", Language: "go", LineStart: 5, LineEnd: 5},
		},
	}

	chunks := c.ChunkFile(result)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks for 3 symbols, got %d", len(chunks))
	}

	// Verify all have unique IDs
	ids := make(map[string]bool)
	for _, ch := range chunks {
		if ids[ch.ID] {
			t.Error("duplicate chunk ID found")
		}
		ids[ch.ID] = true
	}

	// Verify qualified names match
	if chunks[0].QualifiedName != "A" || chunks[1].QualifiedName != "B" || chunks[2].QualifiedName != "C" {
		t.Error("chunk qualified names don't match symbols")
	}
}
