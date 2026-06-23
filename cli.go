package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
)

const (
	defaultConfigPath   = "cf-worker-config.json"
	cloudflareAPIEnvVar = "CLOUDFLARE_API_TOKEN"
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

	fs, configPath, workerKey, failOnDrift := newCommandFlagSet(cmd, stderr)
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

	if *workerKey == "" {
		fs.Usage()
		return fmt.Errorf("--worker is required")
	}

	token := getenv(cloudflareAPIEnvVar)
	if token == "" {
		return fmt.Errorf("%s environment variable is required", cloudflareAPIEnvVar)
	}

	cfg, err := Load(*configPath)
	if err != nil {
		return err
	}

	client := NewClient(token, cfg.AccountID)
	rec := New(client)
	ctx := context.Background()

	switch cmd {
	case "plan":
		desired, err := cfg.GetWorker(*workerKey)
		if err != nil {
			return err
		}
		plan, err := rec.Plan(ctx, desired)
		if err != nil {
			return err
		}
		plan.Print()
		if *failOnDrift && plan.HasChanges() {
			return fmt.Errorf("drift detected: %d change(s)", len(plan.Actions))
		}
		return nil

	case "apply":
		desired, err := cfg.GetWorker(*workerKey)
		if err != nil {
			return err
		}
		plan, err := rec.Plan(ctx, desired)
		if err != nil {
			return err
		}
		plan.Print()
		fmt.Println("\nApplying...")
		return rec.Apply(ctx, desired, plan)

	case "status":
		desired, err := cfg.GetWorker(*workerKey)
		if err != nil {
			return err
		}
		return rec.Status(ctx, desired.WorkerName)

	case "sync-build-vars":
		desired, err := cfg.GetWorker(*workerKey)
		if err != nil {
			return err
		}
		return rec.SyncBuildVars(ctx, desired)

	case "sync-runtime-secrets":
		desired, err := cfg.GetWorker(*workerKey)
		if err != nil {
			return err
		}
		return rec.SyncRuntimeSecrets(ctx, desired)
	}

	return fmt.Errorf("unhandled command: %s", cmd)
}

func newCommandFlagSet(cmd string, output io.Writer) (*flag.FlagSet, *string, *string, *bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(output)
	configPath := fs.String("config", defaultConfigPath, "path to config file")
	workerKey := fs.String("worker", "", "worker key from config")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit 1 if plan has changes (for CI drift checks)")
	fs.Usage = func() {
		printCommandUsage(fs.Output(), cmd)
		writeUsage(fs.Output(), "\nFlags for %q:\n", cmd)
		fs.PrintDefaults()
		printEnvironment(fs.Output())
	}
	return fs, configPath, workerKey, failOnDrift
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
		fs, _, _, _ := newCommandFlagSet(cmd, stdout)
		fs.Usage()
		return nil
	default:
		printGlobalUsage(stderr)
		return fmt.Errorf("help accepts at most one command")
	}
}

func isKnownCommand(cmd string) bool {
	switch cmd {
	case "plan", "apply", "status", "sync-build-vars", "sync-runtime-secrets":
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
`, cmd)
}

func printEnvironment(w io.Writer) {
	writeUsage(w, "\nEnvironment:\n  %s  Cloudflare API token (user-scoped, Workers Builds Configuration: Edit + Workers Scripts: Edit)\n", cloudflareAPIEnvVar)
}

func writeUsage(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}
