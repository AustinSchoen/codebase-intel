package indexer

import (
	"context"
	"crypto/sha256"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// computeASTHash parses source with tree-sitter and produces a structural hash
// that ignores comments and whitespace. Renaming a variable changes the hash,
// but adding a comment does not.
func computeASTHash(source []byte, lang string) (string, error) {
	var language *sitter.Language
	switch lang {
	case "go":
		language = golang.GetLanguage()
	case "python", "py":
		language = python.GetLanguage()
	case "typescript", "ts":
		language = typescript.GetLanguage()
	default:
		return "", fmt.Errorf("unsupported language for AST hash: %s", lang)
	}

	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(language)

	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil {
		return "", fmt.Errorf("parsing for AST hash: %w", err)
	}
	defer tree.Close()

	h := sha256.New()
	walkASTForHash(tree.RootNode(), source, h)
	return fmt.Sprintf("%x", h.Sum(nil)[:16]), nil
}

// walkASTForHash recursively walks the AST, hashing node types and identifier
// names while skipping comment nodes and pure whitespace.
func walkASTForHash(node *sitter.Node, source []byte, h interface{ Write([]byte) (int, error) }) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	// Skip comment nodes entirely
	switch nodeType {
	case "comment", "block_comment", "line_comment":
		return
	}

	// Write the node type to the hash
	h.Write([]byte(nodeType))
	h.Write([]byte{0})

	// For leaf nodes (identifiers, literals, keywords), hash their content
	if node.ChildCount() == 0 {
		content := node.Content(source)
		h.Write([]byte(content))
		h.Write([]byte{1})
	}

	// Recurse into children
	for i := 0; i < int(node.ChildCount()); i++ {
		walkASTForHash(node.Child(i), source, h)
	}
}
