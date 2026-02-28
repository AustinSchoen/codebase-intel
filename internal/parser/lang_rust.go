package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractRust extracts symbols from a Rust AST.
func (p *Parser) extractRust(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkRust(root, source, result, "")
}

func (p *Parser) walkRust(node *sitter.Node, source []byte, result *ParseResult, parentName string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_item":
			sym := p.rustFunction(child, source, parentName)
			sym.Language = "rust"
			result.Symbols = append(result.Symbols, sym)

		case "struct_item":
			sym := p.rustStruct(child, source)
			sym.Language = "rust"
			result.Symbols = append(result.Symbols, sym)

		case "enum_item":
			sym := p.rustEnum(child, source)
			sym.Language = "rust"
			result.Symbols = append(result.Symbols, sym)

		case "trait_item":
			syms := p.rustTrait(child, source)
			for _, sym := range syms {
				sym.Language = "rust"
				result.Symbols = append(result.Symbols, sym)
			}

		case "impl_item":
			syms := p.rustImpl(child, source)
			for _, sym := range syms {
				sym.Language = "rust"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

func (p *Parser) rustFunction(node *sitter.Node, source []byte, parentName string) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	qualified := name
	kind := "function"
	if parentName != "" {
		qualified = parentName + "::" + name
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

func (p *Parser) rustStruct(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "struct",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) rustEnum(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}

	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "enum",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) rustTrait(node *sitter.Node, source []byte) []Symbol {
	nameNode := node.ChildByFieldName("name")
	traitName := ""
	if nameNode != nil {
		traitName = nameNode.Content(source)
	}

	syms := []Symbol{{
		Name:       traitName,
		Qualified:  traitName,
		Kind:       "class", // traits map to class kind
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract methods from trait body
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			if child.Type() == "function_item" {
				sym := p.rustFunction(child, source, traitName)
				syms = append(syms, sym)
			}
		}
	}

	return syms
}

func (p *Parser) rustImpl(node *sitter.Node, source []byte) []Symbol {
	// Get the type being implemented
	typeNode := node.ChildByFieldName("type")
	typeName := ""
	if typeNode != nil {
		typeName = typeNode.Content(source)
	}

	var syms []Symbol
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			if child.Type() == "function_item" {
				sym := p.rustFunction(child, source, typeName)
				syms = append(syms, sym)
			}
		}
	}

	return syms
}
