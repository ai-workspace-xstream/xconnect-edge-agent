# Agent P0 quota disconnect: dependency contract

## Current implementation facts

- The Accounts agent users endpoint returns active users with their proxy UUIDs. It does not include quota state or a revision, and the current Accounts router does not provide the `users/events` endpoint consumed by this Agent.
- The Agent synchronizer coalesces user change triggers and retries from its normal interval. A removed or changed client currently takes the Xray restart path.
- Xray `HandlerService.RemoveUserOperation` removes a credential from the inbound user validator. It does not close an already established VLESS session. The Xray inbound handler's close path also closes the validator and related reverse handlers, but does not expose a per-user active-session close operation.
- Therefore, filtering an exhausted account from the current users response or invoking `xray api rmu` would deny future authentication but would not satisfy immediate disconnection of existing sessions without restarting Xray.

## Proposed Accounts contract

Keep user, subscription, payment, refund, and usage records intact. Extend the agent desired-state response additively with:

- a monotonic `revision` for the complete desired state;
- per account, a stable account identifier plus the existing proxy UUID and client attributes;
- an explicit quota enforcement state, such as `allowed` or `exhausted`, sourced from the authoritative monthly quota state;
- a documented reset/recovery transition that changes the revision and returns the account to `allowed` without rotating its UUID.

Publish a replayable or reconnect-safe SSE event carrying the revision when quota enforcement state changes. The Agent should treat the event as a prompt to fetch the complete desired state, coalesce duplicate/out-of-order revisions, and retain a 30-second full-state poll as recovery when the stream disconnects or events are missed. The current `GET /api/agent-server/v1/users` response can serve as that full-state read if it includes the enforcement state for all relevant active accounts, including exhausted ones.

## Required Xray/runtime capability

Add or expose an idempotent control operation that closes active sessions for a specific account identity while keeping the Xray process, Caddy process, account row, and proxy UUID intact. It must also prevent new sessions while exhausted, support restoring the same identity when quota recovers, and report a retryable error if disconnection is not confirmed. Removing the user from the validator alone is insufficient because it leaves established sessions alive.

Once both contracts exist, the Agent can apply state transitions serially per account: exhaust => mark sync state paused, close that account's active sessions, and reject later user-config syncs for it; recover => apply the retained UUID/client config and clear the pause. Failed side effects must remain pending and retry on duplicate events and the 30-second poll. Event handling must be idempotent under concurrent delivery and reconnect.

## Acceptance tests needed after dependencies are available

- concurrent quota and user-config events serialize without allowing a paused account back into runtime;
- duplicate and stale event revisions do not repeat destructive work or undo newer state;
- an exhausted account's already established session closes, while new connections are denied;
- configuration changes for that account remain paused during exhaustion;
- quota recovery restores the original UUID and resumes configuration sync;
- transient disconnect, deny, and restore failures retry successfully without process restarts or record deletion;
- event-stream loss is repaired by the 30-second full-state poll.

This note records the Agent task's cross-repository dependency. It does not change Accounts or claim the runtime behavior is implemented.

## Implementation update (2026-09-28)

The current Accounts `main` contract was confirmed during implementation: `GET /api/agent-server/v1/users/events` emits `users-changed` after its five-second `authorizedAgentClients` change check; the clients endpoint has ETag handling and filters banned users. The earlier statement above that these interfaces are absent is stale. No Accounts contract change is required for this Agent-side reconciliation.

The Agent coalesces event-triggered full-state reconciliations and polls at most every 30 seconds. Pure additions (including quota recovery) use HandlerService. Any client withdrawal restarts only that target's Xray once for the reconciliation; Caddy is never restarted. A local Xray integration test confirmed that `xray api rmu` rejected a new VLESS authentication but an already established session continued relaying payload, and terminating the Xray process closed that session. Therefore the configured Xray restart is the current immediate-disconnect guarantee, with impact to all sessions on the affected Xray target. Failed restart attempts remain uncommitted and are retried on the next event or poll.
