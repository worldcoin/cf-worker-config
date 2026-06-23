package main

import (
	"context"
	"maps"
	"strings"
	"testing"
)

func TestApplyCreatesTriggerAndSyncsBuildVarsInSameRun(t *testing.T) {
	ctx := context.Background()
	client := newFakeClient()
	client.buildTokens = []BuildToken{{BuildTokenUUID: "token-1", BuildTokenName: "default"}}

	desired := &Worker{
		WorkerName:         "tfh-website-dev",
		Repo:               "worldcoin/tfh-website",
		RepoConnectionUUID: "repo-1",
		BuildTokenUUID:     "token-1",
		ProductionBranch:   "main",
		RootDirectory:      ".",
		BuildCommand:       "npm run build:cf",
		DeployCommand:      "npm run deploy:cf:dev",
		BuildVars:          map[string]string{"NODE_VERSION": "22"},
	}

	rec := New(client)
	plan, err := rec.Plan(ctx, desired)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !plan.HasChanges() {
		t.Fatalf("expected trigger creation in plan")
	}

	if err := rec.Apply(ctx, desired, plan); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if got := client.buildVars["trigger-1"]["NODE_VERSION"].Value; got != "22" {
		t.Fatalf("NODE_VERSION = %q, want 22", got)
	}
}

func TestApplySyncsPreviewBuildVarsAndRefreshesExistingSecretsWhenEnvPresent(t *testing.T) {
	ctx := context.Background()
	t.Setenv("NEXT_SERVER_TOKEN", "rotated")

	client := newFakeClient()
	client.triggers = []Trigger{
		{
			TriggerUUID:    "prod",
			BuildCommand:   "npm run build:cf",
			DeployCommand:  "npm run deploy:cf:dev",
			RootDirectory:  ".",
			BranchIncludes: []string{"main"},
		},
		{
			TriggerUUID:    "preview",
			BuildCommand:   "npm run build:cf",
			DeployCommand:  "npm run deploy:cf:preview",
			RootDirectory:  ".",
			BranchIncludes: []string{"*"},
			BranchExcludes: []string{"main"},
		},
	}
	client.buildVars["prod"] = map[string]BuildEnvVar{
		"NEXT_SERVER_TOKEN": {IsSecret: true},
	}
	client.buildVars["preview"] = map[string]BuildEnvVar{
		"NEXT_SERVER_TOKEN": {IsSecret: true},
	}

	desired := &Worker{
		WorkerName:       "tfh-website-dev",
		Repo:             "worldcoin/tfh-website",
		ProductionBranch: "main",
		RootDirectory:    ".",
		BuildCommand:     "npm run build:cf",
		DeployCommand:    "npm run deploy:cf:dev",
		Preview: Preview{
			Enabled:   true,
			Command:   "npm run deploy:cf:preview",
			BuildVars: map[string]string{"NEXT_PUBLIC_APP_ENV": "preview"},
		},
		BuildVars:    map[string]string{"NODE_VERSION": "22", "NEXT_PUBLIC_APP_ENV": "production"},
		BuildSecrets: []string{"NEXT_SERVER_TOKEN"},
	}

	rec := New(client)
	plan, err := rec.Plan(ctx, desired)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := rec.Apply(ctx, desired, plan); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if got := client.buildVars["prod"]["NEXT_PUBLIC_APP_ENV"].Value; got != "production" {
		t.Fatalf("prod NEXT_PUBLIC_APP_ENV = %q, want production", got)
	}
	if got := client.buildVars["preview"]["NEXT_PUBLIC_APP_ENV"].Value; got != "preview" {
		t.Fatalf("preview NEXT_PUBLIC_APP_ENV = %q, want preview", got)
	}
	if got := client.buildVars["prod"]["NEXT_SERVER_TOKEN"].Value; got != "rotated" {
		t.Fatalf("prod secret value = %q, want rotated", got)
	}
	if got := client.buildVars["preview"]["NEXT_SERVER_TOKEN"].Value; got != "rotated" {
		t.Fatalf("preview secret value = %q, want rotated", got)
	}
}

