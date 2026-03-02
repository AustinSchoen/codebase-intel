package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractGoRelationships walks the Go AST to find function calls,
// struct embedding, and type references.
func (p *Parser) extractGoRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "call_expression":
			p.extractGoCall(node, source, result)
		case "field_declaration":
			p.extractGoEmbedding(node, source, result)
		case "type_identifier":
			p.extractGoTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractGoCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	callee := extractCalleeFromFieldName(funcNode, source, "selector_expression", "operand", "field")
	emitCallRelationship(node, callee, goBuiltinFuncs, result)
}

// extractGoEmbedding detects struct embedding (unnamed fields) as "inherits" relationships.
func (p *Parser) extractGoEmbedding(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return
	}
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}
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
	embeddedType := strings.TrimPrefix(typeNode.Content(source), "*")
	emitInheritsRelationship(node, structName, embeddedType, goBuiltinTypes, result)
}

func (p *Parser) extractGoTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	if isTypeDefinition(node, "type_spec") {
		return
	}
	emitTypeRefRelationship(node, node.Content(source), goBuiltinTypes, result)
}

// findEnclosingStructName walks up from a struct_type node to find the type name.
func findEnclosingStructName(structNode *sitter.Node, source []byte) string {
	parent := structNode.Parent()
	if parent == nil || parent.Type() != "type_spec" {
		return ""
	}
	nameNode := parent.ChildByFieldName("name")
	if nameNode != nil {
		return nameNode.Content(source)
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
