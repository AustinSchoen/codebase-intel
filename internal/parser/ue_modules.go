package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// UEModule represents a discovered Unreal Engine module from a .Build.cs file.
type UEModule struct {
	Name                string   `json:"name"`
	BuildCSPath         string   `json:"build_cs_path"`
	Dependencies        []string `json:"dependencies,omitempty"`
	PublicDependencies  []string `json:"public_dependencies,omitempty"`
	PrivateDependencies []string `json:"private_dependencies,omitempty"`
	PublicIncludePaths  []string `json:"public_include_paths,omitempty"`
	PrivateIncludePaths []string `json:"private_include_paths,omitempty"`
	ModuleDirectory     string   `json:"module_directory"`
}

// UEPlugin represents a discovered .uplugin or .uproject file.
type UEPlugin struct {
	Name        string           `json:"name"`
	FilePath    string           `json:"file_path"`
	Modules     []UEPluginModule `json:"modules,omitempty"`
	Plugins     []UEPluginRef    `json:"plugins,omitempty"`
	Description string           `json:"description,omitempty"`
	Category    string           `json:"category,omitempty"`
}

// UEPluginModule is a module reference in a .uplugin/.uproject file.
type UEPluginModule struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // Runtime, Editor, etc.
}

// UEPluginRef is a plugin dependency reference.
type UEPluginRef struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Regex patterns for Build.cs parsing
var (
	// Match module name from class declaration: public class ModuleName : ModuleRules
	reBuildCSClass = regexp.MustCompile(`class\s+(\w+)\s*:\s*ModuleRules`)

	// Match dependency additions: PublicDependencyModuleNames.AddRange(new string[] { "Core", "CoreUObject" })
	reAddRange = regexp.MustCompile(`(\w+DependencyModuleNames)\.AddRange\s*\(\s*new\s+string\s*\[\]\s*\{([^}]*)\}`)

	// Match single dependency additions: PublicDependencyModuleNames.Add("Core")
	reAddSingle = regexp.MustCompile(`(\w+DependencyModuleNames)\.Add\s*\(\s*"([^"]+)"\s*\)`)

	// Match include path additions
	reIncludePath = regexp.MustCompile(`(Public|Private)IncludePaths\.Add\s*\(\s*"([^"]+)"\s*\)`)

	// Match string literals in arrays
	reStringLiteral = regexp.MustCompile(`"([^"]+)"`)

	// Match JSON-like patterns in .uplugin/.uproject for modules
	rePluginModule  = regexp.MustCompile(`"Name"\s*:\s*"([^"]+)"`)
	rePluginType    = regexp.MustCompile(`"Type"\s*:\s*"([^"]+)"`)
	rePluginEnabled = regexp.MustCompile(`"Enabled"\s*:\s*(true|false)`)
	rePluginDesc    = regexp.MustCompile(`"Description"\s*:\s*"([^"]*)"`)
	rePluginCat     = regexp.MustCompile(`"Category"\s*:\s*"([^"]*)"`)
)

// DiscoverUEModules walks a directory tree and discovers UE modules from .Build.cs files.
func DiscoverUEModules(rootPath string) ([]UEModule, error) {
	var modules []UEModule

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".Build.cs") {
			mod, parseErr := ParseBuildCS(path)
			if parseErr == nil && mod != nil {
				relPath, _ := filepath.Rel(rootPath, path)
				mod.BuildCSPath = relPath
				mod.ModuleDirectory = filepath.Dir(relPath)
				modules = append(modules, *mod)
			}
		}
		return nil
	})

	return modules, err
}

// ParseBuildCS parses a single .Build.cs file and extracts module information.
func ParseBuildCS(path string) (*UEModule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text := string(data)
	mod := &UEModule{}

	// Extract module name
	classMatch := reBuildCSClass.FindStringSubmatch(text)
	if classMatch != nil {
		mod.Name = classMatch[1]
	} else {
		// Fall back to filename
		base := filepath.Base(path)
		mod.Name = strings.TrimSuffix(base, ".Build.cs")
	}

	// Extract AddRange dependencies
	addRangeMatches := reAddRange.FindAllStringSubmatch(text, -1)
	for _, match := range addRangeMatches {
		fieldName := match[1]
		arrayContent := match[2]
		deps := extractStringLiterals(arrayContent)

		switch {
		case strings.HasPrefix(fieldName, "PublicDependency"):
			mod.PublicDependencies = append(mod.PublicDependencies, deps...)
		case strings.HasPrefix(fieldName, "PrivateDependency"):
			mod.PrivateDependencies = append(mod.PrivateDependencies, deps...)
		default:
			mod.Dependencies = append(mod.Dependencies, deps...)
		}
	}

	// Extract Add single dependencies
	addSingleMatches := reAddSingle.FindAllStringSubmatch(text, -1)
	for _, match := range addSingleMatches {
		fieldName := match[1]
		dep := match[2]

		switch {
		case strings.HasPrefix(fieldName, "PublicDependency"):
			mod.PublicDependencies = append(mod.PublicDependencies, dep)
		case strings.HasPrefix(fieldName, "PrivateDependency"):
			mod.PrivateDependencies = append(mod.PrivateDependencies, dep)
		default:
			mod.Dependencies = append(mod.Dependencies, dep)
		}
	}

	// Merge all dependencies
	allDeps := make(map[string]bool)
	for _, d := range mod.PublicDependencies {
		allDeps[d] = true
	}
	for _, d := range mod.PrivateDependencies {
		allDeps[d] = true
	}
	for d := range allDeps {
		mod.Dependencies = append(mod.Dependencies, d)
	}
	if len(mod.Dependencies) == 0 {
		mod.Dependencies = nil
	}

	// Extract include paths
	includeMatches := reIncludePath.FindAllStringSubmatch(text, -1)
	for _, match := range includeMatches {
		visibility := match[1]
		path := match[2]
		if visibility == "Public" {
			mod.PublicIncludePaths = append(mod.PublicIncludePaths, path)
		} else {
			mod.PrivateIncludePaths = append(mod.PrivateIncludePaths, path)
		}
	}

	return mod, nil
}

