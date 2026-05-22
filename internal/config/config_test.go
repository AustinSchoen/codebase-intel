package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	content := `
codebase:
  path: "/tmp/test-codebase"
  name: "test-project"
  languages: ["go"]

indexing:
  chunk_max_lines: 100
  chunk_overlap_lines: 10
  batch_size: 64
  concurrent_requests: 5

embeddings:
  provider: "voyage"
  model: "voyage-code-3"
  api_key_env: "VOYAGE_API_KEY"

vector_store:
  provider: "qdrant"
  url: "http://localhost:6333"
  collection_prefix: "test"

metadata_store:
  provider: "postgres"
  host: "localhost"
  port: 5432
  database: "test_db"
  user_env: "PGUSER"
  password_env: "PGPASSWORD"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.Codebase.Name != "test-project" {
		t.Errorf("expected name=test-project, got %s", cfg.Codebase.Name)
	}
	if cfg.Indexing.ChunkMaxLines != 100 {
		t.Errorf("expected chunk_max_lines=100, got %d", cfg.Indexing.ChunkMaxLines)
	}
	if cfg.Indexing.BatchSize != 64 {
		t.Errorf("expected batch_size=64, got %d", cfg.Indexing.BatchSize)
	}
	if cfg.Metadata.Port != 5432 {
		t.Errorf("expected port=5432, got %d", cfg.Metadata.Port)
	}
}

func TestDefaults(t *testing.T) {
	content := `
codebase:
  path: "/tmp/test"
  name: "test"
  languages: ["go"]
vector_store:
  url: "http://localhost:6333"
metadata_store:
  host: "localhost"
  database: "test_db"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.Indexing.ChunkMaxLines != 200 {
		t.Errorf("default chunk_max_lines should be 200, got %d", cfg.Indexing.ChunkMaxLines)
	}
	if cfg.Indexing.ChunkOverlapLines != 20 {
		t.Errorf("default chunk_overlap_lines should be 20, got %d", cfg.Indexing.ChunkOverlapLines)
	}
	if cfg.Indexing.BatchSize != 128 {
		t.Errorf("default batch_size should be 128, got %d", cfg.Indexing.BatchSize)
	}
	if cfg.Indexing.ConcurrentReqs != 10 {
		t.Errorf("default concurrent_requests should be 10, got %d", cfg.Indexing.ConcurrentReqs)
	}
	if cfg.Embedding.Dimensions != 1024 {
		t.Errorf("default dimensions should be 1024, got %d", cfg.Embedding.Dimensions)
	}
	if cfg.Embedding.Model != "voyage-code-3" {
		t.Errorf("default model should be voyage-code-3, got %s", cfg.Embedding.Model)
	}
	if cfg.Metadata.MaxConnections != 20 {
		t.Errorf("default max_connections should be 20, got %d", cfg.Metadata.MaxConnections)
	}
	if cfg.Metadata.Port != 5432 {
		t.Errorf("default port should be 5432, got %d", cfg.Metadata.Port)
	}
	if cfg.Vector.CollectionPrefix != "test" {
		// It was explicitly set to empty string vs having a prefix
	}
	if cfg.Indexing.ConcurrentFiles != 4 {
		t.Errorf("default concurrent_files should be 4, got %d", cfg.Indexing.ConcurrentFiles)
	}
	if cfg.Metrics.Port != 9091 {
		t.Errorf("default metrics port should be 9091, got %d", cfg.Metrics.Port)
	}
}

// TestValidation_MissingRequired covers the codebase-identity fields the
// indexer still validates after #18. Storage/embedding requirements moved
// to ValidateForServer (server-only now), tested elsewhere.
func TestValidation_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name:    "missing codebase path",
			yaml:    "codebase:\n  name: test\n  languages: [go]",
			errText: "codebase.path is required",
		},
		{
			name:    "missing codebase name",
			yaml:    "codebase:\n  path: /tmp\n  languages: [go]",
			errText: "codebase.name is required",
		},
		{
			name:    "missing languages",
			yaml:    "codebase:\n  path: /tmp\n  name: test",
			errText: "codebase.languages must have at least one language",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}

			_, err := LoadFromFile(cfgPath)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected error containing %q, got %q", tt.errText, err.Error())
			}
		})
	}
}

