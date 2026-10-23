# EdgeBox v0.1 Failure Model

## Safety objective

The primary safety property is:

> Failure or rollback of the data plane must not unnecessarily destroy the EdgeBox's ordinary WAN path, reverse-SSH management access, or control-plane connectivity.

No recovery action may assume physical access. Every network mutation must be bounded, observable, and paired with an automatic rollback path before it is attempted remotely.

## Failure domains and responses

| Failure | Detection | Immediate response | Management expectation |
| --- | --- | --- | --- |
| WAN carrier loss | Link state plus loss of gateway/DNS/HTTPS observations | Enter `NETWORK_WAIT` or `DEGRADED`; stop futile transport reconnect churn; retain desired state | Reverse SSH disconnects naturally and retries with backoff when WAN returns |
| DHCP lease, gateway, or public address change | NetworkManager event and periodic route/address observation | Re-resolve endpoints, re-evaluate effective WAN/MTU, restart only affected transport state | Management reconnects through the new main-table route |
| DNS failure | Resolver status plus bounded resolution probes | Preserve established sessions; report DNS health separately; back off new connections | Existing SSH may survive; reconnection remains degraded until resolution works unless bootstrap uses a validated address strategy |
| Relay unavailable or restarted | Transport handshake/endpoint health timeout | Mark transport unhealthy; bounded reconnect; do not alter main default route | No intentional effect |
| WireGuard handshake loss or UDP degradation | Last-handshake age, transfer counters, loss/RTT probes | Restart or recreate only the transport after threshold; escalate to recovery/SAFE_MODE if verification keeps failing | No intentional effect |
| Invalid desired configuration | Schema, semantic, ownership, conflict, and preflight validation | Reject before apply; retain committed configuration | No effect |
| nftables batch failure | `nft --check`, command result, post-apply inspection | Do not commit; restore prior EdgeBox table or remove newly created table | Main host rules and SSH path remain untouched |
| Policy-routing apply or verify failure | Command result, exact rule/route inspection, path probes | Remove transaction-owned rules/routes and restore previous known-good objects | Locally originated management remains in `main` throughout |
| Traffic-control failure | Command result and exact qdisc/class inspection | Remove only EdgeBox-owned shaping objects; continue unshaped or fail the transaction according to policy | No intentional effect |
| `edge-agent` crash | systemd service state and transaction journal | systemd restarts agent; startup reconciles or rolls back incomplete transaction before applying new work | `edge-management.service` continues independently |
| Management tunnel failure | systemd state, SSH process state, keepalive exit, optional bastion observation | systemd restarts with bounded delay; health reports degraded management | Data plane is not torn down solely for this failure, but risky new network apply is blocked |
| Control plane unavailable | HTTPS failure and stale desired-state age | Continue last committed configuration within policy; report locally; retry with backoff | Management remains available for recovery |
| Power loss during apply | Incomplete durable transaction journal at boot | Reconcile exact owned state; prefer rollback to previous known-good configuration | Management service starts independently from data plane |
| Persistent repeated apply/verification failures | Consecutive failure counter within a time window | Enter SAFE_MODE; stop automatic re-apply loop | Reverse SSH and control traffic get priority |
| Disk full or read-only filesystem | Write/fsync failure and filesystem health | Do not claim commit; retain in-memory service only where safe; report degraded | Avoid restarts that amplify failure; management remains separate |
| Clock error | Time-sync status and impossible timestamps/certificate failures | Report system/control degradation; do not weaken TLS validation | SSH keys remain usable; operator can recover through bastion |

## Configuration transaction

Each high-risk configuration follows these gates:

1. **Validate:** schema, ranges, required references, interface existence, ownership conflicts, port/table/priority uniqueness, and secret-file permissions.
2. **Prepare:** render exact command batches, run syntax/preflight checks, save a durable journal and previous known-good snapshot, and arm rollback.
3. **Apply:** make the smallest scoped changes. Never replace the host default route and never run `nft flush ruleset`.
4. **Verify:** inspect exact owned state; verify main-table WAN and management path; then verify transport, forwarding, NAT, MTU, shaping, and Internet egress as applicable.
5. **Commit:** atomically mark the version known-good and disarm rollback.
6. **Rollback on any failure:** restore only EdgeBox-owned state, re-check ordinary WAN and management, and report the failed version and reason.

An agent crash or power loss between prepare and commit is treated as an uncommitted transaction on the next start.

## SAFE_MODE

SAFE_MODE is a stable degraded state, not an uncontrolled cleanup script. Entry is appropriate after repeated transactional failures, an unrecoverable ownership conflict, or inability to verify management/WAN survival after a data-plane change.

Entry actions, in order, are:

1. stop the data-plane transport;
2. cancel pending data-plane retries and transactions;
3. remove EdgeBox-owned policy rules and routes by exact identifier;
4. remove only the EdgeBox-owned `inet edgebox` nftables table;
5. remove EdgeBox-owned shaping state;
6. verify that the pre-existing main-table default route, DNS, and outbound HTTPS work;
7. leave `edge-management.service` running and verify its local/systemd health where possible;
8. persist and report SAFE_MODE with the triggering reason.

SAFE_MODE must not flush the nftables ruleset, delete unrelated routes, reconfigure NetworkManager, disable SSH, or reboot-loop.

Automatic exit is allowed only for a previously verified configuration under a bounded recovery policy. A new unverified desired configuration must not repeatedly pull the host out of SAFE_MODE. Operator-triggered recovery must run the same transaction and survival checks as an automatic apply.

## Verification strategy

Tests must separate host-local management traffic from forwarded data-plane traffic. At minimum, integration procedures will verify:

- repeated apply produces no duplicate rules or semantic drift;
- a deliberately invalid nftables or route plan leaves existing state intact;
- a forced apply timeout triggers rollback;
- killing WireGuard or the relay degrades only the data plane;
- killing `edge-agent` does not kill the independently supervised management service;
- WAN disconnect/reconnect restores DNS, control, and reverse SSH without physical intervention;
- an incomplete transaction is recovered after agent restart and after power-loss simulation;
- SAFE_MODE removes only EdgeBox-owned objects and preserves the main default route;
- management-path probes still use the physical WAN/main table while data-plane policy is active.

Namespace/VM tests should be used before any target-host integration. Live target changes remain blocked until rollback is implemented and the reverse-SSH survival path is proven.
