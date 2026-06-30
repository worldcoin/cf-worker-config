package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
)

// Action represents a single change to be applied.
type Action struct {
	Type     string // "create", "update", "delete", "set"
	Resource string // "trigger", "build_var", "build_secret", "runtime_secret", "repo_connection"
	Name     string
	Detail   string
	Trigger  string // "production" or "preview" for trigger-scoped resources
}

func (a Action) String() string {
	sym := map[string]string{"create": "+", "update": "~", "delete": "-", "set": "↻"}[a.Type]
	name := a.Name
	if a.Trigger != "" {
		name = a.Trigger + "." + name
	}
	if a.Detail != "" {
		return fmt.Sprintf("  %s %s.%s: %s", sym, a.Resource, name, a.Detail)
	}
	return fmt.Sprintf("  %s %s.%s", sym, a.Resource, name)
}

// Plan represents the set of changes needed to reconcile desired with current state.
type Plan struct {
	WorkerName string
	WorkerTag  string
	Actions    []Action
}

// HasChanges returns true if the plan has any actions.
func (p *Plan) HasChanges() bool {
	return len(p.Actions) > 0
}

// Print prints the plan to stdout.
func (p *Plan) Print() {
	if !p.HasChanges() {
		fmt.Printf("Worker %q: no changes needed.\n", p.WorkerName)
		return
	}
	fmt.Printf("Worker %q (%s): %d change(s):\n", p.WorkerName, p.WorkerTag, len(p.Actions))
	for _, a := range p.Actions {
		fmt.Println(a)
	}
}

// APIClient is the subset of Cloudflare/GitHub APIs required by the reconciler.
type APIClient interface {
	GetWorkerTag(context.Context, string) (string, error)
	ListTriggers(context.Context, string) ([]Trigger, error)
	CreateTrigger(context.Context, CreateTriggerParams) (*Trigger, error)
	UpdateTrigger(context.Context, string, UpdateTriggerParams) error
	TriggerBuild(context.Context, string, TriggerBuildParams) (*Build, error)
	ListBuildEnvVars(context.Context, string) (map[string]BuildEnvVar, error)
	SetBuildEnvVars(context.Context, string, map[string]BuildEnvVar) error
	DeleteBuildEnvVar(context.Context, string, string) error
	ListBuildTokens(context.Context) ([]BuildToken, error)
	CreateRepoConnection(context.Context, RepoConnection) (*RepoConnection, error)
	LookupGitHubRepo(context.Context, string, string) (*GitHubRepoInfo, error)
	ListRuntimeSecrets(context.Context, string) ([]string, error)
	SetRuntimeSecret(context.Context, string, string, string) error
	DeleteRuntimeSecret(context.Context, string, string) error
}

// Reconciler compares desired and current state.
type Reconciler struct {
	client APIClient
}

// ApplyOptions controls optional side effects after reconciliation.
type ApplyOptions struct {
	TriggerBuild    bool
	BuildBranch     string
	BuildCommitHash string
}

// New creates a new Reconciler.
func New(client APIClient) *Reconciler {
	return &Reconciler{client: client}
}

// Plan computes the diff between desired config and current CF state.
func (r *Reconciler) Plan(ctx context.Context, desired *Worker) (*Plan, error) {
	plan := &Plan{WorkerName: desired.WorkerName}

	// Get worker tag
	tag, err := r.client.GetWorkerTag(ctx, desired.WorkerName)
	if err != nil {
		return nil, fmt.Errorf("looking up worker: %w", err)
	}
	plan.WorkerTag = tag

	// Get current triggers
	triggers, err := r.client.ListTriggers(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("listing triggers: %w", err)
	}

	prodTrigger, err := findProductionTrigger(triggers, desired.ProductionBranch)
	if err != nil {
		return nil, err
	}
	previewTrigger, err := findPreviewTrigger(triggers, desired)
	if err != nil {
		return nil, err
	}

	if err := r.planProductionTrigger(ctx, plan, desired, prodTrigger); err != nil {
		return nil, err
	}

	if err := r.planPreviewTrigger(ctx, plan, desired, prodTrigger, previewTrigger); err != nil {
		return nil, err
	}

	// Check runtime secrets
	currentSecrets, err := r.client.ListRuntimeSecrets(ctx, desired.WorkerName)
	if err != nil {
		return nil, fmt.Errorf("listing runtime secrets: %w", err)
	}
	currentSecretSet := make(map[string]struct{})
	for _, s := range currentSecrets {
		currentSecretSet[s] = struct{}{}
	}
	for _, key := range desired.RuntimeSecrets {
		if _, ok := currentSecretSet[key]; !ok {
			plan.Actions = append(plan.Actions, Action{
				Type: "set", Resource: "runtime_secret", Name: key,
				Detail: "missing (run sync-runtime-secrets)",
			})
		}
	}

	return plan, nil
}

