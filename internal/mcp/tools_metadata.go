package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/claudemd"
)

func (s *Server) toolListCodebases(ctx context.Context) (interface{}, error) {
	if s.store == nil {
		return nil, fmt.Errorf("postgres not available")
	}

	codebases, err := s.store.ListCodebases(ctx)
	if err != nil {
		return nil, fmt.Errorf("list codebases: %w", err)
	}

	type indexerInfo struct {
		Connected bool   `json:"connected"`
		NodeID    string `json:"node_id,omitempty"`
		LastSeen  string `json:"last_seen,omitempty"`
		Status    string `json:"status,omitempty"`
	}

	type codebaseResult struct {
		ID          string       `json:"id"`
		DisplayName string       `json:"display_name"`
		RootPath    string       `json:"root_path,omitempty"`
		FileCount   int64        `json:"file_count"`
		SymbolCount int64        `json:"symbol_count"`
		Indexer     *indexerInfo `json:"indexer,omitempty"`
	}

	var out []codebaseResult
	for _, cb := range codebases {
		cr := codebaseResult{
			ID:          cb.ID,
			DisplayName: cb.DisplayName,
			RootPath:    cb.RootPath,
			FileCount:   cb.FileCount,
			SymbolCount: cb.SymbolCount,
		}

		// Add indexer connection status if manager is available
		if s.indexerMgr != nil {
			node := s.indexerMgr.GetNodeForCodebase(cb.ID)
			if node != nil {
				cr.Indexer = &indexerInfo{
					Connected: true,
					NodeID:    node.NodeID,
					LastSeen:  node.LastSeen.Format(time.RFC3339),
					Status:    node.Status,
				}
			} else {
				cr.Indexer = &indexerInfo{Connected: false}
			}
		}

		out = append(out, cr)
	}

	return map[string]interface{}{"codebases": out}, nil
}

func (s *Server) toolGetSymbol(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}
	if params.Kind == "" {
		params.Kind = "any"
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	symbols, err := s.store.FuzzySearchSymbols(ctx, codebase, params.Name, params.Kind, 5)
	if err != nil {
		return nil, fmt.Errorf("symbol lookup: %w", err), codebase
	}

	type symbolResult struct {
		Name       string `json:"name"`
		Qualified  string `json:"qualified"`
		Kind       string `json:"kind"`
		Filepath   string `json:"filepath"`
		LineStart  int    `json:"line_start"`
		LineEnd    int    `json:"line_end"`
		Module     string `json:"module"`
		Signature  string `json:"signature,omitempty"`
		DocComment string `json:"doc_comment,omitempty"`
	}

	var out []symbolResult
	for _, sym := range symbols {
		out = append(out, symbolResult{
			Name:       sym.Name,
			Qualified:  sym.Qualified,
			Kind:       sym.Kind,
			Filepath:   sym.Filepath,
			LineStart:  sym.LineStart,
			LineEnd:    sym.LineEnd,
			Module:     sym.Module,
			Signature:  sym.Signature,
			DocComment: sym.DocComment,
		})
	}

	return map[string]interface{}{"symbols": out, "codebase": codebase}, nil, codebase
}

func (s *Server) toolListModules(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Parent string `json:"parent"`
	}
	if args != nil {
		json.Unmarshal(args, &params)
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	modules, err := s.store.ListModules(ctx, codebase, params.Parent)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err), codebase
	}

	type moduleResult struct {
		Module        string `json:"module"`
		ClassCount    int    `json:"class_count"`
		FunctionCount int    `json:"function_count"`
		StructCount   int    `json:"struct_count"`
		FileCount     int    `json:"file_count"`
		EstimatedLOC  int    `json:"estimated_loc"`
	}

	var out []moduleResult
	for _, m := range modules {
		out = append(out, moduleResult{
			Module:        m.Module,
			ClassCount:    m.ClassCount,
			FunctionCount: m.FunctionCount,
			StructCount:   m.StructCount,
			FileCount:     m.FileCount,
			EstimatedLOC:  m.EstimatedLOC,
		})
	}

	return map[string]interface{}{"modules": out, "codebase": codebase}, nil, codebase
}

