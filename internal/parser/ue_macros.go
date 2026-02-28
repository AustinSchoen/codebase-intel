package parser

import (
	"encoding/json"
	"regexp"
	"strings"
)

// UEMacroMeta represents parsed UE reflection metadata for a class/struct.
type UEMacroMeta struct {
	MacroType  string            `json:"macro_type,omitempty"`  // UCLASS, USTRUCT, UENUM
	Specifiers []string          `json:"specifiers,omitempty"`  // BlueprintType, Blueprintable, etc.
	Meta       map[string]string `json:"meta,omitempty"`        // DisplayName, Category, etc.
	APIMacro   string            `json:"api_macro,omitempty"`   // ENGINE_API, etc.
	Properties []UEProperty      `json:"properties,omitempty"`
	Functions  []UEFunction      `json:"functions,omitempty"`
}

// UEProperty represents a parsed UPROPERTY declaration.
type UEProperty struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Specifiers []string `json:"specifiers,omitempty"`
	Category   string   `json:"category,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// UEFunction represents a parsed UFUNCTION declaration.
type UEFunction struct {
	Name       string   `json:"name"`
	Specifiers []string `json:"specifiers,omitempty"`
	Category   string   `json:"category,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// Regex patterns for UE macros
var (
	// Match UCLASS(...), USTRUCT(...), UENUM(...)
	reClassMacro = regexp.MustCompile(`(?m)^[ \t]*(UCLASS|USTRUCT|UENUM)\s*\(([^)]*)\)\s*\n`)

	// Match class/struct declaration after a macro
	reClassDecl = regexp.MustCompile(`(?m)^[ \t]*(?:class|struct)\s+(?:([A-Z_]+_API)\s+)?(\w+)(?:\s*:\s*public\s+(\w+))?`)

	// Match UPROPERTY(...)
	rePropMacro = regexp.MustCompile(`(?m)^[ \t]*UPROPERTY\s*\(([^)]*)\)\s*\n[ \t]*(\w[\w:<>,\s\*&]*?)\s+(\w+)\s*[;={]`)

	// Match UFUNCTION(...)
	reFuncMacro = regexp.MustCompile(`(?m)^[ \t]*UFUNCTION\s*\(([^)]*)\)\s*\n[ \t]*(?:virtual\s+)?(\w[\w:<>,\s\*&]*?)\s+(\w+)\s*\(`)
)

// ParseUEMacros extracts UE reflection metadata from C++ source code.
// Returns nil if no UE macros are found.
func ParseUEMacros(source []byte) map[string]*UEMacroMeta {
	text := string(source)
	results := make(map[string]*UEMacroMeta)

	// Find class/struct-level macros (UCLASS, USTRUCT, UENUM)
	classMatches := reClassMacro.FindAllStringSubmatchIndex(text, -1)
	for _, match := range classMatches {
		macroType := text[match[2]:match[3]]
		specifierStr := text[match[4]:match[5]]
		macroEnd := match[1]

		// Find the class/struct declaration after this macro
		remaining := text[macroEnd:]
		declMatch := reClassDecl.FindStringSubmatch(remaining)
		if declMatch == nil {
			continue
		}

		className := declMatch[2]
		meta := &UEMacroMeta{
			MacroType: macroType,
			APIMacro:  declMatch[1],
		}
		meta.Specifiers, meta.Meta = parseSpecifiers(specifierStr)

		results[className] = meta
	}

	// Find UPROPERTY declarations and attach to nearest class
	propMatches := rePropMacro.FindAllStringSubmatchIndex(text, -1)
	for _, match := range propMatches {
		specifierStr := text[match[2]:match[3]]
		propType := strings.TrimSpace(text[match[4]:match[5]])
		propName := text[match[6]:match[7]]

		specifiers, metaTags := parseSpecifiers(specifierStr)
		prop := UEProperty{
			Name:       propName,
			Type:       propType,
			Specifiers: specifiers,
			Category:   metaTags["Category"],
			Meta:       metaTags,
		}
		delete(prop.Meta, "Category")
		if len(prop.Meta) == 0 {
			prop.Meta = nil
		}

		// Attach to the nearest class above this property
		propLine := match[0]
		ownerClass := findOwnerClass(text, propLine, results)
		if ownerClass != "" {
			results[ownerClass].Properties = append(results[ownerClass].Properties, prop)
		}
	}

	// Find UFUNCTION declarations and attach to nearest class
	funcMatches := reFuncMacro.FindAllStringSubmatchIndex(text, -1)
	for _, match := range funcMatches {
		specifierStr := text[match[2]:match[3]]
		funcName := text[match[6]:match[7]]

		specifiers, metaTags := parseSpecifiers(specifierStr)
		fn := UEFunction{
			Name:       funcName,
			Specifiers: specifiers,
			Category:   metaTags["Category"],
			Meta:       metaTags,
		}
		delete(fn.Meta, "Category")
		if len(fn.Meta) == 0 {
			fn.Meta = nil
		}

		funcLine := match[0]
		ownerClass := findOwnerClass(text, funcLine, results)
		if ownerClass != "" {
			results[ownerClass].Functions = append(results[ownerClass].Functions, fn)
		}
	}

	if len(results) == 0 {
		return nil
	}
	return results
}