func (r *Reconciler) planProductionTrigger(ctx context.Context, plan *Plan, desired *Worker, trigger *Trigger) error {
	if trigger == nil {
		plan.Actions = append(plan.Actions, Action{
			Type:     "create",
			Resource: "trigger",
			Name:     "production",
			Detail:   fmt.Sprintf("branch=%s, build=%q, deploy=%q", desired.ProductionBranch, desired.BuildCommand, desired.DeployCommand),
		})
		r.planRepoConnectionIfNeeded(plan, desired)
		return nil
	}

	r.planTriggerUpdate(plan, "production", trigger, desired)
	return r.planBuildEnv(ctx, plan, "production", trigger.TriggerUUID, productionBuildVars(desired), productionBuildSecrets(desired))
}

func (r *Reconciler) planPreviewTrigger(ctx context.Context, plan *Plan, desired *Worker, prodTrigger, previewTrigger *Trigger) error {
	switch {
	case desired.Preview.Enabled && previewTrigger == nil:
		plan.Actions = append(plan.Actions, Action{
			Type:     "create",
			Resource: "trigger",
			Name:     "preview",
			Detail:   fmt.Sprintf("command=%q", desired.Preview.Command),
		})
		if prodTrigger != nil && prodTrigger.repoConnectionUUID() == "" {
			r.planRepoConnectionIfNeeded(plan, desired)
		}
		return nil

	case desired.Preview.Enabled:
		r.planTriggerUpdate(plan, "preview", previewTrigger, desired)
		return r.planBuildEnv(ctx, plan, "preview", previewTrigger.TriggerUUID, previewBuildVars(desired), previewBuildSecrets(desired))

	case previewTrigger != nil:
		plan.Actions = append(plan.Actions, Action{
			Type:     "delete",
			Resource: "trigger",
			Name:     "preview",
			Detail:   "preview.enabled=false but a preview trigger exists; remove it in the Cloudflare dashboard",
		})
	}
	return nil
}

func (r *Reconciler) planTriggerUpdate(plan *Plan, kind string, trigger *Trigger, desired *Worker) {
	if diffs := r.diffTrigger(kind, trigger, desired); len(diffs) > 0 {
		plan.Actions = append(plan.Actions, Action{
			Type:     "update",
			Resource: "trigger",
			Name:     kind,
			Detail:   strings.Join(diffs, "; "),
		})
	}
}

func (r *Reconciler) planRepoConnectionIfNeeded(plan *Plan, desired *Worker) {
	if desired.RepoConnectionUUID != "" {
		return
	}
	plan.Actions = append(plan.Actions, Action{
		Type:     "set",
		Resource: "repo_connection",
		Name:     desired.Repo,
		Detail:   "needed for trigger creation",
	})
}

// ApplyWithOptions executes a plan against the CF API.
func (r *Reconciler) ApplyWithOptions(ctx context.Context, desired *Worker, plan *Plan, opts ApplyOptions) error {
	if !plan.HasChanges() {
		fmt.Println("No trigger drift detected. Syncing declared build env/secrets when values are available.")
	}

	for _, action := range plan.Actions {
		if action.Resource == "runtime_secret" {
			fmt.Printf("Skipping: %s (runtime secrets are only synced by sync-runtime-secrets)\n", action)
			continue
		}

		fmt.Printf("Applying: %s\n", action)
		switch {
		case action.Resource == "trigger" && action.Type == "create" && action.Name == "production":
			if err := r.createProductionTrigger(ctx, desired, plan.WorkerTag); err != nil {
				return fmt.Errorf("creating production trigger: %w", err)
			}

		case action.Resource == "trigger" && action.Type == "create" && action.Name == "preview":
			if err := r.createPreviewTrigger(ctx, desired, plan.WorkerTag); err != nil {
				return fmt.Errorf("creating preview trigger: %w", err)
			}

		case action.Resource == "trigger" && action.Type == "update" && action.Name == "production":
			if err := r.updateTrigger(ctx, desired, plan.WorkerTag, "production"); err != nil {
				return err
			}

		case action.Resource == "trigger" && action.Type == "update" && action.Name == "preview":
			if err := r.updateTrigger(ctx, desired, plan.WorkerTag, "preview"); err != nil {
				return err
			}

		case action.Resource == "build_var" && action.Type == "delete":
			triggerUUID, err := r.getTriggerUUID(ctx, desired, plan.WorkerTag, action.Trigger)
			if err != nil {
				return err
			}
			if err := r.client.DeleteBuildEnvVar(ctx, triggerUUID, action.Name); err != nil {
				return err
			}

		case action.Resource == "build_var" || action.Resource == "build_secret" || action.Resource == "repo_connection":
			// Build env and repo connection are reconciled in batch after trigger changes.
			continue

		case action.Resource == "trigger" && action.Type == "delete" && action.Name == "preview":
			return fmt.Errorf("preview trigger deletion is not implemented in Workers Builds API client; remove it in Cloudflare dashboard")

		default:
			return fmt.Errorf("unsupported action: %+v", action)
		}

		fmt.Printf("  ✓ Done\n")
	}

	if err := r.syncBuildEnvForDesiredTriggers(ctx, desired, plan.WorkerTag, false); err != nil {
		return err
	}
	if opts.TriggerBuild {
		if err := r.triggerBuild(ctx, desired, plan.WorkerTag, opts); err != nil {
			return err
		}
	}

	return nil
}

