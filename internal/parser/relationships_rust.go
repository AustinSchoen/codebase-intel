package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractRustRelationships walks the Rust AST to find use declarations,
// function calls, trait implementations, and type references.
func (p *Parser) extractRustRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
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
	})
}

func (p *Parser) extractRustUse(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "scoped_identifier", "identifier", "use_wildcard":
			emitImportRelationship(node, child.Content(source), result)
			return
		case "use_as_clause":
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
					emitImportRelationship(node, gc.Content(source), result)
					return
				}
			}
		case "scoped_use_list":
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
					emitImportRelationship(node, gc.Content(source), result)
					return
				}
			}
		}
	}
}

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
	emitCallRelationship(node, callee, rustBuiltinFuncs, result)
}

func (p *Parser) extractRustImplInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	traitNode := node.ChildByFieldName("trait")
	if traitNode == nil {
		return
	}
	typeNode := node.ChildByFieldName("type")
	if typeNode == nil {
		return
	}

	traitName := traitNode.Content(source)
	typeName := typeNode.Content(source)

	// Strip generic parameters
	if idx := strings.Index(traitName, "<"); idx > 0 {
		traitName = traitName[:idx]
	}
	if idx := strings.Index(typeName, "<"); idx > 0 {
		typeName = typeName[:idx]
	}

	emitInheritsRelationship(node, typeName, traitName, rustBuiltinTypes, result)
}

func (p *Parser) extractRustTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	typeName := node.Content(source)
	if typeName == "" || rustBuiltinTypes[typeName] {
		return
	}
	if isTypeDefinition(node, "struct_item", "enum_item", "trait_item", "type_item") {
		return
	}
	// Skip impl trait/type names (handled by extractRustImplInheritance)
	parent := node.Parent()
	if parent != nil && parent.Type() == "impl_item" {
		traitNode := parent.ChildByFieldName("trait")
		if traitNode != nil && traitNode.StartByte() == node.StartByte() {
			return
		}
		typeNode := parent.ChildByFieldName("type")
		if typeNode != nil && typeNode.StartByte() == node.StartByte() {
			return
		}
	}
	emitTypeRefRelationship(node, typeName, rustBuiltinTypes, result)
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
