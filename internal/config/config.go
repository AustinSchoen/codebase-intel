package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Codebase  CodebaseConfig  `yaml:"codebase"`
	Indexing  IndexingConfig  `yaml:"indexing"`
	Embedding EmbeddingConfig `yaml:"embeddings"`
	Vector    VectorConfig    `yaml:"vector_store"`
	Metadata  MetadataConfig  `yaml:"metadata_store"`
	Summaries SummaryConfig   `yaml:"summaries"`
}

type CodebaseConfig struct {
	Path            string   `yaml:"path"`
	Name            string   `yaml:"name"`
	Languages       []string `yaml:"languages"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
}

type IndexingConfig struct {
	ChunkMaxLines     int  `yaml:"chunk_max_lines"`
	ChunkOverlapLines int  `yaml:"chunk_overlap_lines"`
	ContextPrefix     bool `yaml:"context_prefix"`
	BatchSize         int  `yaml:"batch_size"`
	ConcurrentReqs    int  `yaml:"concurrent_requests"`
	Incremental       bool `yaml:"incremental"`
}

type EmbeddingConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	APIKeyEnv  string `yaml:"api_key_env"`
	Dimensions int    `yaml:"dimensions"`
}

type VectorConfig struct {
	Provider         string `yaml:"provider"`
	URL              string `yaml:"url"`
	CollectionPrefix string `yaml:"collection_prefix"`
}

type MetadataConfig struct {
	Provider       string `yaml:"provider"`
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	Database       string `yaml:"database"`
	UserEnv        string `yaml:"user_env"`
	PasswordEnv    string `yaml:"password_env"`
	MaxConnections int    `yaml:"max_connections"`
}

type SummaryConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Provider        string `yaml:"provider"`
	Model           string `yaml:"model"`
	APIKeyEnv       string `yaml:"api_key_env"`
	TopClasses      int    `yaml:"top_classes"`
	RegenerateOnChg bool   `yaml:"regenerate_on_change"`
}

func Load() (*Config, error) {
	cfgPath := os.Getenv("CODEBASE_INTEL_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", cfgPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Indexing.ChunkMaxLines == 0 { cfg.Indexing.ChunkMaxLines = 200 }
	if cfg.Indexing.ChunkOverlapLines == 0 { cfg.Indexing.ChunkOverlapLines = 20 }
	if cfg.Indexing.BatchSize == 0 { cfg.Indexing.BatchSize = 128 }
	if cfg.Indexing.ConcurrentReqs == 0 { cfg.Indexing.ConcurrentReqs = 10 }
	if cfg.Embedding.Dimensions == 0 { cfg.Embedding.Dimensions = 1024 }
	if cfg.Metadata.MaxConnections == 0 { cfg.Metadata.MaxConnections = 20 }
	if cfg.Summaries.TopClasses == 0 { cfg.Summaries.TopClasses = 1000 }

	return &cfg, nil
}
