package chunker

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AustinSchoen/codebase-intel/internal/parser"
)

// Chunk represents a code chunk ready for embedding and storage.
type Chunk struct {
	ID            string
	Filepath      string
	Module        string
	QualifiedName string
	Kind          string
	Language      string
	Content       string
	ContextPrefix string
	LineStart     int
	LineEnd       int
	ParentClass   string
	Dependencies  []string
}

// Chunker breaks parsed symbols into embeddable chunks.
type Chunker struct {
	maxLines     int
	overlapLines int
}

// New creates a Chunker with the given line limits.
func New(maxLines, overlapLines int) *Chunker {
	if maxLines <= 0 {
		maxLines = 200
	}
	if overlapLines <= 0 {
		overlapLines = 20
	}
	return &Chunker{maxLines: maxLines, overlapLines: overlapLines}
}

// ChunkFile takes a parse result and produces chunks from its symbols.
func (c *Chunker) ChunkFile(result *parser.ParseResult) []Chunk {
	if result == nil || len(result.Symbols) == 0 {
		return nil
	}

	module := inferModule(result.Filepath)
	var chunks []Chunk

	// Build a map of class/struct names for context prefixes
	classDecls := make(map[string]string) // className -> signature/first line
	for _, sym := range result.Symbols {
		if sym.Kind == "class" || sym.Kind == "struct" {
			classDecls[sym.Name] = extractDeclarationHeader(sym.Content)
		}
	}

	for _, sym := range result.Symbols {
		lines := strings.Split(sym.Content, "\n")
		lineCount := len(lines)

		prefix := buildContextPrefix(result.Filepath, module, sym, classDecls)

		if lineCount <= c.maxLines {
			// Single chunk
			chunk := Chunk{
				Filepath:      result.Filepath,
				Module:        module,
				QualifiedName: sym.Qualified,
				Kind:          sym.Kind,
				Language:      sym.Language,
				Content:       sym.Content,
				ContextPrefix: prefix,
				LineStart:     sym.LineStart,
				LineEnd:       sym.LineEnd,
				ParentClass:   sym.ParentClass,
			}
			chunk.ID = chunkID(result.Filepath, sym.Qualified, chunk.LineStart)
			chunks = append(chunks, chunk)
		} else {
			// Split large symbols into overlapping chunks
			splitChunks := c.splitContent(lines, sym, result.Filepath, module, prefix)
			chunks = append(chunks, splitChunks...)
		}
	}

	return chunks
}

// splitContent breaks large content into overlapping chunks.
func (c *Chunker) splitContent(lines []string, sym parser.Symbol, fp, module, prefix string) []Chunk {
	var chunks []Chunk
	stride := c.maxLines - c.overlapLines
	if stride <= 0 {
		stride = c.maxLines
	}

	for start := 0; start < len(lines); start += stride {
		end := start + c.maxLines
		if end > len(lines) {
			end = len(lines)
		}

		content := strings.Join(lines[start:end], "\n")
		lineStart := sym.LineStart + start
		lineEnd := sym.LineStart + end - 1

		chunk := Chunk{
			Filepath:      fp,
			Module:        module,
			QualifiedName: sym.Qualified,
			Kind:          sym.Kind,
			Language:      sym.Language,
			Content:       content,
			ContextPrefix: prefix,
			LineStart:     lineStart,
			LineEnd:       lineEnd,
			ParentClass:   sym.ParentClass,
		}
		chunk.ID = chunkID(fp, sym.Qualified, lineStart)
		chunks = append(chunks, chunk)

		if end >= len(lines) {
			break
		}
	}

	return chunks
}

// buildContextPrefix creates the context string prepended before embedding.
func buildContextPrefix(fp, module string, sym parser.Symbol, classDecls map[string]string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("// File: %s", fp))
	if module != "" {
		parts = append(parts, fmt.Sprintf("// Module: %s", module))
	}
	if sym.ParentClass != "" {
		if decl, ok := classDecls[sym.ParentClass]; ok {
			parts = append(parts, fmt.Sprintf("// Class: %s", decl))
		} else {
			parts = append(parts, fmt.Sprintf("// Parent: %s", sym.ParentClass))
		}
	}
	if sym.Kind != "" {
		parts = append(parts, fmt.Sprintf("// Kind: %s", sym.Kind))
	}
	return strings.Join(parts, "\n")
}

// extractDeclarationHeader gets the first line of a class/struct declaration.
func extractDeclarationHeader(content string) string {
	idx := strings.Index(content, "\n")
	if idx >= 0 {
		return strings.TrimSpace(content[:idx])
	}
	return strings.TrimSpace(content)
}

// inferModule guesses the module from a file path.
func inferModule(fp string) string {
	dir := filepath.Dir(fp)
	parts := strings.Split(dir, string(filepath.Separator))

	// Use the first meaningful directory component
	for _, p := range parts {
		if p == "." || p == "" || p == "internal" || p == "pkg" || p == "src" || p == "lib" {
			continue
		}
		return p
	}
	if len(parts) > 0 && parts[0] != "." {
		return parts[0]
	}
	return ""
}

// chunkID produces a deterministic ID from filepath + symbol + line.
func chunkID(filepath, qualifiedName string, lineStart int) string {
	data := fmt.Sprintf("%s:%s:%d", filepath, qualifiedName, lineStart)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:16])
}

// ContentHash computes a SHA-256 hash of file content.
func ContentHash(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h)
}

// EstimateTokens gives a rough token count (~4 chars per token).
func EstimateTokens(s string) int {
	return len(s) / 4
}
