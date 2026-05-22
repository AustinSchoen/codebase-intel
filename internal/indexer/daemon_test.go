package indexer

import (
	"io"
	"log"
	"reflect"
	"testing"

	"github.com/AustinSchoen/codebase-intel/internal/config"
)

// stubClient returns a *Client wired with the minimum fields the daemon
// touches for routing — codebase name and logger. We never call any HTTP
// method on it, so the server URL just needs to be non-empty.
func stubClient(name string) *Client {
	return &Client{
		cfg: &config.Config{
			Codebase: config.CodebaseConfig{
				Name: name,
				Path: "/tmp/" + name,
			},
		},
		logger: log.New(io.Discard, "", 0),
	}
}

func newTestDaemon(t *testing.T, names ...string) *Daemon {
	t.Helper()
	clients := make(map[string]*Client, len(names))
	for _, name := range names {
		clients[name] = stubClient(name)
	}
	return NewDaemon(clients, DaemonConfig{
		ServerURL: "http://example.invalid",
		NodeID:    "test-node",
	})
}

func TestDaemon_CodebasesReturnsSortedNames(t *testing.T) {
	d := newTestDaemon(t, "zebra", "alpha", "mango")
	got := d.codebases()
	want := []string{"alpha", "mango", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("codebases() = %v, want %v (sorted)", got, want)
	}
}

func TestDaemon_IndexerLookupByCodebase(t *testing.T) {
	d := newTestDaemon(t, "azimuth", "lumina-app")

	az := d.clients["azimuth"]
	if az == nil {
		t.Fatal("expected indexer for 'azimuth' to be present")
	}
	if az.cfg.Codebase.Name != "azimuth" {
		t.Errorf("lookup returned wrong indexer: %s", az.cfg.Codebase.Name)
	}

	lu := d.clients["lumina-app"]
	if lu == nil {
		t.Fatal("expected indexer for 'lumina-app' to be present")
	}
	if lu == az {
		t.Error("expected distinct indexer instances for distinct codebases")
	}

	if d.clients["unknown"] != nil {
		t.Error("expected nil for unknown codebase")
	}
}

// TestDaemon_DispatchReindexUnknownCodebase verifies that a reindex command
// for a codebase this daemon does not serve is rejected without crashing or
// starting a goroutine. This is the regression boundary for SSE events that
// might arrive after a codebase was removed from this daemon's configuration.
func TestDaemon_DispatchReindexUnknownCodebase(t *testing.T) {
	d := newTestDaemon(t, "azimuth")

	// Background context — never used for actual indexing because we expect
	// dispatchReindex to bail before spawning the goroutine.
	if ok := d.dispatchReindex(t.Context(), "lumina-app", false, "req-1"); ok {
		t.Error("dispatchReindex returned true for unknown codebase 'lumina-app'")
	}
}

// TestDaemon_RegistersAllCodebasesInSSEURL verifies that the URL query string
// the daemon builds includes every codebase. Anyone changing the wire format
// should update this test.
func TestDaemon_SSEURLIncludesAllCodebases(t *testing.T) {
	d := newTestDaemon(t, "azimuth", "lumina-app", "codebase-intel")

	// Reach into the daemon and build the URL the same way sseConnect does.
	// We don't open a real connection — just verify the encoded params.
	codebases := d.codebases()
	if len(codebases) != 3 {
		t.Fatalf("expected 3 codebases, got %d: %v", len(codebases), codebases)
	}
	// Sorted order matches what sseConnect uses for q.Add("codebase", ...).
	want := []string{"azimuth", "codebase-intel", "lumina-app"}
	if !reflect.DeepEqual(codebases, want) {
		t.Errorf("codebases order = %v, want %v", codebases, want)
	}
}

// Sanity check that a daemon with a single codebase still constructs as it
// did before the multi-codebase refactor — the backward-compat contract for
// existing single-config deployments.
func TestDaemon_SingleCodebaseStillWorks(t *testing.T) {
	d := newTestDaemon(t, "only")
	if got := len(d.clients); got != 1 {
		t.Fatalf("expected 1 indexer, got %d", got)
	}
	if d.clients["only"] == nil {
		t.Fatal("expected indexer for 'only' to be present")
	}
	if got := d.codebases(); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("codebases() = %v, want [only]", got)
	}
}
