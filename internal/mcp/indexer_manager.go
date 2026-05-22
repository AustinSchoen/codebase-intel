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

// nodeKey identifies a daemon registration by (nodeID, codebase). The map
// previously keyed on nodeID alone, which caused silent collisions when two
// daemons on the same host both registered with the default hostname-derived
// node ID. See issue #4.
type nodeKey struct {
	NodeID   string
	Codebase string
}

// IndexerManager tracks connected indexer daemons and reindex requests.
type IndexerManager struct {
	mu       sync.RWMutex
	nodes    map[nodeKey]*IndexerNode   // (nodeID, codebase) -> *IndexerNode
	requests map[string]*ReindexRequest // requestID -> *ReindexRequest
	logger   *log.Logger
}

// NewIndexerManager creates a new IndexerManager.
func NewIndexerManager(logger *log.Logger) *IndexerManager {
	return &IndexerManager{
		nodes:    make(map[nodeKey]*IndexerNode),
		requests: make(map[string]*ReindexRequest),
		logger:   logger,
	}
}

// RegisterNode registers an indexer daemon connection.
//
// One IndexerNode is created per registration. If the daemon serves multiple
// codebases (the original per-host design — see issue #3), one entry per
// codebase is added to the node map, all sharing the same IndexerNode struct
// (and therefore the same SSE channel).
func (m *IndexerManager) RegisterNode(nodeID string, codebases []string, sseChan chan []byte) {
	node := &IndexerNode{
		NodeID:      nodeID,
		Codebases:   codebases,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
		Status:      "idle",
		SSEChan:     sseChan,
	}
	m.mu.Lock()
	for _, cb := range codebases {
		m.nodes[nodeKey{NodeID: nodeID, Codebase: cb}] = node
	}
	m.mu.Unlock()
	m.updateNodeCount()
	m.logger.Printf("indexer node registered: %s (codebases: %v)", nodeID, codebases)
}

// DeregisterNode removes all map entries belonging to a daemon. Because all
// entries for a single registration share the IndexerNode pointer, the SSE
// channel is closed exactly once.
func (m *IndexerManager) DeregisterNode(nodeID string) {
	m.mu.Lock()
	var node *IndexerNode
	for k, n := range m.nodes {
		if k.NodeID == nodeID {
			node = n
			delete(m.nodes, k)
		}
	}
	m.mu.Unlock()

	if node != nil {
		close(node.SSEChan)
		m.updateNodeCount()
		m.logger.Printf("indexer node deregistered: %s", nodeID)
	}
}

// SendReindex dispatches a reindex command to the appropriate indexer.
// Returns the request ID and an error if no indexer is available.
func (m *IndexerManager) SendReindex(codebase string, full bool) (string, error) {
	requestID := generateRequestID()

	m.mu.Lock()
	// Find an indexer that serves this codebase. Direct map lookup over the
	// (nodeID, codebase) key would require iterating to find ANY nodeID, so
	// just scan — there are very few daemons in practice.
	var targetNode *IndexerNode
	for k, node := range m.nodes {
		if k.Codebase == codebase {
			targetNode = node
			break
		}
	}

	if targetNode == nil {
		available := m.listAvailableCodebasesLocked()
		m.mu.Unlock()
		return "", &NoIndexerError{Codebase: codebase, Available: available}
	}

	req := &ReindexRequest{
		RequestID: requestID,
		Codebase:  codebase,
		NodeID:    targetNode.NodeID,
		Full:      full,
		Status:    "requested",
		StartedAt: time.Now(),
	}
	m.requests[requestID] = req
	m.mu.Unlock()

	// Record metric
	reindexType := "incremental"
	if full {
		reindexType = "full"
	}
	metrics.RecordReindexRequest(codebase, reindexType)

	// Send command via SSE (channel send is safe outside the lock)
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
		m.mu.Lock()
		req.Status = "error"
		req.Error = "failed to send command to indexer (channel full)"
		m.mu.Unlock()
	}

	return requestID, nil
}

