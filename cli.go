package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	defaultConfigPath         = "cf-worker-config.json"
	cloudflareAPIEnvVar       = "CLOUDFLARE_API_TOKEN"
	cloudflareAccountIDEnvVar = "CLOUDFLARE_ACCOUNT_ID"
)

// Run parses arguments and dispatches to the appropriate command.
func Run(args []string) error {
	return run(args, os.Stdout, os.Stderr, os.Getenv)
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(args) == 0 {
		printGlobalUsage(stdout)
		return nil
	}

	cmd := args[0]
	if isHelpArg(cmd) {
		printGlobalUsage(stdout)
		return nil
	}
	if cmd == "help" {
		return printHelp(args[1:], stdout, stderr)
	}
	if !isKnownCommand(cmd) {
		printGlobalUsage(stderr)
		return fmt.Errorf("unknown command: %s", cmd)
	}

	fs, flags := newCommandFlagSet(cmd, stderr)
	if containsHelpArg(args[1:]) {
		fs.SetOutput(stdout)
		fs.Usage()
		return nil
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *flags.WorkerKey == "" {
		fs.Usage()
		return fmt.Errorf("--worker is required")
	}

	token := getenv(cloudflareAPIEnvVar)
	if token == "" {
		return fmt.Errorf("%s environment variable is required", cloudflareAPIEnvVar)
	}

	ctx := context.Background()

	if cmd == "get-repo-connection" {
		accountID := strings.TrimSpace(*flags.AccountID)
		if accountID == "" {
			accountID = strings.TrimSpace(getenv(cloudflareAccountIDEnvVar))
		}
		if accountID == "" {
			return fmt.Errorf("%s environment variable or --account-id is required", cloudflareAccountIDEnvVar)
		}
		rec := New(NewClient(token, accountID))
		desired := &Worker{
			WorkerName:       *flags.WorkerKey,
			ProductionBranch: *flags.ProductionBranch,
		}
		uuid, err := rec.RepoConnectionUUID(ctx, desired)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "repo_connection_uuid: %s\n", uuid)
		_, _ = fmt.Fprintf(stdout, "\"repo_connection_uuid\": %q\n", uuid)
		return nil
	}

	cfg, err := Load(*flags.ConfigPath)
	if err != nil {
		return err
	}

	client := NewClient(token, cfg.AccountID)
	rec := New(client)

	switch cmd {
	case "plan":
		desired, err := cfg.GetWorker(*flags.WorkerKey)
		if err != nil {
			return err
		}
		plan, err := rec.Plan(ctx, desired)
		if err != nil {
			return err
		}
		plan.Print()
		if *flags.FailOnDrift && plan.HasChanges() {
			return fmt.Errorf("drift detected: %d change(s)", len(plan.Actions))
		}
		return nil

	case "apply":
		desired, err := cfg.GetWorker(*flags.WorkerKey)
		if err != nil {
			return err
		}
		plan, err := rec.Plan(ctx, desired)
		if err != nil {
			return err
		}
		plan.Print()
		fmt.Println("\nApplying...")
		return rec.ApplyWithOptions(ctx, desired, plan, ApplyOptions{
			TriggerBuild:    *flags.TriggerBuild,
			BuildBranch:     resolveBuildBranch(desired, *flags.BuildBranch, getenv),
			BuildCommitHash: resolveBuildCommitHash(*flags.BuildCommit, getenv),
		})

	case "status":
		desired, err := cfg.GetWorker(*flags.WorkerKey)
		if err != nil {
			return err
		}
		return rec.Status(ctx, desired.WorkerName)

	case "sync-build-vars":
		desired, err := cfg.GetWorker(*flags.WorkerKey)
		if err != nil {
			return err
		}
		return rec.SyncBuildVars(ctx, desired)

	case "sync-runtime-secrets":
		desired, err := cfg.GetWorker(*flags.WorkerKey)
		if err != nil {
			return err
		}
		return rec.syncRuntimeSecrets(ctx, desired, runtimeSecretSyncOptions{
			requireAllSecrets: true,
			refreshExisting:   true,
			prune:             *flags.Prune,
		})

	}

	return fmt.Errorf("unhandled command: %s", cmd)
}

type commandFlags struct {
	ConfigPath       *string
	WorkerKey        *string
	FailOnDrift      *bool
	TriggerBuild     *bool
	BuildBranch      *string
	BuildCommit      *string
	AccountID        *string
	ProductionBranch *string
	Prune            *bool
}

