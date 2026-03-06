package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractDartRelationships walks the Dart AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractDartRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_or_export":
			p.extractDartImportOrExport(node, source, result)
		case "class_definition":
			p.extractDartClassInheritance(node, source, result)
		case "mixin_declaration":
			p.extractDartMixinInheritance(node, source, result)
		case "expression_statement":
			p.extractDartCallFromExprStmt(node, source, result)
		case "type_identifier":
			p.extractDartTypeRef(node, source, result)
		}
	})
}

// extractDartImportOrExport handles import_or_export nodes.
// Structure: import_or_export > library_import > import_specification > configurable_uri > uri > string_literal
func (p *Parser) extractDartImportOrExport(node *sitter.Node, source []byte, result *ParseResult) {
	var found bool
	walkTree(node, source, result, func(n *sitter.Node, src []byte, res *ParseResult) {
		if found {
			return
		}
		if n.Type() == "string_literal" {
			uri := extractDartStringContent(n, src)
			if uri != "" {
				emitImportRelationship(node, uri, res)
				found = true
			}
		}
	})
}

func (p *Parser) extractDartClassInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	className := dartFindChildByType(node, "identifier", source)
	if className == "" {
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "superclass":
			// extends clause: may contain type_identifier and mixins child
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "type_identifier" {
					emitInheritsRelationship(gc, className, gc.Content(source), dartBuiltinTypes, result)
				}
				if gc.Type() == "mixins" {
					dartExtractTypeIdentifiers(gc, source, result, className)
				}
			}

		case "interfaces":
			dartExtractTypeIdentifiers(child, source, result, className)
		}
	}
}

func (p *Parser) extractDartMixinInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	mixinName := dartFindChildByType(node, "identifier", source)
	if mixinName == "" {
		return
	}

	// mixin_declaration: type_identifier children after "on" keyword
	foundOn := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "on" {
			foundOn = true
			continue
		}
		if foundOn && child.Type() == "type_identifier" {
			emitInheritsRelationship(child, mixinName, child.Content(source), dartBuiltinTypes, result)
		}
		if child.Type() == "class_body" {
			break
		}
		if child.Type() == "interfaces" {
			dartExtractTypeIdentifiers(child, source, result, mixinName)
		}
	}
}

// dartExtractTypeIdentifiers extracts type_identifier children and emits inherits relationships.
func dartExtractTypeIdentifiers(node *sitter.Node, source []byte, result *ParseResult, ownerName string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_identifier" {
			emitInheritsRelationship(child, ownerName, child.Content(source), dartBuiltinTypes, result)
		}
	}
}

// extractDartCallFromExprStmt handles expression_statement nodes that represent
// function calls: identifier + selector > argument_part > arguments
func (p *Parser) extractDartCallFromExprStmt(node *sitter.Node, source []byte, result *ParseResult) {
	if node.ChildCount() < 2 {
		return
	}
	first := node.Child(0)
	if first.Type() != "identifier" {
		return
	}
	second := node.Child(1)
	if second.Type() == "selector" {
		for i := 0; i < int(second.ChildCount()); i++ {
			if second.Child(i).Type() == "argument_part" {
				emitCallRelationship(node, first.Content(source), dartBuiltinFuncs, result)
				return
			}
		}
	}
}

func (p *Parser) extractDartTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || dartBuiltinTypes[typeName] {
		return
	}
	parent := node.Parent()
	if parent == nil {
		return
	}
	switch parent.Type() {
	case "class_definition", "mixin_declaration", "enum_declaration",
		"extension_declaration", "extension_type_declaration":
		if dartFindChildByType(parent, "identifier", source) == typeName {
			return
		}
	case "superclass", "interfaces", "mixins":
		return
	case "type_alias":
		// Skip the name being defined (first type_identifier)
		for i := 0; i < int(parent.ChildCount()); i++ {
			child := parent.Child(i)
			if child.Type() == "type_identifier" {
				if child.StartByte() == node.StartByte() {
					return
				}
				break
			}
		}
	}
	emitTypeRefRelationship(node, typeName, dartBuiltinTypes, result)
}

// extractDartStringContent extracts text from a string_literal, stripping quotes.
func extractDartStringContent(node *sitter.Node, source []byte) string {
	content := node.Content(source)
	if len(content) >= 2 {
		if (content[0] == '\'' && content[len(content)-1] == '\'') ||
			(content[0] == '"' && content[len(content)-1] == '"') {
			return content[1 : len(content)-1]
		}
	}
	return content
}

var dartBuiltinFuncs = map[string]bool{
	"print": true, "debugPrint": true,
	"main": true, "runApp": true,
	"setState": true,
}

var dartBuiltinTypes = map[string]bool{
	"Object": true, "String": true, "int": true, "double": true,
	"num": true, "bool": true, "List": true, "Map": true,
	"Set": true, "Future": true, "Stream": true, "Iterable": true,
	"void": true, "dynamic": true, "Never": true, "Null": true,
	"Type": true, "Symbol": true, "Function": true, "Record": true,
	"Pattern": true,
}
