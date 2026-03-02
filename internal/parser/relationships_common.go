package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// findEnclosingSymbol returns the qualified name of the narrowest symbol
// whose line range contains the given line.
func findEnclosingSymbol(line int, symbols []Symbol) string {
	var bestQualified string
	bestRange := int(^uint(0) >> 1) // max int
	for _, sym := range symbols {
		if line >= sym.LineStart && line <= sym.LineEnd {
			r := sym.LineEnd - sym.LineStart
			if r < bestRange {
				bestQualified = sym.Qualified
				bestRange = r
			}
		}
	}
	return bestQualified
}

// nodeHandler is called for each AST node during tree walking.
// It returns true if the handler processed the node (walk still recurses into children).
type nodeHandler func(node *sitter.Node, source []byte, result *ParseResult)

// walkTree recursively walks the AST, calling handler for each node,
// then recursing into children.
func walkTree(node *sitter.Node, source []byte, result *ParseResult, handler nodeHandler) {
	handler(node, source, result)
	for i := 0; i < int(node.ChildCount()); i++ {
		walkTree(node.Child(i), source, result, handler)
	}
}

// emitCallRelationship checks if callee is a builtin, finds the enclosing symbol,
// and appends a "calls" relationship. Returns true if a relationship was emitted.
func emitCallRelationship(node *sitter.Node, callee string, builtinFuncs map[string]bool, result *ParseResult) bool {
	if callee == "" || builtinFuncs[callee] {
		return false
	}

	line := int(node.StartPoint().Row) + 1
	enclosing := findEnclosingSymbol(line, result.Symbols)
	if enclosing == "" {
		return false
	}

	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: enclosing,
		TargetName:      callee,
		Kind:            "calls",
		Line:            line,
	})
	return true
}

// emitTypeRefRelationship checks if typeName is a builtin, finds the enclosing symbol,
// and appends a "references" relationship. Returns true if a relationship was emitted.
func emitTypeRefRelationship(node *sitter.Node, typeName string, builtinTypes map[string]bool, result *ParseResult) bool {
	if typeName == "" || builtinTypes[typeName] {
		return false
	}

	line := int(node.StartPoint().Row) + 1
	enclosing := findEnclosingSymbol(line, result.Symbols)
	if enclosing == "" || enclosing == typeName {
		return false
	}

	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: enclosing,
		TargetName:      typeName,
		Kind:            "references",
		Line:            line,
	})
	return true
}

// emitImportRelationship appends a file-level "references" relationship for an import.
func emitImportRelationship(node *sitter.Node, importPath string, result *ParseResult) {
	if importPath == "" {
		return
	}
	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: result.Filepath,
		TargetName:      importPath,
		Kind:            "references",
		Line:            line,
	})
}

// emitInheritsRelationship appends an "inherits" relationship.
func emitInheritsRelationship(node *sitter.Node, sourceQualified, targetName string, builtinTypes map[string]bool, result *ParseResult) bool {
	if targetName == "" || builtinTypes[targetName] {
		return false
	}
	line := int(node.StartPoint().Row) + 1
	result.Relationships = append(result.Relationships, Relationship{
		SourceQualified: sourceQualified,
		TargetName:      targetName,
		Kind:            "inherits",
		Line:            line,
	})
	return true
}

// extractCalleeFromFieldName extracts the callee name from a call_expression node
// using field-based "function" child lookup. Handles identifier and member/selector patterns.
// memberTypes maps the member expression node type to its field names: {"object": ..., "property": ...}
func extractCalleeFromFieldName(funcNode *sitter.Node, source []byte, memberType string, objField, propField string) string {
	if funcNode == nil {
		return ""
	}
	switch funcNode.Type() {
	case "identifier", "simple_identifier":
		return funcNode.Content(source)
	case memberType:
		prop := funcNode.ChildByFieldName(propField)
		if prop != nil {
			obj := funcNode.ChildByFieldName(objField)
			if obj != nil && (obj.Type() == "identifier" || obj.Type() == "simple_identifier") {
				return obj.Content(source) + "." + prop.Content(source)
			}
			return prop.Content(source)
		}
	}
	return ""
}

// isTypeDefinition checks if a type_identifier node is the name being defined
// (not referenced) by checking if it matches the parent's "name" field.
func isTypeDefinition(node *sitter.Node, parentTypes ...string) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}
	for _, pt := range parentTypes {
		if parent.Type() == pt {
			nameNode := parent.ChildByFieldName("name")
			if nameNode != nil && nameNode.StartByte() == node.StartByte() {
				return true
			}
		}
	}
	return false
}

// extractNavigationCallee resolves callee from navigation_expression patterns
// used by Kotlin and Swift (obj.method style with simple_identifier children).
// suffixTypes lists additional child types to recurse into (e.g. "navigation_suffix" for Swift).
func extractNavigationCallee(node *sitter.Node, source []byte, idTypes []string, suffixTypes []string) string {
	var ids []string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		for _, t := range idTypes {
			if child.Type() == t {
				ids = append(ids, child.Content(source))
				break
			}
		}
		for _, st := range suffixTypes {
			if child.Type() == st {
				for j := 0; j < int(child.ChildCount()); j++ {
					gc := child.Child(j)
					for _, t := range idTypes {
						if gc.Type() == t {
							ids = append(ids, gc.Content(source))
							break
						}
					}
				}
			}
		}
	}
	if len(ids) >= 2 {
		return ids[len(ids)-2] + "." + ids[len(ids)-1]
	}
	if len(ids) == 1 {
		return ids[0]
	}
	return ""
}