// UpdateStatus updates a reindex request's status from an indexer progress report.
func (m *IndexerManager) UpdateStatus(requestID, nodeID, codebase, status string, filesTotal, filesProcessed, filesIndexed, filesSkipped int, durationMs int64, errMsg string) {
	m.mu.Lock()
	req, ok := m.requests[requestID]
	if !ok {
		// Create a new request entry for unknown request IDs (e.g., file-watch triggered)
		req = &ReindexRequest{
			RequestID: requestID,
			Codebase:  codebase,
			NodeID:    nodeID,
			StartedAt: time.Now(),
		}
		m.requests[requestID] = req
	}

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
	}

	// Update node status. With (nodeID, codebase) keying, look up precisely
	// the registration that emitted this progress event so we don't move state
	// on a sibling daemon that happens to share the hostname.
	if node, ok := m.nodes[nodeKey{NodeID: nodeID, Codebase: codebase}]; ok {
		node.LastSeen = time.Now()
		if status == "started" || status == "progress" {
			node.Status = "indexing"
		} else {
			node.Status = "idle"
		}
	}
	m.mu.Unlock()

	if (status == "complete" || status == "error") && durationMs > 0 {
		metrics.RecordReindexDuration(codebase, time.Duration(durationMs)*time.Millisecond)
	}
}

// GetReindexStatus returns status for a specific request ID.
func (m *IndexerManager) GetReindexStatus(requestID string) *ReindexRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.requests[requestID]
}

// GetReindexStatusByCodebase returns the latest reindex status for a codebase.
func (m *IndexerManager) GetReindexStatusByCodebase(codebase string) *ReindexRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *ReindexRequest
	for _, req := range m.requests {
		if req.Codebase == codebase {
			if latest == nil || req.StartedAt.After(latest.StartedAt) {
				latest = req
			}
		}
	}
	return latest
}

// GetAllReindexStatuses returns all recent reindex statuses.
func (m *IndexerManager) GetAllReindexStatuses() []*ReindexRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := make([]*ReindexRequest, 0, len(m.requests))
	for _, req := range m.requests {
		all = append(all, req)
	}
	return all
}

// GetNodes returns all connected indexer nodes. A daemon serving multiple
// codebases appears in the map under several keys but shares the same
// IndexerNode pointer; dedup so the caller sees one entry per registration.
func (m *IndexerManager) GetNodes() []*IndexerNode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[*IndexerNode]bool, len(m.nodes))
	nodes := make([]*IndexerNode, 0, len(m.nodes))
	for _, node := range m.nodes {
		if seen[node] {
			continue
		}
		seen[node] = true
		nodes = append(nodes, node)
	}
	return nodes
}

// GetNodeForCodebase returns a connected node serving a given codebase, if any.
func (m *IndexerManager) GetNodeForCodebase(codebase string) *IndexerNode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, node := range m.nodes {
		if k.Codebase == codebase {
			return node
		}
	}
	return nil
}

// ListAvailableCodebases returns all codebases served by connected indexers.
func (m *IndexerManager) ListAvailableCodebases() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listAvailableCodebasesLocked()
}

// listAvailableCodebasesLocked requires m.mu to be held.
func (m *IndexerManager) listAvailableCodebasesLocked() []string {
	seen := make(map[string]bool, len(m.nodes))
	codebases := make([]string, 0, len(m.nodes))
	for k := range m.nodes {
		if !seen[k.Codebase] {
			seen[k.Codebase] = true
			codebases = append(codebases, k.Codebase)
		}
	}
	return codebases
}

// updateNodeCount reports the number of distinct connected daemons (not the
// number of (nodeID, codebase) entries).
func (m *IndexerManager) updateNodeCount() {
	m.mu.RLock()
	seen := make(map[*IndexerNode]bool, len(m.nodes))
	for _, n := range m.nodes {
		seen[n] = true
	}
	m.mu.RUnlock()
	metrics.SetIndexerConnectedNodes(float64(len(seen)))
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