func TestApplyCreatesPreviewTriggerWithProductionRepoConnection(t *testing.T) {
	ctx := context.Background()

	client := newFakeClient()
	client.triggers = []Trigger{
		{
			TriggerUUID:        "prod",
			RepoConnectionUUID: "repo-existing",
			BuildTokenUUID:     "token-1",
			BuildCommand:       "npm run build:cf",
			DeployCommand:      "npm run deploy:cf:dev",
			RootDirectory:      ".",
			BranchIncludes:     []string{"main"},
		},
	}

	desired := &Worker{
		WorkerName:       "tfh-website-dev",
		Repo:             "worldcoin/tfh-website",
		BuildTokenUUID:   "token-1",
		ProductionBranch: "main",
		RootDirectory:    ".",
		BuildCommand:     "npm run build:cf",
		DeployCommand:    "npm run deploy:cf:dev",
		Preview: Preview{
			Enabled: true,
			Command: "npm run deploy:cf:preview",
		},
	}

	rec := New(client)
	plan, err := rec.Plan(ctx, desired)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	for _, action := range plan.Actions {
		if action.Resource == "repo_connection" {
			t.Fatalf("Plan() included repo_connection action despite production trigger having one: %+v", action)
		}
	}

	if err := rec.Apply(ctx, desired, plan); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if client.createRepoConnectionCalls != 0 {
		t.Fatalf("CreateRepoConnection() calls = %d, want 0", client.createRepoConnectionCalls)
	}
	if client.lookupGitHubRepoCalls != 0 {
		t.Fatalf("LookupGitHubRepo() calls = %d, want 0", client.lookupGitHubRepoCalls)
	}
	if len(client.triggers) != 2 {
		t.Fatalf("trigger count = %d, want 2", len(client.triggers))
	}
	if got := client.triggers[1].RepoConnectionUUID; got != "repo-existing" {
		t.Fatalf("preview RepoConnectionUUID = %q, want repo-existing", got)
	}
}

func TestApplyRequiresExplicitBuildTokenWhenMultipleTokensExist(t *testing.T) {
	ctx := context.Background()
	client := newFakeClient()
	client.buildTokens = []BuildToken{
		{BuildTokenUUID: "token-1", BuildTokenName: "first"},
		{BuildTokenUUID: "token-2", BuildTokenName: "second"},
	}

	desired := &Worker{
		WorkerName:         "tfh-website-dev",
		Repo:               "worldcoin/tfh-website",
		RepoConnectionUUID: "repo-1",
		ProductionBranch:   "main",
		RootDirectory:      ".",
		BuildCommand:       "npm run build:cf",
		DeployCommand:      "npm run deploy:cf:dev",
	}

	rec := New(client)
	plan, err := rec.Plan(ctx, desired)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	err = rec.Apply(ctx, desired, plan)
	if err == nil || !strings.Contains(err.Error(), "multiple build tokens") {
		t.Fatalf("Apply() error = %v, want multiple build tokens error", err)
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		name      string
		repo      string
		wantOwner string
		wantName  string
		wantErr   string
	}{
		{
			name:      "valid owner repo",
			repo:      "worldcoin/tfh-website",
			wantOwner: "worldcoin",
			wantName:  "tfh-website",
		},
		{
			name:    "missing slash",
			repo:    "tfh-website",
			wantErr: "invalid repo format",
		},
		{
			name:    "rejects nested path in repo name",
			repo:    "worldcoin/some/nested",
			wantErr: "invalid repo format",
		},
		{
			name:    "rejects trailing slash",
			repo:    "worldcoin/",
			wantErr: "invalid repo format",
		},
		{
			name:    "rejects leading slash",
			repo:    "/tfh-website",
			wantErr: "invalid repo format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOwner, gotName, err := splitRepo(tt.repo)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("splitRepo() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("splitRepo() error = %q, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitRepo() error = %v", err)
			}
			if gotOwner != tt.wantOwner || gotName != tt.wantName {
				t.Fatalf("splitRepo() = (%q, %q), want (%q, %q)", gotOwner, gotName, tt.wantOwner, tt.wantName)
			}
		})
	}
}

func TestBuildEnvForPreview(t *testing.T) {
	tests := []struct {
		name        string
		worker      *Worker
		wantVars    map[string]string
		wantSecrets []string
	}{
		{
			name: "inherits production vars and secrets by default",
			worker: &Worker{
				BuildVars:    map[string]string{"NODE_VERSION": "22"},
				BuildSecrets: []string{"NEXT_SERVER_TOKEN"},
			},
			wantVars:    map[string]string{"NODE_VERSION": "22"},
			wantSecrets: []string{"NEXT_SERVER_TOKEN"},
		},
		{
			name: "preview vars override production vars",
			worker: &Worker{
				BuildVars: map[string]string{"NEXT_PUBLIC_APP_ENV": "production", "NODE_VERSION": "22"},
				Preview: Preview{
					BuildVars: map[string]string{"NEXT_PUBLIC_APP_ENV": "preview"},
				},
			},
			wantVars: map[string]string{"NEXT_PUBLIC_APP_ENV": "preview", "NODE_VERSION": "22"},
		},
		{
			name: "preview secrets replace production secrets when explicitly set",
			worker: &Worker{
				BuildSecrets: []string{"PRODUCTION_SECRET"},
				Preview: Preview{
					BuildSecrets: []string{"PREVIEW_SECRET"},
				},
			},
			wantVars:    map[string]string{},
			wantSecrets: []string{"PREVIEW_SECRET"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVars := previewBuildVars(tt.worker)
			if len(gotVars) != len(tt.wantVars) {
				t.Fatalf("previewBuildVars() = %v, want %v", gotVars, tt.wantVars)
			}
			for key, want := range tt.wantVars {
				if gotVars[key] != want {
					t.Fatalf("previewBuildVars()[%q] = %q, want %q", key, gotVars[key], want)
				}
			}

			gotSecrets := previewBuildSecrets(tt.worker)
			if !equalStringSets(gotSecrets, tt.wantSecrets) {
				t.Fatalf("previewBuildSecrets() = %v, want %v", gotSecrets, tt.wantSecrets)
			}
		})
	}
}

