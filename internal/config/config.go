package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Codebase  CodebaseConfig  `yaml:"codebase"`
	Indexing  IndexingConfig  `yaml:"indexing"`
	Embedding EmbeddingConfig `yaml:"embeddings"`
	Vector    VectorConfig    `yaml:"vector_store"`
	Metadata  MetadataConfig  `yaml:"metadata_store"`
	Summaries SummaryConfig   `yaml:"summaries"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	Reranking RerankConfig    `yaml:"reranking"`
	Server    ServerConfig    `yaml:"server"`
}

type ServerConfig struct {
	APIKeyEnv string `yaml:"api_key_env"`

	// Discovery controls LAN auto-discovery via mDNS. When Advertise is
	// true (default), the server publishes itself as
	// `_codebase-intel._tcp.local`, so indexer hosts running setup-indexer.sh
	// can find it without manual URL entry. AdvertiseToken (default true)
	// additionally puts the bearer token in the mDNS TXT record so indexers
	// can auto-configure auth too — disable on LANs with untrusted devices.
	Discovery DiscoveryConfig `yaml:"discovery"`
}

type DiscoveryConfig struct {
	Advertise      *bool `yaml:"advertise,omitempty"`       // pointer so unset → default true
	AdvertiseToken *bool `yaml:"advertise_token,omitempty"` // pointer so unset → default true
}

// AdvertiseEnabled reports whether the server should publish itself on mDNS.
// Defaults to true when unset.
func (d DiscoveryConfig) AdvertiseEnabled() bool {
	if d.Advertise == nil {
		return true
	}
	return *d.Advertise
}

// TokenAdvertiseEnabled reports whether the bearer token should be included
// in the mDNS TXT record. Defaults to true when unset.
func (d DiscoveryConfig) TokenAdvertiseEnabled() bool {
	if d.AdvertiseToken == nil {
		return true
	}
	return *d.AdvertiseToken
}

type RerankConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Provider  string `yaml:"provider"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
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
	ConcurrentFiles   int  `yaml:"concurrent_files"`
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
	APIKeyEnv        string `yaml:"api_key_env"`
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

type MetricsConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

// Load reads config from CODEBASE_INTEL_CONFIG env var or config.yaml.
func Load() (*Config, error) {
	cfgPath := os.Getenv("CODEBASE_INTEL_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	return LoadFromFile(cfgPath)
}

// LoadFromFile reads config from a specific path.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Indexing.ChunkMaxLines == 0 {
		c.Indexing.ChunkMaxLines = 200
	}
	if c.Indexing.ChunkOverlapLines == 0 {
		c.Indexing.ChunkOverlapLines = 20
	}
	if c.Indexing.BatchSize == 0 {
		c.Indexing.BatchSize = 128
	}
	if c.Indexing.ConcurrentReqs == 0 {
		c.Indexing.ConcurrentReqs = 10
	}
	if c.Indexing.ConcurrentFiles == 0 {
		c.Indexing.ConcurrentFiles = 4
	}
	if c.Embedding.Dimensions == 0 {
		c.Embedding.Dimensions = 1024
	}
	if c.Embedding.Model == "" {
		c.Embedding.Model = "voyage-code-3"
	}
	if c.Embedding.APIKeyEnv == "" {
		c.Embedding.APIKeyEnv = "VOYAGE_API_KEY"
	}
	if c.Metadata.MaxConnections == 0 {
		c.Metadata.MaxConnections = 20
	}
	if c.Metadata.Port == 0 {
		c.Metadata.Port = 5432
	}
	if c.Summaries.TopClasses == 0 {
		c.Summaries.TopClasses = 1000
	}
	if c.Summaries.Model == "" {
		c.Summaries.Model = "claude-sonnet-4-5-20250929"
	}
	if c.Summaries.APIKeyEnv == "" {
		c.Summaries.APIKeyEnv = "ANTHROPIC_API_KEY"
	}
	if c.Metrics.Port == 0 {
		c.Metrics.Port = 9091
	}
	if c.Vector.CollectionPrefix == "" {
		c.Vector.CollectionPrefix = "codebase"
	}
	if c.Vector.APIKeyEnv == "" {
		c.Vector.APIKeyEnv = "QDRANT_API_KEY"
	}
	if c.Reranking.APIKeyEnv == "" {
		c.Reranking.APIKeyEnv = "COHERE_API_KEY"
	}
	if c.Reranking.Model == "" {
		c.Reranking.Model = "rerank-v3.5"
	}
	if c.Reranking.Provider == "" {
		c.Reranking.Provider = "cohere"
	}
}

