package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJSRelationships walks the JavaScript AST to find imports, function calls,
// and class inheritance.
func (p *Parser) extractJSRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_statement":
			p.extractJSImport(node, source, result)
		case "call_expression":
			p.extractJSCall(node, source, result)
		case "class_declaration":
			p.extractJSClassInheritance(node, source, result)
		}
	})
}

// extractTSRelationships walks the TypeScript AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractTSRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_statement":
			p.extractJSImport(node, source, result)
		case "call_expression":
			p.extractJSCall(node, source, result)
		case "class_declaration":
			p.extractJSClassInheritance(node, source, result)
		case "type_identifier":
			p.extractTSTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractJSImport(node *sitter.Node, source []byte, result *ParseResult) {
	sourceNode := node.ChildByFieldName("source")
	if sourceNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "string" {
				sourceNode = child
				break
			}
		}
	}
	if sourceNode == nil {
		return
	}
	importPath := strings.Trim(sourceNode.Content(source), "\"'`")
	emitImportRelationship(node, importPath, result)
}

func (p *Parser) extractJSCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	callee := extractCalleeFromFieldName(funcNode, source, "member_expression", "object", "property")
	emitCallRelationship(node, callee, jsBuiltinFuncs, result)
}

func (p *Parser) extractJSClassInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(source)

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "class_heritage", "extends_clause", "implements_clause":
			p.extractHeritageNames(className, child, source, result)
		}
	}
}

// extractHeritageNames extracts base class/interface names from a heritage clause node.
func (p *Parser) extractHeritageNames(className string, node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		var baseName string
		switch child.Type() {
		case "identifier", "type_identifier":
			baseName = child.Content(source)
		case "member_expression":
			baseName = child.Content(source)
		case "generic_type":
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				baseName = nameNode.Content(source)
			}
		case "extends_clause", "implements_clause":
			p.extractHeritageNames(className, child, source, result)
			continue
		}
		emitInheritsRelationship(child, className, baseName, jsBuiltinTypes, result)
	}
}

func (p *Parser) extractTSTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || jsBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions and heritage clauses
	if isTypeDefinition(node, "interface_declaration", "type_alias_declaration", "class_declaration", "enum_declaration") {
		return
	}
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "extends_clause", "implements_clause", "class_heritage":
			return
		}
	}

	emitTypeRefRelationship(node, typeName, jsBuiltinTypes, result)
}

var jsBuiltinFuncs = map[string]bool{
	"console.log": true, "console.error": true, "console.warn": true, "console.info": true, "console.debug": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "isFinite": true,
	"encodeURI": true, "decodeURI": true, "encodeURIComponent": true, "decodeURIComponent": true,
	"setTimeout": true, "setInterval": true, "clearTimeout": true, "clearInterval": true,
	"JSON.parse": true, "JSON.stringify": true,
	"Object.keys": true, "Object.values": true, "Object.entries": true, "Object.assign": true,
	"Array.isArray": true, "Array.from": true,
	"Promise.resolve": true, "Promise.reject": true, "Promise.all": true, "Promise.race": true,
	"require": true,
	"alert": true, "confirm": true, "prompt": true,
	"fetch": true,
}

var jsBuiltinTypes = map[string]bool{
	"string": true, "number": true, "boolean": true, "void": true, "undefined": true, "null": true,
	"any": true, "unknown": true, "never": true, "object": true, "symbol": true, "bigint": true,
	"Array": true, "Object": true, "Function": true, "String": true, "Number": true, "Boolean": true,
	"Promise": true, "Map": true, "Set": true, "WeakMap": true, "WeakSet": true,
	"Error": true, "TypeError": true, "RangeError": true, "SyntaxError": true,
	"Date": true, "RegExp": true, "Symbol": true, "BigInt": true,
	"Record": true, "Partial": true, "Required": true, "Readonly": true, "Pick": true, "Omit": true,
	"Exclude": true, "Extract": true, "NonNullable": true, "ReturnType": true, "InstanceType": true,
	"HTMLElement": true, "Event": true, "Node": true,
}
