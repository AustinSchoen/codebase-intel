package postgres

import (
	"testing"
)

func TestNilIfEmpty(t *testing.T) {
	tests := []struct {
		input  string
		isNil  bool
		expect interface{}
	}{
		{"", true, nil},
		{"hello", false, "hello"},
		{" ", false, " "},
	}

	for _, tt := range tests {
		result := nilIfEmpty(tt.input)
		if tt.isNil {
			if result != nil {
				t.Errorf("nilIfEmpty(%q) = %v, want nil", tt.input, result)
			}
		} else {
			if result != tt.expect {
				t.Errorf("nilIfEmpty(%q) = %v, want %v", tt.input, result, tt.expect)
			}
		}
	}
}

func TestNilIfZero(t *testing.T) {
	tests := []struct {
		input  int
		isNil  bool
		expect interface{}
	}{
		{0, true, nil},
		{1, false, 1},
		{-1, false, -1},
		{100, false, 100},
	}

	for _, tt := range tests {
		result := nilIfZero(tt.input)
		if tt.isNil {
			if result != nil {
				t.Errorf("nilIfZero(%d) = %v, want nil", tt.input, result)
			}
		} else {
			if result != tt.expect {
				t.Errorf("nilIfZero(%d) = %v, want %v", tt.input, result, tt.expect)
			}
		}
	}
}

func TestSymbolType(t *testing.T) {
	// Verify Symbol struct can be created with all fields
	sym := Symbol{
		ID:         "sym-1",
		CodebaseID: "cb-1",
		Name:       "MyFunc",
		Qualified:  "pkg.MyFunc",
		Kind:       "function",
		Filepath:   "pkg/main.go",
		LineStart:  10,
		LineEnd:    20,
		Module:     "pkg",
		ParentID:   "",
		Signature:  "func MyFunc() error",
		DocComment: "// MyFunc does something",
	}

	if sym.ID != "sym-1" {
		t.Errorf("expected ID=sym-1, got %s", sym.ID)
	}
	if sym.Kind != "function" {
		t.Errorf("expected Kind=function, got %s", sym.Kind)
	}
}

func TestChunkRecordType(t *testing.T) {
	chunk := ChunkRecord{
		ID:            "chunk-1",
		CodebaseID:    "cb-1",
		SymbolID:      "sym-1",
		Filepath:      "main.go",
		LineStart:     1,
		LineEnd:       50,
		Kind:          "function",
		QualifiedName: "main.Run",
		Module:        "main",
		TokenCount:    250,
	}

	if chunk.TokenCount != 250 {
		t.Errorf("expected TokenCount=250, got %d", chunk.TokenCount)
	}
}

func TestFileStateType(t *testing.T) {
	state := FileState{
		CodebaseID:     "cb-1",
		Filepath:       "main.go",
		ContentHash:    "abc123",
		StructuralHash: "def456",
		ChunkCount:     5,
	}

	if state.ContentHash != "abc123" {
		t.Errorf("expected ContentHash=abc123, got %s", state.ContentHash)
	}
	if state.ChunkCount != 5 {
		t.Errorf("expected ChunkCount=5, got %d", state.ChunkCount)
	}
}

func TestModuleStatType(t *testing.T) {
	stat := ModuleStat{
		CodebaseID:    "cb-1",
		Module:        "parser",
		ClassCount:    5,
		FunctionCount: 20,
		StructCount:   3,
		FileCount:     8,
		EstimatedLOC:  1500,
	}

	if stat.Module != "parser" {
		t.Errorf("expected Module=parser, got %s", stat.Module)
	}
	if stat.FunctionCount != 20 {
		t.Errorf("expected FunctionCount=20, got %d", stat.FunctionCount)
	}
}

func TestReferenceResultType(t *testing.T) {
	ref := ReferenceResult{
		SymbolName:      "Foo",
		SymbolQualified: "pkg.Foo",
		SymbolKind:      "function",
		Filepath:        "pkg/foo.go",
		LineStart:       10,
		LineEnd:         20,
		Module:          "pkg",
		RefKind:         "calls",
		RefLine:         15,
	}

	if ref.RefKind != "calls" {
		t.Errorf("expected RefKind=calls, got %s", ref.RefKind)
	}
}

func TestHierarchyNodeType(t *testing.T) {
	node := HierarchyNode{
		ID:        "h-1",
		Qualified: "BaseClass",
		Kind:      "class",
		Depth:     0,
		Direction: "self",
	}

	if node.Direction != "self" {
		t.Errorf("expected Direction=self, got %s", node.Direction)
	}

	child := HierarchyNode{
		ID:        "h-2",
		Qualified: "ChildClass",
		Kind:      "class",
		Depth:     1,
		Direction: "child",
	}

	if child.Depth != 1 {
		t.Errorf("expected Depth=1, got %d", child.Depth)
	}
}

func TestSymbolRefType(t *testing.T) {
	ref := SymbolRef{
		ID:        "ref-1",
		Name:      "MyFunc",
		Qualified: "pkg.MyFunc",
	}

	if ref.Qualified != "pkg.MyFunc" {
		t.Errorf("expected Qualified=pkg.MyFunc, got %s", ref.Qualified)
	}
}

func TestCodebaseInfoType(t *testing.T) {
	info := CodebaseInfo{
		ID:          "my-project",
		DisplayName: "My Project",
		RootPath:    "/home/user/project",
		FileCount:   100,
		SymbolCount: 5000,
	}

	if info.FileCount != 100 {
		t.Errorf("expected FileCount=100, got %d", info.FileCount)
	}
	if info.SymbolCount != 5000 {
		t.Errorf("expected SymbolCount=5000, got %d", info.SymbolCount)
	}
}

func TestImportantSymbolType(t *testing.T) {
	sym := ImportantSymbol{
		Qualified:    "Server.Start",
		Kind:         "method",
		Module:       "server",
		Signature:    "func (s *Server) Start() error",
		IncomingRefs: 42,
	}

	if sym.IncomingRefs != 42 {
		t.Errorf("expected IncomingRefs=42, got %d", sym.IncomingRefs)
	}
}
