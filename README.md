# cf-worker-config

Idempotent reconciler for Cloudflare Worker configuration that sits outside Terraform and wrangler.jsonc. Automates the manual CF dashboard configuration that has no Terraform provider or wrangler CLI support.

## What it does

Manages the gap between Terraform (infra) and wrangler.jsonc (runtime):

- **Repo connection** — connects a GitHub repo to a Worker
- **Build triggers** — production branch, build/deploy commands, preview branches
- **Build variables and secrets** — env vars available during `next build`
- **Runtime secrets** — env vars available to the Worker at request time
- **Drift detection** — `plan --fail-on-drift` for scheduled CI checks

## Config

Create a `cf-worker-config.json` in your repo. This is strict JSON, not JSONC:

```json
{
  "account_id": "ae97c44fcd31b9289a7455120d2698b2",
  "workers": {
    "tfh-website-dev": {
      "worker_name": "tfh-website-dev",
      "repo": "worldcoin/tfh-website",
      "production_branch": "main",
      "root_directory": ".",
      "path_includes": ["*"],
      "build_caching_enabled": true,
      "build_command": "npm run build:cf",
      "deploy_command": "npm run deploy:cf:dev",
      "preview": {
        "enabled": true,
        "command": "npm run deploy:cf:preview",
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

If `repo_connection_uuid` is not set, the tool creates/ensures a repo connection from
the `repo` value. For private GitHub repositories, set `GITHUB_TOKEN` or provide
`provider_account_id` and `repo_id` explicitly.

If multiple Workers Builds tokens exist, set `build_token_uuid` or `build_token_name`.
The tool will fail instead of choosing an arbitrary token.

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

# Show current CF state
cf-worker-config status --worker tfh-website-dev

# Sync build vars/secrets only (skip trigger config)
cf-worker-config sync-build-vars --worker tfh-website-dev

# Sync runtime secrets only
cf-worker-config sync-runtime-secrets --worker tfh-website-dev

# Drift check in CI
cf-worker-config plan --worker tfh-website-dev --fail-on-drift
```

`apply` always syncs declared build vars and refreshes declared build/runtime
secrets when their values are present in the environment. Existing secrets are
left unchanged when no env value is present; missing secrets still require env
values and fail fast.

`plan --fail-on-drift` is not a strict runtime-secret drift check yet. Runtime
secret values are never compared, and the plan only flags declared runtime
secrets that are missing in Cloudflare. Extra runtime secrets that exist in
Cloudflare but are not listed in config are not reported or deleted. Strict
runtime-secret drift detection can be added later if this workflow becomes
authoritative for runtime secret names.

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
