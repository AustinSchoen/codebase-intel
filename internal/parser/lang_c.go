package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractC extracts symbols and relationships from a C AST.
func (p *Parser) extractC(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkC(root, source, result)
	p.extractCRelationships(root, source, result)
}

// extractCpp extracts symbols and relationships from a C++ AST.
func (p *Parser) extractCpp(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkCpp(root, source, result, "")
	p.extractCppRelationships(root, source, result)
}

func (p *Parser) walkC(node *sitter.Node, source []byte, result *ParseResult) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_definition":
			sym := p.cFunction(child, source)
			result.Symbols = append(result.Symbols, sym)

		case "struct_specifier":
			sym := p.cStruct(child, source, "struct")
			if sym.Name != "" {
				result.Symbols = append(result.Symbols, sym)
			}

		case "enum_specifier":
			sym := p.cStruct(child, source, "enum")
			if sym.Name != "" {
				result.Symbols = append(result.Symbols, sym)
			}

		case "declaration":
			// Check for typedef
			for j := 0; j < int(child.ChildCount()); j++ {
				grandchild := child.Child(j)
				if grandchild.Type() == "struct_specifier" || grandchild.Type() == "enum_specifier" {
					kind := "struct"
					if grandchild.Type() == "enum_specifier" {
						kind = "enum"
					}
					sym := p.cStruct(grandchild, source, kind)
					if sym.Name != "" {
						result.Symbols = append(result.Symbols, sym)
					}
				}
			}
		}
	}
}

func (p *Parser) walkCpp(node *sitter.Node, source []byte, result *ParseResult, parentClass string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_definition":
			sym := p.cppFunction(child, source, parentClass)
			result.Symbols = append(result.Symbols, sym)

		case "class_specifier":
			syms := p.cppClass(child, source)
			result.Symbols = append(result.Symbols, syms...)

		case "struct_specifier":
			sym := p.cStruct(child, source, "struct")
			if sym.Name != "" {
				sym.Language = "cpp"
				result.Symbols = append(result.Symbols, sym)
			}

		case "enum_specifier":
			sym := p.cStruct(child, source, "enum")
			if sym.Name != "" {
				sym.Language = "cpp"
				result.Symbols = append(result.Symbols, sym)
			}

		case "namespace_definition":
			// Recurse into namespace body
			bodyNode := child.ChildByFieldName("body")
			if bodyNode != nil {
				p.walkCpp(bodyNode, source, result, parentClass)
			}

		case "declaration":
			for j := 0; j < int(child.ChildCount()); j++ {
				grandchild := child.Child(j)
				switch grandchild.Type() {
				case "class_specifier":
					syms := p.cppClass(grandchild, source)
					result.Symbols = append(result.Symbols, syms...)
				case "struct_specifier":
					sym := p.cStruct(grandchild, source, "struct")
					if sym.Name != "" {
						sym.Language = "cpp"
						result.Symbols = append(result.Symbols, sym)
					}
				case "enum_specifier":
					sym := p.cStruct(grandchild, source, "enum")
					if sym.Name != "" {
						sym.Language = "cpp"
						result.Symbols = append(result.Symbols, sym)
					}
				}
			}

		case "template_declaration":
			// Recurse to find the templated item
			p.walkCpp(child, source, result, parentClass)
		}
	}
}

func (p *Parser) cFunction(node *sitter.Node, source []byte) Symbol {
	declarator := node.ChildByFieldName("declarator")
	name := extractDeclaratorName(declarator, source)

	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "function",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		Language:   "c",
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) cppFunction(node *sitter.Node, source []byte, parentClass string) Symbol {
	declarator := node.ChildByFieldName("declarator")
	name := extractDeclaratorName(declarator, source)

	qualified := name
	kind := "function"
	if parentClass != "" {
		qualified = parentClass + "::" + name
		kind = "method"
	}

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentClass,
		Language:    "cpp",
		DocComment:  findPrecedingComment(node, source),
	}
}

func (p *Parser) cStruct(node *sitter.Node, source []byte, kind string) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	lang := "c"
	if kind == "class" {
		lang = "cpp"
	}

	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       kind,
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		Language:   lang,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) cppClass(node *sitter.Node, source []byte) []Symbol {
	nameNode := node.ChildByFieldName("name")
	className := ""
	if nameNode != nil {
		className = nameNode.Content(source)
	}

	syms := []Symbol{{
		Name:       className,
		Qualified:  className,
		Kind:       "class",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		Language:   "cpp",
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract methods from class body
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			switch child.Type() {
			case "function_definition":
				sym := p.cppFunction(child, source, className)
				syms = append(syms, sym)
			case "declaration":
				// Check for method declarations (no body)
				// Skip for now - we only capture definitions with bodies
			}
		}
	}

	return syms
}

// extractDeclaratorName walks a declarator tree to find the function/variable name.
func extractDeclaratorName(node *sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "identifier":
		return node.Content(source)
	case "field_identifier":
		return node.Content(source)
	case "qualified_identifier":
		return node.Content(source)
	case "destructor_name":
		return node.Content(source)
	}

	// Try the declarator field
	declarator := node.ChildByFieldName("declarator")
	if declarator != nil {
		return extractDeclaratorName(declarator, source)
	}

	// Walk children looking for an identifier
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" || child.Type() == "field_identifier" || child.Type() == "qualified_identifier" {
			return child.Content(source)
		}
	}

	return ""
}
