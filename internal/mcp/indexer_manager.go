package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/metrics"
)

// IndexerNode represents a connected indexer daemon.
type IndexerNode struct {
	NodeID      string    `json:"node_id"`
	Codebases   []string  `json:"codebases"`
	ConnectedAt time.Time `json:"connected_at"`
	LastSeen    time.Time `json:"last_seen"`
	Status      string    `json:"status"` // idle, indexing
	SSEChan     chan []byte `json:"-"`
}

// ReindexRequest tracks a reindex operation.
type ReindexRequest struct {
	RequestID      string    `json:"request_id"`
	Codebase       string    `json:"codebase"`
	NodeID         string    `json:"node_id"`
	Full           bool      `json:"full"`
	Status         string    `json:"status"` // requested, started, progress, complete, error
	FilesTotal     int       `json:"files_total"`
	FilesProcessed int       `json:"files_processed"`
	FilesIndexed   int       `json:"files_indexed"`
	FilesSkipped   int       `json:"files_skipped"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	DurationMs     int64     `json:"duration_ms,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// IndexerManager tracks connected indexer daemons and reindex requests.
type IndexerManager struct {
	nodes    sync.Map // nodeID -> *IndexerNode
	requests sync.Map // requestID -> *ReindexRequest
	logger   *log.Logger
}

// NewIndexerManager creates a new IndexerManager.
func NewIndexerManager(logger *log.Logger) *IndexerManager {
	return &IndexerManager{logger: logger}
}

// RegisterNode registers an indexer daemon connection.
func (m *IndexerManager) RegisterNode(nodeID string, codebases []string, sseChan chan []byte) {
	node := &IndexerNode{
		NodeID:      nodeID,
		Codebases:   codebases,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
		Status:      "idle",
		SSEChan:     sseChan,
	}
	m.nodes.Store(nodeID, node)
	m.updateNodeCount()
	m.logger.Printf("indexer node registered: %s (codebases: %v)", nodeID, codebases)
}

// DeregisterNode removes an indexer daemon.
func (m *IndexerManager) DeregisterNode(nodeID string) {
	if v, ok := m.nodes.LoadAndDelete(nodeID); ok {
		node := v.(*IndexerNode)
		close(node.SSEChan)
		m.updateNodeCount()
		m.logger.Printf("indexer node deregistered: %s", nodeID)
	}
}

// SendReindex dispatches a reindex command to the appropriate indexer.
// Returns the request ID and an error if no indexer is available.
func (m *IndexerManager) SendReindex(codebase string, full bool) (string, error) {
	requestID := generateRequestID()

	// Find an indexer that serves this codebase
	var targetNode *IndexerNode
	m.nodes.Range(func(_, v interface{}) bool {
		node := v.(*IndexerNode)
		for _, cb := range node.Codebases {
			if cb == codebase {
				targetNode = node
				return false
			}
		}
		return true
	})

	if targetNode == nil {
		return "", &NoIndexerError{Codebase: codebase, Available: m.ListAvailableCodebases()}
	}

	req := &ReindexRequest{
		RequestID: requestID,
		Codebase:  codebase,
		NodeID:    targetNode.NodeID,
		Full:      full,
		Status:    "requested",
		StartedAt: time.Now(),
	}
	m.requests.Store(requestID, req)

	// Record metric
	reindexType := "incremental"
	if full {
		reindexType = "full"
	}
	metrics.RecordReindexRequest(codebase, reindexType)

	// Send command via SSE
	cmd := map[string]interface{}{
		"type":       "reindex",
		"codebase":   codebase,
		"request_id": requestID,
		"full":       full,
	}
	data, _ := json.Marshal(cmd)

	select {
	case targetNode.SSEChan <- data:
		m.logger.Printf("reindex command sent to %s: codebase=%s full=%v request_id=%s",
			targetNode.NodeID, codebase, full, requestID)
	default:
		m.logger.Printf("warning: failed to send reindex command to %s (channel full)", targetNode.NodeID)
		req.Status = "error"
		req.Error = "failed to send command to indexer (channel full)"
	}

	return requestID, nil
}

// UpdateStatus updates a reindex request's status from an indexer progress report.
func (m *IndexerManager) UpdateStatus(requestID, nodeID, codebase, status string, filesTotal, filesProcessed, filesIndexed, filesSkipped int, durationMs int64, errMsg string) {
	v, ok := m.requests.Load(requestID)
	if !ok {
		// Create a new request entry for unknown request IDs (e.g., file-watch triggered)
		v = &ReindexRequest{
			RequestID: requestID,
			Codebase:  codebase,
			NodeID:    nodeID,
			StartedAt: time.Now(),
		}
		m.requests.Store(requestID, v)
	}

	req := v.(*ReindexRequest)
	req.Status = status
	req.NodeID = nodeID
	req.Codebase = codebase
	req.FilesTotal = filesTotal
	req.FilesProcessed = filesProcessed
	req.FilesIndexed = filesIndexed
	req.FilesSkipped = filesSkipped
	req.DurationMs = durationMs
	req.Error = errMsg

	if status == "complete" || status == "error" {
		req.CompletedAt = time.Now()
		if durationMs > 0 {
			metrics.RecordReindexDuration(codebase, time.Duration(durationMs)*time.Millisecond)
		}
	}

	// Update node status
	if v, ok := m.nodes.Load(nodeID); ok {
		node := v.(*IndexerNode)
		node.LastSeen = time.Now()
		if status == "started" || status == "progress" {
			node.Status = "indexing"
		} else {
			node.Status = "idle"
		}
	}
}

// GetReindexStatus returns status for a specific request ID.
func (m *IndexerManager) GetReindexStatus(requestID string) *ReindexRequest {
	v, ok := m.requests.Load(requestID)
	if !ok {
		return nil
	}
	return v.(*ReindexRequest)
}

// GetReindexStatusByCodebase returns the latest reindex status for a codebase.
func (m *IndexerManager) GetReindexStatusByCodebase(codebase string) *ReindexRequest {
	var latest *ReindexRequest
	m.requests.Range(func(_, v interface{}) bool {
		req := v.(*ReindexRequest)
		if req.Codebase == codebase {
			if latest == nil || req.StartedAt.After(latest.StartedAt) {
				latest = req
			}
		}
		return true
	})
	return latest
}

// GetAllReindexStatuses returns all recent reindex statuses.
func (m *IndexerManager) GetAllReindexStatuses() []*ReindexRequest {
	var all []*ReindexRequest
	m.requests.Range(func(_, v interface{}) bool {
		all = append(all, v.(*ReindexRequest))
		return true
	})
	return all
}

// GetNodes returns all connected indexer nodes.
func (m *IndexerManager) GetNodes() []*IndexerNode {
	var nodes []*IndexerNode
	m.nodes.Range(func(_, v interface{}) bool {
		nodes = append(nodes, v.(*IndexerNode))
		return true
	})
	return nodes
}

// GetNodeForCodebase returns the connected node serving a given codebase, if any.
func (m *IndexerManager) GetNodeForCodebase(codebase string) *IndexerNode {
	var result *IndexerNode
	m.nodes.Range(func(_, v interface{}) bool {
		node := v.(*IndexerNode)
		for _, cb := range node.Codebases {
			if cb == codebase {
				result = node
				return false
			}
		}
		return true
	})
	return result
}

// ListAvailableCodebases returns all codebases served by connected indexers.
func (m *IndexerManager) ListAvailableCodebases() []string {
	seen := make(map[string]bool)
	var codebases []string
	m.nodes.Range(func(_, v interface{}) bool {
		node := v.(*IndexerNode)
		for _, cb := range node.Codebases {
			if !seen[cb] {
				seen[cb] = true
				codebases = append(codebases, cb)
			}
		}
		return true
	})
	return codebases
}

func (m *IndexerManager) updateNodeCount() {
	var count float64
	m.nodes.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	metrics.SetIndexerConnectedNodes(count)
}

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NoIndexerError is returned when no indexer is connected for a codebase.
type NoIndexerError struct {
	Codebase  string
	Available []string
}

func (e *NoIndexerError) Error() string {
	if len(e.Available) == 0 {
		return "no indexer daemons connected for codebase '" + e.Codebase + "' (no indexers connected at all)"
	}
	avail := ""
	for i, cb := range e.Available {
		if i > 0 {
			avail += ", "
		}
		avail += cb
	}
	return "no indexer connected for codebase '" + e.Codebase + "'. Available codebases with connected indexers: " + avail
}
