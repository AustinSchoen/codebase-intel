package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractKotlinRelationships walks the Kotlin AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractKotlinRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkKotlinForRefs(root, source, result)
}

func (p *Parser) walkKotlinForRefs(node *sitter.Node, source []byte, result *ParseResult) {
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

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkKotlinForRefs(node.Child(i), source, result)
	}
}

// extractKotlinImport extracts import statements as "references" relationships.
func (p *Parser) extractKotlinImport(node *sitter.Node, source []byte, result *ParseResult) {
	// import_header contains "import" keyword + identifier (dotted path)
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			importPath := child.Content(source)
			if importPath == "" {
				continue
			}
			line := int(node.StartPoint().Row) + 1
			result.Relationships = append(result.Relationships, Relationship{
				SourceQualified: result.Filepath,
				TargetName:      importPath,
				Kind:            "references",
				Line:            line,
			})
			return
		}
	}
}

// extractKotlinCall extracts function/method calls as "calls" relationships.
func (p *Parser) extractKotlinCall(node *sitter.Node, source []byte, result *ParseResult) {
	if node.ChildCount() == 0 {
		return
	}

	// First child is the callee expression
	calleeNode := node.Child(0)
	var callee string

	switch calleeNode.Type() {
	case "simple_identifier":
		callee = calleeNode.Content(source)
	case "navigation_expression":
		// obj.method — extract last two identifiers
		var ids []string
		for i := 0; i < int(calleeNode.ChildCount()); i++ {
			child := calleeNode.Child(i)
			if child.Type() == "simple_identifier" {
				ids = append(ids, child.Content(source))
			}
		}
		if len(ids) >= 2 {
			callee = ids[len(ids)-2] + "." + ids[len(ids)-1]
		} else if len(ids) == 1 {
			callee = ids[0]
		}
	}

	if callee == "" || kotlinBuiltinFuncs[callee] {
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

// extractKotlinInheritance extracts class inheritance from delegation_specifiers.
func (p *Parser) extractKotlinInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	// Find class name
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

	// Find delegation_specifiers
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
			if baseName == "" || kotlinBuiltinTypes[baseName] {
				continue
			}

			line := int(spec.StartPoint().Row) + 1
			result.Relationships = append(result.Relationships, Relationship{
				SourceQualified: className,
				TargetName:      baseName,
				Kind:            "inherits",
				Line:            line,
			})
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
			// Constructor call: first child is user_type
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

// extractFirstKotlinIdentifier gets the first simple_identifier or type_identifier from a node.
func extractFirstKotlinIdentifier(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "simple_identifier" || child.Type() == "type_identifier" {
			return child.Content(source)
		}
	}
	return ""
}

// extractKotlinTypeRef captures user_type nodes as "references" relationships.
func (p *Parser) extractKotlinTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := extractFirstKotlinIdentifier(node, source)
	if typeName == "" || kotlinBuiltinTypes[typeName] {
		return
	}

	// Skip if inside delegation_specifiers (handled by extractKotlinInheritance)
	parent := node.Parent()
	if parent != nil {
		if parent.Type() == "delegation_specifier" || parent.Type() == "constructor_invocation" {
			return
		}
	}

	// Skip type definitions
	if parent != nil && parent.Type() == "class_declaration" {
		// Check if this user_type is the class name itself
		for i := 0; i < int(parent.ChildCount()); i++ {
			child := parent.Child(i)
			if child.Type() == "type_identifier" && child.Content(source) == typeName {
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