func newCommandFlagSet(cmd string, output io.Writer) (*flag.FlagSet, commandFlags) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(output)
	flags := commandFlags{
		ConfigPath:  fs.String("config", defaultConfigPath, "path to config file"),
		WorkerKey:   fs.String("worker", "", "worker key from config, or worker name for get-repo-connection"),
		FailOnDrift: fs.Bool("fail-on-drift", false, "exit 1 if plan has changes (for CI drift checks)"),
	}
	switch cmd {
	case "apply":
		flags.TriggerBuild = fs.Bool("trigger-build", true, "queue a Workers Build after a successful apply")
		flags.BuildBranch = fs.String("build-branch", "", "branch to build after apply (default: GitHub Actions/git branch, then production_branch)")
		flags.BuildCommit = fs.String("build-commit", "", "commit hash to build after apply (default: GitHub Actions/git SHA when available)")
	case "sync-runtime-secrets":
		flags.Prune = fs.Bool("prune", false, "delete runtime secrets from Cloudflare when they are not listed in config")
	case "get-repo-connection":
		flags.AccountID = fs.String("account-id", "", "Cloudflare account ID (default: CLOUDFLARE_ACCOUNT_ID)")
		flags.ProductionBranch = fs.String("production-branch", "main", "production branch used to identify the production trigger")
	default:
		var (
			disabled bool
			empty    string
		)
		flags.TriggerBuild = &disabled
		flags.BuildBranch = &empty
		flags.BuildCommit = &empty
		flags.AccountID = &empty
		flags.ProductionBranch = &empty
		flags.Prune = &disabled
	}
	fs.Usage = func() {
		printCommandUsage(fs.Output(), cmd)
		writeUsage(fs.Output(), "\nFlags for %q:\n", cmd)
		fs.PrintDefaults()
		printEnvironment(fs.Output())
	}
	return fs, flags
}

func printHelp(args []string, stdout, stderr io.Writer) error {
	switch len(args) {
	case 0:
		printGlobalUsage(stdout)
		return nil
	case 1:
		cmd := args[0]
		if isHelpArg(cmd) {
			printGlobalUsage(stdout)
			return nil
		}
		if !isKnownCommand(cmd) {
			printGlobalUsage(stderr)
			return fmt.Errorf("unknown command: %s", cmd)
		}
		fs, _ := newCommandFlagSet(cmd, stdout)
		fs.Usage()
		return nil
	default:
		printGlobalUsage(stderr)
		return fmt.Errorf("help accepts at most one command")
	}
}

func isKnownCommand(cmd string) bool {
	switch cmd {
	case "plan", "apply", "status", "sync-build-vars", "sync-runtime-secrets", "get-repo-connection":
		return true
	default:
		return false
	}
}

func containsHelpArg(args []string) bool {
	return slices.ContainsFunc(args, isHelpArg)
}

func isHelpArg(arg string) bool {
	switch arg {
	case "-h", "--help":
		return true
	default:
		return false
	}
}

func printGlobalUsage(w io.Writer) {
	writeUsage(w, `cf-worker-config — idempotent reconciler for Cloudflare Worker configuration

Usage:
  cf-worker-config <command> [flags]

Commands:
  plan                 Show what changes would be applied
  apply                Apply changes to match desired state
  status               Show current CF state for a worker
  sync-build-vars      Sync all build vars/secrets to trigger
  sync-runtime-secrets Sync all runtime secrets to worker
  get-repo-connection  Print the repo_connection_uuid for a worker
`)
}

func printCommandUsage(w io.Writer, cmd string) {
	writeUsage(w, `cf-worker-config — idempotent reconciler for Cloudflare Worker configuration

Usage:
  cf-worker-config %s [flags]

Commands:
  plan                 Show what changes would be applied
  apply                Apply changes to match desired state
  status               Show current CF state for a worker
  sync-build-vars      Sync all build vars/secrets to trigger
  sync-runtime-secrets Sync all runtime secrets to worker
  get-repo-connection  Print the repo_connection_uuid for a worker
`, cmd)
}

func printEnvironment(w io.Writer) {
	writeUsage(w, "\nEnvironment:\n  %s  Cloudflare API token (user-scoped, Workers Builds Configuration: Edit + Workers Scripts: Edit)\n  %s  Cloudflare account ID for get-repo-connection when --account-id is not set\n", cloudflareAPIEnvVar, cloudflareAccountIDEnvVar)
}

func resolveBuildBranch(desired *Worker, override string, getenv func(string) string) string {
	if branch := strings.TrimSpace(override); branch != "" {
		return branch
	}
	if branch := gitCurrentBranch(); branch != "" && branch != "HEAD" {
		return branch
	}
	for _, key := range []string{"GITHUB_HEAD_REF", "GITHUB_REF_NAME"} {
		if branch := strings.TrimSpace(getenv(key)); branch != "" {
			return branch
		}
	}
	return desired.ProductionBranch
}

func resolveBuildCommitHash(override string, getenv func(string) string) string {
	if commitHash := strings.TrimSpace(override); commitHash != "" {
		return commitHash
	}
	if commitHash := gitCurrentCommitHash(); commitHash != "" {
		return commitHash
	}
	if commitHash := githubEventCommitHash(getenv("GITHUB_EVENT_PATH")); commitHash != "" {
		return commitHash
	}
	if commitHash := strings.TrimSpace(getenv("GITHUB_SHA")); commitHash != "" {
		return commitHash
	}
	return ""
}

func githubEventCommitHash(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := readFileFromRoot(path)
	if err != nil {
		return ""
	}
	var event struct {
		After       string `json:"after"`
		PullRequest *struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	if event.PullRequest != nil && strings.TrimSpace(event.PullRequest.Head.SHA) != "" {
		return strings.TrimSpace(event.PullRequest.Head.SHA)
	}
	return strings.TrimSpace(event.After)
}

func readFileFromRoot(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	if filepath.IsAbs(cleanPath) {
		cleanPath = strings.TrimPrefix(cleanPath, string(filepath.Separator))
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = root.Close()
	}()
	file, err := root.Open(cleanPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	return io.ReadAll(file)
}

func gitCurrentBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitCurrentCommitHash() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func writeUsage(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}
