// cf-worker-config is an idempotent reconciler for Cloudflare Worker
// Git integration. It manages the gap between Terraform (infra) and wrangler.jsonc
// (runtime) — specifically the Workers Builds configuration that has no Terraform
// provider support and no wrangler CLI coverage.
//
// Usage:
//
//	cf-worker-config plan   --config cf-worker-config.json --worker tfh-website-dev
//	cf-worker-config apply  --config cf-worker-config.json --worker tfh-website-dev
//	cf-worker-config status --config cf-worker-config.json --worker tfh-website-dev
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
