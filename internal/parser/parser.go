package parser

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// Symbol represents a parsed code symbol extracted from the AST.
type Symbol struct {
	Name        string
	Qualified   string
	Kind        string // function, method, class, struct, enum
	Content     string
	Signature   string
	DocComment  string
	LineStart   int
	LineEnd     int
	ParentClass string
	Language    string
}

// Relationship represents a detected reference between symbols.
type Relationship struct {
	SourceQualified string // qualified name of the referencing symbol
	TargetName      string // name of the referenced symbol (may be short or qualified)
	Kind            string // calls, inherits, references
	Line            int    // line where the reference occurs
}

// ParseResult holds all symbols and relationships extracted from a single file.
type ParseResult struct {
	Filepath      string
	Language      string
	Symbols       []Symbol
	Relationships []Relationship
}

// Parser uses tree-sitter to parse source files and extract symbols.
type Parser struct {
	languages map[string]*sitter.Language
}

// New creates a parser with grammars for Go, Python, TypeScript, JavaScript, Rust, C, C++, Kotlin, and Swift.
func New() *Parser {
	return &Parser{
		languages: map[string]*sitter.Language{
			"go":         golang.GetLanguage(),
			"py":         python.GetLanguage(),
			"python":     python.GetLanguage(),
			"ts":         typescript.GetLanguage(),
			"typescript": typescript.GetLanguage(),
			"js":         javascript.GetLanguage(),
			"javascript": javascript.GetLanguage(),
			"jsx":        javascript.GetLanguage(),
			"tsx":        typescript.GetLanguage(),
			"rust":       rust.GetLanguage(),
			"rs":         rust.GetLanguage(),
			"c":          c.GetLanguage(),
			"cpp":        cpp.GetLanguage(),
			"cc":         cpp.GetLanguage(),
			"h":          c.GetLanguage(),
			"hpp":        cpp.GetLanguage(),
			"kotlin":     kotlin.GetLanguage(),
			"kt":         kotlin.GetLanguage(),
			"kts":        kotlin.GetLanguage(),
			"swift":      swift.GetLanguage(),
		},
	}
}

// SupportsLanguage reports whether the parser supports a given language.
func (p *Parser) SupportsLanguage(lang string) bool {
	_, ok := p.languages[lang]
	return ok
}

// ParseFile parses source code and extracts symbols.
func (p *Parser) ParseFile(ctx context.Context, filepath string, source []byte, lang string) (*ParseResult, error) {
	language, ok := p.languages[lang]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}

	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(language)

	tree, err := parser.ParseCtx(ctx, nil, source)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath, err)
	}
	defer tree.Close()

	result := &ParseResult{
		Filepath: filepath,
		Language: lang,
	}

	root := tree.RootNode()
	switch lang {
	case "go":
		p.extractGo(root, source, result)
	case "py", "python":
		p.extractPython(root, source, result)
	case "ts", "typescript", "tsx":
		p.extractTypeScript(root, source, result)
	case "js", "javascript", "jsx":
		p.extractJavaScript(root, source, result)
	case "rust", "rs":
		p.extractRust(root, source, result)
	case "c", "h":
		p.extractC(root, source, result)
	case "cpp", "cc", "hpp":
		p.extractCpp(root, source, result)
	case "kotlin", "kt", "kts":
		p.extractKotlin(root, source, result)
	case "swift":
		p.extractSwift(root, source, result)
	}

	return result, nil
}

// extractGo extracts symbols and relationships from a Go AST.
func (p *Parser) extractGo(root *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.goFunction(child, source)
			sym.Language = "go"
			result.Symbols = append(result.Symbols, sym)

		case "method_declaration":
			sym := p.goMethod(child, source)
			sym.Language = "go"
			result.Symbols = append(result.Symbols, sym)

		case "type_declaration":
			specs := p.goTypeDecl(child, source)
			for _, sym := range specs {
				sym.Language = "go"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}

	// Phase 2: extract relationships (calls, embedding, type references)
	p.extractGoRelationships(root, source, result)
}

func (p *Parser) goFunction(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	sig := extractFirstLine(node.Content(source))

	return Symbol{
		Name:      name,
		Qualified: name,
		Kind:      "function",
		Content:   node.Content(source),
		Signature: sig,
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) goMethod(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	// Extract receiver type
	receiver := ""
	recvNode := node.ChildByFieldName("receiver")
	if recvNode != nil {
		// Walk receiver to find type name
		for j := 0; j < int(recvNode.ChildCount()); j++ {
			param := recvNode.Child(j)
			if param.Type() == "parameter_declaration" {
				typeNode := param.ChildByFieldName("type")
				if typeNode != nil {
					receiver = typeNode.Content(source)
					receiver = strings.TrimPrefix(receiver, "*")
				}
			}
		}
	}

	qualified := name
	if receiver != "" {
		qualified = receiver + "." + name
	}

	sig := extractFirstLine(node.Content(source))

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        "method",
		Content:     node.Content(source),
		Signature:   sig,
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: receiver,
		DocComment:  findPrecedingComment(node, source),
	}
}