func (r *Reconciler) triggerBuild(ctx context.Context, desired *Worker, workerTag string, opts ApplyOptions) error {
	branch := strings.TrimSpace(opts.BuildBranch)
	if branch == "" {
		branch = desired.ProductionBranch
	}
	commitHash := strings.TrimSpace(opts.BuildCommitHash)
	if branch == "" && commitHash == "" {
		return fmt.Errorf("build branch or commit hash is required to trigger a Workers Build")
	}

	kind := "production"
	if branch != "" && branch != desired.ProductionBranch {
		if !desired.Preview.Enabled {
			fmt.Printf("Skipping Workers Build trigger for branch %q: preview builds are disabled for worker %s.\n", branch, desired.WorkerName)
			return nil
		}
		if !branchAllowed(branch, previewBranchIncludes(desired), previewBranchExcludes(desired)) {
			fmt.Printf("Skipping Workers Build trigger for branch %q: branch does not match preview filters for worker %s.\n", branch, desired.WorkerName)
			return nil
		}
		kind = "preview"
	}

	triggers, err := r.client.ListTriggers(ctx, workerTag)
	if err != nil {
		return fmt.Errorf("listing triggers for build trigger: %w", err)
	}
	trigger, err := findTrigger(triggers, desired, kind)
	if err != nil {
		return err
	}
	if trigger == nil {
		return fmt.Errorf("no %s trigger found for build", kind)
	}

	fmt.Printf("Triggering Workers Build on %s trigger %s", kind, trigger.TriggerUUID)
	if branch != "" {
		fmt.Printf(" for branch %q", branch)
	}
	if commitHash != "" {
		fmt.Printf(" at commit %s", commitHash)
	}
	fmt.Println("...")

	build, err := r.client.TriggerBuild(ctx, trigger.TriggerUUID, TriggerBuildParams{
		Branch:     branch,
		CommitHash: commitHash,
	})
	if err != nil {
		return fmt.Errorf("triggering Workers Build: %w", err)
	}
	if build != nil && build.BuildUUID != "" {
		fmt.Printf("  ✓ Queued Workers Build %s\n", build.BuildUUID)
		return nil
	}
	fmt.Println("  ✓ Queued Workers Build")
	return nil
}

func (r *Reconciler) createProductionTrigger(ctx context.Context, desired *Worker, workerTag string) error {
	repoConnectionUUID, err := r.ensureRepoConnection(ctx, desired)
	if err != nil {
		return err
	}
	buildTokenUUID, err := r.resolveBuildToken(ctx, desired)
	if err != nil {
		return err
	}

	_, err = r.client.CreateTrigger(ctx, CreateTriggerParams{
		ExternalScriptID:    workerTag,
		RepoConnectionUUID:  repoConnectionUUID,
		BuildTokenUUID:      buildTokenUUID,
		TriggerName:         "Deploy production",
		BuildCommand:        desired.BuildCommand,
		DeployCommand:       desired.DeployCommand,
		RootDirectory:       desired.RootDirectory,
		BranchIncludes:      []string{desired.ProductionBranch},
		BranchExcludes:      []string{},
		PathIncludes:        desired.PathIncludes,
		PathExcludes:        desired.PathExcludes,
		BuildCachingEnabled: desired.BuildCachingEnabled,
	})
	return err
}

func (r *Reconciler) createPreviewTrigger(ctx context.Context, desired *Worker, workerTag string) error {
	repoConnectionUUID, err := r.previewRepoConnectionUUID(ctx, desired, workerTag)
	if err != nil {
		return err
	}
	buildTokenUUID, err := r.resolveBuildToken(ctx, desired)
	if err != nil {
		return err
	}

	_, err = r.client.CreateTrigger(ctx, CreateTriggerParams{
		ExternalScriptID:    workerTag,
		RepoConnectionUUID:  repoConnectionUUID,
		BuildTokenUUID:      buildTokenUUID,
		TriggerName:         "Deploy preview branches",
		BuildCommand:        desired.BuildCommand,
		DeployCommand:       desired.Preview.Command,
		RootDirectory:       desired.RootDirectory,
		BranchIncludes:      previewBranchIncludes(desired),
		BranchExcludes:      previewBranchExcludes(desired),
		PathIncludes:        desired.PathIncludes,
		PathExcludes:        desired.PathExcludes,
		BuildCachingEnabled: desired.BuildCachingEnabled,
	})
	return err
}

