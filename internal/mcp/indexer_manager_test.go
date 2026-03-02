package mcp

import (
	"encoding/json"
	"io"
	"log"
	"sort"
	"strings"
	"testing"
	"time"
)

func newTestLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func TestRegisterNode(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"myproject"}, ch)

	// Verify GetNodes returns the registered node
	nodes := mgr.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].NodeID != "node-1" {
		t.Errorf("expected node ID 'node-1', got %q", nodes[0].NodeID)
	}
	if len(nodes[0].Codebases) != 1 || nodes[0].Codebases[0] != "myproject" {
		t.Errorf("expected codebases [myproject], got %v", nodes[0].Codebases)
	}
	if nodes[0].Status != "idle" {
		t.Errorf("expected status 'idle', got %q", nodes[0].Status)
	}

	// Verify GetNodeForCodebase works
	node := mgr.GetNodeForCodebase("myproject")
	if node == nil {
		t.Fatal("expected to find node for codebase 'myproject'")
	}
	if node.NodeID != "node-1" {
		t.Errorf("expected node ID 'node-1', got %q", node.NodeID)
	}

	// Verify GetNodeForCodebase returns nil for unknown codebase
	node = mgr.GetNodeForCodebase("unknown")
	if node != nil {
		t.Errorf("expected nil for unknown codebase, got %+v", node)
	}
}

func TestDeregisterNode(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"myproject"}, ch)

	// Verify node is present
	nodes := mgr.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node before deregister, got %d", len(nodes))
	}

	mgr.DeregisterNode("node-1")

	// Verify node is gone
	nodes = mgr.GetNodes()
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes after deregister, got %d", len(nodes))
	}

	// Verify GetNodeForCodebase returns nil
	node := mgr.GetNodeForCodebase("myproject")
	if node != nil {
		t.Errorf("expected nil for deregistered node's codebase, got %+v", node)
	}

	// Verify SSE channel was closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected SSE channel to be closed")
		}
	default:
		t.Error("expected SSE channel to be closed, but it was not")
	}
}

func TestSendReindex_Success(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"myproject"}, ch)

	requestID, err := mgr.SendReindex("myproject", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestID == "" {
		t.Fatal("expected non-empty request ID")
	}

	// Verify a message was sent to the SSE channel
	select {
	case data := <-ch:
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal SSE message: %v", err)
		}
		if msg["type"] != "reindex" {
			t.Errorf("expected message type 'reindex', got %v", msg["type"])
		}
		if msg["codebase"] != "myproject" {
			t.Errorf("expected codebase 'myproject', got %v", msg["codebase"])
		}
		if msg["request_id"] != requestID {
			t.Errorf("expected request_id %q, got %v", requestID, msg["request_id"])
		}
		if msg["full"] != false {
			t.Errorf("expected full=false, got %v", msg["full"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE message")
	}

	// Verify the request was tracked
	req := mgr.GetReindexStatus(requestID)
	if req == nil {
		t.Fatal("expected to find reindex request")
	}
	if req.Status != "requested" {
		t.Errorf("expected status 'requested', got %q", req.Status)
	}
	if req.Codebase != "myproject" {
		t.Errorf("expected codebase 'myproject', got %q", req.Codebase)
	}
	if req.NodeID != "node-1" {
		t.Errorf("expected node ID 'node-1', got %q", req.NodeID)
	}
}

func TestSendReindex_NoIndexer(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	_, err := mgr.SendReindex("myproject", false)
	if err == nil {
		t.Fatal("expected error when no indexers are connected")
	}

	var noIdxErr *NoIndexerError
	if !errorAs(err, &noIdxErr) {
		t.Fatalf("expected NoIndexerError, got %T: %v", err, err)
	}
	if noIdxErr.Codebase != "myproject" {
		t.Errorf("expected codebase 'myproject', got %q", noIdxErr.Codebase)
	}
	if len(noIdxErr.Available) != 0 {
		t.Errorf("expected empty available list, got %v", noIdxErr.Available)
	}
}

func TestSendReindex_WrongCodebase(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"foo"}, ch)

	_, err := mgr.SendReindex("bar", false)
	if err == nil {
		t.Fatal("expected error for wrong codebase")
	}

	var noIdxErr *NoIndexerError
	if !errorAs(err, &noIdxErr) {
		t.Fatalf("expected NoIndexerError, got %T: %v", err, err)
	}
	if noIdxErr.Codebase != "bar" {
		t.Errorf("expected codebase 'bar', got %q", noIdxErr.Codebase)
	}
	if len(noIdxErr.Available) != 1 || noIdxErr.Available[0] != "foo" {
		t.Errorf("expected available [foo], got %v", noIdxErr.Available)
	}
}

func TestUpdateStatus(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"myproject"}, ch)

	requestID, err := mgr.SendReindex("myproject", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Drain the SSE channel
	<-ch

	// Update status to complete
	mgr.UpdateStatus(requestID, "node-1", "myproject", "complete", 100, 100, 95, 5, 5000, "")

	req := mgr.GetReindexStatus(requestID)
	if req == nil {
		t.Fatal("expected to find reindex request")
	}
	if req.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", req.Status)
	}
	if req.FilesTotal != 100 {
		t.Errorf("expected FilesTotal=100, got %d", req.FilesTotal)
	}
	if req.FilesProcessed != 100 {
		t.Errorf("expected FilesProcessed=100, got %d", req.FilesProcessed)
	}
	if req.FilesIndexed != 95 {
		t.Errorf("expected FilesIndexed=95, got %d", req.FilesIndexed)
	}
	if req.FilesSkipped != 5 {
		t.Errorf("expected FilesSkipped=5, got %d", req.FilesSkipped)
	}
	if req.DurationMs != 5000 {
		t.Errorf("expected DurationMs=5000, got %d", req.DurationMs)
	}
	if req.CompletedAt.IsZero() {
		t.Error("expected CompletedAt to be set for complete status")
	}

	// Verify node status was updated to idle
	node := mgr.GetNodeForCodebase("myproject")
	if node == nil {
		t.Fatal("expected to find node")
	}
	if node.Status != "idle" {
		t.Errorf("expected node status 'idle' after complete, got %q", node.Status)
	}
}

