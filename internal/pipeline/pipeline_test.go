package pipeline

import (
	"sort"
	"sync"
	"testing"
)

// TestDetectLanguage covers the file-extension → language-tag mapping the
// server uses to dispatch to per-language parsers. The map is small enough
// to test exhaustively; doing so locks in the wire of "what extensions get
// indexed" since the indexer-side filter delegates the same decisions.
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// Each supported extension, both bare and inside a directory.
		{"main.go", "go"},
		{"pkg/sub/main.go", "go"},
		{"setup.py", "python"},
		{"a.ts", "typescript"},
		{"a.tsx", "typescript"},
		{"a.js", "javascript"},
		{"a.jsx", "javascript"},
		{"lib.rs", "rust"},
		{"main.c", "c"},
		{"main.h", "c"},
		{"main.cpp", "cpp"},
		{"main.cc", "cpp"},
		{"main.hpp", "cpp"},
		{"Main.kt", "kotlin"},
		{"build.kts", "kotlin"},
		{"App.swift", "swift"},
		{"page.dart", "dart"},

		// Case insensitivity: extension is normalized via strings.ToLower.
		{"main.GO", "go"},
		{"App.SWIFT", "swift"},

		// Unsupported / empty.
		{"README.md", ""},
		{"Makefile", ""},
		{"binary.exe", ""},
		{"", ""},
		{"no-extension", ""},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := DetectLanguage(tc.path)
			if got != tc.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestExtractName covers the qualified-name → short-name pull. The result
// is what shows up as the unqualified symbol name in Postgres rows, so
// regressions here would corrupt symbol search.
func TestExtractName(t *testing.T) {
	cases := []struct {
		qualified string
		want      string
	}{
		{"Run", "Run"},                  // already unqualified
		{"Server.Run", "Run"},           // one level of qualification
		{"pkg.Server.Run", "Run"},       // multiple — last segment wins
		{"main.NewServer", "NewServer"}, // function under a package
		{"", ""},                        // empty edge
		{".", ""},                       // single dot only
		{"a.b.c.d.e.f", "f"},            // deep chain
	}

	for _, tc := range cases {
		t.Run(tc.qualified, func(t *testing.T) {
			got := extractName(tc.qualified)
			if got != tc.want {
				t.Errorf("extractName(%q) = %q, want %q", tc.qualified, got, tc.want)
			}
		})
	}
}

// TestResolveTarget covers the relationship-target resolution rules used by
// FinalizeRelationships. The three rules, in order:
//
//  1. Exact qualified-name match wins
//  2. Pointer-prefix strip ("*Foo" → "Foo") is tried before name fallback
//  3. Name-only fallback uses the first registered ID (intentional but
//     ambiguous when multiple symbols share a name)
func TestResolveTarget(t *testing.T) {
	qualifiedToID := map[string]string{
		"pkg.Foo":     "id-foo",
		"pkg.Bar":     "id-bar",
		"otherpkg.Foo": "id-foo-2",
	}
	nameToIDs := map[string][]string{
		"Foo":     {"id-foo", "id-foo-2"}, // ambiguous: two pkgs export "Foo"
		"Bar":     {"id-bar"},
		"Unknown": nil,
	}

	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"qualified match", "pkg.Foo", "id-foo"},
		{"qualified match other pkg", "otherpkg.Foo", "id-foo-2"},
		{"pointer-prefix strip then qualified", "*pkg.Bar", "id-bar"},

		// Pointer-prefix strip only retries the qualified lookup. The
		// name-only fallback uses the original target verbatim (still
		// includes the "*"), so "*Foo" doesn't resolve to "Foo" via the
		// name map. This is a documented limitation — Go's "*pkg.Foo"
		// works because pkg.Foo is in qualifiedToID; bare "*Foo" doesn't.
		{"pointer-prefix on unqualified does NOT fall through to name map", "*Foo", ""},

		{"name-only fallback picks first ID", "Foo", "id-foo"},
		{"unique name resolves", "Bar", "id-bar"},
		{"unknown target", "DoesNotExist", ""},
		{"empty target", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTarget(tc.target, qualifiedToID, nameToIDs)
			if got != tc.want {
				t.Errorf("resolveTarget(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

// TestRelBuffer_AppendAndDrain covers basic accumulation + drain semantics.
func TestRelBuffer_AppendAndDrain(t *testing.T) {
	b := NewRelBuffer()

	// Empty Drain returns nil — not an empty non-nil slice.
	if got := b.Drain(); got != nil {
		t.Errorf("empty Drain() = %v, want nil", got)
	}

	r1 := []RawRelationship{
		{SourceQualified: "a.A", TargetName: "B", Kind: "calls"},
		{SourceQualified: "a.A", TargetName: "C", Kind: "calls"},
	}
	r2 := []RawRelationship{
		{SourceQualified: "b.X", TargetName: "Y", Kind: "references"},
	}
	b.Append(r1)
	b.Append(r2)

	got := b.Drain()
	if len(got) != 3 {
		t.Fatalf("Drain returned %d rels, want 3", len(got))
	}

	// Drain resets the buffer.
	if remaining := b.Drain(); remaining != nil {
		t.Errorf("second Drain() = %v, want nil (buffer should be empty)", remaining)
	}

	// Appending nil/empty is a no-op (used as an explicit guard).
	b.Append(nil)
	b.Append([]RawRelationship{})
	if got := b.Drain(); got != nil {
		t.Errorf("after nil/empty Append, Drain() = %v, want nil", got)
	}
}

// TestRelBuffer_ConcurrentAppend exercises the lock around Append + Drain.
// Concurrent uploads from one /mcp/indexer/files request can race here in
// production, so the buffer must tolerate parallel Append calls.
//
// Run with `go test -race ./internal/pipeline/...` to make this assertion
// meaningful — the race detector flags the bug if the mutex is ever
// removed.
func TestRelBuffer_ConcurrentAppend(t *testing.T) {
	b := NewRelBuffer()

	const (
		workers   = 8
		perWorker = 100
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				b.Append([]RawRelationship{
					{SourceQualified: "src", TargetName: "tgt", Kind: "calls", Line: workerID*1000 + i},
				})
			}
		}(w)
	}
	wg.Wait()

	got := b.Drain()
	if want := workers * perWorker; len(got) != want {
		t.Fatalf("after concurrent appends, len(rels) = %d, want %d", len(got), want)
	}

	// All line numbers should be unique (no lost writes from a missing
	// lock). Sort + dedup.
	lines := make([]int, len(got))
	for i, r := range got {
		lines[i] = r.Line
	}
	sort.Ints(lines)
	for i := 1; i < len(lines); i++ {
		if lines[i] == lines[i-1] {
			t.Fatalf("duplicate line %d at index %d — indicates a lost or duplicated Append", lines[i], i)
		}
	}
}
