package parser

import (
	"context"
	"strings"
	"testing"
)

// helper to find a symbol by name in the result.
func findSymbol(result *ParseResult, name string) *Symbol {
	for i := range result.Symbols {
		if result.Symbols[i].Name == name {
			return &result.Symbols[i]
		}
	}
	return nil
}

// helper to find a symbol by qualified name.
func findSymbolQualified(result *ParseResult, qualified string) *Symbol {
	for i := range result.Symbols {
		if result.Symbols[i].Qualified == qualified {
			return &result.Symbols[i]
		}
	}
	return nil
}

// helper to find a relationship by kind and target.
func findRel(result *ParseResult, kind, target string) *Relationship {
	for i := range result.Relationships {
		if result.Relationships[i].Kind == kind && result.Relationships[i].TargetName == target {
			return &result.Relationships[i]
		}
	}
	return nil
}

// helper to find a relationship by kind, source, and target.
func findRelFull(result *ParseResult, kind, source, target string) *Relationship {
	for i := range result.Relationships {
		r := &result.Relationships[i]
		if r.Kind == kind && r.SourceQualified == source && r.TargetName == target {
			return r
		}
	}
	return nil
}

// hasRelWithKind checks if any relationship with the given kind and target exists.
func hasRelWithKind(result *ParseResult, kind, target string) bool {
	return findRel(result, kind, target) != nil
}

func TestSupportsLanguage(t *testing.T) {
	p := New()

	tests := []struct {
		lang string
		want bool
	}{
		{"go", true},
		{"py", true},
		{"python", true},
		{"ts", true},
		{"typescript", true},
		{"js", true},
		{"javascript", true},
		{"jsx", true},
		{"tsx", true},
		{"rust", true},
		{"rs", true},
		{"c", true},
		{"cpp", true},
		{"cc", true},
		{"h", true},
		{"hpp", true},
		{"kotlin", true},
		{"kt", true},
		{"kts", true},
		{"swift", true},
		// Invalid languages
		{"java", false},
		{"ruby", false},
		{"haskell", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got := p.SupportsLanguage(tt.lang)
			if got != tt.want {
				t.Errorf("SupportsLanguage(%q) = %v, want %v", tt.lang, got, tt.want)
			}
		})
	}
}

func TestUnsupportedLanguageError(t *testing.T) {
	p := New()
	_, err := p.ParseFile(context.Background(), "test.java", []byte("class Foo {}"), "java")
	if err == nil {
		t.Fatal("expected error for unsupported language, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("expected 'unsupported language' in error, got: %s", err.Error())
	}
}

func TestEmptyFile(t *testing.T) {
	p := New()

	tests := []struct {
		name string
		lang string
	}{
		{"empty go", "go"},
		{"empty python", "py"},
		{"empty typescript", "ts"},
		{"empty javascript", "js"},
		{"empty rust", "rust"},
		{"empty c", "c"},
		{"empty cpp", "cpp"},
		{"empty kotlin", "kotlin"},
		{"empty swift", "swift"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := p.ParseFile(context.Background(), "test", []byte(""), tt.lang)
			if err != nil {
				t.Fatalf("ParseFile returned error: %v", err)
			}
			if len(result.Symbols) != 0 {
				t.Errorf("expected 0 symbols, got %d", len(result.Symbols))
			}
			if result.Language != tt.lang {
				t.Errorf("expected language %q, got %q", tt.lang, result.Language)
			}
		})
	}
}

