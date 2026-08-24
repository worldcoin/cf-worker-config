package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase     = "https://api.cloudflare.com/client/v4"
	decimalBase = 10
	httpTimeout = 30 * time.Second

	// Cloudflare API error codes.
	cfErrWorkerNotDeployed = 10007 // Worker exists but has no deployments
	cfErrNoWorkerVersions  = 10222 // Worker exists but no versions uploaded yet
)

// Client wraps the Cloudflare and GitHub HTTP APIs used by cf-worker-config.
type Client struct {
	token     string
	accountID string
	http      *http.Client
}

// NewClient creates a new API client.
func NewClient(token, accountID string) *Client {
	return &Client{
		token:     token,
		accountID: accountID,
		http:      &http.Client{Timeout: httpTimeout},
	}
}

// --- Worker lookup ---

// WorkerInfo contains basic worker metadata.
type WorkerInfo struct {
	ID  string `json:"id"`
	Tag string `json:"tag"`
}

// GetWorkerTag finds the immutable tag for a worker by name.
// It first tries /workers/scripts (lists workers with deployments), then falls
// back to /workers/services/{name} for workers that exist but have no deployments
// yet (e.g. Terraform-created placeholders).
func (c *Client) GetWorkerTag(ctx context.Context, workerName string) (string, error) {
	// Try the scripts list first (covers deployed workers).
	scriptsURL := c.cfURL("workers", "scripts")
	var scripts struct {
		Result []WorkerInfo `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", scriptsURL, nil, &scripts); err != nil {
		return "", fmt.Errorf("listing workers: %w", err)
	}
	for _, w := range scripts.Result {
		if w.ID == workerName {
			return w.Tag, nil
		}
	}

	// Fall back to the services endpoint for undeployed workers.
	serviceURL := c.cfURL("workers", "services", workerName)
	var service struct {
		Result struct {
			DefaultEnvironment struct {
				Script struct {
					Tag string `json:"tag"`
				} `json:"script"`
			} `json:"default_environment"`
		} `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", serviceURL, nil, &service); err != nil {
		return "", fmt.Errorf("looking up worker service %q: %w", workerName, err)
	}
	tag := service.Result.DefaultEnvironment.Script.Tag
	if tag == "" {
		return "", fmt.Errorf("worker %q exists but has no tag", workerName)
	}
	return tag, nil
}

// --- Workers Builds API (raw HTTP) ---

// Trigger represents a Workers Builds trigger.
type Trigger struct {
	TriggerUUID         string         `json:"trigger_uuid"`
	TriggerName         string         `json:"trigger_name"`
	RepoConnectionUUID  string         `json:"repo_connection_uuid"`
	RepoConnection      RepoConnection `json:"repo_connection"`
	BuildCommand        string         `json:"build_command"`
	DeployCommand       string         `json:"deploy_command"`
	RootDirectory       string         `json:"root_directory"`
	BranchIncludes      []string       `json:"branch_includes"`
	BranchExcludes      []string       `json:"branch_excludes"`
	PathIncludes        []string       `json:"path_includes"`
	PathExcludes        []string       `json:"path_excludes"`
	BuildCachingEnabled *bool          `json:"build_caching_enabled"`
	BuildTokenUUID      string         `json:"build_token_uuid"`
}

