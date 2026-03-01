package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJSRelationships walks the JavaScript AST to find imports, function calls,
// and class inheritance.
func (p *Parser) extractJSRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkJSForRefs(root, source, result, false)
}

// extractTSRelationships walks the TypeScript AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractTSRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkJSForRefs(root, source, result, true)
}

func (p *Parser) walkJSForRefs(node *sitter.Node, source []byte, result *ParseResult, withTypes bool) {
	switch node.Type() {
	case "import_statement":
		p.extractJSImport(node, source, result)
	case "call_expression":
		p.extractJSCall(node, source, result)
	case "class_declaration":
		p.extractJSClassInheritance(node, source, result)
	case "type_identifier":
		if withTypes {
			p.extractTSTypeRef(node, source, result)
		}
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkJSForRefs(node.Child(i), source, result, withTypes)
	}
}

// extractJSImport extracts import statements as "references" relationships.
func (p *Parser) extractJSImport(node *sitter.Node, source []byte, result *ParseResult) {
	sourceNode := node.ChildByFieldName("source")
	if sourceNode == nil {
		// Fallback: look for string child
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

	importPath := sourceNode.Content(source)
	importPath = strings.Trim(importPath, "\"'`")
	if importPath == "" {
		return
	}

	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: result.Filepath,
		TargetName:      importPath,
		Kind:            "references",
		Line:            line,
	})
}

// extractJSCall extracts function/method calls as "calls" relationships.
func (p *Parser) extractJSCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	var callee string
	switch funcNode.Type() {
	case "identifier":
		callee = funcNode.Content(source)
	case "member_expression":
		prop := funcNode.ChildByFieldName("property")
		if prop != nil {
			obj := funcNode.ChildByFieldName("object")
			if obj != nil && obj.Type() == "identifier" {
				callee = obj.Content(source) + "." + prop.Content(source)
			} else {
				callee = prop.Content(source)
			}
		}
	}

	if callee == "" || jsBuiltinFuncs[callee] {
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

// extractJSClassInheritance extracts class extends/implements as "inherits" relationships.
func (p *Parser) extractJSClassInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(source)

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "class_heritage":
			p.extractHeritageNames(className, child, source, result)
		case "extends_clause":
			p.extractHeritageNames(className, child, source, result)
		case "implements_clause":
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
			// Nested clause — recurse
			p.extractHeritageNames(className, child, source, result)
			continue
		}
		if baseName == "" || jsBuiltinTypes[baseName] {
			continue
		}

		line := int(child.StartPoint().Row) + 1
		result.Relationships = append(result.Relationships, Relationship{
			SourceQualified: className,
			TargetName:      baseName,
			Kind:            "inherits",
			Line:            line,
		})
	}
}

// extractTSTypeRef captures TypeScript type_identifier nodes as "references" relationships.
func (p *Parser) extractTSTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || jsBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions (the name being defined)
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "interface_declaration", "type_alias_declaration":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "class_declaration":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "enum_declaration":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "extends_clause", "implements_clause", "class_heritage":
			// Already handled by extractJSClassInheritance
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
