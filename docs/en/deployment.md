# Deployment

This repository mixes a Go runtime agent with supporting deployment automation and edge integration artifacts.

Use this page to standardize deployment prerequisites, supported topologies, operational checks, and rollback notes.

## UAT Agent Server deployment

The UAT shared and team editions are separate Agent Server targets. Each uses a dedicated
Ansible inventory; neither depends on Doco-CD or consumes the Web SaaS deployment config.

| edition | GCP VM | labels |
| :--- | :--- | :--- |
| shared | `agent-server-shared-uat` | `app=agent-server`, `edition=shared` |
| team | `agent-server-team-uat` | `app=agent-server`, `edition=team` |

Deployment order:

1. Bootstrap the target with `scripts/setup-proxy.sh` so Xray, Caddy, certificates, and the
   required runtime paths exist.
2. From `deploy/ansible`, run `./deploy.sh --uat-shared --deploy-only` or
   `./deploy.sh --uat-team --deploy-only` for the corresponding VM.
3. Inject `INTERNAL_SERVICE_TOKEN` through the controlled runtime environment. Never put it
   in inventory, vars, command lines, or Git. UAT targets do not update Cloudflare DNS.
4. Verify the `xconnect-edge-agent` systemd unit and perform one authenticated Accounts Agent
   API request.

Each inventory must contain exactly its intended VM. If the node domain, certificate, or UAT
token is missing, stop at preflight; do not guess a domain, reuse production credentials, or
use standalone mode as a substitute for a managed Agent deployment.

## Centralized delivery

The repository workflow builds and publishes immutable release artifacts only.
Environment deployment is owned by `platform-ops-toolkit`: its single
`deploy_tag` is used for the Accounts/Portal release and for the
`ai-workspace-xstream/xconnect-edge-agent` source checkout. The toolkit obtains
`INTERNAL_SERVICE_TOKEN` and the deployment SSH key through its OIDC-to-Vault
flow; this repository does not require GitHub Actions repository secrets for
deployment.

## Current code-aligned notes

- Documentation target: `xconnect-edge-agent`
- Repo kind: `hybrid-agent`
- Manifest and build evidence: go.mod (`xconnect-edge-agent`)
- Primary implementation and ops directories: `cmd/`, `internal/`, `agent/`, `deploy/`, `scripts/`, `example/`, `config/`
- Package scripts snapshot: `deploy`

## Existing docs to reconcile

- `mcp-ssh-manager-setup.md`

## What this page should cover next

- Describe the current implementation rather than an aspirational future-only design.
- Keep terminology aligned with the repository root README, manifests, and actual directories.
- Link deeper runbooks, specs, or subsystem notes from the legacy docs listed above.
- Verify deployment steps against current scripts, manifests, CI/CD flow, and environment contracts before each release.