func (r *Reconciler) previewRepoConnectionUUID(ctx context.Context, desired *Worker, workerTag string) (string, error) {
	if desired.RepoConnectionUUID != "" {
		return desired.RepoConnectionUUID, nil
	}

	triggers, err := r.client.ListTriggers(ctx, workerTag)
	if err != nil {
		return "", fmt.Errorf("listing triggers for preview repo connection: %w", err)
	}
	prodTrigger, err := findProductionTrigger(triggers, desired.ProductionBranch)
	if err != nil {
		return "", err
	}
	if prodTrigger != nil && prodTrigger.repoConnectionUUID() != "" {
		return prodTrigger.repoConnectionUUID(), nil
	}

	return r.ensureRepoConnection(ctx, desired)
}

func (r *Reconciler) updateTrigger(ctx context.Context, desired *Worker, workerTag, kind string) error {
	triggers, err := r.client.ListTriggers(ctx, workerTag)
	if err != nil {
		return err
	}
	trigger, err := findTrigger(triggers, desired, kind)
	if err != nil {
		return err
	}
	if trigger == nil {
		return fmt.Errorf("no %s trigger found", kind)
	}

	params := UpdateTriggerParams{
		BuildCommand:        desired.BuildCommand,
		RootDirectory:       desired.RootDirectory,
		PathIncludes:        desired.PathIncludes,
		PathExcludes:        desired.PathExcludes,
		BuildCachingEnabled: desired.BuildCachingEnabled,
	}
	switch kind {
	case "production":
		params.DeployCommand = desired.DeployCommand
		params.BranchIncludes = []string{desired.ProductionBranch}
		params.BranchExcludes = []string{}
	case "preview":
		params.DeployCommand = desired.Preview.Command
		params.BranchIncludes = previewBranchIncludes(desired)
		params.BranchExcludes = previewBranchExcludes(desired)
	default:
		return fmt.Errorf("unknown trigger kind %q", kind)
	}
	if desired.RepoConnectionUUID != "" {
		params.RepoConnectionUUID = desired.RepoConnectionUUID
	}
	if desired.BuildTokenUUID != "" || desired.BuildTokenName != "" {
		buildTokenUUID, err := r.resolveBuildToken(ctx, desired)
		if err != nil {
			return err
		}
		params.BuildTokenUUID = buildTokenUUID
	}
	return r.client.UpdateTrigger(ctx, trigger.TriggerUUID, params)
}

func (r *Reconciler) getTriggerUUID(ctx context.Context, desired *Worker, workerTag, kind string) (string, error) {
	triggers, err := r.client.ListTriggers(ctx, workerTag)
	if err != nil {
		return "", err
	}
	trigger, err := findTrigger(triggers, desired, kind)
	if err != nil {
		return "", err
	}
	if trigger == nil {
		return "", fmt.Errorf("no %s trigger found", kind)
	}
	return trigger.TriggerUUID, nil
}

// Status prints the current CF state for a worker.
func (r *Reconciler) Status(ctx context.Context, workerName string) error {
	tag, err := r.client.GetWorkerTag(ctx, workerName)
	if err != nil {
		return err
	}
	fmt.Printf("Worker: %s (tag: %s)\n", workerName, tag)

	if err := r.printTriggersStatus(ctx, tag); err != nil {
		return fmt.Errorf("listing triggers: %w", err)
	}
	if err := r.printRuntimeSecretsStatus(ctx, workerName); err != nil {
		return fmt.Errorf("listing runtime secrets: %w", err)
	}
	return nil
}

func (r *Reconciler) printTriggersStatus(ctx context.Context, workerTag string) error {
	triggers, err := r.client.ListTriggers(ctx, workerTag)
	if err != nil {
		return err
	}

	fmt.Printf("Triggers: %d\n", len(triggers))
	for _, t := range triggers {
		fmt.Printf("  - %s (uuid: %s)\n", t.TriggerName, t.TriggerUUID)
		fmt.Printf("    branches: include=%v exclude=%v\n", t.BranchIncludes, t.BranchExcludes)
		fmt.Printf("    build: %q\n", t.BuildCommand)
		fmt.Printf("    deploy: %q\n", t.DeployCommand)
		if err := r.printBuildEnvStatus(ctx, t.TriggerUUID); err != nil {
			return fmt.Errorf("listing build vars for trigger %s: %w", t.TriggerUUID, err)
		}
	}
	return nil
}