// ListTriggers returns all triggers for a worker.
func (c *Client) ListTriggers(ctx context.Context, workerTag string) ([]Trigger, error) {
	apiURL := c.cfURL("builds", "workers", workerTag, "triggers")
	var result struct {
		Result []Trigger `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", apiURL, nil, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

// RepoConnection represents a GitHub repo connection.
type RepoConnection struct {
	RepoConnectionUUID  string `json:"repo_connection_uuid,omitempty"`
	ProviderType        string `json:"provider_type"`
	ProviderAccountID   string `json:"provider_account_id"`
	ProviderAccountName string `json:"provider_account_name"`
	RepoID              string `json:"repo_id"`
	RepoName            string `json:"repo_name"`
}

// GitHubRepoInfo contains the GitHub metadata required by Workers Builds.
type GitHubRepoInfo struct {
	ProviderAccountID   string
	ProviderAccountName string
	RepoID              string
	RepoName            string
}

// CreateRepoConnection connects a GitHub repo to the account.
func (c *Client) CreateRepoConnection(ctx context.Context, conn RepoConnection) (*RepoConnection, error) {
	apiURL := c.cfURL("builds", "repos", "connections")
	var result struct {
		Result RepoConnection `json:"result"`
	}
	if err := c.doJSON(ctx, "PUT", apiURL, conn, &result); err != nil {
		return nil, err
	}
	return &result.Result, nil
}

// CreateTriggerParams contains the parameters for creating a trigger.
type CreateTriggerParams struct {
	ExternalScriptID    string   `json:"external_script_id"`
	RepoConnectionUUID  string   `json:"repo_connection_uuid"`
	BuildTokenUUID      string   `json:"build_token_uuid"`
	TriggerName         string   `json:"trigger_name"`
	BuildCommand        string   `json:"build_command"`
	DeployCommand       string   `json:"deploy_command"`
	RootDirectory       string   `json:"root_directory"`
	BranchIncludes      []string `json:"branch_includes"`
	BranchExcludes      []string `json:"branch_excludes"`
	PathIncludes        []string `json:"path_includes,omitempty"`
	PathExcludes        []string `json:"path_excludes,omitempty"`
	BuildCachingEnabled *bool    `json:"build_caching_enabled,omitempty"`
}

// CreateTrigger creates a new build trigger.
func (c *Client) CreateTrigger(ctx context.Context, params CreateTriggerParams) (*Trigger, error) {
	apiURL := c.cfURL("builds", "triggers")
	var result struct {
		Result Trigger `json:"result"`
	}
	if err := c.doJSON(ctx, "POST", apiURL, params, &result); err != nil {
		return nil, err
	}
	return &result.Result, nil
}

// UpdateTriggerParams contains the parameters for updating a trigger.
type UpdateTriggerParams struct {
	RepoConnectionUUID  string   `json:"repo_connection_uuid,omitempty"`
	BuildTokenUUID      string   `json:"build_token_uuid,omitempty"`
	BuildCommand        string   `json:"build_command,omitempty"`
	DeployCommand       string   `json:"deploy_command,omitempty"`
	RootDirectory       string   `json:"root_directory,omitempty"`
	BranchIncludes      []string `json:"branch_includes"`
	BranchExcludes      []string `json:"branch_excludes"`
	PathIncludes        []string `json:"path_includes,omitempty"`
	PathExcludes        []string `json:"path_excludes,omitempty"`
	BuildCachingEnabled *bool    `json:"build_caching_enabled,omitempty"`
}

// UpdateTrigger patches an existing trigger.
func (c *Client) UpdateTrigger(ctx context.Context, triggerUUID string, params UpdateTriggerParams) error {
	apiURL := c.cfURL("builds", "triggers", triggerUUID)
	return c.doJSON(ctx, "PATCH", apiURL, params, nil)
}

// TriggerBuildParams contains the parameters for manually starting a Workers Build.
type TriggerBuildParams struct {
	Branch     string `json:"branch,omitempty"`
	CommitHash string `json:"commit_hash,omitempty"`
}

// Build represents a Workers Builds build.
type Build struct {
	BuildUUID string `json:"build_uuid"`
}

// TriggerBuild manually queues a build for an existing trigger.
func (c *Client) TriggerBuild(ctx context.Context, triggerUUID string, params TriggerBuildParams) (*Build, error) {
	apiURL := c.cfURL("builds", "triggers", triggerUUID, "builds")
	var result struct {
		Result Build `json:"result"`
	}
	if err := c.doJSON(ctx, "POST", apiURL, params, &result); err != nil {
		return nil, err
	}
	return &result.Result, nil
}

// --- Build environment variables ---

// BuildEnvVar represents a build-time environment variable.
type BuildEnvVar struct {
	Value    string `json:"value"`
	IsSecret bool   `json:"is_secret"`
}

// ListBuildEnvVars lists build environment variables for a trigger.
func (c *Client) ListBuildEnvVars(ctx context.Context, triggerUUID string) (map[string]BuildEnvVar, error) {
	apiURL := c.cfURL("builds", "triggers", triggerUUID, "environment_variables")
	var result struct {
		Result map[string]BuildEnvVar `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", apiURL, nil, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

// SetBuildEnvVars sets build environment variables for a trigger (PATCH — additive).
func (c *Client) SetBuildEnvVars(ctx context.Context, triggerUUID string, vars map[string]BuildEnvVar) error {
	apiURL := c.cfURL("builds", "triggers", triggerUUID, "environment_variables")
	return c.doJSON(ctx, "PATCH", apiURL, vars, nil)
}

// DeleteBuildEnvVar deletes a single build environment variable.
func (c *Client) DeleteBuildEnvVar(ctx context.Context, triggerUUID, key string) error {
	apiURL := c.cfURL("builds", "triggers", triggerUUID, "environment_variables", key)
	return c.doJSON(ctx, "DELETE", apiURL, nil, nil)
}

// --- Build tokens ---

// BuildToken represents a Workers Builds API token.
type BuildToken struct {
	BuildTokenUUID string `json:"build_token_uuid"`
	BuildTokenName string `json:"build_token_name"`
}

// ListBuildTokens returns available build tokens.
func (c *Client) ListBuildTokens(ctx context.Context) ([]BuildToken, error) {
	apiURL := c.cfURL("builds", "tokens")
	var result struct {
		Result []BuildToken `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", apiURL, nil, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

// LookupGitHubRepo returns GitHub account and repository IDs for a repo.
func (c *Client) LookupGitHubRepo(ctx context.Context, owner, repo string) (*GitHubRepoInfo, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := c.doGitHubJSON(ctx, ghURL("users", owner), &user); err != nil {
		return nil, fmt.Errorf("looking up GitHub account %q: %w", owner, err)
	}

	var repository struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := c.doGitHubJSON(ctx, ghURL("repos", owner, repo), &repository); err != nil {
		return nil, fmt.Errorf("looking up GitHub repo %q/%q: %w", owner, repo, err)
	}

	return &GitHubRepoInfo{
		ProviderAccountID:   strconv.FormatInt(user.ID, decimalBase),
		ProviderAccountName: user.Login,
		RepoID:              strconv.FormatInt(repository.ID, decimalBase),
		RepoName:            repository.Name,
	}, nil
}

// --- Runtime secrets ---

type runtimeSecret struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListRuntimeSecrets returns the names of runtime secrets on a worker.
// Returns an empty list for workers that have no deployments yet (CF returns 404).
func (c *Client) ListRuntimeSecrets(ctx context.Context, workerName string) ([]string, error) {
	apiURL := c.cfURL("workers", "scripts", workerName, "secrets")
	var result struct {
		Result []runtimeSecret `json:"result"`
	}
	if err := c.doJSON(ctx, "GET", apiURL, nil, &result); err != nil {
		// Workers without any deployment return 10007 on the secrets endpoint.
		if hasErrorCode(err, cfErrWorkerNotDeployed) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing runtime secrets: %w", err)
	}

	var names []string
	for _, secret := range result.Result {
		names = append(names, secret.Name)
	}
	return names, nil
}

// SecretMutation is the value type in a PatchLatestVersionSecrets map:
// a non-nil pointer is a create/update, nil is a delete.
type SecretMutation = *string

// SecretValue is a helper to build a create/update mutation.
func SecretValue(v string) SecretMutation { return &v }

// PatchLatestVersionSecrets applies a batch of runtime secret create/update/delete
// operations to the worker's latest uploaded version. Uses the versioned merge-patch
// endpoint so it works even when the latest version is not deployed (which is the
// default state when Workers Builds uploads via `wrangler versions upload` without
// promoting). Whole batch becomes one new worker version rather than N.
//
// Values: pointer -> create/update; nil -> delete. Empty map is a no-op.
func (c *Client) PatchLatestVersionSecrets(ctx context.Context, workerName string, secrets map[string]SecretMutation) error {
	if len(secrets) == 0 {
		return nil
	}

	type secretText struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	env := make(map[string]any, len(secrets))
	for name, val := range secrets {
		if val == nil {
			env[name] = nil
			continue
		}
		env[name] = secretText{Type: "secret_text", Text: *val}
	}

	body := struct {
		Env map[string]any `json:"env"`
	}{Env: env}

	// Versioned secrets PATCH is routed under `workers/workers/` in CF, not `workers/scripts/`.
	apiURL := c.cfURL("workers", "workers", workerName, "versions", "latest")
	return c.doMergePatch(ctx, apiURL, body)
}

// doMergePatch is a variant of doJSON for endpoints that require RFC 7396
// merge-patch semantics (null == delete). Currently used only for the versioned
// secrets endpoint.
func (c *Client) doMergePatch(ctx context.Context, apiURL string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", apiURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "cf-worker-config")
	req.Header.Set("Content-Type", "application/merge-patch+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		var envelope cfEnvelope
		if err := json.Unmarshal(respBody, &envelope); err == nil && len(envelope.Errors) > 0 {
			return &apiError{StatusCode: resp.StatusCode, Errors: envelope.Errors}
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var envelope cfEnvelope
	if err := json.Unmarshal(respBody, &envelope); err == nil && envelope.Success != nil && !*envelope.Success {
		return &apiError{StatusCode: resp.StatusCode, Errors: envelope.Errors}
	}

	return nil
}

// --- URL construction ---

// cfURL builds a Cloudflare API URL by appending escaped path segments.
func (c *Client) cfURL(segments ...string) string {
	parts := append([]string{"accounts", c.accountID}, segments...)
	return escapedURL(apiBase, parts...)
}

// ghURL builds a GitHub API URL by appending escaped path segments.
func ghURL(segments ...string) string {
	return escapedURL("https://api.github.com", segments...)
}

func escapedURL(base string, segments ...string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, escapePathSegment(segment))
	}
	if len(escaped) == 0 {
		return strings.TrimRight(base, "/")
	}
	return strings.TrimRight(base, "/") + "/" + strings.Join(escaped, "/")
}

func escapePathSegment(segment string) string {
	switch segment {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return url.PathEscape(segment)
	}
}

// --- error types ---

type cfErrorDetail struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type cfEnvelope struct {
	Success *bool           `json:"success"`
	Errors  []cfErrorDetail `json:"errors"`
}

// apiError represents a Cloudflare API error with structured error codes.
type apiError struct {
	StatusCode int
	Errors     []cfErrorDetail
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, formatAPIErrors(e.Errors))
}

// hasErrorCode checks whether an error is a CF API error containing a specific code.
func hasErrorCode(err error, code int64) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	for _, e := range ae.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

// --- raw HTTP helper ---

func (c *Client) doJSON(ctx context.Context, method, apiURL string, body any, result any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiURL, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "cf-worker-config")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		var envelope cfEnvelope
		if err := json.Unmarshal(respBody, &envelope); err == nil && len(envelope.Errors) > 0 {
			return &apiError{StatusCode: resp.StatusCode, Errors: envelope.Errors}
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var envelope cfEnvelope
	if err := json.Unmarshal(respBody, &envelope); err == nil && envelope.Success != nil && !*envelope.Success {
		return &apiError{StatusCode: resp.StatusCode, Errors: envelope.Errors}
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}

func (c *Client) doGitHubJSON(ctx context.Context, apiURL string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cf-worker-config")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading GitHub response body: %w", err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GitHub HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("decoding GitHub response: %w", err)
	}
	return nil
}

func formatAPIErrors(errs []cfErrorDetail) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	var buf bytes.Buffer
	for i, e := range errs {
		if i > 0 {
			buf.WriteString("; ")
		}
		if e.Code != 0 {
			fmt.Fprintf(&buf, "%d: ", e.Code)
		}
		buf.WriteString(e.Message)
	}
	return buf.String()
}
