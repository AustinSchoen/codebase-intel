package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractPythonRelationships walks the Python AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractPythonRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	walkTree(root, source, result, func(node *sitter.Node, source []byte, result *ParseResult) {
		switch node.Type() {
		case "import_statement":
			p.extractPythonImport(node, source, result)
		case "import_from_statement":
			p.extractPythonFromImport(node, source, result)
		case "call":
			p.extractPythonCall(node, source, result)
		case "class_definition":
			p.extractPythonInheritance(node, source, result)
		case "type":
			p.extractPythonTypeRef(node, source, result)
		}
	})
}

func (p *Parser) extractPythonImport(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		var name string
		switch child.Type() {
		case "dotted_name":
			name = child.Content(source)
		case "aliased_import":
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				name = nameNode.Content(source)
			}
		}
		if name != "" {
			emitImportRelationship(node, name, result)
		}
	}
}

func (p *Parser) extractPythonFromImport(node *sitter.Node, source []byte, result *ParseResult) {
	moduleNode := node.ChildByFieldName("module_name")
	if moduleNode == nil {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "dotted_name" || child.Type() == "relative_import" {
				moduleNode = child
				break
			}
		}
	}
	if moduleNode != nil {
		emitImportRelationship(node, moduleNode.Content(source), result)
	}
}

func (p *Parser) extractPythonCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	callee := extractCalleeFromFieldName(funcNode, source, "attribute", "object", "attribute")
	emitCallRelationship(node, callee, pythonBuiltinFuncs, result)
}

func (p *Parser) extractPythonInheritance(node *sitter.Node, source []byte, result *ParseResult) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(source)

	superclasses := node.ChildByFieldName("superclasses")
	if superclasses == nil {
		return
	}

	for i := 0; i < int(superclasses.ChildCount()); i++ {
		child := superclasses.Child(i)
		var baseName string
		switch child.Type() {
		case "identifier", "attribute":
			baseName = child.Content(source)
		case "keyword_argument":
			continue
		}
		emitInheritsRelationship(child, className, baseName, pythonBuiltinTypes, result)
	}
}

func (p *Parser) extractPythonTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			emitTypeRefRelationship(child, child.Content(source), pythonBuiltinTypes, result)
		}
	}
}

var pythonBuiltinFuncs = map[string]bool{
	"print": true, "len": true, "range": true, "type": true,
	"int": true, "str": true, "float": true, "bool": true,
	"list": true, "dict": true, "set": true, "tuple": true,
	"isinstance": true, "issubclass": true, "hasattr": true, "getattr": true, "setattr": true, "delattr": true,
	"open": true, "input": true, "repr": true, "id": true,
	"abs": true, "min": true, "max": true, "sum": true,
	"sorted": true, "reversed": true, "enumerate": true, "zip": true, "map": true, "filter": true,
	"super": true, "property": true, "staticmethod": true, "classmethod": true,
	"iter": true, "next": true, "hash": true, "callable": true,
	"vars": true, "dir": true, "globals": true, "locals": true,
	"format": true, "chr": true, "ord": true, "hex": true, "oct": true, "bin": true,
	"round": true, "pow": true, "divmod": true,
	"any": true, "all": true,
	"breakpoint": true,
}

var pythonBuiltinTypes = map[string]bool{
	"int": true, "str": true, "float": true, "bool": true, "bytes": true, "bytearray": true,
	"list": true, "dict": true, "set": true, "tuple": true, "frozenset": true,
	"None": true, "object": true, "type": true,
	"complex": true, "range": true, "memoryview": true,
	"Any": true, "Optional": true, "Union": true, "List": true, "Dict": true,
	"Set": true, "Tuple": true, "Sequence": true, "Mapping": true, "Iterable": true,
	"Iterator": true, "Generator": true, "Callable": true, "Type": true,
	"ClassVar": true, "Final": true, "Literal": true,
}