// Validate checks codebase-config fields that the (now thin-client) indexer
// daemon cares about: codebase identity and the walk parameters. Storage
// (vector_store, metadata_store) and embedding configs are server-only since
// #18 and are not validated here — the indexer ignores them if present.
//
// The chunking knobs (chunk_max_lines, batch_size, etc.) likewise moved
// server-side; only `incremental` survives as an indexer-side flag because
// the daemon controls the per-request incremental bit it sends to the server.
func (c *Config) Validate() error {
	var errs []string

	if c.Codebase.Path == "" {
		errs = append(errs, "codebase.path is required")
	} else {
		absPath, err := filepath.Abs(c.Codebase.Path)
		if err == nil {
			c.Codebase.Path = absPath
		}
	}
	if c.Codebase.Name == "" {
		errs = append(errs, "codebase.name is required")
	}
	if len(c.Codebase.Languages) == 0 {
		errs = append(errs, "codebase.languages must have at least one language")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ValidateForServer checks config for server mode (multi-codebase).
// Does not require codebase.path, codebase.name, or indexing config.
func (c *Config) ValidateForServer() error {
	var errs []string

	if c.Vector.URL == "" {
		errs = append(errs, "vector_store.url is required")
	}

	if c.Metadata.Host == "" {
		errs = append(errs, "metadata_store.host is required")
	}
	if c.Metadata.Database == "" {
		errs = append(errs, "metadata_store.database is required")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// LoadFromFileForServer reads config for server mode (no codebase config required).
func LoadFromFileForServer(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.ValidateForServer(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return &cfg, nil
}

// ResolvedEnv holds the actual values from environment variables.
type ResolvedEnv struct {
	EmbeddingAPIKey string
	VectorAPIKey    string
	PGUser          string
	PGPassword      string
	SummaryAPIKey   string
	CohereAPIKey    string
}

// ResolveEnv resolves environment variable references in the config.
func (c *Config) ResolveEnv() (ResolvedEnv, error) {
	var env ResolvedEnv
	var errs []string

	env.EmbeddingAPIKey = os.Getenv(c.Embedding.APIKeyEnv)
	if env.EmbeddingAPIKey == "" {
		errs = append(errs, fmt.Sprintf("env var %s not set (embeddings.api_key_env)", c.Embedding.APIKeyEnv))
	}

	// Vector store API key (optional)
	if c.Vector.APIKeyEnv != "" {
		env.VectorAPIKey = os.Getenv(c.Vector.APIKeyEnv)
	}

	if c.Metadata.UserEnv != "" {
		env.PGUser = os.Getenv(c.Metadata.UserEnv)
		if env.PGUser == "" {
			errs = append(errs, fmt.Sprintf("env var %s not set (metadata_store.user_env)", c.Metadata.UserEnv))
		}
	}
	if c.Metadata.PasswordEnv != "" {
		env.PGPassword = os.Getenv(c.Metadata.PasswordEnv)
		if env.PGPassword == "" {
			errs = append(errs, fmt.Sprintf("env var %s not set (metadata_store.password_env)", c.Metadata.PasswordEnv))
		}
	}

	if c.Summaries.Enabled && c.Summaries.APIKeyEnv != "" {
		env.SummaryAPIKey = os.Getenv(c.Summaries.APIKeyEnv)
	}

	if c.Reranking.Enabled && c.Reranking.APIKeyEnv != "" {
		env.CohereAPIKey = os.Getenv(c.Reranking.APIKeyEnv)
	}

	if len(errs) > 0 {
		return env, fmt.Errorf("missing env vars: %s", strings.Join(errs, "; "))
	}
	return env, nil
}

// PostgresDSN builds a connection string from config + resolved env.
func (c *Config) PostgresDSN(env ResolvedEnv) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		env.PGUser, env.PGPassword,
		c.Metadata.Host, c.Metadata.Port, c.Metadata.Database)
}
