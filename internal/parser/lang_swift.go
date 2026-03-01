package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractSwift extracts symbols and relationships from a Swift AST.
func (p *Parser) extractSwift(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkSwift(root, source, result, "")
	p.extractSwiftRelationships(root, source, result)
}

func (p *Parser) walkSwift(node *sitter.Node, source []byte, result *ParseResult, parentName string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.swiftFunction(child, source, parentName)
			sym.Language = "swift"
			result.Symbols = append(result.Symbols, sym)

		case "class_declaration":
			syms := p.swiftClassDecl(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "swift"
				result.Symbols = append(result.Symbols, sym)
			}

		case "protocol_declaration":
			syms := p.swiftProtocol(child, source)
			for _, sym := range syms {
				sym.Language = "swift"
				result.Symbols = append(result.Symbols, sym)
			}

		case "property_declaration":
			sym := p.swiftProperty(child, source, parentName)
			if sym.Name != "" {
				sym.Language = "swift"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

// swiftClassDecl handles class_declaration which covers class, struct, enum, and extension.
func (p *Parser) swiftClassDecl(node *sitter.Node, source []byte, parentName string) []Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	// Determine the variant from the first keyword child
	kind := "class"
	isExtension := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "struct":
			kind = "struct"
		case "enum":
			kind = "enum"
		case "extension":
			isExtension = true
		}
	}

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	var syms []Symbol

	// For extensions, don't emit the extension itself as a symbol — just extract its members
	if !isExtension {
		syms = append(syms, Symbol{
			Name:        name,
			Qualified:   qualified,
			Kind:        kind,
			Content:     node.Content(source),
			Signature:   extractFirstLine(node.Content(source)),
			LineStart:   int(node.StartPoint().Row) + 1,
			LineEnd:     int(node.EndPoint().Row) + 1,
			ParentClass: parentName,
			DocComment:  findPrecedingComment(node, source),
		})
	}

	// Extract members from body
	body := node.ChildByFieldName("body")
	if body != nil {
		p.swiftBody(body, source, name, &syms)
	}

	return syms
}

func (p *Parser) swiftBody(body *sitter.Node, source []byte, parentName string, syms *[]Symbol) {
	for i := 0; i < int(body.ChildCount()); i++ {
		child := body.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.swiftFunction(child, source, parentName)
			sym.Language = "swift"
			*syms = append(*syms, sym)

		case "class_declaration":
			nested := p.swiftClassDecl(child, source, parentName)
			for _, sym := range nested {
				sym.Language = "swift"
				*syms = append(*syms, sym)
			}

		case "property_declaration":
			sym := p.swiftProperty(child, source, parentName)
			if sym.Name != "" {
				sym.Language = "swift"
				*syms = append(*syms, sym)
			}
		}
	}
}

func (p *Parser) swiftFunction(node *sitter.Node, source []byte, parentName string) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	qualified := name
	kind := "function"
	if parentName != "" {
		qualified = parentName + "." + name
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
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}
}

func (p *Parser) swiftProtocol(node *sitter.Node, source []byte) []Symbol {
	nameNode := node.ChildByFieldName("name")
	protocolName := ""
	if nameNode != nil {
		protocolName = nameNode.Content(source)
	}

	syms := []Symbol{{
		Name:       protocolName,
		Qualified:  protocolName,
		Kind:       "class", // protocols map to class kind
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract method declarations from protocol body
	body := node.ChildByFieldName("body")
	if body != nil {
		for i := 0; i < int(body.ChildCount()); i++ {
			child := body.Child(i)
			if child.Type() == "protocol_function_declaration" {
				nameNode := child.ChildByFieldName("name")
				methodName := ""
				if nameNode != nil {
					methodName = nameNode.Content(source)
				}
				syms = append(syms, Symbol{
					Name:        methodName,
					Qualified:   protocolName + "." + methodName,
					Kind:        "method",
					Content:     child.Content(source),
					Signature:   extractFirstLine(child.Content(source)),
					LineStart:   int(child.StartPoint().Row) + 1,
					LineEnd:     int(child.EndPoint().Row) + 1,
					ParentClass: protocolName,
					DocComment:  findPrecedingComment(child, source),
				})
			}
		}
	}

	return syms
}

func (p *Parser) swiftProperty(node *sitter.Node, source []byte, parentName string) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	if name == "" {
		return Symbol{}
	}

	qualified := name
	kind := "function" // properties map to function kind for consistency
	if parentName != "" {
		qualified = parentName + "." + name
	}

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}
}
