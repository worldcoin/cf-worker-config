package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUsageAndValidation(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		env              map[string]string
		wantErr          string
		wantStdout       []string
		wantStderr       []string
		wantErrExcludes  []string
		wantStdoutAbsent []string
		wantStderrAbsent []string
	}{
		{
			name:       "no args prints global usage",
			wantStdout: []string{"Usage:\n  cf-worker-config <command> [flags]", "Commands:"},
		},
		{
			name:       "root help flag prints global usage",
			args:       []string{"--help"},
			wantStdout: []string{"Usage:\n  cf-worker-config <command> [flags]", "Commands:"},
		},
		{
			name:       "help command prints command usage",
			args:       []string{"help", "plan"},
			wantStdout: []string{"Usage:\n  cf-worker-config plan [flags]", `Flags for "plan":`, cloudflareAPIEnvVar},
		},
		{
			name:       "command help flag prints command usage",
			args:       []string{"plan", "--help"},
			wantStdout: []string{"Usage:\n  cf-worker-config plan [flags]", `Flags for "plan":`, cloudflareAPIEnvVar},
		},
		{
			name:            "unknown command does not require token",
			args:            []string{"nope"},
			wantErr:         "unknown command: nope",
			wantStderr:      []string{"Usage:\n  cf-worker-config <command> [flags]", "Commands:"},
			wantErrExcludes: []string{"environment variable is required"},
		},
		{
			name:            "known command requires worker before token",
			args:            []string{"plan"},
			wantErr:         "--worker is required",
			wantStderr:      []string{"Usage:\n  cf-worker-config plan [flags]", `Flags for "plan":`},
			wantErrExcludes: []string{"environment variable is required"},
		},
		{
			name:       "known command requires token after worker",
			args:       []string{"plan", "--worker", "tfh-website-dev"},
			wantErr:    cloudflareAPIEnvVar + " environment variable is required",
			wantStderr: nil,
		},
		{
			name:            "bad flag does not require token",
			args:            []string{"plan", "--badflag"},
			wantErr:         "flag provided but not defined: -badflag",
			wantStderr:      []string{"flag provided but not defined: -badflag", `Flags for "plan":`},
			wantErrExcludes: []string{"environment variable is required"},
		},
		{
			name:            "help rejects unknown command",
			args:            []string{"help", "nope"},
			wantErr:         "unknown command: nope",
			wantStderr:      []string{"Usage:\n  cf-worker-config <command> [flags]", "Commands:"},
			wantErrExcludes: []string{"environment variable is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			getenv := func(key string) string {
				return tt.env[key]
			}

			err := run(tt.args, &stdout, &stderr, getenv)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("run() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatalf("run() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("run() error = %q, want to contain %q", err, tt.wantErr)
				}
			}

			assertContainsAll(t, "stdout", stdout.String(), tt.wantStdout)
			assertContainsAll(t, "stderr", stderr.String(), tt.wantStderr)
			assertContainsNone(t, "error", errorString(err), tt.wantErrExcludes)
			assertContainsNone(t, "stdout", stdout.String(), tt.wantStdoutAbsent)
			assertContainsNone(t, "stderr", stderr.String(), tt.wantStderrAbsent)
		})
	}
}

func TestResolveBuildRefFromGitHubPullRequestEvent(t *testing.T) {
	t.Chdir(t.TempDir())

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"pull_request": {
			"head": {
				"sha": "head-sha"
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("writing event: %v", err)
	}

	env := map[string]string{
		"GITHUB_HEAD_REF":   "feature-branch",
		"GITHUB_REF_NAME":   "ignored-ref",
		"GITHUB_EVENT_PATH": eventPath,
		"GITHUB_SHA":        "merge-sha",
	}
	getenv := func(key string) string {
		return env[key]
	}
	desired := &Worker{ProductionBranch: "main"}

	if got := resolveBuildBranch(desired, "", getenv); got != "feature-branch" {
		t.Fatalf("resolveBuildBranch() = %q, want feature-branch", got)
	}
	if got := resolveBuildCommitHash("", getenv); got != "head-sha" {
		t.Fatalf("resolveBuildCommitHash() = %q, want head-sha", got)
	}
}

func TestResolveBuildCommitPrefersCheckoutHead(t *testing.T) {
	t.Chdir(t.TempDir())
	runGit(t, "init")
	runGit(t, "config", "user.email", "test@example.com")
	runGit(t, "config", "user.name", "Test User")
	if err := os.WriteFile("README.md", []byte("test\n"), 0o600); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	runGit(t, "add", "README.md")
	runGit(t, "commit", "-m", "initial")
	head := gitCurrentCommitHash()
	if head == "" {
		t.Fatalf("git HEAD is empty")
	}

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"pull_request": {
			"head": {
				"sha": "event-sha"
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("writing event: %v", err)
	}
	env := map[string]string{
		"GITHUB_EVENT_PATH": eventPath,
		"GITHUB_SHA":        "github-sha",
	}
	getenv := func(key string) string {
		return env[key]
	}

	if got := resolveBuildCommitHash("", getenv); got != head {
		t.Fatalf("resolveBuildCommitHash() = %q, want checkout HEAD %q", got, head)
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func assertContainsAll(t *testing.T, name, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("%s = %q, want to contain %q", name, got, want)
		}
	}
}

func assertContainsNone(t *testing.T, name, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if strings.Contains(got, want) {
			t.Fatalf("%s = %q, want not to contain %q", name, got, want)
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