func (r *Reconciler) printBuildEnvStatus(ctx context.Context, triggerUUID string) error {
	vars, err := r.client.ListBuildEnvVars(ctx, triggerUUID)
	if err != nil {
		return err
	}

	fmt.Printf("    build vars: %d\n", len(vars))
	for _, key := range sortedBuildEnvVarKeys(vars) {
		value := vars[key]
		if value.IsSecret {
			fmt.Printf("      %s = [secret]\n", key)
			continue
		}
		fmt.Printf("      %s = %s\n", key, value.Value)
	}
	return nil
}

func (r *Reconciler) printRuntimeSecretsStatus(ctx context.Context, workerName string) error {
	secrets, err := r.client.ListRuntimeSecrets(ctx, workerName)
	if err != nil {
		return err
	}

	fmt.Printf("Runtime secrets: %d\n", len(secrets))
	for _, s := range secrets {
		fmt.Printf("  - %s\n", s)
	}
	return nil
}

// RepoConnectionUUID returns the repo connection UUID used by the worker triggers.
func (r *Reconciler) RepoConnectionUUID(ctx context.Context, desired *Worker) (string, error) {
	tag, err := r.client.GetWorkerTag(ctx, desired.WorkerName)
	if err != nil {
		return "", fmt.Errorf("looking up worker: %w", err)
	}
	triggers, err := r.client.ListTriggers(ctx, tag)
	if err != nil {
		return "", fmt.Errorf("listing triggers: %w", err)
	}

	prodTrigger, err := findProductionTrigger(triggers, desired.ProductionBranch)
	if err != nil {
		return "", err
	}
	if prodTrigger != nil && prodTrigger.repoConnectionUUID() != "" {
		return prodTrigger.repoConnectionUUID(), nil
	}

	previewTrigger, err := findPreviewTrigger(triggers, desired)
	if err != nil {
		return "", err
	}
	if previewTrigger != nil && previewTrigger.repoConnectionUUID() != "" {
		return previewTrigger.repoConnectionUUID(), nil
	}

	if desired.RepoConnectionUUID != "" {
		return desired.RepoConnectionUUID, nil
	}

	return "", fmt.Errorf("repo_connection_uuid not found on existing triggers")
}

func sortedBuildEnvVarKeys(vars map[string]BuildEnvVar) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SyncBuildVars syncs all build vars and secrets from config to CF.
func (r *Reconciler) SyncBuildVars(ctx context.Context, desired *Worker) error {
	tag, err := r.client.GetWorkerTag(ctx, desired.WorkerName)
	if err != nil {
		return err
	}

	return r.syncBuildEnvForDesiredTriggers(ctx, desired, tag, true)
}

type runtimeSecretSyncOptions struct {
	requireAllSecrets bool
	refreshExisting   bool
	prune             bool
}

