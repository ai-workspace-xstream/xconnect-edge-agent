# Cloudflare Containers — Agent + Xray Multi-Transport Runtime

This directory provides a container runtime for Cloudflare Workers Containers where three long-lived processes run together:

| Process | Command | Transport |
|---------|---------|-----------|
| **xconnect-edge-agent** | `/usr/local/bin/xconnect-edge-agent -config /etc/agent/account-agent.yaml` | Control Plane |
| **xray (XHTTP)** | `/usr/local/bin/xray run -config /usr/local/etc/xray/config.json` | XHTTP on Unix socket |
| **xray (TCP)** | `/usr/local/bin/xray run -config /usr/local/etc/xray/tcp-config.json` | TCP+TLS on port 1443 |

All processes are managed by `container-entrypoint.sh` under `tini`.

## Configurable Variables

The following agent config fields are injected via environment variables:

| Agent Config Field | Environment Variable | Fallback Variable | Description |
|-|-|-|-|
| `agent.id` | `AGENT_ID` | `DOMAIN` | Unique node identifier (e.g., `hk-xhttp.svc.plus`) |
| `agent.controllerUrl` | `AGENT_CONTROLLER_URL` | `CONTROLLER_URL` | Accounts controller URL |
| `agent.apiToken` | `AGENT_API_TOKEN` | `INTERNAL_SERVICE_TOKEN` | Service authentication token |

## Cloudflare Dashboard Configuration

When creating from Cloudflare dashboard (Workers & Pages → Create → Import a repository):

- **Build context**: repo root (`.`)
- **Dockerfile path**: `deploy/cloudflare/containers/Dockerfile`
- **Deploy command**: `npm run deploy` (from `deploy/cloudflare/containers/`)

Set runtime variables/secrets:

| Type | Name | Value |
|------|------|-------|
| Variable | `AGENT_ID` | `hk-xhttp.svc.plus` |
| Variable | `AGENT_CONTROLLER_URL` | `https://accounts-svc-plus-266500572462.asia-northeast1.run.app` |
| Secret | `AGENT_API_TOKEN` | *(set via `wrangler secret put`)* |

## Quick Start

```bash
# Install dependencies
cd deploy/cloudflare/containers
npm install

# Set the API token secret
npx wrangler secret put AGENT_API_TOKEN

# Dry-run validation
npm run check

# Deploy
npm run deploy
```

## Local Build Validation

```bash
# Build from repo root
docker build -f deploy/cloudflare/containers/Dockerfile -t xconnect-edge-agent-runtime:local .

# Run locally
docker run --rm -p 8080:8080 -p 1443:1443 \
  -e AGENT_ID=hk-xhttp.svc.plus \
  -e AGENT_CONTROLLER_URL=https://accounts-svc-plus-266500572462.asia-northeast1.run.app \
  -e AGENT_API_TOKEN=replace-with-token \
  xconnect-edge-agent-runtime:local
```

Health checks:

```bash
curl -fsS http://127.0.0.1:8080/healthz       # Always OK
curl -fsS http://127.0.0.1:8080/readyz         # OK when xray + agent running; includes per-process status
```

## Files

```
deploy/cloudflare/containers/
├── Dockerfile                                # Multi-stage: Go build + Debian runtime
├── wrangler.toml                             # Cloudflare Containers binding
├── package.json                              # NPM scripts for deploy
├── .dev.vars.example                         # Local dev environment template
├── worker/src/index.js                       # Worker HTTP gateway
├── container-src/healthz/main.go             # Health check server (Go)
├── runtime/
│   ├── entrypoint.sh                         # Container entrypoint (manages all 3 processes)
│   ├── account-agent.container.yaml          # Agent config template
│   ├── xray.bootstrap.json                   # XHTTP bootstrap config
│   ├── xray-tcp.bootstrap.json               # TCP bootstrap config
│   ├── restart-xray.sh                       # Xray restart helper
│   └── systemctl-shim.sh                     # systemctl shim for agent compatibility
└── cloud-run.service.yaml                    # Cloud Run deployment (same image)
```

## Architecture

```
                 Cloudflare Edge
Client ──HTTPS──► Worker (index.js) ──HTTP──► Container
                                               ├─ agent-healthz (:8080)
                                               ├─ xray (XHTTP: /dev/shm/xray.sock)
                                               ├─ xray (TCP: :1443)
                                               └─ xconnect-edge-agent (control plane)
```

### Config Sync Flow

1. Container starts → agent loads `account-agent.yaml` (with injected AGENT_ID, etc.)
2. Agent connects to controller → syncs client UUIDs and config
3. Agent renders xray templates → writes to `/usr/local/etc/xray/config.json` and `tcp-config.json`
4. Agent calls `systemctl restart xray.service` → shim kills xray PID → loop auto-restarts

### Transport Matrix

| Transport | Port / Socket | TLS | Cloudflare CDN | Notes |
|-----------|--------------|-----|----------------|-------|
| XHTTP | `/dev/shm/xray.sock` | Handled by Cloudflare Edge | ✅ Proxied | Ideal for CF Containers |
| TCP | `:1443` | Handled by Xray (XTLS) | ❌ Not proxied | Requires direct IP / L4 LB |

> **Note**: Cloudflare Containers only support inbound HTTP. The TCP xray instance runs for config sync purposes and works when deployed to VMs/Cloud Run, but cannot receive direct L4 TCP traffic in Cloudflare Containers.

## Cloud Run Deployment (Same Image)

The same Docker image works on Cloud Run:

```bash
gcloud builds submit --tag REGION-docker.pkg.dev/PROJECT/REPO/xconnect-edge-agent-runtime:latest .
gcloud run services replace deploy/cloudflare/containers/cloud-run.service.yaml --region REGION
```

## Troubleshooting

### Agent can't connect to controller
- Verify `AGENT_CONTROLLER_URL` is reachable from the container
- Check `AGENT_API_TOKEN` is set (via `wrangler secret put`)
- Look at container logs: `npx wrangler tail`

### Xray TCP not starting
- Expected in Cloudflare Containers (no L4 ingress)
- Check `/readyz` — XHTTP xray should still be running
- For TCP transport, deploy on VM or K8s instead

### Build fails
- Docker must be running locally (`docker info`)
- Build context must be the **repo root**, not `deploy/cloudflare/containers/`
- Ensure `go.mod` and `go.sum` are up to date
