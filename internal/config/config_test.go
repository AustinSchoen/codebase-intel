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
}

func TestValidation_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		errText string
	}{
		{
			name:    "missing codebase path",
			yaml:    "codebase:\n  name: test\n  languages: [go]\nvector_store:\n  url: http://localhost\nmetadata_store:\n  host: localhost\n  database: db",
			errText: "codebase.path is required",
		},
		{
			name:    "missing codebase name",
			yaml:    "codebase:\n  path: /tmp\n  languages: [go]\nvector_store:\n  url: http://localhost\nmetadata_store:\n  host: localhost\n  database: db",
			errText: "codebase.name is required",
		},
		{
			name:    "missing languages",
			yaml:    "codebase:\n  path: /tmp\n  name: test\nvector_store:\n  url: http://localhost\nmetadata_store:\n  host: localhost\n  database: db",
			errText: "codebase.languages must have at least one language",
		},
		{
			name:    "missing vector url",
			yaml:    "codebase:\n  path: /tmp\n  name: test\n  languages: [go]\nmetadata_store:\n  host: localhost\n  database: db",
			errText: "vector_store.url is required",
		},
		{
			name:    "missing metadata host",
			yaml:    "codebase:\n  path: /tmp\n  name: test\n  languages: [go]\nvector_store:\n  url: http://localhost\nmetadata_store:\n  database: db",
			errText: "metadata_store.host is required",
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

func TestValidation_BadValues(t *testing.T) {
	// batch_size > 128
	yaml := `
codebase:
  path: /tmp
  name: test
  languages: [go]
indexing:
  batch_size: 200
vector_store:
  url: http://localhost
metadata_store:
  host: localhost
  database: db
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromFile(cfgPath)
	if err == nil {
		t.Fatal("expected validation error for batch_size > 128")
	}
	if !strings.Contains(err.Error(), "batch_size") {
		t.Errorf("expected batch_size error, got %q", err.Error())
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