func (r *Reconciler) syncBuildEnvForDesiredTriggers(ctx context.Context, desired *Worker, workerTag string, requireAllSecrets bool) error {
	prodUUID, err := r.getTriggerUUID(ctx, desired, workerTag, "production")
	if err != nil {
		return err
	}
	if err := r.syncBuildEnvForTrigger(ctx, "production", prodUUID, productionBuildVars(desired), productionBuildSecrets(desired), requireAllSecrets); err != nil {
		return err
	}

	if desired.Preview.Enabled {
		previewUUID, err := r.getTriggerUUID(ctx, desired, workerTag, "preview")
		if err != nil {
			return err
		}
		if err := r.syncBuildEnvForTrigger(ctx, "preview", previewUUID, previewBuildVars(desired), previewBuildSecrets(desired), requireAllSecrets); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) syncBuildEnvForTrigger(ctx context.Context, triggerName, triggerUUID string, plainVars map[string]string, secretNames []string, requireAllSecrets bool) error {
	currentVars, err := r.client.ListBuildEnvVars(ctx, triggerUUID)
	if err != nil {
		return fmt.Errorf("listing %s build env vars: %w", triggerName, err)
	}

	vars := make(map[string]BuildEnvVar)
	for k, v := range plainVars {
		vars[k] = BuildEnvVar{Value: v}
	}
	for _, key := range secretNames {
		value := os.Getenv(key)
		if value == "" {
			if _, exists := currentVars[key]; exists && !requireAllSecrets {
				continue
			}
			return fmt.Errorf("env var %s is empty — cannot set %s build secret", key, triggerName)
		}
		vars[key] = BuildEnvVar{Value: value, IsSecret: true}
	}

	if len(vars) == 0 {
		return nil
	}
	fmt.Printf("Syncing %d build vars/secrets to %s trigger %s...\n", len(vars), triggerName, triggerUUID)
	if err := r.client.SetBuildEnvVars(ctx, triggerUUID, vars); err != nil {
		return err
	}
	fmt.Printf("  ✓ Synced %s build env\n", triggerName)
	return nil
}

func (r *Reconciler) syncRuntimeSecrets(ctx context.Context, desired *Worker, opts runtimeSecretSyncOptions) error {
	currentSecrets, err := r.client.ListRuntimeSecrets(ctx, desired.WorkerName)
	if err != nil {
		return fmt.Errorf("listing runtime secrets: %w", err)
	}
	currentSecretSet := make(map[string]struct{})
	for _, s := range currentSecrets {
		currentSecretSet[s] = struct{}{}
	}
	desiredSecretSet := make(map[string]struct{}, len(desired.RuntimeSecrets))
	for _, key := range desired.RuntimeSecrets {
		desiredSecretSet[key] = struct{}{}
	}

	fmt.Printf("Syncing runtime secrets to worker %s when values are available...\n", desired.WorkerName)
	for _, key := range desired.RuntimeSecrets {
		_, exists := currentSecretSet[key]
		if exists && !opts.refreshExisting {
			fmt.Printf("  - %s already exists; skipping refresh\n", key)
			continue
		}

		value := os.Getenv(key)
		if value == "" {
			if exists && !opts.requireAllSecrets {
				continue
			}
			return fmt.Errorf("env var %s is empty — cannot set runtime secret", key)
		}
		if err := r.client.SetRuntimeSecret(ctx, desired.WorkerName, key, value); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
		fmt.Printf("  ✓ %s\n", key)
	}
	if opts.prune {
		for _, key := range currentSecrets {
			if _, desired := desiredSecretSet[key]; desired {
				continue
			}
			if err := r.client.DeleteRuntimeSecret(ctx, desired.WorkerName, key); err != nil {
				return fmt.Errorf("deleting %s: %w", key, err)
			}
			fmt.Printf("  - deleted %s\n", key)
		}
	}
	fmt.Println("✓ Done")
	return nil
}

func (r *Reconciler) planBuildEnv(ctx context.Context, plan *Plan, triggerName, triggerUUID string, desiredVars map[string]string, desiredSecrets []string) error {
	currentVars, err := r.client.ListBuildEnvVars(ctx, triggerUUID)
	if err != nil {
		return fmt.Errorf("listing %s build vars: %w", triggerName, err)
	}

	for key, val := range desiredVars {
		cur, exists := currentVars[key]
		switch {
		case !exists:
			plan.Actions = append(plan.Actions, Action{Type: "create", Resource: "build_var", Name: key, Trigger: triggerName})
		case cur.IsSecret:
			plan.Actions = append(plan.Actions, Action{Type: "update", Resource: "build_var", Name: key, Trigger: triggerName, Detail: "currently secret, desired plain text"})
		case cur.Value != val:
			plan.Actions = append(plan.Actions, Action{Type: "update", Resource: "build_var", Name: key, Trigger: triggerName, Detail: "value changed"})
		}
	}

	desiredSecretSet := make(map[string]struct{}, len(desiredSecrets))
	for _, key := range desiredSecrets {
		desiredSecretSet[key] = struct{}{}
		cur, exists := currentVars[key]
		switch {
		case !exists:
			plan.Actions = append(plan.Actions, Action{Type: "set", Resource: "build_secret", Name: key, Trigger: triggerName, Detail: "missing (will read from env)"})
		case !cur.IsSecret:
			plan.Actions = append(plan.Actions, Action{Type: "set", Resource: "build_secret", Name: key, Trigger: triggerName, Detail: "currently plain text, desired secret"})
		}
	}

	for key := range currentVars {
		if _, ok := desiredVars[key]; ok {
			continue
		}
		if _, ok := desiredSecretSet[key]; ok {
			continue
		}
		plan.Actions = append(plan.Actions, Action{Type: "delete", Resource: "build_var", Name: key, Trigger: triggerName, Detail: "not in desired config"})
	}
	return nil
}

func (r *Reconciler) diffTrigger(kind string, current *Trigger, desired *Worker) []string {
	var diffs []string
	deployCommand := desired.DeployCommand
	branchIncludes := []string{desired.ProductionBranch}
	var branchExcludes []string
	if kind == "preview" {
		deployCommand = desired.Preview.Command
		branchIncludes = previewBranchIncludes(desired)
		branchExcludes = previewBranchExcludes(desired)
	}

	if current.BuildCommand != desired.BuildCommand {
		diffs = append(diffs, fmt.Sprintf("build_command %q -> %q", current.BuildCommand, desired.BuildCommand))
	}
	if current.DeployCommand != deployCommand {
		diffs = append(diffs, fmt.Sprintf("deploy_command %q -> %q", current.DeployCommand, deployCommand))
	}
	if current.RootDirectory != desired.RootDirectory {
		diffs = append(diffs, fmt.Sprintf("root_directory %q -> %q", current.RootDirectory, desired.RootDirectory))
	}
	if !equalStringSets(current.BranchIncludes, branchIncludes) {
		diffs = append(diffs, fmt.Sprintf("branch_includes %v -> %v", current.BranchIncludes, branchIncludes))
	}
	if !equalStringSets(current.BranchExcludes, branchExcludes) {
		diffs = append(diffs, fmt.Sprintf("branch_excludes %v -> %v", current.BranchExcludes, branchExcludes))
	}
	if desired.PathIncludes != nil && !equalStringSets(current.PathIncludes, desired.PathIncludes) {
		diffs = append(diffs, fmt.Sprintf("path_includes %v -> %v", current.PathIncludes, desired.PathIncludes))
	}
	if desired.PathExcludes != nil && !equalStringSets(current.PathExcludes, desired.PathExcludes) {
		diffs = append(diffs, fmt.Sprintf("path_excludes %v -> %v", current.PathExcludes, desired.PathExcludes))
	}
	if desired.BuildCachingEnabled != nil && (current.BuildCachingEnabled == nil || *current.BuildCachingEnabled != *desired.BuildCachingEnabled) {
		diffs = append(diffs, "build_caching_enabled changed")
	}
	if desired.RepoConnectionUUID != "" && current.repoConnectionUUID() != "" && current.repoConnectionUUID() != desired.RepoConnectionUUID {
		diffs = append(diffs, "repo_connection_uuid changed")
	}
	if desired.BuildTokenUUID != "" && current.BuildTokenUUID != "" && current.BuildTokenUUID != desired.BuildTokenUUID {
		diffs = append(diffs, "build_token_uuid changed")
	}
	return diffs
}

func (r *Reconciler) ensureRepoConnection(ctx context.Context, desired *Worker) (string, error) {
	if desired.RepoConnectionUUID != "" {
		return desired.RepoConnectionUUID, nil
	}

	owner, repo, err := splitRepo(desired.Repo)
	if err != nil {
		return "", err
	}

	providerType := desired.ProviderType
	if providerType == "" {
		providerType = "github"
	}
	providerAccountID := desired.ProviderAccountID
	providerAccountName := desired.ProviderAccountName
	repoID := desired.RepoID
	repoName := desired.RepoName

	if providerAccountID == "" || repoID == "" {
		info, err := r.client.LookupGitHubRepo(ctx, owner, repo)
		if err != nil {
			return "", fmt.Errorf("repo_connection_uuid is not configured and GitHub lookup failed; set repo_connection_uuid/provider_account_id/repo_id explicitly or provide GITHUB_TOKEN: %w", err)
		}
		if providerAccountID == "" {
			providerAccountID = info.ProviderAccountID
		}
		if providerAccountName == "" {
			providerAccountName = info.ProviderAccountName
		}
		if repoID == "" {
			repoID = info.RepoID
		}
		if repoName == "" {
			repoName = info.RepoName
		}
	}
	if providerAccountName == "" {
		providerAccountName = owner
	}
	if repoName == "" {
		repoName = repo
	}

	conn, err := r.client.CreateRepoConnection(ctx, RepoConnection{
		ProviderType:        providerType,
		ProviderAccountID:   providerAccountID,
		ProviderAccountName: providerAccountName,
		RepoID:              repoID,
		RepoName:            repoName,
	})
	if err != nil {
		return "", fmt.Errorf("ensuring repo connection: %w", err)
	}
	if conn.RepoConnectionUUID == "" {
		return "", fmt.Errorf("empty repo_connection_uuid in response")
	}
	return conn.RepoConnectionUUID, nil
}

func (r *Reconciler) resolveBuildToken(ctx context.Context, desired *Worker) (string, error) {
	if desired.BuildTokenUUID != "" {
		return desired.BuildTokenUUID, nil
	}

	tokens, err := r.client.ListBuildTokens(ctx)
	if err != nil {
		return "", fmt.Errorf("listing build tokens: %w", err)
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("no build tokens available — create one in CF dashboard first")
	}
	if desired.BuildTokenName != "" {
		for _, token := range tokens {
			if token.BuildTokenName == desired.BuildTokenName {
				return token.BuildTokenUUID, nil
			}
		}
		return "", fmt.Errorf("build token %q not found", desired.BuildTokenName)
	}
	if len(tokens) > 1 {
		return "", fmt.Errorf("multiple build tokens available; set build_token_uuid or build_token_name")
	}
	return tokens[0].BuildTokenUUID, nil
}

func findProductionTrigger(triggers []Trigger, productionBranch string) (*Trigger, error) {
	return findTrigger(triggers, &Worker{ProductionBranch: productionBranch}, "production")
}

func (t Trigger) repoConnectionUUID() string {
	if t.RepoConnectionUUID != "" {
		return t.RepoConnectionUUID
	}
	return t.RepoConnection.RepoConnectionUUID
}

func findPreviewTrigger(triggers []Trigger, desired *Worker) (*Trigger, error) {
	return findTrigger(triggers, desired, "preview")
}

func findTrigger(triggers []Trigger, desired *Worker, kind string) (*Trigger, error) {
	for i := range triggers {
		trigger := &triggers[i]
		if kind == "production" && equalStringSets(trigger.BranchIncludes, []string{desired.ProductionBranch}) && len(trigger.BranchExcludes) == 0 {
			return trigger, nil
		}
		if kind == "preview" && equalStringSets(trigger.BranchIncludes, previewBranchIncludes(desired)) && equalStringSets(trigger.BranchExcludes, previewBranchExcludes(desired)) {
			return trigger, nil
		}
	}

	var candidates []*Trigger
	for i := range triggers {
		trigger := &triggers[i]
		switch kind {
		case "production":
			if isProductionTriggerCandidate(*trigger) {
				candidates = append(candidates, trigger)
			}
		case "preview":
			if isPreviewTriggerCandidate(*trigger) {
				candidates = append(candidates, trigger)
			}
		default:
			return nil, fmt.Errorf("unknown trigger kind %q", kind)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("multiple %s trigger candidates found; refusing to choose one", kind)
	}
	return candidates[0], nil
}

func isProductionTriggerCandidate(trigger Trigger) bool {
	if isProductionTriggerName(trigger.TriggerName) {
		return true
	}
	if isPreviewTriggerCandidate(trigger) {
		return false
	}
	return len(trigger.BranchIncludes) > 0 && !equalStringSets(trigger.BranchIncludes, []string{"*"}) && len(trigger.BranchExcludes) == 0
}

func isPreviewTriggerCandidate(trigger Trigger) bool {
	if strings.EqualFold(strings.TrimSpace(trigger.TriggerName), "Deploy preview branches") {
		return true
	}
	return equalStringSets(trigger.BranchIncludes, []string{"*"}) && len(trigger.BranchExcludes) > 0
}

func isProductionTriggerName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "deploy production", "deploy default branch":
		return true
	default:
		return false
	}
}