// parseSpecifiers splits a UE macro specifier string into simple specifiers and meta key=value pairs.
// Example: "BlueprintType, Blueprintable, meta=(DisplayName=\"My Actor\", Category=\"Test\")"
func parseSpecifiers(s string) ([]string, map[string]string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	var specifiers []string
	meta := make(map[string]string)

	// Check for meta=(...) block
	metaIdx := strings.Index(s, "meta=(")
	if metaIdx >= 0 {
		// Parse the part before meta
		before := strings.TrimSpace(s[:metaIdx])
		before = strings.TrimRight(before, ", ")

		// Find matching closing paren for meta
		metaStart := metaIdx + len("meta=(")
		depth := 1
		metaEnd := metaStart
		for metaEnd < len(s) && depth > 0 {
			if s[metaEnd] == '(' {
				depth++
			} else if s[metaEnd] == ')' {
				depth--
			}
			if depth > 0 {
				metaEnd++
			}
		}
		metaContent := s[metaStart:metaEnd]

		// Parse meta key=value pairs
		for _, pair := range splitTopLevel(metaContent) {
			pair = strings.TrimSpace(pair)
			if eqIdx := strings.Index(pair, "="); eqIdx >= 0 {
				key := strings.TrimSpace(pair[:eqIdx])
				val := strings.TrimSpace(pair[eqIdx+1:])
				val = strings.Trim(val, "\"")
				meta[key] = val
			}
		}

		// Parse the part after meta
		after := ""
		if metaEnd+1 < len(s) {
			after = strings.TrimSpace(s[metaEnd+1:])
			after = strings.TrimLeft(after, ", ")
		}

		// Combine before + after for specifiers
		combined := before
		if after != "" {
			if combined != "" {
				combined += ", " + after
			} else {
				combined = after
			}
		}
		s = combined
	}

	// Parse remaining specifiers (also handle Category="X" as a specifier-level key=value)
	for _, part := range splitTopLevel(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if eqIdx := strings.Index(part, "="); eqIdx >= 0 {
			key := strings.TrimSpace(part[:eqIdx])
			val := strings.TrimSpace(part[eqIdx+1:])
			val = strings.Trim(val, "\"")
			meta[key] = val
		} else {
			specifiers = append(specifiers, part)
		}
	}

	if len(meta) == 0 {
		meta = nil
	}
	return specifiers, meta
}

// splitTopLevel splits a comma-separated string, respecting parentheses and quotes.
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	inQuote := false
	start := 0

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inQuote = !inQuote
		}
		if !inQuote {
			if ch == '(' {
				depth++
			} else if ch == ')' {
				depth--
			} else if ch == ',' && depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if start < len(s) {
		parts = append(parts, strings.TrimSpace(s[start:]))
	}
	return parts
}

// findOwnerClass finds the class that "owns" a position in the source.
// It finds the UCLASS/USTRUCT that appears most recently before the given byte offset.
func findOwnerClass(text string, pos int, classes map[string]*UEMacroMeta) string {
	bestClass := ""
	bestPos := -1

	for className := range classes {
		// Find where this class is declared
		classIdx := strings.Index(text, "class "+className)
		if classIdx < 0 {
			classIdx = strings.Index(text, "struct "+className)
		}
		if classIdx >= 0 && classIdx < pos && classIdx > bestPos {
			bestClass = className
			bestPos = classIdx
		}
	}
	return bestClass
}

// UEMetaJSON converts a UEMacroMeta to a JSON string for storage.
func UEMetaJSON(meta *UEMacroMeta) string {
	if meta == nil {
		return ""
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(data)
}

// IsCppFile returns true if the file extension indicates a C++ source or header.
func IsCppFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".cpp") ||
		strings.HasSuffix(lower, ".h") ||
		strings.HasSuffix(lower, ".hpp") ||
		strings.HasSuffix(lower, ".cc") ||
		strings.HasSuffix(lower, ".cxx") ||
		strings.HasSuffix(lower, ".hxx")
}
