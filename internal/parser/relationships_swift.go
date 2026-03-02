package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractSwiftRelationships walks the Swift AST to find imports, function calls,
// class/protocol inheritance, and type references.
func (p *Parser) extractSwiftRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_declaration":
			p.extractSwiftImport(node, source, result)
		case "call_expression":
			p.extractSwiftCall(node, source, result)
		case "class_declaration":
			p.extractSwiftClassInheritance(node, source, result)
		case "protocol_declaration":
			p.extractSwiftProtocolInheritance(node, source, result)
		case "type_identifier":
			p.extractSwiftTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractSwiftImport(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" || child.Type() == "simple_identifier" || child.Type() == "type_identifier" {
			emitImportRelationship(node, child.Content(source), result)
			return
		}
	}
}

func (p *Parser) extractSwiftCall(node *sitter.Node, source []byte, result *ParseResult) {
	if node.ChildCount() == 0 {
		return
	}
	calleeNode := node.ChildByFieldName("function")
	if calleeNode == nil {
		calleeNode = node.Child(0)
	}
	var callee string
	switch calleeNode.Type() {
	case "simple_identifier", "identifier":
		callee = calleeNode.Content(source)
	case "navigation_expression":
		callee = extractNavigationCallee(calleeNode, source,
			[]string{"simple_identifier", "identifier"}, []string{"navigation_suffix"})
	case "member_expression":
		callee = extractCalleeFromFieldName(calleeNode, source, "member_expression", "object", "property")
	}
	emitCallRelationship(node, callee, swiftBuiltinFuncs, result)
}

// extractSwiftColonInheritance is shared between class and protocol declarations.
// It finds names after ":" and before the body.
func (p *Parser) extractSwiftColonInheritance(node *sitter.Node, source []byte, result *ParseResult, ownerName string) {
	bodyNode := node.ChildByFieldName("body")
	foundColon := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if bodyNode != nil && child.StartByte() >= bodyNode.StartByte() {
			break
		}
		if child.Type() == ":" {
			foundColon = true
			continue
		}
		if !foundColon {
			continue
		}
		baseName := extractSwiftInheritedType(child, source)
		if baseName == "" || baseName == "," {
			continue
		}
		emitInheritsRelationship(child, ownerName, baseName, swiftBuiltinTypes, result)
	}
}

func (p *Parser) extractSwiftClassInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	p.extractSwiftColonInheritance(node, source, result, nameNode.Content(source))
}

func (p *Parser) extractSwiftProtocolInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	p.extractSwiftColonInheritance(node, source, result, nameNode.Content(source))
}

// extractSwiftInheritedType extracts the type name from an inheritance specifier node.
func extractSwiftInheritedType(node *sitter.Node, source []byte) string {
	switch node.Type() {
	case "type_identifier", "simple_identifier", "identifier":
		return node.Content(source)
	case "user_type":
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "type_identifier" || child.Type() == "simple_identifier" {
				return child.Content(source)
			}
		}
	case "inheritance_specifier":
		for i := 0; i < int(node.ChildCount()); i++ {
			name := extractSwiftInheritedType(node.Child(i), source)
			if name != "" {
				return name
			}
		}
	}
	return ""
}

func (p *Parser) extractSwiftTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || swiftBuiltinTypes[typeName] {
		return
	}
	if isTypeDefinition(node, "class_declaration", "protocol_declaration") {
		return
	}
	parent := node.Parent()
	if parent != nil && parent.Type() == "inheritance_specifier" {
		return
	}
	// Skip user_type in inheritance position
	if parent != nil {
		gp := parent.Parent()
		if gp != nil && (gp.Type() == "class_declaration" || gp.Type() == "protocol_declaration") {
			if parent.Type() == "user_type" {
				return
			}
		}
	}
	emitTypeRefRelationship(node, typeName, swiftBuiltinTypes, result)
}

var swiftBuiltinFuncs = map[string]bool{
	"print": true, "debugPrint": true, "dump": true,
	"fatalError": true, "precondition": true, "preconditionFailure": true,
	"assert": true, "assertionFailure": true,
	"min": true, "max": true, "abs": true, "stride": true,
	"zip": true, "swap": true, "type": true,
	"withUnsafePointer": true, "withUnsafeMutablePointer": true,
	"DispatchQueue.main.async": true,
}

var swiftBuiltinTypes = map[string]bool{
	"Int": true, "Int8": true, "Int16": true, "Int32": true, "Int64": true,
	"UInt": true, "UInt8": true, "UInt16": true, "UInt32": true, "UInt64": true,
	"Float": true, "Double": true, "Bool": true, "String": true, "Character": true,
	"Void": true, "Never": true, "Any": true, "AnyObject": true, "AnyClass": true,
	"Optional": true, "Array": true, "Dictionary": true, "Set": true,
	"Error": true, "Result": true, "Codable": true, "Hashable": true, "Equatable": true,
	"Comparable": true, "Identifiable": true, "CustomStringConvertible": true,
	"Sequence": true, "Collection": true, "IteratorProtocol": true,
	"Self": true,
}
