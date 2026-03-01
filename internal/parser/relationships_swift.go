package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractSwiftRelationships walks the Swift AST to find imports, function calls,
// class/protocol inheritance, and type references.
func (p *Parser) extractSwiftRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkSwiftForRefs(root, source, result)
}

func (p *Parser) walkSwiftForRefs(node *sitter.Node, source []byte, result *ParseResult) {
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

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkSwiftForRefs(node.Child(i), source, result)
	}
}

// extractSwiftImport extracts import declarations as "references" relationships.
func (p *Parser) extractSwiftImport(node *sitter.Node, source []byte, result *ParseResult) {
	// import_declaration children: "import" keyword + identifier(s)
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" || child.Type() == "simple_identifier" || child.Type() == "type_identifier" {
			importName := child.Content(source)
			if importName == "" {
				continue
			}
			line := int(node.StartPoint().Row) + 1
			result.Relationships = append(result.Relationships, Relationship{
				SourceQualified: result.Filepath,
				TargetName:      importName,
				Kind:            "references",
				Line:            line,
			})
			return
		}
	}
}

// extractSwiftCall extracts function/method calls as "calls" relationships.
func (p *Parser) extractSwiftCall(node *sitter.Node, source []byte, result *ParseResult) {
	if node.ChildCount() == 0 {
		return
	}

	// Try field-based access first, then fall back to first child
	calleeNode := node.ChildByFieldName("function")
	if calleeNode == nil {
		calleeNode = node.Child(0)
	}

	var callee string
	switch calleeNode.Type() {
	case "simple_identifier", "identifier":
		callee = calleeNode.Content(source)
	case "navigation_expression":
		// obj.method — extract identifiers
		var ids []string
		for i := 0; i < int(calleeNode.ChildCount()); i++ {
			child := calleeNode.Child(i)
			if child.Type() == "simple_identifier" || child.Type() == "identifier" {
				ids = append(ids, child.Content(source))
			}
			// Handle navigation_suffix
			if child.Type() == "navigation_suffix" {
				for j := 0; j < int(child.ChildCount()); j++ {
					gc := child.Child(j)
					if gc.Type() == "simple_identifier" || gc.Type() == "identifier" {
						ids = append(ids, gc.Content(source))
					}
				}
			}
		}
		if len(ids) >= 2 {
			callee = ids[len(ids)-2] + "." + ids[len(ids)-1]
		} else if len(ids) == 1 {
			callee = ids[0]
		}
	case "member_expression":
		// Alternate member access pattern
		prop := calleeNode.ChildByFieldName("property")
		if prop != nil {
			obj := calleeNode.ChildByFieldName("object")
			if obj != nil && (obj.Type() == "simple_identifier" || obj.Type() == "identifier") {
				callee = obj.Content(source) + "." + prop.Content(source)
			} else {
				callee = prop.Content(source)
			}
		}
	}

	if callee == "" || swiftBuiltinFuncs[callee] {
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

// extractSwiftClassInheritance extracts class inheritance (class Foo: Bar, Baz).
func (p *Parser) extractSwiftClassInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(source)

	bodyNode := node.ChildByFieldName("body")
	foundColon := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)

		// Stop when we reach the body
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
		if baseName == "" || baseName == "," || swiftBuiltinTypes[baseName] {
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

// extractSwiftProtocolInheritance extracts protocol inheritance (protocol Foo: Bar, Baz).
func (p *Parser) extractSwiftProtocolInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	protocolName := nameNode.Content(source)

	bodyNode := node.ChildByFieldName("body")
	foundColon := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)

		// Stop when we reach the body
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
		if baseName == "" || baseName == "," || swiftBuiltinTypes[baseName] {
			continue
		}

		line := int(child.StartPoint().Row) + 1
		result.Relationships = append(result.Relationships, Relationship{
			SourceQualified: protocolName,
			TargetName:      baseName,
			Kind:            "inherits",
			Line:            line,
		})
	}
}

// extractSwiftInheritedType extracts the type name from an inheritance specifier node.
func extractSwiftInheritedType(node *sitter.Node, source []byte) string {
	switch node.Type() {
	case "type_identifier", "simple_identifier", "identifier":
		return node.Content(source)
	case "user_type":
		// Walk children for the type identifier
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "type_identifier" || child.Type() == "simple_identifier" {
				return child.Content(source)
			}
		}
	case "inheritance_specifier":
		// Wrapper node containing the actual type
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			name := extractSwiftInheritedType(child, source)
			if name != "" {
				return name
			}
		}
	}
	return ""
}

// extractSwiftTypeRef captures type_identifier nodes as "references" relationships.
func (p *Parser) extractSwiftTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || swiftBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions (the name being defined)
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "class_declaration", "protocol_declaration":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "inheritance_specifier":
			// Handled by extractSwiftClassInheritance/extractSwiftProtocolInheritance
			return
		}
	}

	// Also skip if this is a direct child of class/protocol declaration after ":"
	// (inheritance position) — handles cases without inheritance_specifier wrapper
	if parent != nil {
		gp := parent.Parent()
		if gp != nil && (gp.Type() == "class_declaration" || gp.Type() == "protocol_declaration") {
			// If the parent is user_type and it's in inheritance position, skip
			if parent.Type() == "user_type" {
				return
			}
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