func (p *Parser) goTypeDecl(node *sitter.Node, source []byte) []Symbol {
	var syms []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		spec := node.Child(i)
		if spec.Type() != "type_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(source)
		typeNode := spec.ChildByFieldName("type")
		kind := "struct"
		if typeNode != nil {
			switch typeNode.Type() {
			case "struct_type":
				kind = "struct"
			case "interface_type":
				kind = "class" // interfaces map to class kind
			default:
				kind = "typedef"
			}
		}

		syms = append(syms, Symbol{
			Name:       name,
			Qualified:  name,
			Kind:       kind,
			Content:    node.Content(source),
			LineStart:  int(node.StartPoint().Row) + 1,
			LineEnd:    int(node.EndPoint().Row) + 1,
			DocComment: findPrecedingComment(node, source),
		})
	}
	return syms
}

// extractPython extracts symbols from a Python AST.
func (p *Parser) extractPython(root *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		switch child.Type() {
		case "function_definition":
			sym := p.pythonFunction(child, source, "")
			sym.Language = "python"
			result.Symbols = append(result.Symbols, sym)

		case "class_definition":
			syms := p.pythonClass(child, source)
			for _, sym := range syms {
				sym.Language = "python"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

func (p *Parser) pythonFunction(node *sitter.Node, source []byte, parentClass string) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	qualified := name
	kind := "function"
	if parentClass != "" {
		qualified = parentClass + "." + name
		kind = "method"
	}

	sig := extractFirstLine(node.Content(source))

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   sig,
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentClass,
		DocComment:  findPrecedingComment(node, source),
	}
}

func (p *Parser) pythonClass(node *sitter.Node, source []byte) []Symbol {
	nameNode := node.ChildByFieldName("name")
	className := ""
	if nameNode != nil {
		className = nameNode.Content(source)
	}

	// Class declaration (without method bodies for the class chunk)
	syms := []Symbol{{
		Name:       className,
		Qualified:  className,
		Kind:       "class",
		Content:    node.Content(source),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract methods from class body
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			if child.Type() == "function_definition" {
				sym := p.pythonFunction(child, source, className)
				syms = append(syms, sym)
			}
		}
	}

	return syms
}

// extractTypeScript extracts symbols from a TypeScript AST.
func (p *Parser) extractTypeScript(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkTS(root, source, result, "")
}

func (p *Parser) walkTS(node *sitter.Node, source []byte, result *ParseResult, parentClass string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.tsFunction(child, source)
			sym.Language = "typescript"
			result.Symbols = append(result.Symbols, sym)

		case "class_declaration":
			syms := p.tsClass(child, source)
			for _, sym := range syms {
				sym.Language = "typescript"
				result.Symbols = append(result.Symbols, sym)
			}

		case "interface_declaration":
			sym := p.tsInterface(child, source)
			sym.Language = "typescript"
			result.Symbols = append(result.Symbols, sym)

		case "enum_declaration":
			sym := p.tsEnum(child, source)
			sym.Language = "typescript"
			result.Symbols = append(result.Symbols, sym)

		case "export_statement":
			p.walkTS(child, source, result, parentClass)

		case "lexical_declaration":
			syms := p.tsLexicalDecl(child, source)
			for _, sym := range syms {
				sym.Language = "typescript"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

func (p *Parser) tsFunction(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}
	sig := extractFirstLine(node.Content(source))
	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "function",
		Content:    node.Content(source),
		Signature:  sig,
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) tsClass(node *sitter.Node, source []byte) []Symbol {
	nameNode := node.ChildByFieldName("name")
	className := ""
	if nameNode != nil {
		className = nameNode.Content(source)
	}

	syms := []Symbol{{
		Name:       className,
		Qualified:  className,
		Kind:       "class",
		Content:    node.Content(source),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract methods
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			if child.Type() == "method_definition" {
				nameNode := child.ChildByFieldName("name")
				methodName := ""
				if nameNode != nil {
					methodName = nameNode.Content(source)
				}
				sig := extractFirstLine(child.Content(source))
				syms = append(syms, Symbol{
					Name:        methodName,
					Qualified:   className + "." + methodName,
					Kind:        "method",
					Content:     child.Content(source),
					Signature:   sig,
					LineStart:   int(child.StartPoint().Row) + 1,
					LineEnd:     int(child.EndPoint().Row) + 1,
					ParentClass: className,
					DocComment:  findPrecedingComment(child, source),
				})
			}
		}
	}

	return syms
}

func (p *Parser) tsInterface(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}
	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "struct", // interfaces map to struct kind
		Content:    node.Content(source),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) tsEnum(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}
	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "enum",
		Content:    node.Content(source),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) tsLexicalDecl(node *sitter.Node, source []byte) []Symbol {
	var syms []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "variable_declarator" {
			nameNode := child.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			name := nameNode.Content(source)
			// Check if the value is an arrow function
			valueNode := child.ChildByFieldName("value")
			if valueNode != nil && valueNode.Type() == "arrow_function" {
				syms = append(syms, Symbol{
					Name:       name,
					Qualified:  name,
					Kind:       "function",
					Content:    node.Content(source),
					Signature:  extractFirstLine(node.Content(source)),
					LineStart:  int(node.StartPoint().Row) + 1,
					LineEnd:    int(node.EndPoint().Row) + 1,
					DocComment: findPrecedingComment(node, source),
				})
			}
		}
	}
	return syms
}

func findPrecedingComment(node *sitter.Node, source []byte) string {
	prev := node.PrevSibling()
	if prev == nil {
		return ""
	}
	if prev.Type() == "comment" || prev.Type() == "block_comment" {
		return prev.Content(source)
	}
	return ""
}

func extractFirstLine(s string) string {
	idx := strings.Index(s, "\n")
	if idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}
