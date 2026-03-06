package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractDart extracts symbols and relationships from a Dart AST.
func (p *Parser) extractDart(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkDart(root, source, result, "")
	p.extractDartRelationships(root, source, result)
}

func (p *Parser) walkDart(node *sitter.Node, source []byte, result *ParseResult, parentName string) {
	childCount := int(node.ChildCount())
	for i := 0; i < childCount; i++ {
		child := node.Child(i)
		switch child.Type() {
		case "class_definition":
			syms := p.dartClass(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "mixin_declaration":
			syms := p.dartMixin(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "extension_declaration":
			syms := p.dartExtension(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "extension_type_declaration":
			syms := p.dartExtensionType(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "enum_declaration":
			syms := p.dartEnum(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "function_signature":
			// At program level, function_signature is followed by function_body sibling
			sym := p.dartFunction(child, source, parentName)
			// Extend range to include the function_body if present
			if i+1 < childCount {
				next := node.Child(i + 1)
				if next.Type() == "function_body" {
					sym.LineEnd = int(next.EndPoint().Row) + 1
					i++ // skip the function_body
				}
			}
			sym.Language = "dart"
			result.Symbols = append(result.Symbols, sym)

		case "getter_signature":
			sym := p.dartGetterSetter(child, source, parentName, "getter")
			if i+1 < childCount {
				next := node.Child(i + 1)
				if next.Type() == "function_body" {
					sym.LineEnd = int(next.EndPoint().Row) + 1
					i++
				}
			}
			if sym.Name != "" {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "type_alias":
			sym := p.dartTypeAlias(child, source)
			if sym.Name != "" {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "static_final_declaration_list":
			sym := p.dartStaticFinalDecl(child, source, parentName)
			if sym.Name != "" {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}

		case "initialized_identifier_list":
			sym := p.dartInitializedIdList(child, source, parentName)
			if sym.Name != "" {
				sym.Language = "dart"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

// dartClass handles class_definition nodes.
func (p *Parser) dartClass(node *sitter.Node, source []byte, parentName string) []Symbol {
	name := dartFindChildByType(node, "identifier", source)

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	syms := []Symbol{{
		Name:        name,
		Qualified:   qualified,
		Kind:        "class",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	p.dartClassBody(node, source, name, &syms)

	return syms
}

// dartMixin handles mixin_declaration nodes.
func (p *Parser) dartMixin(node *sitter.Node, source []byte, parentName string) []Symbol {
	name := dartFindChildByType(node, "identifier", source)

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	syms := []Symbol{{
		Name:        name,
		Qualified:   qualified,
		Kind:        "class",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	p.dartClassBody(node, source, name, &syms)

	return syms
}

// dartExtension handles extension_declaration nodes.
func (p *Parser) dartExtension(node *sitter.Node, source []byte, parentName string) []Symbol {
	name := dartFindChildByType(node, "identifier", source)
	if name == "" {
		name = "<extension>"
	}

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	syms := []Symbol{{
		Name:        name,
		Qualified:   qualified,
		Kind:        "class",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	p.dartClassBody(node, source, name, &syms)

	return syms
}

// dartExtensionType handles extension_type_declaration nodes.
func (p *Parser) dartExtensionType(node *sitter.Node, source []byte, parentName string) []Symbol {
	name := dartFindChildByType(node, "identifier", source)

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	syms := []Symbol{{
		Name:        name,
		Qualified:   qualified,
		Kind:        "class",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	p.dartClassBody(node, source, name, &syms)

	return syms
}

// dartEnum handles enum_declaration nodes.
func (p *Parser) dartEnum(node *sitter.Node, source []byte, parentName string) []Symbol {
	name := dartFindChildByType(node, "identifier", source)

	qualified := name
	if parentName != "" {
		qualified = parentName + "." + name
	}

	return []Symbol{{
		Name:        name,
		Qualified:   qualified,
		Kind:        "enum",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}
}

// dartFunction handles function_signature nodes.
func (p *Parser) dartFunction(node *sitter.Node, source []byte, parentName string) Symbol {
	name := dartFindChildByType(node, "identifier", source)

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

// dartTypeAlias handles type_alias nodes (typedef).
func (p *Parser) dartTypeAlias(node *sitter.Node, source []byte) Symbol {
	// type_alias: typedef type_identifier = ...
	name := dartFindChildByType(node, "type_identifier", source)
	if name == "" {
		name = dartFindChildByType(node, "identifier", source)
	}
	if name == "" {
		return Symbol{}
	}

	return Symbol{
		Name:       name,
		Qualified:  name,
		Kind:       "class",
		Content:    node.Content(source),
		Signature:  extractFirstLine(node.Content(source)),
		LineStart:  int(node.StartPoint().Row) + 1,
		LineEnd:    int(node.EndPoint().Row) + 1,
		DocComment: findPrecedingComment(node, source),
	}
}

// dartStaticFinalDecl handles static_final_declaration_list (const vars).
func (p *Parser) dartStaticFinalDecl(node *sitter.Node, source []byte, parentName string) Symbol {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "static_final_declaration" {
			name := dartFindChildByType(child, "identifier", source)
			if name == "" || strings.HasPrefix(name, "_") {
				return Symbol{}
			}
			qualified := name
			if parentName != "" {
				qualified = parentName + "." + name
			}
			return Symbol{
				Name:        name,
				Qualified:   qualified,
				Kind:        "function",
				Content:     node.Content(source),
				Signature:   extractFirstLine(node.Content(source)),
				LineStart:   int(node.StartPoint().Row) + 1,
				LineEnd:     int(node.EndPoint().Row) + 1,
				ParentClass: parentName,
				DocComment:  findPrecedingComment(node, source),
			}
		}
	}
	return Symbol{}
}

// dartInitializedIdList handles initialized_identifier_list (var declarations).
func (p *Parser) dartInitializedIdList(node *sitter.Node, source []byte, parentName string) Symbol {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "initialized_identifier" {
			name := dartFindChildByType(child, "identifier", source)
			if name == "" || strings.HasPrefix(name, "_") {
				return Symbol{}
			}
			qualified := name
			if parentName != "" {
				qualified = parentName + "." + name
			}
			return Symbol{
				Name:        name,
				Qualified:   qualified,
				Kind:        "function",
				Content:     node.Content(source),
				Signature:   extractFirstLine(node.Content(source)),
				LineStart:   int(node.StartPoint().Row) + 1,
				LineEnd:     int(node.EndPoint().Row) + 1,
				ParentClass: parentName,
				DocComment:  findPrecedingComment(node, source),
			}
		}
	}
	return Symbol{}
}

// dartClassBody extracts members from a class/mixin/extension body.
func (p *Parser) dartClassBody(node *sitter.Node, source []byte, className string, syms *[]Symbol) {
	childCount := int(node.ChildCount())
	for i := 0; i < childCount; i++ {
		child := node.Child(i)
		switch child.Type() {
		case "class_body", "extension_body", "mixin_body", "enum_body":
			p.dartClassBody(child, source, className, syms)

		case "declaration":
			p.dartDeclarationInBody(child, source, className, syms)

		case "method_signature":
			sym := p.dartMethodSig(child, source, className)
			if sym.Name != "" {
				// Extend range to include function_body if next sibling
				if i+1 < childCount {
					next := node.Child(i + 1)
					if next.Type() == "function_body" {
						sym.LineEnd = int(next.EndPoint().Row) + 1
						i++
					}
				}
				sym.Language = "dart"
				*syms = append(*syms, sym)
			}

		case "getter_signature":
			sym := p.dartGetterSetter(child, source, className, "getter")
			if sym.Name != "" {
				sym.Language = "dart"
				*syms = append(*syms, sym)
			}

		case "setter_signature":
			sym := p.dartGetterSetter(child, source, className, "setter")
			if sym.Name != "" {
				sym.Language = "dart"
				*syms = append(*syms, sym)
			}

		case "constructor_signature":
			sym := p.dartConstructor(child, source, className)
			if sym.Name != "" {
				sym.Language = "dart"
				*syms = append(*syms, sym)
			}
		}
	}
}

// dartDeclarationInBody handles 'declaration' nodes inside class bodies.
func (p *Parser) dartDeclarationInBody(node *sitter.Node, source []byte, className string, syms *[]Symbol) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "constructor_signature":
			sym := p.dartConstructor(child, source, className)
			if sym.Name != "" {
				sym.Language = "dart"
				*syms = append(*syms, sym)
			}
		}
	}
}

// dartMethodSig handles method_signature nodes inside class bodies.
func (p *Parser) dartMethodSig(node *sitter.Node, source []byte, className string) Symbol {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_signature":
			sym := p.dartFunction(child, source, className)
			sym.Content = node.Content(source)
			sym.LineStart = int(node.StartPoint().Row) + 1
			sym.LineEnd = int(node.EndPoint().Row) + 1
			return sym
		case "getter_signature":
			return p.dartGetterSetter(child, source, className, "getter")
		case "setter_signature":
			return p.dartGetterSetter(child, source, className, "setter")
		case "constructor_signature":
			return p.dartConstructor(child, source, className)
		}
	}
	return Symbol{}
}

// dartGetterSetter handles getter_signature and setter_signature nodes.
func (p *Parser) dartGetterSetter(node *sitter.Node, source []byte, className string, _ string) Symbol {
	name := dartFindChildByType(node, "identifier", source)
	if name == "" {
		return Symbol{}
	}

	qualified := name
	kind := "method"
	if className != "" {
		qualified = className + "." + name
	} else {
		kind = "function"
	}

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: className,
		DocComment:  findPrecedingComment(node, source),
	}
}

// dartConstructor handles constructor_signature nodes.
func (p *Parser) dartConstructor(node *sitter.Node, source []byte, className string) Symbol {
	// Named constructors: identifier . identifier (params)
	// Default constructors: identifier (params)
	name := className
	foundClassName := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "identifier" {
			content := child.Content(source)
			if !foundClassName {
				foundClassName = true
				continue // skip the class name identifier
			}
			// This is the named constructor suffix
			name = className + "." + content
			break
		}
	}

	return Symbol{
		Name:        name,
		Qualified:   name,
		Kind:        "method",
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: className,
		DocComment:  findPrecedingComment(node, source),
	}
}

// dartFindChildByType returns the content of the first child with the given type.
func dartFindChildByType(node *sitter.Node, childType string, source []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == childType {
			return child.Content(source)
		}
	}
	return ""
}
