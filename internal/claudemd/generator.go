package claudemd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
)

// Generate creates CLAUDE.md content from indexed codebase data.
func Generate(ctx context.Context, codebaseID, codebasePath string, store *postgres.Store, topSymbols int) (string, error) {
	if topSymbols <= 0 {
		topSymbols = 20
	}

	var md strings.Builder

	// Header
	md.WriteString(fmt.Sprintf("# %s\n\n", codebaseID))

	// Module summaries
	modules, err := store.ListModules(ctx, codebaseID, "")
	if err != nil {
		return "", fmt.Errorf("list modules: %w", err)
	}

	if len(modules) > 0 {
		md.WriteString("## Modules\n\n")
		md.WriteString("| Module | Classes | Functions | Structs | Files | ~LOC |\n")
		md.WriteString("|--------|---------|-----------|---------|-------|------|\n")
		for _, m := range modules {
			md.WriteString(fmt.Sprintf("| %s | %d | %d | %d | %d | %d |\n",
				m.Module, m.ClassCount, m.FunctionCount, m.StructCount, m.FileCount, m.EstimatedLOC))
		}
		md.WriteString("\n")

		// Include module summaries if available
		summaries, err := store.ListSummaries(ctx, codebaseID, "module")
		if err == nil && len(summaries) > 0 {
			md.WriteString("### Module Descriptions\n\n")
			for _, s := range summaries {
				md.WriteString(fmt.Sprintf("**%s**: %s\n\n", s.Scope, truncate(s.SummaryText, 300)))
			}
		}
	}

	// Important symbols
	importantSymbols, err := store.GetTopSymbolsByImportance(ctx, codebaseID, topSymbols)
	if err != nil {
		// Non-fatal: materialized view may not exist yet
		importantSymbols = nil
	}

	if len(importantSymbols) > 0 {
		md.WriteString("## Key Symbols\n\n")
		md.WriteString("Most-referenced symbols in the codebase:\n\n")
		for _, sym := range importantSymbols {
			line := fmt.Sprintf("- **%s** (%s)", sym.Qualified, sym.Kind)
			if sym.Module != "" {
				line += fmt.Sprintf(" — module: %s", sym.Module)
			}
			if sym.Signature != "" {
				line += fmt.Sprintf("\n  `%s`", sym.Signature)
			}
			md.WriteString(line + "\n")
		}
		md.WriteString("\n")
	}

	// File structure (from module list)
	if len(modules) > 0 {
		md.WriteString("## File Structure\n\n")
		md.WriteString("```\n")
		for _, m := range modules {
			md.WriteString(fmt.Sprintf("%s/  (%d files)\n", m.Module, m.FileCount))
		}
		md.WriteString("```\n\n")
	}

	// Dependencies from summaries
	summaries, err := store.ListSummaries(ctx, codebaseID, "module")
	if err == nil && len(summaries) > 0 {
		hasDeps := false
		for _, s := range summaries {
			var deps []string
			json.Unmarshal([]byte(s.Dependencies), &deps)
			if len(deps) > 0 {
				if !hasDeps {
					md.WriteString("## Module Dependencies\n\n")
					hasDeps = true
				}
				md.WriteString(fmt.Sprintf("- **%s** depends on: %s\n", s.Scope, strings.Join(deps, ", ")))
			}
		}
		if hasDeps {
			md.WriteString("\n")
		}
	}

	return md.String(), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