// DiscoverUEPlugins finds .uplugin and .uproject files and parses them.
func DiscoverUEPlugins(rootPath string) ([]UEPlugin, error) {
	var plugins []UEPlugin

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".uplugin" || ext == ".uproject" {
			plugin, parseErr := ParsePluginFile(path)
			if parseErr == nil && plugin != nil {
				relPath, _ := filepath.Rel(rootPath, path)
				plugin.FilePath = relPath
				plugins = append(plugins, *plugin)
			}
		}
		return nil
	})

	return plugins, err
}

// ParsePluginFile parses a .uplugin or .uproject JSON file.
// Uses regex rather than full JSON parsing to handle UE's sometimes non-standard JSON.
func ParsePluginFile(path string) (*UEPlugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text := string(data)
	base := filepath.Base(path)
	plugin := &UEPlugin{
		Name: strings.TrimSuffix(base, filepath.Ext(base)),
	}

	// Extract description
	if match := rePluginDesc.FindStringSubmatch(text); match != nil {
		plugin.Description = match[1]
	}

	// Extract category
	if match := rePluginCat.FindStringSubmatch(text); match != nil {
		plugin.Category = match[1]
	}

	// Extract modules - look for "Modules" array sections
	modulesIdx := strings.Index(text, `"Modules"`)
	if modulesIdx >= 0 {
		// Find the array content
		arrStart := strings.Index(text[modulesIdx:], "[")
		if arrStart >= 0 {
			arrStart += modulesIdx
			depth := 0
			arrEnd := arrStart
			for i := arrStart; i < len(text); i++ {
				if text[i] == '[' {
					depth++
				} else if text[i] == ']' {
					depth--
					if depth == 0 {
						arrEnd = i + 1
						break
					}
				}
			}
			modulesText := text[arrStart:arrEnd]

			// Split by { } blocks
			for _, block := range splitJSONBlocks(modulesText) {
				mod := UEPluginModule{}
				if match := rePluginModule.FindStringSubmatch(block); match != nil {
					mod.Name = match[1]
				}
				if match := rePluginType.FindStringSubmatch(block); match != nil {
					mod.Type = match[1]
				}
				if mod.Name != "" {
					plugin.Modules = append(plugin.Modules, mod)
				}
			}
		}
	}

	// Extract plugin dependencies
	pluginsIdx := strings.Index(text, `"Plugins"`)
	if pluginsIdx >= 0 {
		arrStart := strings.Index(text[pluginsIdx:], "[")
		if arrStart >= 0 {
			arrStart += pluginsIdx
			depth := 0
			arrEnd := arrStart
			for i := arrStart; i < len(text); i++ {
				if text[i] == '[' {
					depth++
				} else if text[i] == ']' {
					depth--
					if depth == 0 {
						arrEnd = i + 1
						break
					}
				}
			}
			pluginsText := text[arrStart:arrEnd]

			for _, block := range splitJSONBlocks(pluginsText) {
				ref := UEPluginRef{}
				if match := rePluginModule.FindStringSubmatch(block); match != nil {
					ref.Name = match[1]
				}
				if match := rePluginEnabled.FindStringSubmatch(block); match != nil {
					ref.Enabled = match[1] == "true"
				}
				if ref.Name != "" {
					plugin.Plugins = append(plugin.Plugins, ref)
				}
			}
		}
	}

	return plugin, nil
}

// extractStringLiterals finds all quoted strings in a text.
func extractStringLiterals(s string) []string {
	matches := reStringLiteral.FindAllStringSubmatch(s, -1)
	var result []string
	for _, m := range matches {
		result = append(result, m[1])
	}
	return result
}

// splitJSONBlocks splits a JSON array string into individual object blocks.
func splitJSONBlocks(s string) []string {
	var blocks []string
	depth := 0
	start := -1

	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 && start >= 0 {
				blocks = append(blocks, s[start:i+1])
				start = -1
			}
		}
	}
	return blocks
}