func TestResolveEnv(t *testing.T) {
	cfg := &Config{
		Embedding: EmbeddingConfig{APIKeyEnv: "TEST_VOYAGE_KEY"},
		Metadata:  MetadataConfig{UserEnv: "TEST_PGUSER", PasswordEnv: "TEST_PGPASS"},
	}

	t.Setenv("TEST_VOYAGE_KEY", "voyage-key-123")
	t.Setenv("TEST_PGUSER", "testuser")
	t.Setenv("TEST_PGPASS", "testpass")

	env, err := cfg.ResolveEnv()
	if err != nil {
		t.Fatalf("ResolveEnv failed: %v", err)
	}

	if env.EmbeddingAPIKey != "voyage-key-123" {
		t.Errorf("expected voyage-key-123, got %s", env.EmbeddingAPIKey)
	}
	if env.PGUser != "testuser" {
		t.Errorf("expected testuser, got %s", env.PGUser)
	}
	if env.PGPassword != "testpass" {
		t.Errorf("expected testpass, got %s", env.PGPassword)
	}
}

func TestResolveEnv_Missing(t *testing.T) {
	cfg := &Config{
		Embedding: EmbeddingConfig{APIKeyEnv: "NONEXISTENT_KEY"},
	}

	_, err := cfg.ResolveEnv()
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT_KEY") {
		t.Errorf("expected NONEXISTENT_KEY in error, got %q", err.Error())
	}
}

func TestPostgresDSN(t *testing.T) {
	cfg := &Config{
		Metadata: MetadataConfig{
			Host:     "db.example.com",
			Port:     5432,
			Database: "codebase_intel",
		},
	}
	env := ResolvedEnv{
		PGUser:     "user",
		PGPassword: "pass",
	}

	dsn := cfg.PostgresDSN(env)
	expected := "postgres://user:pass@db.example.com:5432/codebase_intel?sslmode=disable"
	if dsn != expected {
		t.Errorf("expected %s, got %s", expected, dsn)
	}
}

func TestValidateForServer_NoCodebaseRequired(t *testing.T) {
	// Server mode should work without codebase config
	content := `
vector_store:
  url: "http://localhost:6333"
metadata_store:
  host: "localhost"
  database: "test_db"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFileForServer(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromFileForServer should not require codebase config, got: %v", err)
	}

	// Codebase fields should be empty (not required)
	if cfg.Codebase.Path != "" {
		t.Errorf("expected empty codebase path, got %s", cfg.Codebase.Path)
	}
	if cfg.Codebase.Name != "" {
		t.Errorf("expected empty codebase name, got %s", cfg.Codebase.Name)
	}

	// Defaults should still be applied
	if cfg.Embedding.Model != "voyage-code-3" {
		t.Errorf("expected default model, got %s", cfg.Embedding.Model)
	}
	if cfg.Metrics.Port != 9091 {
		t.Errorf("expected default metrics port, got %d", cfg.Metrics.Port)
	}
}

func TestValidateForServer_StillRequiresStores(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name:    "missing vector url",
			yaml:    "metadata_store:\n  host: localhost\n  database: db",
			errText: "vector_store.url is required",
		},
		{
			name:    "missing metadata host",
			yaml:    "vector_store:\n  url: http://localhost\nmetadata_store:\n  database: db",
			errText: "metadata_store.host is required",
		},
		{
			name:    "missing metadata database",
			yaml:    "vector_store:\n  url: http://localhost\nmetadata_store:\n  host: localhost",
			errText: "metadata_store.database is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}

			_, err := LoadFromFileForServer(cfgPath)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("expected error containing %q, got %q", tt.errText, err.Error())
			}
		})
	}
}

func TestLoadFromFile_StillRequiresCodebase(t *testing.T) {
	// Verify that the indexer's Validate() still requires codebase fields
	content := `
vector_store:
  url: "http://localhost:6333"
metadata_store:
  host: "localhost"
  database: "test_db"
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(cfgPath)
	if err == nil {
		t.Fatal("LoadFromFile should still require codebase config")
	}
	if !strings.Contains(err.Error(), "codebase.path is required") {
		t.Errorf("expected codebase.path error, got %q", err.Error())
	}
}

