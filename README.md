# cf-worker-config

Idempotent reconciler for Cloudflare Worker configuration that sits outside Terraform and wrangler.jsonc. Automates the manual CF dashboard configuration that has no Terraform provider or wrangler CLI support.

## What it does

Manages the gap between Terraform (infra) and wrangler.jsonc (runtime):

- **Repo connection** — connects a GitHub repo to a Worker
- **Build triggers** — production branch, build/deploy commands, preview branches
- **Build variables and secrets** — env vars available during `next build`
- **Runtime secrets** — env vars available to the Worker at request time
- **Post-apply builds** — queues a Workers Build after reconciliation
- **Drift detection** — `plan --fail-on-drift` for scheduled CI checks

## Config

Create a `cf-worker-config.json` in your repo. This is strict JSON, not JSONC:

```json
{
  "account_id": "ae97c44fcd31b9289a7455120d2698b2",
  "repo": "worldcoin/tfh-website",
  "repo_connection_uuid": "52ef781c-6fdd-4e2a-b868-c501ed11f0da",
  "workers": {
    "tfh-website-dev": {
      "production_branch": "main",
      "root_directory": ".",
      "path_includes": ["*"],
      "build_caching_enabled": true,
      "build_command": "npm run build:cf",
      "deploy_command": "npm run deploy:cf:dev",
      "preview": {
        "enabled": true,
        "command": "npm run deploy:cf:preview",
        "branch_includes": ["*"],
        "branch_excludes": ["main", "prod", "promote-main-to-prod"],
        "build_vars": {
          "NEXT_PUBLIC_APP_ENV": "preview"
        }
      },
      "build_vars": {
        "NODE_VERSION": "22",
        "NEXT_PUBLIC_APP_ENV": "production",
        "NEXT_PUBLIC_METRICS_API_URL": "https://metrics.worldcoin.org",
        "NEXT_PUBLIC_GTM": "GTM-NC5VGCVL"
      },
      "build_secrets": [
        "NEXT_SERVER_HUBSPOT_API_TOKEN",
        "NEXT_SERVER_ASHBY_API_KEY",
        "NEXT_SERVER_IRONCLAD_API_KEY",
        "NEXT_SERVER_IP_HUB_API_KEY",
        "NEXT_PUBLIC_EMAIL_ENCRYPTION_KEY"
      ],
      "runtime_secrets": [
        "NEXT_SERVER_HUBSPOT_API_TOKEN",
        "NEXT_SERVER_ASHBY_API_KEY",
        "NEXT_SERVER_IRONCLAD_API_KEY",
        "NEXT_SERVER_IP_HUB_API_KEY",
        "NEXT_PUBLIC_EMAIL_ENCRYPTION_KEY"
      ]
    }
  }
}
```

Secret **values** are read from environment variables at apply time, not from the config file.

`repo` and `repo_connection_uuid` are top-level because a config file normally
lives in one application repository and the repo connection is shared by all
Workers in that file. Worker-level repo connection fields are intentionally not
part of the JSON config schema.

If `repo_connection_uuid` is not set, the tool creates/ensures a repo connection
from the `repo` value. For private GitHub repositories, set `GITHUB_TOKEN` or
provide top-level `provider_account_id` and `repo_id` explicitly.

If multiple Workers Builds tokens exist, set `build_token_uuid` or `build_token_name`.
The tool will fail instead of choosing an arbitrary token.

Preview branch filters are optional. If `preview.branch_includes` and
`preview.branch_excludes` are omitted, the tool preserves the Cloudflare
dashboard default: include all branches (`["*"]`) and exclude the worker's
`production_branch`. Configure these fields when multiple Workers are connected
to the same repo and each environment needs its own preview branch scope.
Cloudflare supports `*` as the all-branches wildcard and exact branch names;
partial glob patterns such as `promote-*-to-prod` are not supported by the
Workers Builds API.

The config parser uses Go's standard `encoding/json` with unknown fields rejected.
This keeps the tool dependency-free and catches typos in config keys.

## Auth

Requires `CLOUDFLARE_API_TOKEN` with:
- `Workers Builds Configuration: Edit`
- `Workers Scripts: Edit`
- `Workers Scripts: Read`

This must be a **user-scoped** token (CF limitation for Builds API).

## Commands

```bash
# Show what would change
cf-worker-config plan --worker tfh-website-dev

# Apply changes
cf-worker-config apply --worker tfh-website-dev

# Apply changes without queueing a Workers Build
cf-worker-config apply --worker tfh-website-dev --trigger-build=false

# Apply and explicitly choose the build ref
cf-worker-config apply --worker tfh-website-dev --build-branch feature/foo --build-commit abc123

# Show current CF state
cf-worker-config status --worker tfh-website-dev

# Sync build vars/secrets only (skip trigger config)
cf-worker-config sync-build-vars --worker tfh-website-dev

# Sync runtime secrets only
cf-worker-config sync-runtime-secrets --worker tfh-website-dev

# Sync runtime secrets and delete runtime secrets not listed in config
cf-worker-config sync-runtime-secrets --worker tfh-website-dev --prune

# Print the repo connection UUID currently used by a worker
CLOUDFLARE_ACCOUNT_ID=ae97c44fcd31b9289a7455120d2698b2 \
  cf-worker-config get-repo-connection --worker tfh-website-dev

# Drift check in CI
cf-worker-config plan --worker tfh-website-dev --fail-on-drift
```

`apply` always syncs declared build vars and refreshes declared build secrets
when their values are present in the environment. Runtime secrets are not
created or refreshed during `apply`; use `sync-runtime-secrets` when
intentionally creating or rotating runtime secrets. Pass `--prune` to
`sync-runtime-secrets` to delete runtime secrets that exist in Cloudflare but
are not listed in `runtime_secrets`.

`get-repo-connection` does not read `cf-worker-config.json`. It reads the
existing Workers Builds triggers directly from Cloudflare and requires
`--account-id` or `CLOUDFLARE_ACCOUNT_ID`.

`apply` queues a Workers Build by default after reconciliation. In GitHub
Actions, the tool uses `GITHUB_HEAD_REF`/`GITHUB_REF_NAME` and the pull request
head SHA from `GITHUB_EVENT_PATH` when available. Locally, it falls back to the
current git branch and commit, then to `production_branch` for the branch. Pass
`--trigger-build=false` to skip this, or use `--build-branch`/`--build-commit`
to override the detected ref.

`plan --fail-on-drift` is not a strict runtime-secret drift check yet. Runtime
secret values are never compared, and the plan only flags declared runtime
secrets that are missing in Cloudflare. Extra runtime secrets that exist in
Cloudflare but are not listed in config are only deleted by
`sync-runtime-secrets --prune`.

`plan` compares typed desired config against typed Cloudflare API responses. That
is intentionally more stable than a raw JSON diff because Cloudflare responses
contain API-only fields, IDs, defaults, and ordering differences.

## How it fits

```
Terraform           → Worker, KV, domains, DNS, WAF, Zero Trust
wrangler.jsonc       → Runtime bindings, compatibility, observability
cf-worker-config     → Workers Builds integration, repo connection, build env/secrets, runtime secrets
Workers Builds       → Auto build/deploy on git push
```

## Project layout

This is a small single-purpose CLI, so the Go code is intentionally flat and uses
one `package main`:

```
main.go          entrypoint
cli.go           command parsing and dispatch
config.go        cf-worker-config.json loading and validation
cloudflare.go    Cloudflare/GitHub HTTP client
reconciler.go    plan/apply/status logic
```

## Build

```bash
cd scripts/cf-worker-config
go build -o cf-worker-config .
```
