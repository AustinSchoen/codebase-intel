package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractKotlinRelationships walks the Kotlin AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractKotlinRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_header":
			p.extractKotlinImport(node, source, result)
		case "call_expression":
			p.extractKotlinCall(node, source, result)
		case "class_declaration":
			p.extractKotlinInheritance(node, source, result)
		case "user_type":
			p.extractKotlinTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractKotlinImport(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			emitImportRelationship(node, child.Content(source), result)
			return
		}
	}
}

func (p *Parser) extractKotlinCall(node *sitter.Node, source []byte, result *ParseResult) {
	if node.ChildCount() == 0 {
		return
	}
	calleeNode := node.Child(0)
	var callee string
	switch calleeNode.Type() {
	case "simple_identifier":
		callee = calleeNode.Content(source)
	case "navigation_expression":
		callee = extractNavigationCallee(calleeNode, source, []string{"simple_identifier"}, nil)
	}
	emitCallRelationship(node, callee, kotlinBuiltinFuncs, result)
}

func (p *Parser) extractKotlinInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	className := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_identifier" {
			className = child.Content(source)
			break
		}
	}
	if className == "" {
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "delegation_specifiers" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			spec := child.Child(j)
			if spec.Type() != "delegation_specifier" {
				continue
			}
			baseName := extractKotlinBaseType(spec, source)
			emitInheritsRelationship(spec, className, baseName, kotlinBuiltinTypes, result)
		}
	}
}

// extractKotlinBaseType extracts the type name from a delegation_specifier node.
func extractKotlinBaseType(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "user_type":
			return extractFirstKotlinIdentifier(child, source)
		case "constructor_invocation":
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "user_type" {
					return extractFirstKotlinIdentifier(gc, source)
				}
			}
		}
	}
	return ""
}

func extractFirstKotlinIdentifier(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "simple_identifier" || child.Type() == "type_identifier" {
			return child.Content(source)
		}
	}
	return ""
}

func (p *Parser) extractKotlinTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := extractFirstKotlinIdentifier(node, source)
	if typeName == "" || kotlinBuiltinTypes[typeName] {
		return
	}
	// Skip delegation_specifiers (handled by extractKotlinInheritance)
	parent := node.Parent()
	if parent != nil {
		if parent.Type() == "delegation_specifier" || parent.Type() == "constructor_invocation" {
			return
		}
		// Skip class definitions
		if parent.Type() == "class_declaration" {
			for i := 0; i < int(parent.ChildCount()); i++ {
				child := parent.Child(i)
				if child.Type() == "type_identifier" && child.Content(source) == typeName {
					return
				}
			}
		}
	}
	emitTypeRefRelationship(node, typeName, kotlinBuiltinTypes, result)
}

var kotlinBuiltinFuncs = map[string]bool{
	"println": true, "print": true, "readLine": true,
	"listOf": true, "mutableListOf": true, "arrayListOf": true,
	"mapOf": true, "mutableMapOf": true, "hashMapOf": true,
	"setOf": true, "mutableSetOf": true, "hashSetOf": true,
	"arrayOf": true, "intArrayOf": true, "longArrayOf": true, "doubleArrayOf": true,
	"emptyList": true, "emptyMap": true, "emptySet": true,
	"require": true, "check": true, "error": true,
	"TODO": true, "run": true, "let": true, "also": true, "apply": true, "with": true,
	"lazy": true, "repeat": true, "buildString": true,
	"maxOf": true, "minOf": true,
}

var kotlinBuiltinTypes = map[string]bool{
	"Int": true, "Long": true, "Short": true, "Byte": true,
	"Float": true, "Double": true, "Boolean": true, "Char": true,
	"String": true, "Unit": true, "Nothing": true, "Any": true,
	"Array": true, "IntArray": true, "LongArray": true, "DoubleArray": true,
	"List": true, "MutableList": true, "ArrayList": true,
	"Map": true, "MutableMap": true, "HashMap": true,
	"Set": true, "MutableSet": true, "HashSet": true,
	"Pair": true, "Triple": true,
	"Comparable": true, "Iterable": true, "Collection": true, "Sequence": true,
}
