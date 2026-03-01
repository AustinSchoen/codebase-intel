package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractPythonRelationships walks the Python AST to find imports, function calls,
// class inheritance, and type references.
func (p *Parser) extractPythonRelationships(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkPythonForRefs(root, source, result)
}

func (p *Parser) walkPythonForRefs(node *sitter.Node, source []byte, result *ParseResult) {
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

	for i := 0; i < int(node.ChildCount()); i++ {
		p.walkPythonForRefs(node.Child(i), source, result)
	}
}

// extractPythonImport handles `import foo` and `import foo.bar` statements.
func (p *Parser) extractPythonImport(node *sitter.Node, source []byte, result *ParseResult) {
	line := int(node.StartPoint().Row) + 1
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
			result.Relationships = append(result.Relationships, Relationship{
				SourceQualified: result.Filepath,
				TargetName:      name,
				Kind:            "references",
				Line:            line,
			})
		}
	}
}

// extractPythonFromImport handles `from foo import bar` statements.
func (p *Parser) extractPythonFromImport(node *sitter.Node, source []byte, result *ParseResult) {
	moduleNode := node.ChildByFieldName("module_name")
	if moduleNode == nil {
		// Fallback: look for dotted_name or relative_import child
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child.Type() == "dotted_name" || child.Type() == "relative_import" {
				moduleNode = child
				break
			}
		}
	}
	if moduleNode == nil {
		return
	}

	moduleName := moduleNode.Content(source)
	if moduleName == "" {
		return
	}

	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: result.Filepath,
		TargetName:      moduleName,
		Kind:            "references",
		Line:            line,
	})
}

// extractPythonCall extracts function/method calls as "calls" relationships.
func (p *Parser) extractPythonCall(node *sitter.Node, source []byte, result *ParseResult) {
	funcNode := node.ChildByFieldName("function")
	if funcNode == nil {
		return
	}

	var callee string
	switch funcNode.Type() {
	case "identifier":
		callee = funcNode.Content(source)
	case "attribute":
		attrNode := funcNode.ChildByFieldName("attribute")
		if attrNode != nil {
			objNode := funcNode.ChildByFieldName("object")
			if objNode != nil && objNode.Type() == "identifier" {
				callee = objNode.Content(source) + "." + attrNode.Content(source)
			} else {
				callee = attrNode.Content(source)
			}
		}
	}

	if callee == "" || pythonBuiltinFuncs[callee] {
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

// extractPythonInheritance extracts class inheritance from superclasses.
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
		case "identifier":
			baseName = child.Content(source)
		case "attribute":
			baseName = child.Content(source)
		case "keyword_argument":
			// metaclass=ABCMeta etc. — skip
			continue
		}
		if baseName == "" || pythonBuiltinTypes[baseName] {
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

// extractPythonTypeRef captures type annotations as "references" relationships.
func (p *Parser) extractPythonTypeRef(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() != "identifier" {
			continue
		}
		typeName := child.Content(source)
		if typeName == "" || pythonBuiltinTypes[typeName] {
			continue
		}

		line := int(child.StartPoint().Row) + 1
		enclosing := findEnclosingSymbol(line, result.Symbols)
		if enclosing == "" || enclosing == typeName {
			continue
		}

		result.Relationships = append(result.Relationships, Relationship{
			SourceQualified: enclosing,
			TargetName:      typeName,
			Kind:            "references",
			Line:            line,
		})
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
	// typing module common types
	"Any": true, "Optional": true, "Union": true, "List": true, "Dict": true,
	"Set": true, "Tuple": true, "Sequence": true, "Mapping": true, "Iterable": true,
	"Iterator": true, "Generator": true, "Callable": true, "Type": true,
	"ClassVar": true, "Final": true, "Literal": true,
}
