package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractCppRelationships walks the C++ AST to find includes, inheritance,
// function calls, and type references.
func (p *Parser) extractCppRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkCppForRefs(root, source, result)
}

func (p *Parser) walkCppForRefs(node *sitter.Node, source []byte, result *ParseResult) {
	switch node.Type() {
	case "preproc_include":
		p.extractCppInclude(node, source, result)
	case "base_class_clause":
		p.extractCppInheritance(node, source, result)
	case "call_expression":
		p.extractCppCall(node, source, result)
	case "type_identifier":
		p.extractCppTypeRef(node, source, result)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkCppForRefs(node.Child(i), source, result)
	}
}

// extractCppInclude extracts #include directives as "references" relationships.
func (p *Parser) extractCppInclude(node *sitter.Node, source []byte, result *ParseResult) {
	// The path child contains the included file (e.g. "foo.h" or <vector>)
	pathNode := node.ChildByFieldName("path")
	if pathNode == nil {
		// Try finding string_literal or system_lib_string child
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "string_literal" || child.Type() == "system_lib_string" {
				pathNode = child
				break
			}
		}
	}
	if pathNode == nil {
		return
	}

	includePath := pathNode.Content(source)
	// Strip quotes/angle brackets
	includePath = strings.Trim(includePath, "\"<>")
	if includePath == "" {
		return
	}

	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: result.Filepath,
		TargetName:      includePath,
		Kind:            "references",
		Line:            line,
	})
}

// extractCppInheritance extracts class inheritance from base_class_clause nodes.
// A base_class_clause contains one or more base_class_specifier children.
func (p *Parser) extractCppInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	// Find the class name from the parent class_specifier
	parent := node.Parent()
	if parent == nil {
		return
	}
	className := ""
	nameNode := parent.ChildByFieldName("name")
	if nameNode != nil {
		className = nameNode.Content(source)
	}
	if className == "" {
		return
	}

	// Walk base_class_specifier children
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "base_class_specifier" {
			continue
		}
		baseName := extractBaseClassName(child, source)
		if baseName == "" || cppBuiltinTypes[baseName] {
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

// extractBaseClassName gets the type name from a base_class_specifier.
// It skips access specifiers (public, private, protected, virtual).
func extractBaseClassName(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier":
			return child.Content(source)
		case "qualified_identifier":
			return child.Content(source)
		case "template_type":
			// e.g. Base<T> — extract the name part
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				return nameNode.Content(source)
			}
		}
	}
	return ""
}

// extractCppCall extracts function/method calls as "calls" relationships.
func (p *Parser) extractCppCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	var callee string
	switch funcNode.Type() {
	case "identifier":
		callee = funcNode.Content(source)
	case "field_expression":
		// obj.method() or obj->method()
		field := funcNode.ChildByFieldName("field")
		if field != nil {
			callee = field.Content(source)
		}
	case "qualified_identifier":
		callee = funcNode.Content(source)
	case "template_function":
		nameNode := funcNode.ChildByFieldName("name")
		if nameNode != nil {
			callee = nameNode.Content(source)
		}
	}

	if callee == "" || cppBuiltinFuncs[callee] || ueMacros[callee] {
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

// extractCppTypeRef captures type_identifier nodes as "references" relationships.
func (p *Parser) extractCppTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || cppBuiltinTypes[typeName] || ueMacros[typeName] {
		return
	}

	// Skip type definitions (the name of the class/struct being defined)
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "class_specifier", "struct_specifier", "enum_specifier":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "base_class_specifier":
			// Already handled by extractCppInheritance
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

// cppBuiltinTypes lists C++ types that should not generate references.
var cppBuiltinTypes = map[string]bool{
	// Fundamental types
	"void": true, "bool": true, "char": true, "short": true, "int": true,
	"long": true, "float": true, "double": true, "auto": true,
	"signed": true, "unsigned": true, "wchar_t": true,
	"char8_t": true, "char16_t": true, "char32_t": true,
	"size_t": true, "ptrdiff_t": true, "nullptr_t": true,
	"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
	"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
	// STL vocabulary types that are too common to be useful
	"string": true, "string_view": true,
}

// cppBuiltinFuncs lists C/C++ built-in functions that should not generate call relationships.
var cppBuiltinFuncs = map[string]bool{
	"sizeof": true, "alignof": true, "decltype": true, "typeid": true,
	"static_cast": true, "dynamic_cast": true, "const_cast": true, "reinterpret_cast": true,
	"static_assert": true,
	// C standard lib functions too ubiquitous to be useful
	"printf": true, "fprintf": true, "sprintf": true, "snprintf": true,
	"malloc": true, "calloc": true, "realloc": true, "free": true,
	"memcpy": true, "memset": true, "memmove": true, "memcmp": true,
	"strlen": true, "strcmp": true, "strncmp": true, "strcpy": true, "strncpy": true,
}

// ueMacros lists Unreal Engine macros that should not generate relationships.
var ueMacros = map[string]bool{
	"UCLASS": true, "USTRUCT": true, "UPROPERTY": true, "UFUNCTION": true,
	"UENUM": true, "UINTERFACE": true, "UMETA": true,
	"GENERATED_BODY": true, "GENERATED_UCLASS_BODY": true, "GENERATED_USTRUCT_BODY": true,
	"DECLARE_DYNAMIC_MULTICAST_DELEGATE": true,
	"DECLARE_DELEGATE": true,
	"DECLARE_EVENT": true,
	"TEXT": true, "LOCTEXT": true, "NSLOCTEXT": true,
	"UE_LOG": true, "UE_CLOG": true,
	"check": true, "checkf": true, "ensure": true, "ensureMsgf": true,
	"IMPLEMENT_MODULE": true, "IMPLEMENT_GAME_MODULE": true,
}