func (s *Server) toolGetReferences(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Symbol  string `json:"symbol"`
		RefKind string `json:"ref_kind"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}
	if params.RefKind == "" || params.RefKind == "any" {
		params.RefKind = ""
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	refs, err := s.store.GetReferences(ctx, codebase, params.Symbol, params.RefKind, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("get references: %w", err), codebase
	}

	type refResult struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Filepath  string `json:"filepath"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Module    string `json:"module,omitempty"`
		RefKind   string `json:"ref_kind"`
		RefLine   int    `json:"ref_line"`
	}

	var out []refResult
	for _, r := range refs {
		out = append(out, refResult{
			Name:      r.SymbolName,
			Qualified: r.SymbolQualified,
			Kind:      r.SymbolKind,
			Filepath:  r.Filepath,
			LineStart: r.LineStart,
			LineEnd:   r.LineEnd,
			Module:    r.Module,
			RefKind:   r.RefKind,
			RefLine:   r.RefLine,
		})
	}

	return map[string]interface{}{"references": out, "symbol": params.Symbol, "codebase": codebase}, nil, codebase
}

func (s *Server) toolGetClassHierarchy(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		ClassName string `json:"class_name"`
		Direction string `json:"direction"`
		Depth     int    `json:"depth"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}
	if params.Direction == "" {
		params.Direction = "both"
	}
	if params.Depth <= 0 {
		params.Depth = 5
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	nodes, err := s.store.GetClassHierarchy(ctx, codebase, params.ClassName, params.Direction, params.Depth)
	if err != nil {
		return nil, fmt.Errorf("get class hierarchy: %w", err), codebase
	}

	type hierarchyResult struct {
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Depth     int    `json:"depth"`
		Direction string `json:"direction"`
	}

	var out []hierarchyResult
	for _, n := range nodes {
		out = append(out, hierarchyResult{
			Qualified: n.Qualified,
			Kind:      n.Kind,
			Depth:     n.Depth,
			Direction: n.Direction,
		})
	}

	return map[string]interface{}{"class_name": params.ClassName, "hierarchy": out, "codebase": codebase}, nil, codebase
}

func (s *Server) toolGetFileContext(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Filepath  string `json:"filepath"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	// Get symbols defined in this file
	symbols, err := s.store.GetFileSymbols(ctx, codebase, params.Filepath)
	if err != nil {
		return nil, fmt.Errorf("get file symbols: %w", err), codebase
	}

	type symInfo struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
		Signature string `json:"signature,omitempty"`
	}
	var symOut []symInfo
	module := ""
	for _, sym := range symbols {
		symOut = append(symOut, symInfo{
			Name:      sym.Name,
			Qualified: sym.Qualified,
			Kind:      sym.Kind,
			LineStart: sym.LineStart,
			LineEnd:   sym.LineEnd,
			Signature: sym.Signature,
		})
		if module == "" && sym.Module != "" {
			module = sym.Module
		}
	}

	// Get dependents (other files that reference this file's symbols)
	dependents, err := s.store.GetFileDependents(ctx, codebase, params.Filepath, 20)
	if err != nil {
		return nil, fmt.Errorf("get file dependents: %w", err), codebase
	}

	type depInfo struct {
		Name      string `json:"name"`
		Qualified string `json:"qualified"`
		Kind      string `json:"kind"`
		Filepath  string `json:"filepath"`
		RefKind   string `json:"ref_kind"`
	}
	var depOut []depInfo
	for _, d := range dependents {
		depOut = append(depOut, depInfo{
			Name:      d.SymbolName,
			Qualified: d.SymbolQualified,
			Kind:      d.SymbolKind,
			Filepath:  d.Filepath,
			RefKind:   d.RefKind,
		})
	}

	result := map[string]interface{}{
		"filepath":   params.Filepath,
		"module":     module,
		"symbols":    symOut,
		"dependents": depOut,
		"codebase":   codebase,
	}

	// Try to read file content.
	contentRead := false
	rootPath := s.cfg.Codebase.Path
	if rootPath == "" {
		dbPath, err := s.store.GetCodebaseRootPath(ctx, codebase)
		if err == nil && dbPath != "" {
			rootPath = dbPath
		}
	}

	if rootPath != "" {
		fullPath := filepath.Join(rootPath, params.Filepath)
		data, err := os.ReadFile(fullPath)
		if err == nil {
			content := string(data)
			if params.LineStart > 0 || params.LineEnd > 0 {
				lines := strings.Split(content, "\n")
				start := params.LineStart - 1
				if start < 0 {
					start = 0
				}
				end := params.LineEnd
				if end <= 0 || end > len(lines) {
					end = len(lines)
				}
				if start < len(lines) {
					content = strings.Join(lines[start:end], "\n")
				}
			}
			result["content"] = content
			contentRead = true
		}
	}

	if !contentRead {
		result["_note"] = "File content unavailable: server does not have local access to this codebase's files. Symbols and dependency information are still available."
	}

	return result, nil, codebase
}

