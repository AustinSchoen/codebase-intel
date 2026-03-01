package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractCRelationships walks the C AST to find includes, function calls,
// and type references.
func (p *Parser) extractCRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkCForRefs(root, source, result)
}

func (p *Parser) walkCForRefs(node *sitter.Node, source []byte, result *ParseResult) {
	switch node.Type() {
	case "preproc_include":
		p.extractCInclude(node, source, result)
	case "call_expression":
		p.extractCCall(node, source, result)
	case "type_identifier":
		p.extractCTypeRef(node, source, result)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkCForRefs(node.Child(i), source, result)
	}
}

// extractCInclude extracts #include directives as "references" relationships.
func (p *Parser) extractCInclude(node *sitter.Node, source []byte, result *ParseResult) {
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

	includePath := pathNode.Content(source)
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

// extractCCall extracts function calls as "calls" relationships.
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
		// ptr->func() style calls
		field := funcNode.ChildByFieldName("field")
		if field != nil {
			callee = field.Content(source)
		}
	}

	if callee == "" || cBuiltinFuncs[callee] {
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

// extractCTypeRef captures type_identifier nodes as "references" relationships.
func (p *Parser) extractCTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || cBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions (the name being defined)
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "struct_specifier", "enum_specifier", "union_specifier":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
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

// cBuiltinTypes lists C built-in types that should not generate references.
var cBuiltinTypes = map[string]bool{
	"void": true, "char": true, "short": true, "int": true,
	"long": true, "float": true, "double": true,
	"signed": true, "unsigned": true,
	"size_t": true, "ptrdiff_t": true, "ssize_t": true,
	"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
	"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
	"bool": true, "FILE": true,
}

// cBuiltinFuncs lists C standard library functions too common to be useful.
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