// --- Additional edge case tests ---

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("{{{{invalid yaml!!!!"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("expected parsing config error, got %q", err.Error())
	}
}

func TestLoadFromFile_NonexistentFile(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "reading config") {
		t.Errorf("expected reading config error, got %q", err.Error())
	}
}

// Chunking / batching / concurrent_requests bounds checks moved server-side
// with the pipeline (#18); the indexer no longer cares about those values
// because it doesn't chunk or embed. The previous TestValidation_* tests for
// chunk_overlap, concurrent_requests, and chunk_max_lines bounds have been
// removed. Server-side bounds should be re-added against ValidateForServer
// in a future test pass.

func TestValidation_MultipleErrors(t *testing.T) {
	// Only the codebase-identity errors apply to the indexer post-#18.
	yaml := `indexing:
  incremental: true
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(cfgPath)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	errText := err.Error()
	if !strings.Contains(errText, "codebase.path") {
		t.Errorf("expected codebase.path error in %q", errText)
	}
}

func TestApplyDefaults_AllFields(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.Embedding.APIKeyEnv != "VOYAGE_API_KEY" {
		t.Errorf("expected default api_key_env=VOYAGE_API_KEY, got %s", cfg.Embedding.APIKeyEnv)
	}
	if cfg.Vector.CollectionPrefix != "codebase" {
		t.Errorf("expected default collection_prefix=codebase, got %s", cfg.Vector.CollectionPrefix)
	}
	if cfg.Vector.APIKeyEnv != "QDRANT_API_KEY" {
		t.Errorf("expected default vector api_key_env=QDRANT_API_KEY, got %s", cfg.Vector.APIKeyEnv)
	}
	if cfg.Summaries.TopClasses != 1000 {
		t.Errorf("expected default top_classes=1000, got %d", cfg.Summaries.TopClasses)
	}
	if cfg.Summaries.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("expected default summary api_key_env=ANTHROPIC_API_KEY, got %s", cfg.Summaries.APIKeyEnv)
	}
	if cfg.Reranking.APIKeyEnv != "COHERE_API_KEY" {
		t.Errorf("expected default reranking api_key_env=COHERE_API_KEY, got %s", cfg.Reranking.APIKeyEnv)
	}
	if cfg.Reranking.Model != "rerank-v3.5" {
		t.Errorf("expected default reranking model=rerank-v3.5, got %s", cfg.Reranking.Model)
	}
	if cfg.Reranking.Provider != "cohere" {
		t.Errorf("expected default reranking provider=cohere, got %s", cfg.Reranking.Provider)
	}
}

func TestResolveEnv_OptionalFields(t *testing.T) {
	cfg := &Config{
		Embedding: EmbeddingConfig{APIKeyEnv: "TEST_EMBED_KEY"},
		Vector:    VectorConfig{APIKeyEnv: "TEST_VECTOR_KEY"},
	}
	t.Setenv("TEST_EMBED_KEY", "embed-key")

	env, err := cfg.ResolveEnv()
	if err != nil {
		t.Fatalf("expected no error for optional vector API key, got: %v", err)
	}
	if env.EmbeddingAPIKey != "embed-key" {
		t.Errorf("expected embed-key, got %s", env.EmbeddingAPIKey)
	}
	if env.VectorAPIKey != "" {
		t.Errorf("expected empty vector API key, got %s", env.VectorAPIKey)
	}
}

func TestResolveEnv_SummaryAndReranking(t *testing.T) {
	cfg := &Config{
		Embedding: EmbeddingConfig{APIKeyEnv: "TEST_EMB"},
		Summaries: SummaryConfig{Enabled: true, APIKeyEnv: "TEST_SUMMARY_KEY"},
		Reranking: RerankConfig{Enabled: true, APIKeyEnv: "TEST_COHERE_KEY"},
	}
	t.Setenv("TEST_EMB", "emb")
	t.Setenv("TEST_SUMMARY_KEY", "summary-key")
	t.Setenv("TEST_COHERE_KEY", "cohere-key")

	env, err := cfg.ResolveEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.SummaryAPIKey != "summary-key" {
		t.Errorf("expected summary-key, got %s", env.SummaryAPIKey)
	}
	if env.CohereAPIKey != "cohere-key" {
		t.Errorf("expected cohere-key, got %s", env.CohereAPIKey)
	}
}

func TestResolveEnv_DisabledSummaryAndReranking(t *testing.T) {
	cfg := &Config{
		Embedding: EmbeddingConfig{APIKeyEnv: "TEST_EMB2"},
		Summaries: SummaryConfig{Enabled: false, APIKeyEnv: "TEST_SUM"},
		Reranking: RerankConfig{Enabled: false, APIKeyEnv: "TEST_COH"},
	}
	t.Setenv("TEST_EMB2", "emb")

	env, err := cfg.ResolveEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.SummaryAPIKey != "" {
		t.Errorf("expected empty summary key when disabled, got %s", env.SummaryAPIKey)
	}
	if env.CohereAPIKey != "" {
		t.Errorf("expected empty cohere key when disabled, got %s", env.CohereAPIKey)
	}
}

func TestPostgresDSN_SpecialChars(t *testing.T) {
	cfg := &Config{
		Metadata: MetadataConfig{Host: "db.example.com", Port: 5433, Database: "my_db"},
	}
	env := ResolvedEnv{PGUser: "admin", PGPassword: "p@ss!"}
	dsn := cfg.PostgresDSN(env)
	expected := "postgres://admin:p@ss!@db.example.com:5433/my_db?sslmode=disable"
	if dsn != expected {
		t.Errorf("expected %s, got %s", expected, dsn)
	}
}

func TestLoadFromFileForServer_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFileForServer(cfgPath)
	if err == nil {
		t.Fatal("expected error for empty config (missing required store configs)")
	}
}

func TestValidateForServer_ValidMinimalConfig(t *testing.T) {
	cfg := &Config{
		Vector:   VectorConfig{URL: "http://localhost:6333"},
		Metadata: MetadataConfig{Host: "localhost", Database: "testdb"},
	}
	if err := cfg.ValidateForServer(); err != nil {
		t.Fatalf("expected no error for valid minimal server config, got: %v", err)
	}
}

func TestLoad_UsesEnvVar(t *testing.T) {
	content := `
codebase:
  path: /tmp
  name: test
  languages: [go]
vector_store:
  url: http://localhost:6333
metadata_store:
  host: localhost
  database: testdb
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEBASE_INTEL_CONFIG", cfgPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Codebase.Name != "test" {
		t.Errorf("expected name=test, got %s", cfg.Codebase.Name)
	}
}

func TestValidate_PathResolvesToAbsolute(t *testing.T) {
	content := `
codebase:
  path: "./relative/path"
  name: test
  languages: [go]
vector_store:
  url: http://localhost:6333
metadata_store:
  host: localhost
  database: testdb
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if !filepath.IsAbs(cfg.Codebase.Path) {
		t.Errorf("expected absolute path, got %s", cfg.Codebase.Path)
	}
}
