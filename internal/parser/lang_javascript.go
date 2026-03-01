package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// extractJavaScript extracts symbols and relationships from a JavaScript AST.
// This handles .js and .jsx files. TypeScript (.ts/.tsx) uses the existing extractTypeScript.
func (p *Parser) extractJavaScript(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkJS(root, source, result, "")
	p.extractJSRelationships(root, source, result)
}

func (p *Parser) walkJS(node *sitter.Node, source []byte, result *ParseResult, parentClass string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.jsFunction(child, source)
			sym.Language = "javascript"
			result.Symbols = append(result.Symbols, sym)

		case "class_declaration":
			syms := p.jsClass(child, source)
			for j := range syms {
				syms[j].Language = "javascript"
			}
			result.Symbols = append(result.Symbols, syms...)

		case "export_statement":
			p.walkJS(child, source, result, parentClass)

		case "lexical_declaration", "variable_declaration":
			syms := p.jsLexicalDecl(child, source)
			for j := range syms {
				syms[j].Language = "javascript"
			}
			result.Symbols = append(result.Symbols, syms...)
		}
	}
}

func (p *Parser) jsFunction(node *sitter.Node, source []byte) Symbol {
	nameNode := node.ChildByFieldName("name")
	name := ""
	if nameNode != nil {
		name = nameNode.Content(source)
	}
	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "function",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

func (p *Parser) jsClass(node *sitter.Node, source []byte) []Symbol {
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
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}}

	// Extract methods
	bodyNode := node.ChildByFieldName("body")
	if bodyNode != nil {
		for i := 0; i < int(bodyNode.ChildCount()); i++ {
			child := bodyNode.Child(i)
			if child.Type() == "method_definition" {
				nameNode := child.ChildByFieldName("name")
				methodName := ""
				if nameNode != nil {
					methodName = nameNode.Content(source)
				}
				syms = append(syms, Symbol{
					Name:        methodName,
					Qualified:   className + "." + methodName,
					Kind:        "method",
					Content:     child.Content(source),
					Signature:   extractFirstLine(child.Content(source)),
					LineStart:   int(child.StartPoint().Row) + 1,
					LineEnd:     int(child.EndPoint().Row) + 1,
					ParentClass: className,
					DocComment:  findPrecedingComment(child, source),
				})
			}
		}
	}

	return syms
}

func (p *Parser) jsLexicalDecl(node *sitter.Node, source []byte) []Symbol {
	var syms []Symbol
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "variable_declarator" {
			nameNode := child.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			name := nameNode.Content(source)
			valueNode := child.ChildByFieldName("value")
			if valueNode != nil && valueNode.Type() == "arrow_function" {
				syms = append(syms, Symbol{
					Name:       name,
					Qualified:  name,
					Kind:       "function",
					Content:    node.Content(source),
					Signature:  extractFirstLine(node.Content(source)),
					LineStart:  int(node.StartPoint().Row) + 1,
					LineEnd:    int(node.EndPoint().Row) + 1,
					DocComment: findPrecedingComment(node, source),
				})
			}
		}
	}
	return syms
}
