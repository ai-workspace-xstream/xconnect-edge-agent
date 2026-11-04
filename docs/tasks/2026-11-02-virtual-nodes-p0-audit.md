# Virtual nodes P0 source audit and implementation boundary

Task: https://github.com/ai-workspace-xstream/xconnect-edge-agent/issues/130

Review increments: [Agent #131](https://github.com/ai-workspace-xstream/xconnect-edge-agent/pull/131), [Accounts #290](https://github.com/ai-workspace-services/accounts/pull/290).

This is a source audit on 2026-11-02, not deployment or billing acceptance.

## Fixed source baselines

| Owner | Freshly fetched origin/main |
| --- | --- |
| Agent | `4458fe617699f0f9b40a6164c1dd6bcb17380d08` |
| Accounts | `677fb48ef92be426904864f8f849a1e9ac044bab` |
| xray-exporter | `1094bd8e11af30eda62fdbb2ecf9d8560d493829` |
| Billing | `2252253736591805689966805e8c51c4914c8837` |

Deployment ownership was read from Playbooks local HEAD
`31ae41832ea43b2eca73a63b2d2808b0d0b69f77`, not asserted as freshly fetched
or as the deployed version. No production hosts or databases were accessed.
Portal/UI behavior has not yet been source-audited or accepted.

## Accounts identity and scope

`internal/agentserver/registry.go` indexes status by logical agent ID and
credentials by token digest. Source IP is not the registration deduplication key;
multiple identities on one host are possible without a host registration schema.
However, `api/network_access.go:resolveAgentIdentity` permits non-team credential
aliases, and `api/agent_server.go:reportAgentStatus` accepts a separate reported
nodeId when writing health snapshots. Independent files containing a shared token
would not establish node isolation.

The companion Accounts change adds opt-in `nodeId` to a dedicated credential,
checks every identity selector and reported nodeId, prevents legacy aliases from
taking over the bound identity, and returns authenticated agentId/nodeId in user
snapshots. For bound custom nodes the enrolled credential owns allowedEmails and
`custom:allow-free`; headers cannot widen these. Credential-owned team nodes retain their existing fixed scope; the shared
credential managed approval/revocation path is unchanged and cannot take over a
node-bound identity. Legacy credentials retain their existing path.
This is static configuration binding, not a newly implemented token issuer or
proof of least privilege for existing deployed credentials. No SQL is required
for this first identity-binding increment. Durable topology/history and token
rotation still need their own delivery contract.

## Metering path and concrete gaps

- `xray-exporter/exporter.go` uses gRPC `QueryStatsRequest{Reset_: false}`:
  cumulative reads do not clear another collector's counters.
- `internal/snapshot/service.go` collapses inbound samples per stable Accounts
  UUID and emits node_id, env, collected_at and cumulative integer byte totals.
  It does not emit historical host_id, a source identity, explicit counter epoch,
  or an immutable event/sequence key.
- `internal/snapshot/store.go` retains a JSON window with rename-based updates;
  it has no persisted delivery ACK/outbox cursor or fsync protocol.
  `main.go:runSnapshotLoop` logs a failed push and proceeds to future collections.
  The saved window is not automatically a replay queue for the push path.
- Actual host/exporter wiring belongs to Playbooks
  `roles/vhosts/xray-exporter`; it already accepts exporter instance entries.
  Defaults use transport-suffixed node IDs, which must not be mistaken for the
  desired stable virtual-node identity. Do not rewrite deployed history by
  silently removing suffixes.
- Playbooks `roles/vhosts/vector-agent/templates/vector.toml.j2` forwards the
  snapshot object to Billing with a disk buffer. This template has no explicit
  end-to-end acknowledgement contract; accepting an HTTP request is not proof
  that Billing committed the event.
- Billing `internal/model/types.go`, `internal/service/service.go` and
  `sql/billing-service-schema.sql` already preserve node identity in checkpoints
  and minute buckets. Quota state is account-keyed, not allocated per node.
- Billing `processSample` infers reset only from decreasing totals. It has no
  explicit epoch and does not reject stale collected_at against LastSeenAt.
  Replaying old counters can therefore look like a reset. A restart followed by
  totals larger than the previous total cannot be detected this way.
- Billing's repository writes bucket, ledger, quota and checkpoint separately;
  its mutex is per process. `ON CONFLICT` ledger IDs do not make these writes one
  transaction. Failure after ledger insertion but before quota/checkpoint update
  can leave inconsistent state; another service instance can race the same user.
  Multiple samples in one minute reuse a bucket/ledger key while their deltas
  differ, so replacing those rows is not immutable event accounting.
- Snapshot ingest uses a shared bearer token; it does not bind an authenticated
  source to node_id. Host history and actual/billable/free attribution cannot be
  reconstructed reliably from current schemas alone.

These are blockers for enabling billable virtual nodes, not permission to alter
production billing behavior during this task.

## Minimum schema and repository evaluation (proposal, not applied)

A zero-schema promise is not justified. Review an additive migration with:

1. An authenticated stream registration/progress record binding source ID to
   stable nodeId, env, historical hostId and current epoch/sequence. Credential
   material remains outside the database payload/receipts. Epoch transitions
   require a writer handoff; counters alone cannot prove a process lifetime.
2. An immutable usage-event table keyed by source/epoch/sequence/account, storing
   collection interval, captured hostId, nodeId, actual uplink/downlink integer
   bytes, billable/free bytes and references to the ledger and entitlement rule.
   Store unresolved gaps explicitly. Keep historical events after node retirement.
3. One repository transaction that rejects duplicate/stale events, locks the
   account quota row, records usage, creates the ledger entry, updates global
   quota and advances stream/checkpoint state. All competing legacy and new quota
   writers must participate in the same lock/transaction protocol before mixed
   traffic is accepted. Node/user/host queries derive from these events; they do
   not apply a second charge.

This proposal deliberately does not include executable migration SQL. Before an
owner implementation PR fixes the DDL, inspect migration conventions, Accounts
quota synchronization and actual/free entitlement semantics; choose retention
and compatibility/backfill behavior. Old usage without host evidence is unknown,
not attributed to today's host. Database migration execution remains a separate
production authorization boundary.

Required isolated tests include: two nodes using the same account, concurrent
service instances, duplicate delivery, out-of-order samples, interruption after
each transactional write, restart with lower and higher totals, offline replay,
free benefit consumption, disabled/paused account revocation, and historical
host migration. Real PostgreSQL transaction tests are necessary; an in-memory
repository test alone cannot establish atomicity.

## First reviewable increment

Agent now exposes `-capabilities` and `-check-config` before telemetry or runtime
initialization. Version 2 main envelopes use strict known fields, reject duplicate
keys/documents/aliases, and preserve disabled/absent legacy behavior. Disabled
lists are never opened. Enabling a list fails closed, including ambiguous legacy
business environment variables and empty lists. The capability explicitly reports
`virtualNodeRuntime: false`; it must not be used as a multi-node release.

Full node-child/runtime/path/socket validation, isolated node execution, bounded
reconciliation, systemd adapter, shared Caddy delivery, exporter replay and atomic
Billing remain unimplemented. The first increment is intentionally not an
activation mechanism. Existing single-node targets and billing scheduling are
untouched. Future support must replace the explicit gate only after owning
contracts and their tests are implemented; do not silently change the capability
or deploy the Draft v2 samples.

## Local verification receipts

- Agent `go test ./...`: PASS, including legacy runner/targets and customAllowFree.
- Accounts `go test ./internal/agentserver ./api`: PASS. Final focused identity,
  Agent and team-enrollment regressions also PASS after the takeover guard.
- Exporter `go test ./internal/snapshot`: baseline PASS; no exporter implementation
  changes were made.
- Billing `go test ./internal/service ./internal/httpapi ./internal/repository`:
  baseline PASS (repository package has no tests); this does not establish
  PostgreSQL atomicity or prove the gaps above fixed.
- CLI capability probe returns `virtualNodeRuntime: false`. Offline multi-node
  sample fails with the explicit runtime-prerequisite error and no child reads.

CI status belongs to each PR; local test success is not a build, merge, fixed
artifact, UAT or production receipt.