type fakeClient struct {
	workerTag                 string
	triggers                  []Trigger
	buildVars                 map[string]map[string]BuildEnvVar
	buildTokens               []BuildToken
	runtimeSecrets            []string
	createRepoConnectionCalls int
	lookupGitHubRepoCalls     int
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		workerTag: "worker-tag",
		buildVars: make(map[string]map[string]BuildEnvVar),
	}
}

func (f *fakeClient) GetWorkerTag(context.Context, string) (string, error) {
	return f.workerTag, nil
}

func (f *fakeClient) ListTriggers(context.Context, string) ([]Trigger, error) {
	return append([]Trigger(nil), f.triggers...), nil
}

func (f *fakeClient) CreateTrigger(_ context.Context, params CreateTriggerParams) (*Trigger, error) {
	trigger := Trigger{
		TriggerUUID:         "trigger-1",
		RepoConnectionUUID:  params.RepoConnectionUUID,
		BuildTokenUUID:      params.BuildTokenUUID,
		BuildCommand:        params.BuildCommand,
		DeployCommand:       params.DeployCommand,
		RootDirectory:       params.RootDirectory,
		BranchIncludes:      params.BranchIncludes,
		BranchExcludes:      params.BranchExcludes,
		PathIncludes:        params.PathIncludes,
		PathExcludes:        params.PathExcludes,
		BuildCachingEnabled: params.BuildCachingEnabled,
	}
	if len(f.triggers) > 0 {
		trigger.TriggerUUID = "trigger-2"
	}
	f.triggers = append(f.triggers, trigger)
	return &trigger, nil
}

func (f *fakeClient) UpdateTrigger(_ context.Context, triggerUUID string, params UpdateTriggerParams) error {
	for i := range f.triggers {
		if f.triggers[i].TriggerUUID != triggerUUID {
			continue
		}
		f.triggers[i].BuildCommand = params.BuildCommand
		f.triggers[i].DeployCommand = params.DeployCommand
		f.triggers[i].RootDirectory = params.RootDirectory
		f.triggers[i].BranchIncludes = params.BranchIncludes
		f.triggers[i].BranchExcludes = params.BranchExcludes
		f.triggers[i].PathIncludes = params.PathIncludes
		f.triggers[i].PathExcludes = params.PathExcludes
		f.triggers[i].BuildCachingEnabled = params.BuildCachingEnabled
		if params.RepoConnectionUUID != "" {
			f.triggers[i].RepoConnectionUUID = params.RepoConnectionUUID
		}
		if params.BuildTokenUUID != "" {
			f.triggers[i].BuildTokenUUID = params.BuildTokenUUID
		}
		return nil
	}
	return nil
}

func (f *fakeClient) ListBuildEnvVars(_ context.Context, triggerUUID string) (map[string]BuildEnvVar, error) {
	vars := make(map[string]BuildEnvVar)
	maps.Copy(vars, f.buildVars[triggerUUID])
	return vars, nil
}

func (f *fakeClient) SetBuildEnvVars(_ context.Context, triggerUUID string, vars map[string]BuildEnvVar) error {
	if f.buildVars[triggerUUID] == nil {
		f.buildVars[triggerUUID] = make(map[string]BuildEnvVar)
	}
	maps.Copy(f.buildVars[triggerUUID], vars)
	return nil
}

func (f *fakeClient) DeleteBuildEnvVar(_ context.Context, triggerUUID, key string) error {
	delete(f.buildVars[triggerUUID], key)
	return nil
}

func (f *fakeClient) ListBuildTokens(context.Context) ([]BuildToken, error) {
	return append([]BuildToken(nil), f.buildTokens...), nil
}

func (f *fakeClient) CreateRepoConnection(context.Context, RepoConnection) (*RepoConnection, error) {
	f.createRepoConnectionCalls++
	return &RepoConnection{RepoConnectionUUID: "repo-created"}, nil
}

func (f *fakeClient) LookupGitHubRepo(context.Context, string, string) (*GitHubRepoInfo, error) {
	f.lookupGitHubRepoCalls++
	return &GitHubRepoInfo{
		ProviderAccountID:   "1",
		ProviderAccountName: "worldcoin",
		RepoID:              "2",
		RepoName:            "tfh-website",
	}, nil
}

func (f *fakeClient) ListRuntimeSecrets(context.Context, string) ([]string, error) {
	return append([]string(nil), f.runtimeSecrets...), nil
}

func (f *fakeClient) SetRuntimeSecret(_ context.Context, _ string, name, _ string) error {
	f.runtimeSecrets = append(f.runtimeSecrets, name)
	return nil
}
