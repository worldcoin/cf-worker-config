package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		wantWorker string
		assert     func(t *testing.T, cfg *Config, worker *Worker)
		wantErr    string
	}{
		{
			name:       "minimal valid config",
			fixture:    "minimal.json",
			wantWorker: "tfh-website-dev",
			assert: func(t *testing.T, cfg *Config, worker *Worker) {
				t.Helper()
				if cfg.AccountID != "account-123" {
					t.Fatalf("AccountID = %q, want account-123", cfg.AccountID)
				}
				if cfg.Repo != "worldcoin/tfh-website" {
					t.Fatalf("Repo = %q, want worldcoin/tfh-website", cfg.Repo)
				}
				if worker.WorkerName != "tfh-website-dev" {
					t.Fatalf("WorkerName = %q, want tfh-website-dev", worker.WorkerName)
				}
				if worker.Repo != "worldcoin/tfh-website" {
					t.Fatalf("Repo = %q, want worldcoin/tfh-website", worker.Repo)
				}
				if worker.BuildVars["NODE_VERSION"] != "22" {
					t.Fatalf("NODE_VERSION = %q, want 22", worker.BuildVars["NODE_VERSION"])
				}
			},
		},
		{
			name:       "full valid config",
			fixture:    "full.json",
			wantWorker: "tfh-website",
			assert: func(t *testing.T, cfg *Config, worker *Worker) {
				t.Helper()
				if cfg.RepoConnectionUUID != "repo-connection-123" {
					t.Fatalf("Config.RepoConnectionUUID = %q, want repo-connection-123", cfg.RepoConnectionUUID)
				}
				if worker.RepoConnectionUUID != "repo-connection-123" {
					t.Fatalf("RepoConnectionUUID = %q, want repo-connection-123", worker.RepoConnectionUUID)
				}
				if worker.ProviderAccountID != "42" {
					t.Fatalf("ProviderAccountID = %q, want 42", worker.ProviderAccountID)
				}
				if worker.BuildTokenName != "tfh-build-token" {
					t.Fatalf("BuildTokenName = %q, want tfh-build-token", worker.BuildTokenName)
				}
				if worker.BuildCachingEnabled == nil || !*worker.BuildCachingEnabled {
					t.Fatalf("BuildCachingEnabled = %v, want true", worker.BuildCachingEnabled)
				}
				if len(worker.PathIncludes) != 2 || worker.PathIncludes[1] != "scripts/**" {
					t.Fatalf("PathIncludes = %v, want app and scripts globs", worker.PathIncludes)
				}
				if !worker.Preview.Enabled {
					t.Fatalf("Preview.Enabled = false, want true")
				}
				if worker.Preview.BuildVars["NEXT_PUBLIC_APP_ENV"] != "preview" {
					t.Fatalf("preview NEXT_PUBLIC_APP_ENV = %q, want preview", worker.Preview.BuildVars["NEXT_PUBLIC_APP_ENV"])
				}
				if len(worker.Preview.BuildSecrets) != 1 || worker.Preview.BuildSecrets[0] != "PREVIEW_ONLY_SECRET" {
					t.Fatalf("Preview.BuildSecrets = %v, want [PREVIEW_ONLY_SECRET]", worker.Preview.BuildSecrets)
				}
			},
		},
		{
			name:    "invalid json",
			fixture: "invalid-json.json",
			wantErr: "parsing config",
		},
		{
			name:    "missing account id",
			fixture: "missing-account.json",
			wantErr: "account_id is required",
		},
		{
			name:    "missing repo",
			fixture: "missing-repo.json",
			wantErr: "repo is required",
		},
		{
			name:    "worker repo field rejected",
			fixture: "worker-repo-field.json",
			wantErr: `json: unknown field "repo"`,
		},
		{
			name:    "worker name field rejected",
			fixture: "worker-name-field.json",
			wantErr: `json: unknown field "worker_name"`,
		},
		{
			name:    "missing preview command",
			fixture: "missing-preview-command.json",
			wantErr: `worker "tfh-website-dev": preview.command is required when preview.enabled=true`,
		},
		{
			name:    "unknown field",
			fixture: "unknown-field.json",
			wantErr: `json: unknown field "unknown_field"`,
		},
		{
			name:    "path traversal",
			fixture: "../../go.mod",
			wantErr: "reading config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(filepath.Join("testdata", tt.fixture))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %q, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			worker, err := cfg.GetWorker(tt.wantWorker)
			if err != nil {
				t.Fatalf("GetWorker(%q) error = %v", tt.wantWorker, err)
			}
			tt.assert(t, cfg, worker)
		})
	}
}

func TestGetWorker(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "minimal.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	tests := []struct {
		name    string
		worker  string
		wantErr string
	}{
		{
			name:   "existing worker",
			worker: "tfh-website-dev",
		},
		{
			name:    "missing worker",
			worker:  "tfh-website",
			wantErr: `worker "tfh-website" not found in config`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cfg.GetWorker(tt.worker)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("GetWorker() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("GetWorker() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("GetWorker() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}
