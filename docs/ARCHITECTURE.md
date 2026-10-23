# EdgeBox v0.1 Architecture

## Status and scope

This document defines the first implementation boundary for EdgeBox v0.1. It is subordinate to the repository `AGENTS.md` and deliberately does not design the future relay or control-plane backends beyond the contracts needed by `edge-agent`.

The v0.1 deliverable is one small Go agent plus an independently supervised reverse-SSH management service on a Debian/Armbian appliance. The target is ARM64, but application code must remain portable across Linux architectures.

## Non-negotiable invariants

1. The EdgeBox initiates every cloud connection. Operation must not require a public address, port forwarding, UPnP, DDNS, or inbound Internet reachability.
2. Control, management, and data planes have separate failure domains.
3. The host's ordinary WAN path in the main routing table remains the path for locally originated management, control, DNS, package, and administration traffic.
4. Data-plane routing is explicit, scoped to forwarded EdgeBox traffic, and reversible. It must not blindly replace the host default route.
5. EdgeBox owns only its named routing rules, routes, traffic-control objects, and the isolated `inet edgebox` nftables table. It never flushes or rewrites unrelated host state.
6. Network configuration is transactional: validate, prepare, apply, verify, then commit; otherwise rollback.
7. A data-plane failure degrades data service without intentionally disrupting the management tunnel, control connectivity, or the host's normal WAN connectivity.
8. SAFE_MODE favors recoverability and removes EdgeBox-owned data-plane state without reboot-looping the appliance.

## Logical planes

### Control plane

`edge-agent` will use outbound HTTPS for registration, desired runtime configuration, release metadata, telemetry, and structured health reports. Bootstrap configuration under `/etc/edgebox/` contains only the stable information needed to locate and authenticate to central infrastructure. Runtime configuration and known-good state live under `/var/lib/edgebox/`.

No control backend is required for the initial local foundation. Its client boundary should be explicit so a later implementation does not leak HTTP details into state, networking, or transport code.

### Management plane

A separate `edge-management.service` maintains an outbound OpenSSH reverse tunnel to a bastion. The bastion-side forwarded port binds to `127.0.0.1` only and terminates at the EdgeBox's local SSH server. Each device has its own Ed25519 key and assigned bastion port.

This service is independently restartable and is not supervised by the data-plane state machine. Its connection remains ordinary locally originated traffic in the main routing table. The detailed design and bastion hardening are in [REMOTE_MANAGEMENT.md](REMOTE_MANAGEMENT.md).

### Data plane

The initial flow is:

```text
Relay traffic
  -> WireGuard Transport V1 packet endpoint
  -> data-plane classification
  -> EdgeBox-owned policy routing
  -> EdgeBox-owned forwarding and NAT
  -> physical WAN
```

Only traffic admitted from the configured transport endpoint is eligible for this path. Locally originated EdgeBox traffic is excluded by design.

WireGuard is one transport implementation, not an architectural dependency. The transport contract describes lifecycle, a generic packet endpoint, effective MTU, and structured health. It does not own nftables, NAT, policy routing, management SSH, or the generic health model. A future userspace transport need not masquerade as a WireGuard interface.

## `edge-agent` boundaries

Packages are introduced only when they contain behavior. Expected boundaries are:

| Boundary | v0.1 responsibility |
| --- | --- |
| configuration | Load, validate, normalize, version, and persist bootstrap/runtime configuration without logging secrets. |
| identity | Load the persistent `device_id` and references to device-local credentials. Location remains mutable deployment metadata. |
| state | Own the explicit lifecycle and transitions; prevent scattered restart loops. |
| transport | Define transport lifecycle and packet endpoint; contain WireGuard-specific implementation. |
| dataplane/network | Build reversible plans for policy routing, forwarding, NAT, MTU, and bandwidth ceiling. |
| management | Observe reverse-SSH service health and report it; do not implement a remote command API. |
| health/telemetry | Publish per-domain health and low-volume operational metrics without an on-device database. |
| control | Exchange desired state and reports over outbound HTTPS when that client is implemented. |

The first implementation may combine closely related boundaries where that keeps the code clearer. Empty package scaffolding is not an objective.

## State model

The initial state machine is:

```text
BOOT -> NETWORK_WAIT -> REGISTERING -> CONFIGURING -> CONNECTING -> ONLINE
                        |               |               |          |
                        +---------------+---------------+----------+
                                        v
                                    DEGRADED
                                        |
                                    RECOVERING
                                        |
                       +----------------+----------------+
                       |                                 |
                    ONLINE                           SAFE_MODE
```

Transitions are driven by typed observations and bounded retry policy. A reboot is not a recovery transition. Management health is reported independently and does not become false merely because transport or egress verification fails.

## Transactional network ownership

A configuration version produces an immutable desired plan before commands run. The transaction records:

- pre-apply observations relevant to EdgeBox-owned state;
- every intended rule, route, nftables object, and traffic-control object;
- the commands needed to apply and remove those objects;
- verification probes and timeouts;
- the previously committed known-good version.

Apply operations must use exact identifiers and idempotent replacement where supported. nftables changes are submitted as one checked batch that touches only `inet edgebox`. Policy rules use reserved priorities and routing table identifiers documented in configuration, not discovered by guessing at runtime. Rollback removes or restores only objects owned by the failed transaction.

