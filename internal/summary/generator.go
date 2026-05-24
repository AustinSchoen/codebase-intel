package summary

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/storage/postgres"
)

// Generator creates LLM-powered summaries for modules, classes, and subsystems.
type Generator struct {
	apiKey     string
	model      string
	store      *postgres.Store
	codebaseID string
	logger     *log.Logger
	client     *http.Client
}

// NewGenerator creates a summary generator.
func NewGenerator(apiKey, model string, store *postgres.Store, codebaseID string, logger *log.Logger) *Generator {
	if model == "" {
		model = "claude-sonnet-4-5-20250929"
	}
	return &Generator{
		apiKey:     apiKey,
		model:      model,
		store:      store,
		codebaseID: codebaseID,
		logger:     logger,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	Messages  []anthropicMsg `json:"messages"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is a simplified response from the Anthropic Messages API.
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// callLLM sends a prompt to the Anthropic API and returns the response text.
func (g *Generator) callLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	// Build the user message with system context
	fullPrompt := userPrompt
	if systemPrompt != "" {
		fullPrompt = systemPrompt + "\n\n" + userPrompt
	}

	reqBody := anthropicRequest{
		Model:     g.model,
		MaxTokens: 2048,
		Messages: []anthropicMsg{
			{Role: "user", Content: fullPrompt},
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	var text strings.Builder
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}

	return text.String(), nil
}

// GenerateModuleSummary creates or updates a summary for a module.
func (g *Generator) GenerateModuleSummary(ctx context.Context, module string) (*postgres.Summary, error) {
	// Get module files and symbols
	files, err := g.store.GetModuleFiles(ctx, g.codebaseID, module)
	if err != nil {
		return nil, fmt.Errorf("get module files: %w", err)
	}

	symbols, err := g.store.GetModuleSymbolSummary(ctx, g.codebaseID, module, 50)
	if err != nil {
		return nil, fmt.Errorf("get module symbols: %w", err)
	}

	// Compute source hash from file list + symbol names
	sourceHash := computeSourceHash(files, symbols)

	// Check if existing summary is still valid
	existing, err := g.store.GetSummary(ctx, g.codebaseID, module, "module")
	if err != nil {
		return nil, fmt.Errorf("check existing summary: %w", err)
	}
	if existing != nil && existing.SourceHash == sourceHash {
		return existing, nil // still valid
	}

	// Build prompt
	var promptBuilder strings.Builder
	promptBuilder.WriteString(fmt.Sprintf("Generate a concise architectural summary (200-300 words) for the '%s' module.\n\n", module))
	promptBuilder.WriteString("Files in this module:\n")
	for _, f := range files {
		promptBuilder.WriteString(fmt.Sprintf("- %s\n", f))
	}
	promptBuilder.WriteString("\nKey symbols:\n")
	for _, sym := range symbols {
		line := fmt.Sprintf("- %s (%s)", sym.Qualified, sym.Kind)
		if sym.Signature != "" {
			line += ": " + sym.Signature
		}
		promptBuilder.WriteString(line + "\n")
	}
	promptBuilder.WriteString("\nProvide:\n1. A summary of the module's purpose and architecture\n2. Key classes/types (as a JSON array of strings)\n3. Dependencies (as a JSON array of strings)\n\nFormat your response as JSON:\n```json\n{\"summary\": \"...\", \"key_classes\": [\"Class1\", \"Class2\"], \"dependencies\": [\"Dep1\"]}\n```")

	systemPrompt := "You are a code architecture analyst. Analyze the provided module structure and generate concise, accurate summaries. Always respond with valid JSON."

	response, err := g.callLLM(ctx, systemPrompt, promptBuilder.String())
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Parse the response
	summaryData := parseSummaryResponse(response)

	// Get module stats for additional context
	modules, err := g.store.ListModules(ctx, g.codebaseID, module)
	if err != nil {
		g.logger.Printf("warning: could not get module stats: %v", err)
	}

	keyClassesJSON, _ := json.Marshal(summaryData.KeyClasses)
	depsJSON, _ := json.Marshal(summaryData.Dependencies)

	id := summaryID(g.codebaseID, module, "module")
	sum := postgres.Summary{
		ID:           id,
		CodebaseID:   g.codebaseID,
		Scope:        module,
		Level:        "module",
		SummaryText:  summaryData.Summary,
		KeyClasses:   string(keyClassesJSON),
		Dependencies: string(depsJSON),
		SourceHash:   sourceHash,
	}

	if err := g.store.UpsertSummary(ctx, sum); err != nil {
		return nil, fmt.Errorf("store summary: %w", err)
	}

	// Attach stats if available
	if len(modules) > 0 {
		// Stats are available but stored separately in module_stats view
	}

	return &sum, nil
}

// GenerateSubsystemExplanation generates an architectural narrative about a topic.
// Unlike module summaries, this is always generated on-demand using relevant context.
func (g *Generator) GenerateSubsystemExplanation(ctx context.Context, topic string, relevantChunks []string) (string, []string, []string, error) {
	var promptBuilder strings.Builder
	promptBuilder.WriteString(fmt.Sprintf("Explain the architectural design and implementation of '%s' in this codebase.\n\n", topic))

	if len(relevantChunks) > 0 {
		promptBuilder.WriteString("Relevant code context:\n")
		for i, chunk := range relevantChunks {
			if i >= 10 {
				break // limit context
			}
			promptBuilder.WriteString(fmt.Sprintf("---\n%s\n", chunk))
		}
	}

	promptBuilder.WriteString("\nProvide:\n1. A detailed architectural explanation (300-500 words)\n2. Key classes involved (as a JSON array of strings)\n3. Related modules (as a JSON array of strings)\n\nFormat your response as JSON:\n```json\n{\"explanation\": \"...\", \"key_classes\": [\"Class1\"], \"related_modules\": [\"Module1\"]}\n```")

	systemPrompt := "You are a code architecture expert. Provide clear, detailed explanations of how subsystems and features work, including data flow, key extension points, and common patterns. Always respond with valid JSON."

	response, err := g.callLLM(ctx, systemPrompt, promptBuilder.String())
	if err != nil {
		return "", nil, nil, fmt.Errorf("LLM call: %w", err)
	}

	data := parseExplanationResponse(response)
	return data.Explanation, data.KeyClasses, data.RelatedModules, nil
}

// GenerateModuleSummaries generates summaries for all modules that need updating.
func (g *Generator) GenerateModuleSummaries(ctx context.Context, changedModules map[string]bool) error {
	if len(changedModules) == 0 {
		// Get all modules
		modules, err := g.store.ListModules(ctx, g.codebaseID, "")
		if err != nil {
			return fmt.Errorf("list modules: %w", err)
		}
		for _, m := range modules {
			changedModules[m.Module] = true
		}
	}

	for module := range changedModules {
		if module == "" {
			continue
		}
		g.logger.Printf("generating summary for module: %s", module)
		if _, err := g.GenerateModuleSummary(ctx, module); err != nil {
			g.logger.Printf("warning: failed to generate summary for %s: %v", module, err)
			continue
		}
	}
	return nil
}

// summaryResponse holds the parsed LLM response for a module summary.
type summaryResponse struct {
	Summary      string   `json:"summary"`
	KeyClasses   []string `json:"key_classes"`
	Dependencies []string `json:"dependencies"`
}

// explanationResponse holds the parsed LLM response for a subsystem explanation.
type explanationResponse struct {
	Explanation    string   `json:"explanation"`
	KeyClasses     []string `json:"key_classes"`
	RelatedModules []string `json:"related_modules"`
}

// parseSummaryResponse extracts structured data from the LLM response.
func parseSummaryResponse(response string) summaryResponse {
	var data summaryResponse

	// Try to extract JSON from the response
	jsonStr := extractJSON(response)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &data); err == nil {
			return data
		}
	}

	// Fallback: use the whole response as the summary
	data.Summary = response
	return data
}

