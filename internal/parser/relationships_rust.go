package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractRustRelationships walks the Rust AST to find use declarations,
// function calls, trait implementations, and type references.
func (p *Parser) extractRustRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkRustForRefs(root, source, result)
}

func (p *Parser) walkRustForRefs(node *sitter.Node, source []byte, result *ParseResult) {
	switch node.Type() {
	case "use_declaration":
		p.extractRustUse(node, source, result)
	case "call_expression":
		p.extractRustCall(node, source, result)
	case "impl_item":
		p.extractRustImplInheritance(node, source, result)
	case "type_identifier":
		p.extractRustTypeRef(node, source, result)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkRustForRefs(node.Child(i), source, result)
	}
}

// extractRustUse extracts `use` declarations as "references" relationships.
func (p *Parser) extractRustUse(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "scoped_identifier", "identifier", "use_wildcard":
			usePath := child.Content(source)
			if usePath == "" {
				continue
			}
			line := int(node.StartPoint().Row) + 1
			result.Relationships = append(result.Relationships, Relationship{
				SourceQualified: result.Filepath,
				TargetName:      usePath,
				Kind:            "references",
				Line:            line,
			})
			return
		case "use_as_clause":
			// use foo::bar as baz — extract the original path
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
					usePath := gc.Content(source)
					if usePath != "" {
						line := int(node.StartPoint().Row) + 1
						result.Relationships = append(result.Relationships, Relationship{
							SourceQualified: result.Filepath,
							TargetName:      usePath,
							Kind:            "references",
							Line:            line,
						})
					}
					return
				}
			}
		case "scoped_use_list":
			// use foo::{bar, baz} — extract the prefix path
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
					usePath := gc.Content(source)
					if usePath != "" {
						line := int(node.StartPoint().Row) + 1
						result.Relationships = append(result.Relationships, Relationship{
							SourceQualified: result.Filepath,
							TargetName:      usePath,
							Kind:            "references",
							Line:            line,
						})
					}
					return
				}
			}
		}
	}
}

// extractRustCall extracts function calls as "calls" relationships.
func (p *Parser) extractRustCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	var callee string
	switch funcNode.Type() {
	case "identifier":
		callee = funcNode.Content(source)
	case "scoped_identifier":
		callee = funcNode.Content(source)
	case "field_expression":
		field := funcNode.ChildByFieldName("field")
		if field != nil {
			callee = field.Content(source)
		}
	}

	if callee == "" || rustBuiltinFuncs[callee] {
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

// extractRustImplInheritance extracts `impl Trait for Type` as "inherits" relationships.
func (p *Parser) extractRustImplInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	traitNode := node.ChildByFieldName("trait")
	if traitNode == nil {
		return // plain impl block, no trait
	}
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}

	traitName := traitNode.Content(source)
	typeName := typeNode.Content(source)

	// Strip generic parameters: Iterator<Item=T> → Iterator
	if idx := strings.Index(traitName, "<"); idx > 0 {
		traitName = traitName[:idx]
	}
	if idx := strings.Index(typeName, "<"); idx > 0 {
		typeName = typeName[:idx]
	}

	if traitName == "" || rustBuiltinTypes[traitName] {
		return
	}

	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: typeName,
		TargetName:      traitName,
		Kind:            "inherits",
		Line:            line,
	})
}

// extractRustTypeRef captures type_identifier nodes as "references" relationships.
func (p *Parser) extractRustTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || rustBuiltinTypes[typeName] {
		return
	}

	// Skip type definitions
	parent := node.Parent()
	if parent != nil {
		switch parent.Type() {
		case "struct_item", "enum_item", "trait_item", "type_item":
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return
			}
		case "impl_item":
			// Trait and type names are handled by extractRustImplInheritance
			traitNode := parent.ChildByFieldName("trait")
			if traitNode != nil && traitNode.StartByte() == node.StartByte() {
				return
			}
			typeNode := parent.ChildByFieldName("type")
			if typeNode != nil && typeNode.StartByte() == node.StartByte() {
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

var rustBuiltinFuncs = map[string]bool{
	"println": true, "print": true, "eprintln": true, "eprint": true,
	"format": true, "write": true, "writeln": true,
	"panic": true, "todo": true, "unimplemented": true, "unreachable": true,
	"assert": true, "assert_eq": true, "assert_ne": true,
	"debug_assert": true, "debug_assert_eq": true, "debug_assert_ne": true,
	"vec": true, "dbg": true, "cfg": true,
	"Some": true, "None": true, "Ok": true, "Err": true,
	"Box::new": true, "Rc::new": true, "Arc::new": true,
	"Vec::new": true, "String::new": true, "String::from": true,
	"HashMap::new": true, "HashSet::new": true,
	"Default::default": true,
}

var rustBuiltinTypes = map[string]bool{
	"bool": true, "char": true,
	"i8": true, "i16": true, "i32": true, "i64": true, "i128": true, "isize": true,
	"u8": true, "u16": true, "u32": true, "u64": true, "u128": true, "usize": true,
	"f32": true, "f64": true,
	"str": true, "String": true,
	"Vec": true, "Box": true, "Rc": true, "Arc": true, "Cell": true, "RefCell": true,
	"Option": true, "Result": true,
	"HashMap": true, "HashSet": true, "BTreeMap": true, "BTreeSet": true,
	"Self": true,
}
