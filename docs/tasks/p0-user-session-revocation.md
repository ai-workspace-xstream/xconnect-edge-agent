# P0-A per-user session revocation

Maintained core: ai-workspace-xstream/Xray-core, branch release/xwork-v26.3.27.
Baseline is upstream v26.3.27 at d2758a023cd7f4174a5a5fa4ff66e487d4342ba0.

Set `dynamicUsers.enabled: true` and `dynamicUsers.terminateSessions: true` only
after installing the reviewed extension on the corresponding Xray target.
The Agent calls `xray api rmus` with a capability-specific operation. Stock cores
reject it. Revocation errors leave the reconciliation pending for retry and do not
restart other users' sessions. Legacy targets retain the existing restart behavior
until explicitly opted in. Addition/recovery uses stock adu.

The operation withdraws authentication and closes registered logical VLESS sessions
under the same user email used by the account-to-proxy contract. It covers all
VLESS inbounds in the target template. Account, UUID and financial records stay in
Accounts. Caddy configuration does not change for quota events.

Run `P0_SESSION_EXTENSION=1 XRAY_SESSION_TEST_BINARY=<absolute binary>
bash scripts/test-p0-agent-quota-disconnect.sh` after building the maintained core.
The extension tests require real TCP, XHTTP packet-up/stream-up, and TCP Mux clients.
They verify revoked sessions close and unrelated users keep transferring. TCP also
verifies new authentication rejection, replay safety, and hot restoration.

UAT release order: reviewed core/Agent source → immutable artifacts and checksums →
GitOps UAT-only references → Playbooks install/check/backup and opt-in → Toolkit
dispatch and evidence → dedicated Accounts quota/SSE/recovery acceptance.
Binary replacement and initial reconciliation may restart Xray; subsequent revocation/recovery
must preserve Xray and Caddy PIDs. Rollback restores the previous binaries and
removes terminateSessions before restarting the affected target.

Local transport acceptance passed on 2026-10-13. UAT host acceptance is pending;
do not mark the Project P0-A card Done until live quota/event/session evidence is
bound to the deployed tag, source SHA, binary checksum and target.