// parseExplanationResponse extracts structured data from the LLM response.
func parseExplanationResponse(response string) explanationResponse {
	var data explanationResponse

	jsonStr := extractJSON(response)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &data); err == nil {
			return data
		}
	}

	data.Explanation = response
	return data
}

// extractJSON attempts to find a JSON object in a response that may contain markdown code blocks.
func extractJSON(s string) string {
	// Try to find JSON in a code block
	start := strings.Index(s, "```json")
	if start >= 0 {
		start += len("```json")
		end := strings.Index(s[start:], "```")
		if end >= 0 {
			return strings.TrimSpace(s[start : start+end])
		}
	}

	// Try to find JSON in a generic code block
	start = strings.Index(s, "```")
	if start >= 0 {
		start += len("```")
		// Skip language identifier on first line
		nl := strings.Index(s[start:], "\n")
		if nl >= 0 {
			start += nl + 1
		}
		end := strings.Index(s[start:], "```")
		if end >= 0 {
			return strings.TrimSpace(s[start : start+end])
		}
	}

	// Try to find a raw JSON object
	start = strings.Index(s, "{")
	if start >= 0 {
		// Find matching closing brace
		depth := 0
		for i := start; i < len(s); i++ {
			if s[i] == '{' {
				depth++
			} else if s[i] == '}' {
				depth--
				if depth == 0 {
					return s[start : i+1]
				}
			}
		}
	}

	return ""
}

// computeSourceHash creates a hash from the file list and symbol names.
func computeSourceHash(files []string, symbols []postgres.Symbol) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	for _, s := range symbols {
		h.Write([]byte(s.Qualified))
		h.Write([]byte(s.Kind))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:16])
}

// summaryID creates a deterministic ID for a summary.
func summaryID(codebaseID, scope, level string) string {
	data := fmt.Sprintf("%s:%s:%s", codebaseID, scope, level)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:16])
}
