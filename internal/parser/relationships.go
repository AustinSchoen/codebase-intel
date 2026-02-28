package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractGoRelationships walks the Go AST to find function calls,
// struct embedding, and type references.
func (p *Parser) extractGoRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkGoForRefs(root, source, result)
}

func (p *Parser) walkGoForRefs(node *sitter.Node, source []byte, result *ParseResult) {
	switch node.Type() {
	case "call_expression":
		p.extractGoCall(node, source, result)
	case "field_declaration":
		p.extractGoEmbedding(node, source, result)
	case "type_identifier":
		p.extractGoTypeRef(node, source, result)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkGoForRefs(node.Child(i), source, result)
	}
}

// extractGoCall extracts a "calls" relationship from a call_expression node.
func (p *Parser) extractGoCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	var callee string
	switch funcNode.Type() {
	case "identifier":
		callee = funcNode.Content(source)
	case "selector_expression":
		field := funcNode.ChildByFieldName("field")
		if field != nil {
			operand := funcNode.ChildByFieldName("operand")
			if operand != nil {
				callee = operand.Content(source) + "." + field.Content(source)
			} else {
				callee = field.Content(source)
			}
		}
	}

	if callee == "" || goBuiltinFuncs[callee] {
		return
	}

	line := int(node.StartPoint().Row) + 1
	enclosing := findEnclosingSymbol(line, result.Symbols)
	if enclosing == "" {
		return
	}

	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: enclosing,
		TargetName:      callee,
		Kind:            "calls",
		Line:            line,
	})
}

// extractGoEmbedding detects struct embedding (unnamed fields) as "inherits" relationships.
func (p *Parser) extractGoEmbedding(node *sitter.Node, source []byte, result *ParseResult) {
	// Only unnamed fields (embedded) — skip if it has a name
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return
	}

	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}

	// Verify this is inside a struct
	parent := node.Parent()
	if parent == nil || parent.Type() != "field_declaration_list" {
		return
	}
	grandparent := parent.Parent()
	if grandparent == nil || grandparent.Type() != "struct_type" {
		return
	}

	structName := findEnclosingStructName(grandparent, source)
	if structName == "" {
		return
	}

	embeddedType := typeNode.Content(source)
	embeddedType = strings.TrimPrefix(embeddedType, "*")
	if embeddedType == "" {
		return
	}

	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: structName,
		TargetName:      embeddedType,
		Kind:            "inherits",
		Line:            line,
	})
}

// extractGoTypeRef captures type references (type_identifier nodes) as "references" relationships.
func (p *Parser) extractGoTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || goBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions (the name being defined, not referenced)
	parent := node.Parent()
	if parent != nil && parent.Type() == "type_spec" {
		defName := parent.ChildByFieldName("name")
		if defName != nil && defName.StartByte() == node.StartByte() {
			return
		}
	}

	line := int(node.StartPoint().Row) + 1
	enclosing := findEnclosingSymbol(line, result.Symbols)
	if enclosing == "" || enclosing == typeName {
		return
	}

	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: enclosing,
		TargetName:      typeName,
		Kind:            "references",
		Line:            line,
	})
}

// findEnclosingSymbol returns the qualified name of the narrowest symbol
// whose line range contains the given line.
func findEnclosingSymbol(line int, symbols []Symbol) string {
	var bestQualified string
	bestRange := int(^uint(0) >> 1) // max int
	for _, sym := range symbols {
		if line >= sym.LineStart && line <= sym.LineEnd {
			r := sym.LineEnd - sym.LineStart
			if r < bestRange {
				bestQualified = sym.Qualified
				bestRange = r
			}
		}
	}
	return bestQualified
}

// findEnclosingStructName walks up from a struct_type node to find the type name.
func findEnclosingStructName(structNode *sitter.Node, source []byte) string {
	// struct_type → type_spec → type_declaration
	parent := structNode.Parent()
	if parent == nil {
		return ""
	}
	if parent.Type() == "type_spec" {
		nameNode := parent.ChildByFieldName("name")
		if nameNode != nil {
			return nameNode.Content(source)
		}
	}
	return ""
}

var goBuiltinFuncs = map[string]bool{
	"make": true, "new": true, "len": true, "cap": true,
	"append": true, "copy": true, "close": true, "delete": true,
	"panic": true, "recover": true, "print": true, "println": true,
	"complex": true, "real": true, "imag": true, "clear": true,
	"min": true, "max": true,
}

var goBuiltinTypes = map[string]bool{
	"bool": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true,
	"complex64": true, "complex128": true,
	"string": true, "error": true, "any": true, "comparable": true,
}