Commit occurs only after management-path, ordinary WAN, transport, policy-routing, NAT, and egress checks appropriate to the change succeed. A watchdog must be able to roll back if the applying process crashes or verification does not complete.

## Persistent layout

The proposed layout is:

```text
/etc/edgebox/bootstrap.yaml              stable, non-secret bootstrap data
/var/lib/edgebox/identity/device-id       persistent identity
/var/lib/edgebox/credentials/             root-owned device credentials
/var/lib/edgebox/config/current/           committed runtime configuration
/var/lib/edgebox/config/previous/          previous known-good configuration
/var/lib/edgebox/config/factory/           safe/default configuration
/var/lib/edgebox/transactions/             bounded transaction journals
/var/lib/edgebox/management/               reverse-SSH key and known_hosts
```

Secret files must be root-owned with restrictive modes. Runtime logs contain credential references or fingerprints, never secret values.

## Health and observability

Health is structured by domain: system, WAN, management, control, transport, routing, NAT, Internet egress, and agent state. Each item carries a status, observation time, concise reason code, and optional non-sensitive measurements. Overall health must not collapse these dimensions into a single reachability boolean.

Logs are structured for journald and include `device_id`, component, event, transport, relay, configuration version, and error where applicable. Telemetry is low-volume and includes uptime, capacity, temperature when available, interface and data-plane counters, relay quality, reconnects, public address observations, selected transport/relay, committed version, and SAFE_MODE state. Automated bulk speed tests are out of scope.

## Explicit non-goals for v0.1

- MASQUE, custom QUIC tunneling, and automatic transport switching
- complex relay selection or a production control-plane backend
- Kubernetes, containers, microservices, or an on-device database
- billing, geo-routing, or full IPv6 egress
- A/B root filesystem updates

## Approved Phase 2C extension

The user's Phase 2C instruction explicitly adds an offline management AP, a
small Go Web console, and signed application OTA to the original scope. It
supersedes the earlier Web-UI non-goal, not the management-survival invariants.
The unprivileged `edge-web` talks to a separately supervised `edge-updater`
through a restricted Unix socket. Only fixed, validated operations are exposed;
HTTP never accepts command text. Reverse SSH and the isolated AP are independent
of both processes. Desired node profiles do not represent an active transport.

The approved URL-update extension accepts a direct public HTTPS signed
application ZIP, not arbitrary firmware or TUF target metadata. The root broker
performs bounded, destination-restricted downloads and admits only verified
packages into its owned inbox. Installation still requires explicit confirmation
and the existing transactional updater. URLs are not persisted, no periodic
polling is added, and the Web process cannot alter the release trust root.
Deployment-specific qualification records are kept locally.
See the source packages and
the public installation guide for current distribution boundaries.

## Authorized first live data path — 2026-10-15

The owner confirmed that the existing VPS is the common relay and authorized
allocation of addresses/ports and device-local credentials. Client entry is
VLESS + REALITY through Xray on the VPS, compatible with the existing Windows
v2rayN client and corresponding mobile clients. The live flow is client -> VPS
application proxy -> generic kernel packet endpoint -> N1 IPv4 forwarding/NAT
-> physical WAN. WireGuard remains an internal transport, not the user-facing
client protocol.

`cmd/edge-dataplane` is a small independently supervised privileged executor.
It is installed outside the unprivileged application release tree at
`/opt/edgebox-network/`; current signed Web updates cannot replace it. Its
WireGuard endpoint lifecycle lives in `internal/transport/wireguard` and its
policy/NAT/shaping implementation lives in `internal/network/egress.go`.
This executable is a first operational increment; it is not a claim that the
original unprivileged `edge-agent` foundation now implements a control backend
or transactional remote desired-configuration activation.

The edge classifies only forwarded packets arriving on the reserved endpoint.
The relay selects only packets with the proxy's dedicated source address;
its proxy additionally binds the endpoint. Both reserve a separate routing
table with a terminal unreachable route. No operating-system default route is
replaced. The edge rejects private/special destination ranges and access to
the appliance itself from the data endpoint. NAT and shaping belong to the
network layer; the management AP still rejects forwarding.

The executor reconciles crash remnants before startup, removes owned state
on orderly stop and via systemd ExecStopPost, and retries from SAFE_MODE after
apply failure or a stale handshake. Physical WAN gateway/interface changes
trigger cleanup and rediscovery. Status distinguishes observed transport state
from externally verified Internet egress; a handshake alone does not certify
egress. Startup fail-safe cleanup and repeated apply/cleanup were tested in an
isolated network namespace before live activation. First deployment used
independent root rollback timers, cancelled only after actual client egress and
fresh reverse SSH checks.

Scope limits: the new executor has not been connected to Web 2c.2's desired-node
store. Its root-owned configuration is not remotely editable through that UI.
Full factory/previous/current runtime-configuration transactions, a signed upgrade contract for the privileged executor, hard power-cut qualification, and gateway-change qualification remain follow-up work.

## Public installation entry point

The source bootstrap builds all application commands. It prepares a fresh data-plane deployment without activating it. An independent reverse SSH service is a prerequisite to edge activation. Operators exchange only public keys, stage explicit endpoint configuration, start under a systemd rollback timer, verify fresh management access and real client egress, then explicitly commit on both hosts. Existing installations are not overwritten. Web/AP/application OTA activation is separate from data-plane installation; no full-system OTA is promised. See [installation guide](INSTALL.zh-CN.md).
