package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractCRelationships walks the C AST to find includes, function calls,
// and type references.
func (p *Parser) extractCRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "preproc_include":
			extractInclude(node, source, result)
		case "call_expression":
			p.extractCCall(node, source, result)
		case "type_identifier":
			p.extractCTypeRef(node, source, result)
		}
	})
}

// extractInclude extracts #include directives as "references" relationships.
// Shared by C and C++.
func extractInclude(node *sitter.Node, source []byte, result *ParseResult) {
	pathNode := node.ChildByFieldName("path")
	if pathNode == nil {
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
	includePath := strings.Trim(pathNode.Content(source), "\"<>")
	emitImportRelationship(node, includePath, result)
}

func (p *Parser) extractCCall(node *sitter.Node, source []byte, result *ParseResult) {
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
	}
	emitCallRelationship(node, callee, cBuiltinFuncs, result)
}

func (p *Parser) extractCTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	if isTypeDefinition(node, "struct_specifier", "enum_specifier", "union_specifier") {
		return
	}
	emitTypeRefRelationship(node, node.Content(source), cBuiltinTypes, result)
}

var cBuiltinTypes = map[string]bool{
	"void": true, "char": true, "short": true, "int": true,
	"long": true, "float": true, "double": true,
	"signed": true, "unsigned": true,
	"size_t": true, "ptrdiff_t": true, "ssize_t": true,
	"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
	"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
	"bool": true, "FILE": true,
}

var cBuiltinFuncs = map[string]bool{
	"printf": true, "fprintf": true, "sprintf": true, "snprintf": true,
	"scanf": true, "fscanf": true, "sscanf": true,
	"malloc": true, "calloc": true, "realloc": true, "free": true,
	"memcpy": true, "memset": true, "memmove": true, "memcmp": true,
	"strlen": true, "strcmp": true, "strncmp": true, "strcpy": true, "strncpy": true,
	"strcat": true, "strncat": true, "strstr": true, "strchr": true,
	"fopen": true, "fclose": true, "fread": true, "fwrite": true,
	"fgets": true, "fputs": true, "fseek": true, "ftell": true,
	"assert": true, "abort": true, "exit": true,
	"sizeof": true,
}
