package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClientCFURL(t *testing.T) {
	client := NewClient("token", "account/id")

	tests := []struct {
		name     string
		segments []string
		want     string
	}{
		{
			name:     "static path",
			segments: []string{"workers", "scripts"},
			want:     "https://api.cloudflare.com/client/v4/accounts/account%2Fid/workers/scripts",
		},
		{
			name:     "escapes dynamic path segment separators and spaces",
			segments: []string{"workers", "scripts", "worker/name with spaces", "secrets"},
			want:     "https://api.cloudflare.com/client/v4/accounts/account%2Fid/workers/scripts/worker%2Fname%20with%20spaces/secrets",
		},
		{
			name:     "escapes dot segments",
			segments: []string{"builds", "triggers", "..", "environment_variables", "."},
			want:     "https://api.cloudflare.com/client/v4/accounts/account%2Fid/builds/triggers/%2E%2E/environment_variables/%2E",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := client.cfURL(tt.segments...); got != tt.want {
				t.Fatalf("cfURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGHURL(t *testing.T) {
	tests := []struct {
		name     string
		segments []string
		want     string
	}{
		{
			name:     "static repo path",
			segments: []string{"repos", "worldcoin", "tfh-website"},
			want:     "https://api.github.com/repos/worldcoin/tfh-website",
		},
		{
			name:     "escapes dynamic path segment separators and spaces",
			segments: []string{"repos", "worldcoin", "nested/repo with spaces"},
			want:     "https://api.github.com/repos/worldcoin/nested%2Frepo%20with%20spaces",
		},
		{
			name:     "escapes dot segments",
			segments: []string{"repos", "..", "."},
			want:     "https://api.github.com/repos/%2E%2E/%2E",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ghURL(tt.segments...); got != tt.want {
				t.Fatalf("ghURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int64
		want bool
	}{
		{
			name: "matching API error code",
			err: &apiError{
				StatusCode: http.StatusNotFound,
				Errors: []cfErrorDetail{
					{Code: cfErrWorkerNotDeployed, Message: "worker has no deployments"},
				},
			},
			code: cfErrWorkerNotDeployed,
			want: true,
		},
		{
			name: "wrapped matching API error code",
			err: fmt.Errorf("listing runtime secrets: %w", &apiError{
				StatusCode: http.StatusNotFound,
				Errors: []cfErrorDetail{
					{Code: cfErrWorkerNotDeployed, Message: "worker has no deployments"},
				},
			}),
			code: cfErrWorkerNotDeployed,
			want: true,
		},
		{
			name: "different API error code",
			err: &apiError{
				StatusCode: http.StatusForbidden,
				Errors: []cfErrorDetail{
					{Code: 10000, Message: "forbidden"},
				},
			},
			code: cfErrWorkerNotDeployed,
		},
		{
			name: "non API error",
			err:  errors.New("network failed"),
			code: cfErrWorkerNotDeployed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasErrorCode(tt.err, tt.code); got != tt.want {
				t.Fatalf("hasErrorCode() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestListRuntimeSecretsTreatsUndeployedWorkerAsEmpty(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			wantPath := "/client/v4/accounts/account-id/workers/scripts/tfh-website-dev/secrets"
			if req.URL.Path != wantPath {
				t.Fatalf("path = %q, want %q", req.URL.Path, wantPath)
			}
			return jsonResponse(http.StatusNotFound, `{
				"success": false,
				"errors": [{"code": 10007, "message": "worker has no deployments"}]
			}`), nil
		}),
	}

	secrets, err := client.ListRuntimeSecrets(context.Background(), "tfh-website-dev")
	if err != nil {
		t.Fatalf("ListRuntimeSecrets() error = %v, want nil", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("ListRuntimeSecrets() = %v, want empty list", secrets)
	}
}

func TestListRuntimeSecretsReturnsOtherCloudflareErrors(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusForbidden, `{
				"success": false,
				"errors": [{"code": 10000, "message": "auth failed"}]
			}`), nil
		}),
	}

	_, err := client.ListRuntimeSecrets(context.Background(), "tfh-website-dev")
	if err == nil {
		t.Fatalf("ListRuntimeSecrets() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "10000: auth failed") {
		t.Fatalf("ListRuntimeSecrets() error = %q, want Cloudflare error detail", err)
	}
}

func TestDeleteRuntimeSecretDeletesSecretByName(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodDelete {
				t.Fatalf("method = %s, want DELETE", req.Method)
			}
			wantPath := "/client/v4/accounts/account-id/workers/scripts/tfh-website-dev/secrets/secret%2Fname"
			if req.URL.EscapedPath() != wantPath {
				t.Fatalf("path = %q, want %q", req.URL.EscapedPath(), wantPath)
			}
			return jsonResponse(http.StatusOK, `{"success": true}`), nil
		}),
	}

	if err := client.DeleteRuntimeSecret(context.Background(), "tfh-website-dev", "secret/name"); err != nil {
		t.Fatalf("DeleteRuntimeSecret() error = %v, want nil", err)
	}
}

func TestTriggerBuildPostsBranchAndCommit(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			wantPath := "/client/v4/accounts/account-id/builds/triggers/trigger-id/builds"
			if req.URL.Path != wantPath {
				t.Fatalf("path = %q, want %q", req.URL.Path, wantPath)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if got := string(body); got != `{"branch":"feature","commit_hash":"abc123"}` {
				t.Fatalf("body = %q, want branch and commit", got)
			}
			return jsonResponse(http.StatusOK, `{
				"success": true,
				"result": {"build_uuid": "build-1"}
			}`), nil
		}),
	}

	build, err := client.TriggerBuild(context.Background(), "trigger-id", TriggerBuildParams{
		Branch:     "feature",
		CommitHash: "abc123",
	})
	if err != nil {
		t.Fatalf("TriggerBuild() error = %v, want nil", err)
	}
	if build.BuildUUID != "build-1" {
		t.Fatalf("BuildUUID = %q, want build-1", build.BuildUUID)
	}
}

func TestDoJSONSetsCloudflareHeaders(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization header = %q, want bearer token", got)
			}
			if got := req.Header.Get("User-Agent"); got != "cf-worker-config" {
				t.Fatalf("User-Agent header = %q, want cf-worker-config", got)
			}
			if got := req.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type header = %q, want application/json", got)
			}
			return jsonResponse(http.StatusOK, `{"success": true}`), nil
		}),
	}

	err := client.doJSON(context.Background(), http.MethodPatch, client.cfURL("builds", "triggers", "trigger-id"), map[string]string{"name": "value"}, nil)
	if err != nil {
		t.Fatalf("doJSON() error = %v, want nil", err)
	}
}

func TestDoJSONReturnsErrorForUnsuccessfulEnvelopeWithoutErrors(t *testing.T) {
	client := NewClient("token", "account-id")
	client.http = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"success": false}`), nil
		}),
	}

	err := client.doJSON(context.Background(), http.MethodPatch, client.cfURL("builds", "triggers", "trigger-id"), nil, nil)
	if err == nil {
		t.Fatalf("doJSON() error = nil, want Cloudflare envelope error")
	}
	if !strings.Contains(err.Error(), "unknown error") {
		t.Fatalf("doJSON() error = %q, want unknown error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
