package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Config is the top-level configuration for cf-worker-config.
type Config struct {
	Schema    string            `json:"$schema,omitempty"`
	AccountID string            `json:"account_id"`
	Workers   map[string]Worker `json:"workers"`
}

// Worker describes the desired Workers Builds configuration for a single worker.
type Worker struct {
	WorkerName          string            `json:"worker_name"`
	Repo                string            `json:"repo"`
	RepoConnectionUUID  string            `json:"repo_connection_uuid"`
	ProviderType        string            `json:"provider_type"`
	ProviderAccountID   string            `json:"provider_account_id"`
	ProviderAccountName string            `json:"provider_account_name"`
	RepoID              string            `json:"repo_id"`
	RepoName            string            `json:"repo_name"`
	BuildTokenUUID      string            `json:"build_token_uuid"`
	BuildTokenName      string            `json:"build_token_name"`
	ProductionBranch    string            `json:"production_branch"`
	RootDirectory       string            `json:"root_directory"`
	PathIncludes        []string          `json:"path_includes"`
	PathExcludes        []string          `json:"path_excludes"`
	BuildCachingEnabled *bool             `json:"build_caching_enabled"`
	BuildCommand        string            `json:"build_command"`
	DeployCommand       string            `json:"deploy_command"`
	Preview             Preview           `json:"preview"`
	BuildVars           map[string]string `json:"build_vars"`
	BuildSecrets        []string          `json:"build_secrets"`
	RuntimeSecrets      []string          `json:"runtime_secrets"`
}

// Preview configures non-production branch builds.
type Preview struct {
	Enabled      bool              `json:"enabled"`
	Command      string            `json:"command"`
	BuildVars    map[string]string `json:"build_vars"`
	BuildSecrets []string          `json:"build_secrets"`
}

// Load reads and parses a cf-worker-config.json file from the current directory tree.
func Load(configPath string) (*Config, error) {
	if filepath.IsAbs(configPath) {
		return nil, fmt.Errorf("config path must be relative: %q", configPath)
	}

	root, err := os.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("opening config root: %w", err)
	}
	defer func() {
		_ = root.Close()
	}()

	file, err := root.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	var cfg Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
		return nil, fmt.Errorf("parsing config: multiple JSON values")
	}

	if cfg.AccountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}

	for name, w := range cfg.Workers {
		if w.WorkerName == "" {
			return nil, fmt.Errorf("worker %q: worker_name is required", name)
		}
		if w.WorkerName != name {
			return nil, fmt.Errorf("worker %q: worker_name %q must match the map key", name, w.WorkerName)
		}
		if w.Repo == "" {
			return nil, fmt.Errorf("worker %q: repo is required", name)
		}
		if w.ProductionBranch == "" {
			return nil, fmt.Errorf("worker %q: production_branch is required", name)
		}
		if w.BuildCommand == "" {
			return nil, fmt.Errorf("worker %q: build_command is required", name)
		}
		if w.DeployCommand == "" {
			return nil, fmt.Errorf("worker %q: deploy_command is required", name)
		}
		if w.Preview.Enabled && w.Preview.Command == "" {
			return nil, fmt.Errorf("worker %q: preview.command is required when preview.enabled=true", name)
		}
	}

	return &cfg, nil
}

// GetWorker returns the configuration for a specific worker, or an error if not found.
func (c *Config) GetWorker(name string) (*Worker, error) {
	w, ok := c.Workers[name]
	if !ok {
		available := make([]string, 0, len(c.Workers))
		for k := range c.Workers {
			available = append(available, k)
		}
		return nil, fmt.Errorf("worker %q not found in config (available: %v)", name, available)
	}
	return &w, nil
}