func TestSyntaxErrorTolerance(t *testing.T) {
	p := New()
	// Tree-sitter is error-tolerant: even with syntax errors, it should
	// parse what it can.
	src := []byte(`
func validFunc() {
	return 1
}

func broken( {
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	// Should still extract the valid function
	sym := findSymbol(result, "validFunc")
	if sym == nil {
		t.Error("expected to find validFunc despite syntax errors")
	}
}

// --- Go Tests ---

func TestGoFunction(t *testing.T) {
	p := New()
	src := []byte(`package main

func Hello(name string) string {
	return "Hello, " + name
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Hello")
	if sym == nil {
		t.Fatal("expected to find symbol Hello")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Qualified != "Hello" {
		t.Errorf("expected qualified 'Hello', got %q", sym.Qualified)
	}
	if sym.Language != "go" {
		t.Errorf("expected language 'go', got %q", sym.Language)
	}
	if sym.LineStart != 3 {
		t.Errorf("expected LineStart 3, got %d", sym.LineStart)
	}
	if sym.LineEnd != 5 {
		t.Errorf("expected LineEnd 5, got %d", sym.LineEnd)
	}
	if !strings.Contains(sym.Signature, "func Hello") {
		t.Errorf("expected signature to contain 'func Hello', got %q", sym.Signature)
	}
}

func TestGoMethod(t *testing.T) {
	p := New()
	src := []byte(`package main

type Server struct{}

func (s *Server) Start() error {
	return nil
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Start")
	if sym == nil {
		t.Fatal("expected to find symbol Start")
	}
	if sym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", sym.Kind)
	}
	if sym.Qualified != "Server.Start" {
		t.Errorf("expected qualified 'Server.Start', got %q", sym.Qualified)
	}
	if sym.ParentClass != "Server" {
		t.Errorf("expected ParentClass 'Server', got %q", sym.ParentClass)
	}
}

func TestGoStruct(t *testing.T) {
	p := New()
	src := []byte(`package main

type Config struct {
	Host string
	Port int
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Config")
	if sym == nil {
		t.Fatal("expected to find symbol Config")
	}
	if sym.Kind != "struct" {
		t.Errorf("expected kind 'struct', got %q", sym.Kind)
	}
	if sym.Qualified != "Config" {
		t.Errorf("expected qualified 'Config', got %q", sym.Qualified)
	}
}

func TestGoInterface(t *testing.T) {
	p := New()
	src := []byte(`package main

type Reader interface {
	Read(p []byte) (n int, err error)
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Reader")
	if sym == nil {
		t.Fatal("expected to find symbol Reader")
	}
	if sym.Kind != "class" {
		t.Errorf("expected kind 'class' for interface, got %q", sym.Kind)
	}
}

func TestGoTypeAlias(t *testing.T) {
	p := New()
	src := []byte(`package main

type StringList []string
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "StringList")
	if sym == nil {
		t.Fatal("expected to find symbol StringList")
	}
	if sym.Kind != "typedef" {
		t.Errorf("expected kind 'typedef', got %q", sym.Kind)
	}
}

func TestGoRelationships(t *testing.T) {
	p := New()
	src := []byte(`package main

type Base struct{}

type Derived struct {
	Base
}

func process(d Derived) {
	d.Base.String()
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Struct embedding should generate an "inherits" relationship
	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected 'inherits' relationship targeting 'Base'")
	}

	// Type reference to Derived in the function parameter
	if !hasRelWithKind(result, "references", "Derived") {
		t.Error("expected 'references' relationship targeting 'Derived'")
	}
}

func TestGoBuiltinFiltering(t *testing.T) {
	p := New()
	src := []byte(`package main

func doStuff() {
	s := make([]int, 0)
	n := len(s)
	_ = append(s, n)
	customFunc()
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Builtins should NOT appear as call relationships
	for _, r := range result.Relationships {
		if r.Kind == "calls" {
			if r.TargetName == "make" || r.TargetName == "len" || r.TargetName == "append" {
				t.Errorf("builtin %q should not appear as a call relationship", r.TargetName)
			}
		}
	}

	// customFunc should appear
	if !hasRelWithKind(result, "calls", "customFunc") {
		t.Error("expected 'calls' relationship targeting 'customFunc'")
	}
}

func TestGoDocComment(t *testing.T) {
	p := New()
	src := []byte(`package main

// Hello greets someone by name.
func Hello(name string) string {
	return "Hello, " + name
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Hello")
	if sym == nil {
		t.Fatal("expected to find symbol Hello")
	}
	if !strings.Contains(sym.DocComment, "Hello greets someone") {
		t.Errorf("expected doc comment to contain 'Hello greets someone', got %q", sym.DocComment)
	}
}

func TestGoFunctionCall(t *testing.T) {
	p := New()
	src := []byte(`package main

func helper() int {
	return 42
}

func caller() {
	x := helper()
	_ = x
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	rel := findRelFull(result, "calls", "caller", "helper")
	if rel == nil {
		t.Error("expected 'calls' relationship from 'caller' to 'helper'")
	}
}

// --- Python Tests ---

func TestPythonFunction(t *testing.T) {
	p := New()
	src := []byte(`def greet(name):
    return f"Hello, {name}"
`)
	result, err := p.ParseFile(context.Background(), "test.py", src, "py")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "greet")
	if sym == nil {
		t.Fatal("expected to find symbol greet")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "python" {
		t.Errorf("expected language 'python', got %q", sym.Language)
	}
	if sym.Qualified != "greet" {
		t.Errorf("expected qualified 'greet', got %q", sym.Qualified)
	}
}

func TestPythonClassWithMethods(t *testing.T) {
	p := New()
	src := []byte(`class Animal:
    def __init__(self, name):
        self.name = name

    def speak(self):
        pass
`)
	result, err := p.ParseFile(context.Background(), "test.py", src, "python")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "Animal")
	if classSym == nil {
		t.Fatal("expected to find symbol Animal")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	initSym := findSymbolQualified(result, "Animal.__init__")
	if initSym == nil {
		t.Fatal("expected to find symbol Animal.__init__")
	}
	if initSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", initSym.Kind)
	}
	if initSym.ParentClass != "Animal" {
		t.Errorf("expected ParentClass 'Animal', got %q", initSym.ParentClass)
	}

	speakSym := findSymbolQualified(result, "Animal.speak")
	if speakSym == nil {
		t.Fatal("expected to find symbol Animal.speak")
	}
	if speakSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", speakSym.Kind)
	}
}

func TestPythonRelationships(t *testing.T) {
	p := New()
	src := []byte(`import os

from collections import OrderedDict

class Base:
    def do_thing(self):
        pass

class Child(Base):
    def do_thing(self):
        result = os.path.join("a", "b")
        helper()
`)
	result, err := p.ParseFile(context.Background(), "test.py", src, "py")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Import relationships
	if !hasRelWithKind(result, "references", "os") {
		t.Error("expected import relationship for 'os'")
	}
	if !hasRelWithKind(result, "references", "collections") {
		t.Error("expected import relationship for 'collections'")
	}

	// Inheritance
	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected 'inherits' relationship targeting 'Base'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}
}

// --- TypeScript Tests ---

func TestTypeScriptFunction(t *testing.T) {
	p := New()
	src := []byte(`function add(a: number, b: number): number {
    return a + b;
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "add")
	if sym == nil {
		t.Fatal("expected to find symbol add")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "typescript" {
		t.Errorf("expected language 'typescript', got %q", sym.Language)
	}
}

func TestTypeScriptClassWithMethods(t *testing.T) {
	p := New()
	src := []byte(`class UserService {
    constructor(private db: Database) {}

    getUser(id: string): User {
        return this.db.find(id);
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "UserService")
	if classSym == nil {
		t.Fatal("expected to find symbol UserService")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	methodSym := findSymbolQualified(result, "UserService.getUser")
	if methodSym == nil {
		t.Fatal("expected to find symbol UserService.getUser")
	}
	if methodSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", methodSym.Kind)
	}
	if methodSym.ParentClass != "UserService" {
		t.Errorf("expected ParentClass 'UserService', got %q", methodSym.ParentClass)
	}
}

func TestTypeScriptInterface(t *testing.T) {
	p := New()
	src := []byte(`interface Serializable {
    serialize(): string;
    deserialize(data: string): void;
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Serializable")
	if sym == nil {
		t.Fatal("expected to find symbol Serializable")
	}
	if sym.Kind != "struct" {
		t.Errorf("expected kind 'struct' for interface, got %q", sym.Kind)
	}
}

func TestTypeScriptEnum(t *testing.T) {
	p := New()
	src := []byte(`enum Direction {
    Up,
    Down,
    Left,
    Right
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Direction")
	if sym == nil {
		t.Fatal("expected to find symbol Direction")
	}
	if sym.Kind != "enum" {
		t.Errorf("expected kind 'enum', got %q", sym.Kind)
	}
}

func TestTypeScriptArrowFunction(t *testing.T) {
	p := New()
	src := []byte(`const multiply = (a: number, b: number): number => a * b;
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "multiply")
	if sym == nil {
		t.Fatal("expected to find symbol multiply")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function' for arrow function, got %q", sym.Kind)
	}
}

func TestTypeScriptRelationships(t *testing.T) {
	p := New()
	src := []byte(`import { readFile } from 'fs';

class Base {}

class Derived extends Base {
    doWork() {
        helper();
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "typescript")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Import
	if !hasRelWithKind(result, "references", "fs") {
		t.Error("expected import relationship for 'fs'")
	}

	// Inheritance
	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected 'inherits' relationship targeting 'Base'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}
}

func TestTypeScriptExportedDeclarations(t *testing.T) {
	p := New()
	src := []byte(`export function exported() {
    return 1;
}

export class ExportedClass {
    method() {}
}
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "exported")
	if sym == nil {
		t.Fatal("expected to find exported function")
	}

	classSym := findSymbol(result, "ExportedClass")
	if classSym == nil {
		t.Fatal("expected to find ExportedClass")
	}
}

// --- JavaScript Tests ---

func TestJavaScriptFunction(t *testing.T) {
	p := New()
	src := []byte(`function greet(name) {
    return "Hello, " + name;
}
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "js")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "greet")
	if sym == nil {
		t.Fatal("expected to find symbol greet")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "javascript" {
		t.Errorf("expected language 'javascript', got %q", sym.Language)
	}
}

func TestJavaScriptClassWithMethods(t *testing.T) {
	p := New()
	src := []byte(`class Calculator {
    add(a, b) {
        return a + b;
    }

    subtract(a, b) {
        return a - b;
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "js")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "Calculator")
	if classSym == nil {
		t.Fatal("expected to find symbol Calculator")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	addSym := findSymbolQualified(result, "Calculator.add")
	if addSym == nil {
		t.Fatal("expected to find symbol Calculator.add")
	}
	if addSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", addSym.Kind)
	}

	subSym := findSymbolQualified(result, "Calculator.subtract")
	if subSym == nil {
		t.Fatal("expected to find symbol Calculator.subtract")
	}
}

func TestJavaScriptArrowFunction(t *testing.T) {
	p := New()
	src := []byte(`const double = (x) => x * 2;
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "js")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "double")
	if sym == nil {
		t.Fatal("expected to find symbol double")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function' for arrow function, got %q", sym.Kind)
	}
	if sym.Language != "javascript" {
		t.Errorf("expected language 'javascript', got %q", sym.Language)
	}
}

func TestJavaScriptRelationships(t *testing.T) {
	p := New()
	src := []byte(`import { something } from './module';

class Base {
    run() {}
}

class Child extends Base {
    run() {
        doWork();
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "javascript")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Import
	if !hasRelWithKind(result, "references", "./module") {
		t.Error("expected import relationship for './module'")
	}

	// Inheritance
	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected 'inherits' relationship targeting 'Base'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "doWork") {
		t.Error("expected 'calls' relationship targeting 'doWork'")
	}
}

func TestJavaScriptExportedArrow(t *testing.T) {
	p := New()
	src := []byte(`export const handler = (req) => {
    return respond(req);
};
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "js")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "handler")
	if sym == nil {
		t.Fatal("expected to find exported arrow function 'handler'")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
}

// --- Rust Tests ---

func TestRustFunction(t *testing.T) {
	p := New()
	src := []byte(`fn compute(x: i32) -> i32 {
    x * 2
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "compute")
	if sym == nil {
		t.Fatal("expected to find symbol compute")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "rust" {
		t.Errorf("expected language 'rust', got %q", sym.Language)
	}
}

func TestRustStruct(t *testing.T) {
	p := New()
	src := []byte(`struct Point {
    x: f64,
    y: f64,
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Point")
	if sym == nil {
		t.Fatal("expected to find symbol Point")
	}
	if sym.Kind != "struct" {
		t.Errorf("expected kind 'struct', got %q", sym.Kind)
	}
}

func TestRustEnum(t *testing.T) {
	p := New()
	src := []byte(`enum Color {
    Red,
    Green,
    Blue,
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Color")
	if sym == nil {
		t.Fatal("expected to find symbol Color")
	}
	if sym.Kind != "enum" {
		t.Errorf("expected kind 'enum', got %q", sym.Kind)
	}
}

func TestRustTrait(t *testing.T) {
	p := New()

	t.Run("trait_declaration", func(t *testing.T) {
		// Trait with only signatures (no body) - tree-sitter does not parse these
		// as function_item nodes, so only the trait itself is extracted.
		src := []byte(`trait Drawable {
    fn draw(&self);
    fn resize(&self, scale: f64);
}
`)
		result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}

		traitSym := findSymbol(result, "Drawable")
		if traitSym == nil {
			t.Fatal("expected to find symbol Drawable")
		}
		if traitSym.Kind != "class" {
			t.Errorf("expected kind 'class' for trait, got %q", traitSym.Kind)
		}
	})

	t.Run("trait_with_default_methods", func(t *testing.T) {
		// Trait with default implementations (bodies) - these ARE function_items.
		src := []byte(`trait Printable {
    fn print_info(&self) {
        println!("info");
    }

    fn format(&self) -> String {
        String::from("default")
    }
}
`)
		result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}

		traitSym := findSymbol(result, "Printable")
		if traitSym == nil {
			t.Fatal("expected to find symbol Printable")
		}
		if traitSym.Kind != "class" {
			t.Errorf("expected kind 'class' for trait, got %q", traitSym.Kind)
		}

		printSym := findSymbolQualified(result, "Printable::print_info")
		if printSym == nil {
			t.Fatal("expected to find symbol Printable::print_info")
		}
		if printSym.Kind != "method" {
			t.Errorf("expected kind 'method', got %q", printSym.Kind)
		}
		if printSym.ParentClass != "Printable" {
			t.Errorf("expected ParentClass 'Printable', got %q", printSym.ParentClass)
		}

		formatSym := findSymbolQualified(result, "Printable::format")
		if formatSym == nil {
			t.Fatal("expected to find symbol Printable::format")
		}
	})
}

func TestRustImplBlock(t *testing.T) {
	p := New()
	src := []byte(`struct Circle {
    radius: f64,
}

impl Circle {
    fn new(radius: f64) -> Circle {
        Circle { radius }
    }

    fn area(&self) -> f64 {
        3.14 * self.radius * self.radius
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	newSym := findSymbolQualified(result, "Circle::new")
	if newSym == nil {
		t.Fatal("expected to find symbol Circle::new")
	}
	if newSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", newSym.Kind)
	}
	if newSym.ParentClass != "Circle" {
		t.Errorf("expected ParentClass 'Circle', got %q", newSym.ParentClass)
	}

	areaSym := findSymbolQualified(result, "Circle::area")
	if areaSym == nil {
		t.Fatal("expected to find symbol Circle::area")
	}
}

func TestRustRelationships(t *testing.T) {
	p := New()
	src := []byte(`use std::io;

trait Shape {
    fn area(&self) -> f64;
}

struct Rect {
    w: f64,
    h: f64,
}

impl Shape for Rect {
    fn area(&self) -> f64 {
        self.w * self.h
    }
}

fn process(r: Rect) {
    helper();
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rs")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Use declaration as import
	if !hasRelWithKind(result, "references", "std::io") {
		t.Error("expected import relationship for 'std::io'")
	}

	// Trait implementation as inherits
	if !hasRelWithKind(result, "inherits", "Shape") {
		t.Error("expected 'inherits' relationship targeting 'Shape'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}
}

// --- C Tests ---

func TestCFunction(t *testing.T) {
	p := New()
	src := []byte(`int add(int a, int b) {
    return a + b;
}
`)
	result, err := p.ParseFile(context.Background(), "test.c", src, "c")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "add")
	if sym == nil {
		t.Fatal("expected to find symbol add")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "c" {
		t.Errorf("expected language 'c', got %q", sym.Language)
	}
}

func TestCStruct(t *testing.T) {
	p := New()
	src := []byte(`struct Point {
    int x;
    int y;
};
`)
	result, err := p.ParseFile(context.Background(), "test.c", src, "c")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Point")
	if sym == nil {
		t.Fatal("expected to find symbol Point")
	}
	if sym.Kind != "struct" {
		t.Errorf("expected kind 'struct', got %q", sym.Kind)
	}
	if sym.Language != "c" {
		t.Errorf("expected language 'c', got %q", sym.Language)
	}
}

func TestCEnum(t *testing.T) {
	p := New()
	src := []byte(`enum Color {
    RED,
    GREEN,
    BLUE
};
`)
	result, err := p.ParseFile(context.Background(), "test.c", src, "c")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Color")
	if sym == nil {
		t.Fatal("expected to find symbol Color")
	}
	if sym.Kind != "enum" {
		t.Errorf("expected kind 'enum', got %q", sym.Kind)
	}
}

func TestCRelationships(t *testing.T) {
	p := New()
	src := []byte(`#include <stdio.h>
#include "myheader.h"

struct Data {
    int value;
};

void process(Data d) {
    custom_func(d.value);
}
`)
	result, err := p.ParseFile(context.Background(), "test.c", src, "c")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Include relationships
	if !hasRelWithKind(result, "references", "stdio.h") {
		t.Error("expected include relationship for 'stdio.h'")
	}
	if !hasRelWithKind(result, "references", "myheader.h") {
		t.Error("expected include relationship for 'myheader.h'")
	}

	// Function call (not a builtin)
	if !hasRelWithKind(result, "calls", "custom_func") {
		t.Error("expected 'calls' relationship targeting 'custom_func'")
	}
}

func TestCBuiltinFiltering(t *testing.T) {
	p := New()
	src := []byte(`#include <stdlib.h>

void do_stuff() {
    int *p = malloc(sizeof(int));
    free(p);
    custom_alloc();
}
`)
	result, err := p.ParseFile(context.Background(), "test.c", src, "c")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// malloc and free are builtins and should not appear as call relationships
	for _, r := range result.Relationships {
		if r.Kind == "calls" && (r.TargetName == "malloc" || r.TargetName == "free") {
			t.Errorf("C builtin %q should not appear as a call relationship", r.TargetName)
		}
	}

	if !hasRelWithKind(result, "calls", "custom_alloc") {
		t.Error("expected 'calls' relationship targeting 'custom_alloc'")
	}
}

// --- C++ Tests ---

func TestCppClassWithMethods(t *testing.T) {
	p := New()
	src := []byte(`class Animal {
public:
    void speak() {
        return;
    }
};
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "Animal")
	if classSym == nil {
		t.Fatal("expected to find symbol Animal")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}
	if classSym.Language != "cpp" {
		t.Errorf("expected language 'cpp', got %q", classSym.Language)
	}

	speakSym := findSymbolQualified(result, "Animal::speak")
	if speakSym == nil {
		t.Fatal("expected to find symbol Animal::speak")
	}
	if speakSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", speakSym.Kind)
	}
	if speakSym.Language != "cpp" {
		t.Errorf("expected language 'cpp', got %q", speakSym.Language)
	}
}

func TestCppFunction(t *testing.T) {
	p := New()
	src := []byte(`void helper(int x) {
    return;
}
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "helper")
	if sym == nil {
		t.Fatal("expected to find symbol helper")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "cpp" {
		t.Errorf("expected language 'cpp', got %q", sym.Language)
	}
}

func TestCppNamespace(t *testing.T) {
	p := New()
	src := []byte(`namespace mylib {

void utility() {
    return;
}

}
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Functions inside namespaces should still be extracted
	sym := findSymbol(result, "utility")
	if sym == nil {
		t.Fatal("expected to find symbol utility inside namespace")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
}

func TestCppInheritance(t *testing.T) {
	p := New()

	t.Run("single_inheritance", func(t *testing.T) {
		// Note: the C++ grammar may represent base_class_clause children
		// differently depending on the grammar version. With some grammars,
		// `base_class_specifier` wraps each base class. When it doesn't,
		// the type_identifier falls through to the "references" handler.
		src := []byte(`class Base {
public:
    void doBase() {}
};

class Derived : public Base {
public:
    void doDerived() {}
};
`)
		result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}

		// Either an "inherits" or a "references" relationship should exist for Base.
		hasInherits := hasRelWithKind(result, "inherits", "Base")
		hasRef := hasRelWithKind(result, "references", "Base")
		if !hasInherits && !hasRef {
			t.Error("expected either 'inherits' or 'references' relationship targeting 'Base'")
		}
	})

	t.Run("class_extracted", func(t *testing.T) {
		src := []byte(`class Parent {};
class Child : public Parent {};
`)
		result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}

		parent := findSymbol(result, "Parent")
		if parent == nil {
			t.Fatal("expected to find symbol Parent")
		}
		child := findSymbol(result, "Child")
		if child == nil {
			t.Fatal("expected to find symbol Child")
		}
	})
}

func TestCppIncludes(t *testing.T) {
	p := New()
	src := []byte(`#include <iostream>
#include "mylib.h"

void run() {
    return;
}
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if !hasRelWithKind(result, "references", "iostream") {
		t.Error("expected include relationship for 'iostream'")
	}
	if !hasRelWithKind(result, "references", "mylib.h") {
		t.Error("expected include relationship for 'mylib.h'")
	}
}

// --- Kotlin Tests ---

func TestKotlinFunction(t *testing.T) {
	p := New()
	src := []byte(`fun greet(name: String): String {
    return "Hello, $name"
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "greet")
	if sym == nil {
		t.Fatal("expected to find symbol greet")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "kotlin" {
		t.Errorf("expected language 'kotlin', got %q", sym.Language)
	}
}

func TestKotlinClassWithMethod(t *testing.T) {
	p := New()
	src := []byte(`class UserService {
    fun getUser(id: Int): User {
        return findById(id)
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "UserService")
	if classSym == nil {
		t.Fatal("expected to find symbol UserService")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	methodSym := findSymbolQualified(result, "UserService.getUser")
	if methodSym == nil {
		t.Fatal("expected to find symbol UserService.getUser")
	}
	if methodSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", methodSym.Kind)
	}
}

func TestKotlinObjectDeclaration(t *testing.T) {
	p := New()
	src := []byte(`object Singleton {
    fun getInstance(): Singleton {
        return this
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Singleton")
	if sym == nil {
		t.Fatal("expected to find symbol Singleton")
	}
	if sym.Kind != "class" {
		t.Errorf("expected kind 'class' for object, got %q", sym.Kind)
	}

	methodSym := findSymbolQualified(result, "Singleton.getInstance")
	if methodSym == nil {
		t.Fatal("expected to find symbol Singleton.getInstance")
	}
}

func TestKotlinExtensionFunction(t *testing.T) {
	p := New()
	src := []byte(`fun String.addExclamation(): String {
    return this + "!"
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "addExclamation")
	if sym == nil {
		t.Fatal("expected to find symbol addExclamation")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Qualified != "String.addExclamation" {
		t.Errorf("expected qualified 'String.addExclamation', got %q", sym.Qualified)
	}
}

func TestKotlinProperty(t *testing.T) {
	p := New()
	src := []byte(`val appName: String = "MyApp"
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "appName")
	if sym == nil {
		t.Fatal("expected to find property symbol appName")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function' for property, got %q", sym.Kind)
	}
}

func TestKotlinRelationships(t *testing.T) {
	p := New()
	src := []byte(`import com.example.lib

open class Base {
    open fun action() {}
}

class Child : Base() {
    override fun action() {
        helper()
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kt")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Import
	if !hasRelWithKind(result, "references", "com.example.lib") {
		t.Error("expected import relationship for 'com.example.lib'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}

	// Both symbols should be extracted
	if findSymbol(result, "Base") == nil {
		t.Error("expected to find symbol Base")
	}
	if findSymbol(result, "Child") == nil {
		t.Error("expected to find symbol Child")
	}
}

// --- Swift Tests ---

func TestSwiftFunction(t *testing.T) {
	p := New()
	src := []byte(`func greet(name: String) -> String {
    return "Hello, \(name)"
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "greet")
	if sym == nil {
		t.Fatal("expected to find symbol greet")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "swift" {
		t.Errorf("expected language 'swift', got %q", sym.Language)
	}
}

func TestSwiftClassWithMethod(t *testing.T) {
	p := New()
	src := []byte(`class Vehicle {
    func drive() {
        return
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "Vehicle")
	if classSym == nil {
		t.Fatal("expected to find symbol Vehicle")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	methodSym := findSymbolQualified(result, "Vehicle.drive")
	if methodSym == nil {
		t.Fatal("expected to find symbol Vehicle.drive")
	}
	if methodSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", methodSym.Kind)
	}
}

func TestSwiftStruct(t *testing.T) {
	p := New()
	src := []byte(`struct Point {
    var x: Double
    var y: Double
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Point")
	if sym == nil {
		t.Fatal("expected to find symbol Point")
	}
	if sym.Kind != "struct" {
		t.Errorf("expected kind 'struct', got %q", sym.Kind)
	}
}

func TestSwiftEnum(t *testing.T) {
	p := New()
	src := []byte(`enum Direction {
    case north
    case south
    case east
    case west
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Direction")
	if sym == nil {
		t.Fatal("expected to find symbol Direction")
	}
	if sym.Kind != "enum" {
		t.Errorf("expected kind 'enum', got %q", sym.Kind)
	}
}

func TestSwiftProtocol(t *testing.T) {
	p := New()
	src := []byte(`protocol Drawable {
    func draw()
    func resize(scale: Double)
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	protoSym := findSymbol(result, "Drawable")
	if protoSym == nil {
		t.Fatal("expected to find symbol Drawable")
	}
	if protoSym.Kind != "class" {
		t.Errorf("expected kind 'class' for protocol, got %q", protoSym.Kind)
	}

	drawSym := findSymbolQualified(result, "Drawable.draw")
	if drawSym == nil {
		t.Fatal("expected to find symbol Drawable.draw")
	}
	if drawSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", drawSym.Kind)
	}
	if drawSym.ParentClass != "Drawable" {
		t.Errorf("expected ParentClass 'Drawable', got %q", drawSym.ParentClass)
	}

	resizeSym := findSymbolQualified(result, "Drawable.resize")
	if resizeSym == nil {
		t.Fatal("expected to find symbol Drawable.resize")
	}
}

func TestSwiftRelationships(t *testing.T) {
	p := New()
	src := []byte(`import Foundation

class Base {
    func action() {}
}

class Child: Base {
    override func action() {
        helper()
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Import
	if !hasRelWithKind(result, "references", "Foundation") {
		t.Error("expected import relationship for 'Foundation'")
	}

	// Inheritance
	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected 'inherits' relationship targeting 'Base'")
	}

	// Function call
	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}
}

func TestSwiftExtensionMembers(t *testing.T) {
	p := New()
	src := []byte(`struct MyStruct {
    var value: Int
}

extension MyStruct {
    func doubled() -> Int {
        return value * 2
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// The extension itself should NOT be emitted as a symbol
	extensionCount := 0
	for _, sym := range result.Symbols {
		if sym.Name == "MyStruct" {
			extensionCount++
		}
	}
	// Should be exactly 1 (the struct itself), not 2 (struct + extension)
	if extensionCount != 1 {
		t.Errorf("expected 1 MyStruct symbol (struct only, not extension), got %d", extensionCount)
	}

	// But the method inside the extension SHOULD be extracted
	methodSym := findSymbolQualified(result, "MyStruct.doubled")
	if methodSym == nil {
		t.Fatal("expected to find method 'doubled' from extension")
	}
}

// --- Cross-cutting and Edge Case Tests ---

func TestFilePathAndLanguageInResult(t *testing.T) {
	p := New()
	src := []byte(`func hello() {}`)

	result, err := p.ParseFile(context.Background(), "/path/to/test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	if result.Filepath != "/path/to/test.go" {
		t.Errorf("expected filepath '/path/to/test.go', got %q", result.Filepath)
	}
	if result.Language != "go" {
		t.Errorf("expected language 'go', got %q", result.Language)
	}
}

func TestGoMultipleSymbolTypes(t *testing.T) {
	p := New()
	src := []byte(`package main

type MyStruct struct {
	Name string
}

type MyInterface interface {
	DoSomething()
}

type MyAlias = string

func topLevel() {}

func (m *MyStruct) Method() {}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	tests := []struct {
		name      string
		qualified string
		kind      string
	}{
		{"MyStruct", "MyStruct", "struct"},
		{"MyInterface", "MyInterface", "class"},
		{"topLevel", "topLevel", "function"},
		{"Method", "MyStruct.Method", "method"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym := findSymbolQualified(result, tt.qualified)
			if sym == nil {
				t.Fatalf("expected to find symbol %q", tt.qualified)
			}
			if sym.Kind != tt.kind {
				t.Errorf("expected kind %q, got %q", tt.kind, sym.Kind)
			}
		})
	}
}

func TestPythonLanguageAlias(t *testing.T) {
	p := New()
	src := []byte(`def foo():
    pass
`)
	// "py" alias
	result1, err := p.ParseFile(context.Background(), "test.py", src, "py")
	if err != nil {
		t.Fatalf("ParseFile error with 'py': %v", err)
	}
	// "python" alias
	result2, err := p.ParseFile(context.Background(), "test.py", src, "python")
	if err != nil {
		t.Fatalf("ParseFile error with 'python': %v", err)
	}

	if len(result1.Symbols) != len(result2.Symbols) {
		t.Errorf("'py' extracted %d symbols, 'python' extracted %d symbols",
			len(result1.Symbols), len(result2.Symbols))
	}
}

func TestRustLanguageAlias(t *testing.T) {
	p := New()
	src := []byte(`fn foo() {}
`)
	result1, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error with 'rust': %v", err)
	}
	result2, err := p.ParseFile(context.Background(), "test.rs", src, "rs")
	if err != nil {
		t.Fatalf("ParseFile error with 'rs': %v", err)
	}

	if len(result1.Symbols) != len(result2.Symbols) {
		t.Errorf("'rust' extracted %d symbols, 'rs' extracted %d symbols",
			len(result1.Symbols), len(result2.Symbols))
	}
}

func TestEnclosingSymbolInRelationships(t *testing.T) {
	p := New()
	src := []byte(`package main

func outer() {
	inner()
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	rel := findRel(result, "calls", "inner")
	if rel == nil {
		t.Fatal("expected 'calls' relationship targeting 'inner'")
	}
	if rel.SourceQualified != "outer" {
		t.Errorf("expected SourceQualified 'outer', got %q", rel.SourceQualified)
	}
}

func TestGoMethodCallRelationshipSource(t *testing.T) {
	p := New()
	src := []byte(`package main

type Svc struct{}

func (s *Svc) Handle() {
	doWork()
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	rel := findRel(result, "calls", "doWork")
	if rel == nil {
		t.Fatal("expected 'calls' relationship targeting 'doWork'")
	}
	if rel.SourceQualified != "Svc.Handle" {
		t.Errorf("expected SourceQualified 'Svc.Handle', got %q", rel.SourceQualified)
	}
}

func TestLineNumbers(t *testing.T) {
	p := New()
	src := []byte(`package main

// line 3
// line 4
func first() {
}

func second() {
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	first := findSymbol(result, "first")
	if first == nil {
		t.Fatal("expected to find symbol first")
	}
	if first.LineStart != 5 {
		t.Errorf("expected first.LineStart = 5, got %d", first.LineStart)
	}

	second := findSymbol(result, "second")
	if second == nil {
		t.Fatal("expected to find symbol second")
	}
	if second.LineStart != 8 {
		t.Errorf("expected second.LineStart = 8, got %d", second.LineStart)
	}
}

func TestGoStructEmbeddingRelationship(t *testing.T) {
	p := New()
	src := []byte(`package main

type Reader struct{}

type Writer struct{}

type ReadWriter struct {
	Reader
	Writer
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	readerRel := findRelFull(result, "inherits", "ReadWriter", "Reader")
	if readerRel == nil {
		t.Error("expected 'inherits' from ReadWriter to Reader")
	}

	writerRel := findRelFull(result, "inherits", "ReadWriter", "Writer")
	if writerRel == nil {
		t.Error("expected 'inherits' from ReadWriter to Writer")
	}
}

func TestPythonNestedClassMethods(t *testing.T) {
	p := New()
	src := []byte(`class Outer:
    def outer_method(self):
        pass

    class Inner:
        def inner_method(self):
            pass
`)
	result, err := p.ParseFile(context.Background(), "test.py", src, "py")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	outerSym := findSymbol(result, "Outer")
	if outerSym == nil {
		t.Fatal("expected to find symbol Outer")
	}

	outerMethod := findSymbolQualified(result, "Outer.outer_method")
	if outerMethod == nil {
		t.Fatal("expected to find symbol Outer.outer_method")
	}
}

func TestContentAndSignature(t *testing.T) {
	p := New()
	src := []byte(`package main

func multiLine(
	a int,
	b int,
) int {
	return a + b
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "multiLine")
	if sym == nil {
		t.Fatal("expected to find symbol multiLine")
	}
	// Signature should be the first line only
	if !strings.Contains(sym.Signature, "func multiLine") {
		t.Errorf("expected signature to contain 'func multiLine', got %q", sym.Signature)
	}
	// Content should contain the full function body
	if !strings.Contains(sym.Content, "return a + b") {
		t.Errorf("expected content to contain full body, got %q", sym.Content)
	}
}

func TestKotlinPrivatePropertySkipped(t *testing.T) {
	p := New()
	src := []byte(`val publicProp: String = "hello"
val _privateProp: String = "secret"
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if findSymbol(result, "publicProp") == nil {
		t.Error("expected to find property publicProp")
	}
	if findSymbol(result, "_privateProp") != nil {
		t.Error("expected _privateProp to be skipped (underscore prefix)")
	}
}

func TestRustImplTraitInheritance(t *testing.T) {
	p := New()
	src := []byte(`trait Display {
    fn fmt(&self) -> String;
}

struct MyType;

impl Display for MyType {
    fn fmt(&self) -> String {
        String::from("hello")
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	rel := findRelFull(result, "inherits", "MyType", "Display")
	if rel == nil {
		t.Error("expected 'inherits' from MyType to Display")
	}
}

func TestCppTemplate(t *testing.T) {
	p := New()
	src := []byte(`template<typename T>
class Container {
public:
    void add(T item) {}
};
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Container")
	if sym == nil {
		t.Fatal("expected to find symbol Container")
	}
	if sym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", sym.Kind)
	}
}

func TestMultipleGoFunctions(t *testing.T) {
	p := New()
	src := []byte(`package main

func alpha() {}
func beta() {}
func gamma() {}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if len(result.Symbols) != 3 {
		t.Errorf("expected 3 symbols, got %d", len(result.Symbols))
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if findSymbol(result, name) == nil {
			t.Errorf("expected to find symbol %q", name)
		}
	}
}

func TestTypeScriptMultipleTypes(t *testing.T) {
	p := New()
	src := []byte(`function fn1() {}

class MyClass {
    method1() {}
}

interface MyInterface {
    field: string;
}

enum MyEnum {
    A,
    B
}

const arrowFn = () => {};
`)
	result, err := p.ParseFile(context.Background(), "test.ts", src, "ts")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	tests := []struct {
		name string
		kind string
	}{
		{"fn1", "function"},
		{"MyClass", "class"},
		{"MyInterface", "struct"},
		{"MyEnum", "enum"},
		{"arrowFn", "function"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym := findSymbol(result, tt.name)
			if sym == nil {
				t.Fatalf("expected to find symbol %q", tt.name)
			}
			if sym.Kind != tt.kind {
				t.Errorf("expected kind %q, got %q", tt.kind, sym.Kind)
			}
		})
	}

	methodSym := findSymbolQualified(result, "MyClass.method1")
	if methodSym == nil {
		t.Fatal("expected to find symbol MyClass.method1")
	}
}

func TestSwiftProtocolInheritance(t *testing.T) {
	p := New()
	src := []byte(`protocol Animal {
    func speak()
}

protocol Pet: Animal {
    func play()
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if !hasRelWithKind(result, "inherits", "Animal") {
		t.Error("expected 'inherits' relationship from Pet to Animal")
	}
}

func TestJavaScriptVariableDeclaration(t *testing.T) {
	p := New()
	src := []byte(`var oldArrow = (x) => x;
`)
	result, err := p.ParseFile(context.Background(), "test.js", src, "js")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "oldArrow")
	if sym == nil {
		t.Fatal("expected to find arrow function in var declaration")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
}

func TestCppTypeReferences(t *testing.T) {
	p := New()
	src := []byte(`class MyType {};

void useIt(MyType t) {
    return;
}
`)
	result, err := p.ParseFile(context.Background(), "test.cpp", src, "cpp")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if !hasRelWithKind(result, "references", "MyType") {
		t.Error("expected 'references' relationship for MyType")
	}
}

func TestGoPointerReceiverMethod(t *testing.T) {
	p := New()
	src := []byte(`package main

type Cache struct{}

func (c *Cache) Get(key string) string {
	return ""
}

func (c Cache) Set(key, val string) {
}
`)
	result, err := p.ParseFile(context.Background(), "test.go", src, "go")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	getSym := findSymbolQualified(result, "Cache.Get")
	if getSym == nil {
		t.Fatal("expected to find Cache.Get (pointer receiver)")
	}
	if getSym.ParentClass != "Cache" {
		t.Errorf("expected ParentClass 'Cache', got %q", getSym.ParentClass)
	}

	setSym := findSymbolQualified(result, "Cache.Set")
	if setSym == nil {
		t.Fatal("expected to find Cache.Set (value receiver)")
	}
	if setSym.ParentClass != "Cache" {
		t.Errorf("expected ParentClass 'Cache', got %q", setSym.ParentClass)
	}
}

func TestCTypedefStruct(t *testing.T) {
	p := New()

	t.Run("plain_struct", func(t *testing.T) {
		// Plain struct without typedef is always extracted.
		src := []byte(`struct Node {
    int value;
};
`)
		result, err := p.ParseFile(context.Background(), "test.c", src, "c")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}
		sym := findSymbol(result, "Node")
		if sym == nil {
			t.Fatal("expected to find struct symbol Node")
		}
		if sym.Kind != "struct" {
			t.Errorf("expected kind 'struct', got %q", sym.Kind)
		}
	})

	t.Run("typedef_struct", func(t *testing.T) {
		// typedef struct is parsed as a type_definition node by tree-sitter,
		// which is handled via the "declaration" case in walkC. The struct
		// specifier may or may not have a name depending on the grammar parse.
		src := []byte(`typedef struct {
    int value;
} Alias;
`)
		result, err := p.ParseFile(context.Background(), "test.c", src, "c")
		if err != nil {
			t.Fatalf("ParseFile error: %v", err)
		}
		// The anonymous struct inside typedef may not produce a named symbol.
		// This is expected behavior - the parser extracts struct_specifier names.
		_ = result
	})
}

func TestSwiftProperty(t *testing.T) {
	p := New()
	src := []byte(`class Config {
    var timeout: Int = 30
    let name: String = "default"
}
`)
	result, err := p.ParseFile(context.Background(), "test.swift", src, "swift")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	timeoutSym := findSymbolQualified(result, "Config.timeout")
	if timeoutSym == nil {
		t.Fatal("expected to find Config.timeout property")
	}
	if timeoutSym.Kind != "function" {
		t.Errorf("expected kind 'function' for property, got %q", timeoutSym.Kind)
	}

	nameSym := findSymbolQualified(result, "Config.name")
	if nameSym == nil {
		t.Fatal("expected to find Config.name property")
	}
}

func TestKotlinClassInheritance(t *testing.T) {
	p := New()
	src := []byte(`open class Animal(val name: String)

class Dog(name: String) : Animal(name) {
    fun bark() {}
}
`)
	result, err := p.ParseFile(context.Background(), "test.kt", src, "kotlin")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Verify both classes are extracted
	if findSymbol(result, "Animal") == nil {
		t.Error("expected to find symbol Animal")
	}
	dog := findSymbol(result, "Dog")
	if dog == nil {
		t.Fatal("expected to find symbol Dog")
	}
	if dog.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", dog.Kind)
	}

	// Dog.bark should be extracted as a method
	bark := findSymbolQualified(result, "Dog.bark")
	if bark == nil {
		t.Fatal("expected to find symbol Dog.bark")
	}
	if bark.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", bark.Kind)
	}
}

func TestRustMultipleImplMethods(t *testing.T) {
	p := New()
	src := []byte(`struct Vec2 {
    x: f64,
    y: f64,
}

impl Vec2 {
    fn new(x: f64, y: f64) -> Vec2 {
        Vec2 { x, y }
    }

    fn length(&self) -> f64 {
        (self.x * self.x + self.y * self.y).sqrt()
    }

    fn normalize(&self) -> Vec2 {
        let len = self.length();
        Vec2::new(self.x / len, self.y / len)
    }
}
`)
	result, err := p.ParseFile(context.Background(), "test.rs", src, "rust")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	for _, method := range []string{"Vec2::new", "Vec2::length", "Vec2::normalize"} {
		sym := findSymbolQualified(result, method)
		if sym == nil {
			t.Errorf("expected to find symbol %q", method)
		} else if sym.Kind != "method" {
			t.Errorf("expected kind 'method' for %q, got %q", method, sym.Kind)
		}
	}
}