func (s *Server) toolGetModuleSummary(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Module string `json:"module"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	// Try to get cached summary first
	sum, err := s.store.GetSummary(ctx, codebase, params.Module, "module")
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err), codebase
	}

	// If no cached summary, try on-demand generation
	summarGen := s.getSummarGen(codebase)
	if sum == nil && summarGen != nil {
		sum, err = summarGen.GenerateModuleSummary(ctx, params.Module)
		if err != nil {
			s.logger.Printf("warning: on-demand summary generation failed: %v", err)
		}
	}

	// Get module stats
	modules, err := s.store.ListModules(ctx, codebase, params.Module)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err), codebase
	}

	// Build result
	result := map[string]interface{}{
		"module":   params.Module,
		"codebase": codebase,
	}

	if sum != nil {
		result["summary"] = sum.SummaryText

		var keyClasses []string
		json.Unmarshal([]byte(sum.KeyClasses), &keyClasses)
		result["key_classes"] = keyClasses

		var deps []string
		json.Unmarshal([]byte(sum.Dependencies), &deps)
		result["dependencies"] = deps
	}

	// Aggregate stats across matching modules
	var totalFiles, totalLOC int
	var submodules []string
	for _, m := range modules {
		totalFiles += m.FileCount
		totalLOC += m.EstimatedLOC
		if m.Module != params.Module {
			submodules = append(submodules, m.Module)
		}
	}
	result["file_count"] = totalFiles
	result["estimated_loc"] = totalLOC
	if len(submodules) > 0 {
		result["submodules"] = submodules
	}

	if sum == nil {
		result["summary"] = fmt.Sprintf("No summary available for module '%s'. Summary generation may be disabled or the module may not exist.", params.Module)
	}

	return result, nil, codebase
}

func (s *Server) toolGenerateClaudeMD(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		TopSymbols int `json:"top_symbols"`
	}
	if args != nil {
		json.Unmarshal(args, &params)
	}
	if params.TopSymbols <= 0 {
		params.TopSymbols = 20
	}

	if s.store == nil {
		return nil, fmt.Errorf("postgres not available"), codebase
	}

	// Look up root_path for the codebase
	codebasePath := s.cfg.Codebase.Path
	if codebasePath == "" {
		dbPath, err := s.store.GetCodebaseRootPath(ctx, codebase)
		if err == nil {
			codebasePath = dbPath
		}
	}

	content, err := claudemd.Generate(ctx, codebase, codebasePath, s.store, params.TopSymbols)
	if err != nil {
		return nil, fmt.Errorf("generate CLAUDE.md: %w", err), codebase
	}

	return map[string]interface{}{"content": content, "codebase": codebase}, nil, codebase
}
