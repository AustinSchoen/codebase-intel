package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractCppRelationships walks the C++ AST to find includes, inheritance,
// function calls, and type references.
func (p *Parser) extractCppRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "preproc_include":
			extractInclude(node, source, result) // shared with C
		case "base_class_clause":
			p.extractCppInheritance(node, source, result)
		case "call_expression":
			p.extractCppCall(node, source, result)
		case "type_identifier":
			p.extractCppTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractCppInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	parent := node.Parent()
	if parent == nil {
		return
	}
	nameNode := parent.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(source)
	if className == "" {
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "base_class_specifier" {
			continue
		}
		baseName := extractBaseClassName(child, source)
		emitInheritsRelationship(child, className, baseName, cppBuiltinTypes, result)
	}
}

// extractBaseClassName gets the type name from a base_class_specifier,
// skipping access specifiers (public, private, protected, virtual).
func extractBaseClassName(node *sitter.Node, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier", "qualified_identifier":
			return child.Content(source)
		case "template_type":
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				return nameNode.Content(source)
			}
		}
	}
	return ""
}

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
	if callee == "" || ueMacros[callee] {
		return
	}
	emitCallRelationship(node, callee, cppBuiltinFuncs, result)
}

func (p *Parser) extractCppTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || cppBuiltinTypes[typeName] || ueMacros[typeName] {
		return
	}
	if isTypeDefinition(node, "class_specifier", "struct_specifier", "enum_specifier") {
		return
	}
	parent := node.Parent()
	if parent != nil && parent.Type() == "base_class_specifier" {
		return
	}
	emitTypeRefRelationship(node, typeName, cppBuiltinTypes, result)
}

var cppBuiltinTypes = map[string]bool{
	"void": true, "bool": true, "char": true, "short": true, "int": true,
	"long": true, "float": true, "double": true, "auto": true,
	"signed": true, "unsigned": true, "wchar_t": true,
	"char8_t": true, "char16_t": true, "char32_t": true,
	"size_t": true, "ptrdiff_t": true, "nullptr_t": true,
	"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
	"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
	"string": true, "string_view": true,
}

var cppBuiltinFuncs = map[string]bool{
	"sizeof": true, "alignof": true, "decltype": true, "typeid": true,
	"static_cast": true, "dynamic_cast": true, "const_cast": true, "reinterpret_cast": true,
	"static_assert": true,
	"printf":        true, "fprintf": true, "sprintf": true, "snprintf": true,
	"malloc": true, "calloc": true, "realloc": true, "free": true,
	"memcpy": true, "memset": true, "memmove": true, "memcmp": true,
	"strlen": true, "strcmp": true, "strncmp": true, "strcpy": true, "strncpy": true,
}

var ueMacros = map[string]bool{
	"UCLASS": true, "USTRUCT": true, "UPROPERTY": true, "UFUNCTION": true,
	"UENUM": true, "UINTERFACE": true, "UMETA": true,
	"GENERATED_BODY": true, "GENERATED_UCLASS_BODY": true, "GENERATED_USTRUCT_BODY": true,
	"DECLARE_DYNAMIC_MULTICAST_DELEGATE": true,
	"DECLARE_DELEGATE":                   true,
	"DECLARE_EVENT":                      true,
	"TEXT":                               true, "LOCTEXT": true, "NSLOCTEXT": true,
	"UE_LOG": true, "UE_CLOG": true,
	"check": true, "checkf": true, "ensure": true, "ensureMsgf": true,
	"IMPLEMENT_MODULE": true, "IMPLEMENT_GAME_MODULE": true,
}
