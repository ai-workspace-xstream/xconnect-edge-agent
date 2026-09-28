# xconnect-edge-agent Proxy / Runtime Architecture

## Scope

`xconnect-edge-agent` is the lightweight runtime control service that runs on a VM. It is not a traditional data service; it synchronizes Xray configuration, reports heartbeat/status, schedules reconciliation jobs, and bridges the node to `accounts.svc.plus`.

## Architecture

```mermaid
flowchart TB
  VM["Linux VM / systemd"]
  Config["account-agent.yaml\nor env vars"]
  Main["cmd/agent/main.go"]
  Loop["agentmode.Run\nsync loop + status reporter"]
  Client["agentmode.Client\nHTTP client to controller"]
  Sync["xrayconfig periodic syncers"]
  Xray["xray-core"]
  Exporter["xray-exporter"]
  Caddy["Caddy / TLS / ACME"]
  Accounts["accounts.svc.plus"]
  Billing["billing-service"]
  AgentAPI["/api/agent-server/v1/users\n/api/agent-server/v1/status"]
  Edge["Optional Cloudflare Workers / container runtime"]

  VM --> Main
  Config --> Main
  Main --> Loop
  Loop --> Client
  Client -->|Authorization: Bearer + X-Service-Token + X-Agent-ID| AgentAPI
  Loop --> Sync
  Sync --> Xray
  Sync --> Caddy
  Xray --> Caddy
  Xray --> Exporter
  Exporter --> Billing
  AgentAPI --> Accounts
  Billing --> Accounts
  Edge --> AgentAPI
```

## API Matrix

| Name | Path | Purpose | Database / table | Auth mode |
| --- | --- | --- | --- | --- |
| List clients | `GET /api/agent-server/v1/users` | Fetch controller-authorized Xray clients; quota-exhausted clients are omitted | `account_quota_states`, `account_billing_profiles` (controller-side) | `Authorization: Bearer <agent token>` and `X-Service-Token`; optional `X-Agent-ID` |
| Report status | `POST /api/agent-server/v1/status` | Report heartbeat, health, and sync revision | N/A | `Authorization: Bearer <agent token>` and `X-Service-Token`; optional `X-Agent-ID` |
| Health | `GET /healthz` | Edge / worker health endpoint when deployed with the optional worker layer | N/A | none |

## Runtime Responsibilities

- Load `account-agent.yaml` or environment variables.
- Build and reload Xray configuration files.
- Keep TLS certificates live via Caddy.
- Poll accounts for client and node updates.
- Apply pure client additions and quota-renewal restores online through Xray HandlerService without restarting Xray.
- Treat controller events as the primary synchronization trigger, with a full-state poll at least every 30 seconds as a disconnect and missed-event fallback.
- Apply pure additions and quota-renewal restores online through Xray HandlerService. For client withdrawal, restart only the affected Xray target once per reconciled batch: `RemoveUser` rejects new authentication but does not close established VLESS sessions. Caddy is not restarted. This closes all sessions on that Xray target, not only the exhausted user's session; user, subscription, payment, refund, usage, and UUID records remain in the control plane.
- Report agent health and sync progress back to the controller.
- Schedule billing reconciliation and future control actions without owning the billing source of truth.
- Leave traffic metric translation to the separate exporter layer.

## Data / Storage Notes

- `xconnect-edge-agent` does not own a persistent application database in the core runtime path.
- Any persistence is externalized to the controller-side `accounts.svc.plus` tables, especially `agents`, `nodes`, and `users`.

## Notes

- The agent runtime supports a standalone mode, but the architecture above reflects the controller-managed mode used in the main Cloud-Neutral Toolkit flow.
- The optional edge deployment exposes the same agent-server endpoints without changing the controller contract.
- Caddy remains the TLS/XHTTP entrypoint. Per-account quota enforcement happens in the controller-to-Xray sync path because Caddy cannot see the VLESS account identity.
- Caddy is never restarted for a client-list change. A destructive XHTTP update briefly resets its upstream Unix-socket connections while Caddy itself remains available.