func productionBuildVars(desired *Worker) map[string]string {
	return cloneStringMap(desired.BuildVars)
}

func previewBuildVars(desired *Worker) map[string]string {
	vars := cloneStringMap(desired.BuildVars)
	maps.Copy(vars, desired.Preview.BuildVars)
	return vars
}

func productionBuildSecrets(desired *Worker) []string {
	return append([]string(nil), desired.BuildSecrets...)
}

func previewBuildSecrets(desired *Worker) []string {
	if desired.Preview.BuildSecrets != nil {
		return append([]string(nil), desired.Preview.BuildSecrets...)
	}
	return append([]string(nil), desired.BuildSecrets...)
}

func previewBranchIncludes(desired *Worker) []string {
	if desired.Preview.BranchIncludes != nil {
		return append([]string(nil), desired.Preview.BranchIncludes...)
	}
	return []string{"*"}
}

func previewBranchExcludes(desired *Worker) []string {
	if desired.Preview.BranchExcludes != nil {
		return append([]string(nil), desired.Preview.BranchExcludes...)
	}
	return []string{desired.ProductionBranch}
}

func branchAllowed(branch string, includes, excludes []string) bool {
	if branch == "" {
		return true
	}
	matchesAny := func(patterns []string) bool {
		return slices.ContainsFunc(patterns, func(pattern string) bool {
			return branchMatches(pattern, branch)
		})
	}
	if len(includes) > 0 && !matchesAny(includes) {
		return false
	}
	return !matchesAny(excludes)
}

func branchMatches(pattern, branch string) bool {
	if pattern == branch || pattern == "*" {
		return true
	}

	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return false
	}

	if !strings.HasPrefix(branch, parts[0]) {
		return false
	}
	remaining := branch[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(remaining, part)
		if idx < 0 {
			return false
		}
		remaining = remaining[idx+len(part):]
	}
	return strings.HasSuffix(remaining, parts[len(parts)-1])
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func equalStringSets(a, b []string) bool {
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	return slices.Equal(aCopy, bCopy)
}

// splitRepo splits "owner/name" into parts.
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, found := strings.Cut(repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("invalid repo format %q (expected exactly owner/name)", repo)
	}
	return owner, name, nil
}
