package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractKotlin extracts symbols and relationships from a Kotlin AST.
func (p *Parser) extractKotlin(root *sitter.Node, source []byte, result *ParseResult) {
	p.walkKotlin(root, source, result, "")
	p.extractKotlinRelationships(root, source, result)
}

func (p *Parser) walkKotlin(node *sitter.Node, source []byte, result *ParseResult, parentName string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.kotlinFunction(child, source, parentName)
			sym.Language = "kotlin"
			result.Symbols = append(result.Symbols, sym)

		case "class_declaration":
			syms := p.kotlinClass(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "kotlin"
				result.Symbols = append(result.Symbols, sym)
			}

		case "object_declaration":
			syms := p.kotlinObject(child, source, parentName)
			for _, sym := range syms {
				sym.Language = "kotlin"
				result.Symbols = append(result.Symbols, sym)
			}

		case "property_declaration":
			sym := p.kotlinProperty(child, source, parentName)
			if sym.Name != "" {
				sym.Language = "kotlin"
				result.Symbols = append(result.Symbols, sym)
			}
		}
	}
}

func (p *Parser) kotlinFunction(node *sitter.Node, source []byte, parentName string) Symbol {
	name := ""
	receiver := ""

	// Walk children to find the function name and optional receiver type.
	// Extension functions have: user_type "." simple_identifier
	foundDot := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "user_type":
			// Potential extension receiver (appears before the dot)
			receiver = child.Content(source)
		case ".":
			foundDot = true
		case "simple_identifier":
			name = child.Content(source)
		}
	}

	if !foundDot {
		receiver = "" // Not an extension function
	}

	qualified := name
	kind := "function"

	// Check for suspend modifier
	sig := extractFirstLine(node.Content(source))

	if receiver != "" {
		// Extension function: Type.name
		qualified = receiver + "." + name
	}
	if parentName != "" {
		qualified = parentName + "." + name
		kind = "method"
	}

	return Symbol{
		Name:        name,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   sig,
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}
}

func (p *Parser) kotlinClass(node *sitter.Node, source []byte, parentName string) []Symbol {
	// Determine class variant from keyword children and modifiers
	className := ""
	kind := "class"
	isInterface := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		switch child.Type() {
		case "type_identifier":
			if className == "" {
				className = child.Content(source)
			}
		case "interface":
			isInterface = true
			kind = "class" // interfaces map to class kind
		case "enum":
			kind = "enum"
		case "modifiers":
			// Check for data, sealed modifiers
			for j := 0; j < int(child.ChildCount()); j++ {
				mod := child.Child(j)
				if mod.Type() == "class_modifier" {
					modContent := mod.Content(source)
					if modContent == "data" || modContent == "sealed" {
						// These are still "class" kind but the modifier is in the signature
					}
				}
			}
		}
	}

	if isInterface {
		kind = "class"
	}

	qualified := className
	if parentName != "" {
		qualified = parentName + "." + className
	}

	syms := []Symbol{{
		Name:        className,
		Qualified:   qualified,
		Kind:        kind,
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	// Extract members from class_body or enum_class_body
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "class_body" || child.Type() == "enum_class_body" {
			p.kotlinClassBody(child, source, className, &syms)
		}
	}

	return syms
}

func (p *Parser) kotlinClassBody(body *sitter.Node, source []byte, className string, syms *[]Symbol) {
	for i := 0; i < int(body.ChildCount()); i++ {
		child := body.Child(i)
		switch child.Type() {
		case "function_declaration":
			sym := p.kotlinFunction(child, source, className)
			sym.Language = "kotlin"
			*syms = append(*syms, sym)

		case "class_declaration":
			nested := p.kotlinClass(child, source, className)
			for _, sym := range nested {
				sym.Language = "kotlin"
				*syms = append(*syms, sym)
			}

		case "companion_object":
			companionSyms := p.kotlinCompanionObject(child, source, className)
			for _, sym := range companionSyms {
				sym.Language = "kotlin"
				*syms = append(*syms, sym)
			}

		case "object_declaration":
			nested := p.kotlinObject(child, source, className)
			for _, sym := range nested {
				sym.Language = "kotlin"
				*syms = append(*syms, sym)
			}

		case "property_declaration":
			sym := p.kotlinProperty(child, source, className)
			if sym.Name != "" {
				sym.Language = "kotlin"
				*syms = append(*syms, sym)
			}
		}
	}
}

func (p *Parser) kotlinObject(node *sitter.Node, source []byte, parentName string) []Symbol {
	objName := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_identifier" {
			objName = child.Content(source)
			break
		}
	}

	qualified := objName
	if parentName != "" {
		qualified = parentName + "." + objName
	}

	syms := []Symbol{{
		Name:        objName,
		Qualified:   qualified,
		Kind:        "class", // objects map to class kind
		Content:     node.Content(source),
		Signature:   extractFirstLine(node.Content(source)),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}}

	// Extract members
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "class_body" {
			p.kotlinClassBody(child, source, objName, &syms)
		}
	}

	return syms
}

func (p *Parser) kotlinCompanionObject(node *sitter.Node, source []byte, parentName string) []Symbol {
	// Companion objects may or may not have a name
	companionName := "Companion"
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "type_identifier" {
			companionName = child.Content(source)
			break
		}
	}

	qualified := companionName
	if parentName != "" {
		qualified = parentName + "." + companionName
	}

	var syms []Symbol

	// Extract methods from companion body, attributed to the parent class
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "class_body" {
			for j := 0; j < int(child.ChildCount()); j++ {
				bodyChild := child.Child(j)
				switch bodyChild.Type() {
				case "function_declaration":
					sym := p.kotlinFunction(bodyChild, source, parentName)
					sym.Language = "kotlin"
					// Mark as companion method in qualified name
					sym.Qualified = qualified + "." + sym.Name
					syms = append(syms, sym)

				case "property_declaration":
					sym := p.kotlinProperty(bodyChild, source, parentName)
					if sym.Name != "" {
						sym.Language = "kotlin"
						sym.Qualified = qualified + "." + sym.Name
						syms = append(syms, sym)
					}
				}
			}
		}
	}

	return syms
}

func (p *Parser) kotlinProperty(node *sitter.Node, source []byte, parentName string) Symbol {
	name := ""

	// Property name is in variable_declaration > simple_identifier
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "variable_declaration" {
			for j := 0; j < int(child.ChildCount()); j++ {
				vc := child.Child(j)
				if vc.Type() == "simple_identifier" {
					name = vc.Content(source)
					break
				}
			}
			break
		}
	}

	if name == "" {
		return Symbol{}
	}

	// Skip private-looking or trivial properties
	content := node.Content(source)
	if strings.HasPrefix(name, "_") {
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
		Content:     content,
		Signature:   extractFirstLine(content),
		LineStart:   int(node.StartPoint().Row) + 1,
		LineEnd:     int(node.EndPoint().Row) + 1,
		ParentClass: parentName,
		DocComment:  findPrecedingComment(node, source),
	}
}