func TestUpdateStatus_NewRequest(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	// Update status for an unknown request ID (e.g., file-watch triggered)
	mgr.UpdateStatus("unknown-req-id", "node-1", "myproject", "started", 50, 0, 0, 0, 0, "")

	req := mgr.GetReindexStatus("unknown-req-id")
	if req == nil {
		t.Fatal("expected new entry to be created for unknown request ID")
	}
	if req.RequestID != "unknown-req-id" {
		t.Errorf("expected request ID 'unknown-req-id', got %q", req.RequestID)
	}
	if req.Codebase != "myproject" {
		t.Errorf("expected codebase 'myproject', got %q", req.Codebase)
	}
	if req.Status != "started" {
		t.Errorf("expected status 'started', got %q", req.Status)
	}
	if req.FilesTotal != 50 {
		t.Errorf("expected FilesTotal=50, got %d", req.FilesTotal)
	}
}

func TestGetReindexStatusByCodebase(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"myproject"}, ch)

	// Send two reindex requests
	reqID1, err := mgr.SendReindex("myproject", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-ch // drain

	// Small pause so the second request has a later StartedAt
	time.Sleep(10 * time.Millisecond)

	reqID2, err := mgr.SendReindex("myproject", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-ch // drain

	if reqID1 == reqID2 {
		t.Fatal("expected different request IDs")
	}

	// GetReindexStatusByCodebase should return the latest (reqID2)
	latest := mgr.GetReindexStatusByCodebase("myproject")
	if latest == nil {
		t.Fatal("expected to find latest reindex status")
	}
	if latest.RequestID != reqID2 {
		t.Errorf("expected latest request ID %q, got %q", reqID2, latest.RequestID)
	}

	// Non-existent codebase should return nil
	missing := mgr.GetReindexStatusByCodebase("nonexistent")
	if missing != nil {
		t.Errorf("expected nil for nonexistent codebase, got %+v", missing)
	}
}

func TestGetAllReindexStatuses(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	ch := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"proj-a", "proj-b"}, ch)

	_, err := mgr.SendReindex("proj-a", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-ch

	_, err = mgr.SendReindex("proj-b", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	<-ch

	all := mgr.GetAllReindexStatuses()
	if len(all) != 2 {
		t.Fatalf("expected 2 reindex statuses, got %d", len(all))
	}

	// Verify both codebases are represented
	codebases := make(map[string]bool)
	for _, req := range all {
		codebases[req.Codebase] = true
	}
	if !codebases["proj-a"] || !codebases["proj-b"] {
		t.Errorf("expected both proj-a and proj-b in results, got %v", codebases)
	}
}

func TestListAvailableCodebases(t *testing.T) {
	mgr := NewIndexerManager(newTestLogger())

	// Register two nodes with overlapping codebases
	ch1 := make(chan []byte, 10)
	mgr.RegisterNode("node-1", []string{"proj-a", "proj-b"}, ch1)

	ch2 := make(chan []byte, 10)
	mgr.RegisterNode("node-2", []string{"proj-b", "proj-c"}, ch2)

	codebases := mgr.ListAvailableCodebases()

	// Should be deduplicated
	sort.Strings(codebases)
	expected := []string{"proj-a", "proj-b", "proj-c"}
	if len(codebases) != len(expected) {
		t.Fatalf("expected %d codebases, got %d: %v", len(expected), len(codebases), codebases)
	}
	for i, cb := range codebases {
		if cb != expected[i] {
			t.Errorf("expected codebase[%d]=%q, got %q", i, expected[i], cb)
		}
	}
}

func TestNoIndexerError_Format(t *testing.T) {
	// Test with empty available list
	err1 := &NoIndexerError{Codebase: "myproject", Available: nil}
	msg1 := err1.Error()
	if !strings.Contains(msg1, "myproject") {
		t.Errorf("expected error to contain codebase name, got %q", msg1)
	}
	if !strings.Contains(msg1, "no indexers connected at all") {
		t.Errorf("expected error to mention no indexers connected, got %q", msg1)
	}

	// Test with non-empty available list
	err2 := &NoIndexerError{Codebase: "bar", Available: []string{"foo", "baz"}}
	msg2 := err2.Error()
	if !strings.Contains(msg2, "bar") {
		t.Errorf("expected error to contain codebase name 'bar', got %q", msg2)
	}
	if !strings.Contains(msg2, "foo") || !strings.Contains(msg2, "baz") {
		t.Errorf("expected error to list available codebases, got %q", msg2)
	}
	if strings.Contains(msg2, "no indexers connected at all") {
		t.Errorf("should not mention 'no indexers connected at all' when codebases are available, got %q", msg2)
	}
}

// errorAs is a helper that mimics errors.As for *NoIndexerError.
func errorAs(err error, target **NoIndexerError) bool {
	e, ok := err.(*NoIndexerError)
	if ok {
		*target = e
	}
	return ok
}
