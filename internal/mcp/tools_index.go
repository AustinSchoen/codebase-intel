package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Server) toolReindex(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	if s.indexerMgr == nil {
		return nil, fmt.Errorf("reindex not available (server not in HTTP mode)"), ""
	}

	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Full bool `json:"full"`
	}
	if args != nil {
		json.Unmarshal(args, &params)
	}

	requestID, err := s.indexerMgr.SendReindex(codebase, params.Full)
	if err != nil {
		return nil, err, codebase
	}

	reindexType := "incremental"
	if params.Full {
		reindexType = "full"
	}

	return map[string]interface{}{
		"request_id": requestID,
		"codebase":   codebase,
		"type":       reindexType,
		"status":     "requested",
	}, nil, codebase
}

func (s *Server) toolReindexStatus(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if s.indexerMgr == nil {
		return nil, fmt.Errorf("reindex_status not available (server not in HTTP mode)")
	}

	var params struct {
		RequestID string `json:"request_id"`
		Codebase  string `json:"codebase"`
	}
	if args != nil {
		json.Unmarshal(args, &params)
	}

	if params.RequestID != "" {
		req := s.indexerMgr.GetReindexStatus(params.RequestID)
		if req == nil {
			return map[string]interface{}{"error": "request not found", "request_id": params.RequestID}, nil
		}
		return req, nil
	}

	if params.Codebase != "" {
		req := s.indexerMgr.GetReindexStatusByCodebase(params.Codebase)
		if req == nil {
			return map[string]interface{}{"message": "no reindex history for codebase", "codebase": params.Codebase}, nil
		}
		return req, nil
	}

	// Return all
	all := s.indexerMgr.GetAllReindexStatuses()
	if len(all) == 0 {
		return map[string]interface{}{"message": "no reindex requests recorded"}, nil
	}
	return map[string]interface{}{"requests": all}, nil
}
