# EdgeBox — AGENTS.md

## 1. Project Mission

EdgeBox is a remotely deployed Linux edge egress appliance.

It runs on low-power ARM64 Linux devices such as Phicomm N1, but the architecture MUST NOT depend on a specific hardware model.

An EdgeBox is typically deployed behind:

* residential routers
* NAT
* CGNAT
* dynamic IPv4
* possibly unstable international connectivity

The device must actively establish all required cloud connections.

Never require inbound Internet connectivity to the EdgeBox.

The long-term system consists of three logical components:

* edge-agent — runs on the residential EdgeBox
* edge-relay — runs on VPS infrastructure and carries the data plane
* edge-control — provides configuration, device registry, telemetry, releases and policy

The current priority is EdgeBox v0.1.

Do not prematurely build all future components.

---

# 2. Core Architectural Invariants

These rules MUST NOT be violated.

## 2.1 Edge initiates connections

The EdgeBox must work behind NAT and CGNAT.

It must never depend on:

* public IPv4
* port forwarding
* UPnP
* DDNS
* inbound connectivity

The EdgeBox initiates connections to:

* Control Plane
* Relay
* Management Bastion

---

## 2.2 Control Plane, Management Plane and Data Plane are independent

There are three separate logical planes.

### Control Plane

Responsible for:

* registration
* configuration
* telemetry
* health reporting
* release information
* policy

Normally transported over HTTPS.

### Management Plane

Responsible for human emergency access.

Primary mechanism:

* outbound reverse SSH tunnel from EdgeBox to Management Bastion

Optional secondary mechanism:

* dedicated low-volume management WireGuard tunnel

The management plane MUST remain usable when the data plane fails.

### Data Plane

Responsible only for carrying user IP traffic through the EdgeBox residential WAN.

A failure in the Data Plane MUST NOT intentionally break:

* Control Plane connectivity
* Management SSH connectivity
* normal WAN connectivity of the EdgeBox itself

---

# 3. Remote Management Is a First-Class Requirement

EdgeBox is a remote appliance.

Assume physical access may be impossible.

SSH recovery access is mandatory.

Do NOT expose TCP/22 directly to the public Internet.

The preferred design is:

EdgeBox
|
| outbound SSH connection
|
v
Management Bastion
|
| reverse forwarded localhost port
|
Operator

Example logical mapping:

EdgeBox localhost:22
->
Bastion 127.0.0.1:22001

The remote-forwarded SSH port MUST bind only to loopback on the bastion by default.

Do not expose reverse SSH ports using:

0.0.0.0

unless explicitly requested.

Each EdgeBox MUST have unique credentials.

Never share a private SSH key between all devices.

Prefer Ed25519 keys.

Password SSH login should be disabled.

The reverse tunnel should be maintained by systemd.

It should use settings equivalent to:

* ExitOnForwardFailure=yes
* ServerAliveInterval
* ServerAliveCountMax
* BatchMode=yes

systemd should restart the tunnel after failure.

The management SSH process and its traffic MUST NOT depend on the EdgeBox data-plane routing table.

---

# 4. Management Bastion Security

The management bastion is security-sensitive.

Create a dedicated tunnel account.

Restrict it as much as practical.

Desired OpenSSH restrictions include:

* public key authentication only
* no password authentication
* no agent forwarding
* no X11 forwarding
* no arbitrary local forwarding
* remote forwarding only where required
* no public GatewayPorts
* no unnecessary shell access
* explicit PermitListen or equivalent restrictions where practical

Remote tunnel ports must be tracked per device.

Example:

KZ001 -> 22001
KZ002 -> 22002
MY001 -> 22101

The port assignment implementation may later be moved into edge-control.

Do not build a complex dynamic allocation service in v0.1.

---

# 5. Management Survival Rule

Network modifications MUST preserve management access.

Never solve data-plane routing by blindly replacing the operating system default route.

Management traffic must continue to use the physical WAN path.

Use Linux policy routing where required.

Conceptually:

main routing table
=
device management traffic

dedicated EdgeBox routing table
=
forwarded egress traffic

The following traffic must remain independent from the data plane:

* reverse SSH
* edge-control HTTPS
* package/release retrieval
* DNS required for management
* system administration

---

# 6. Safe Mode

The system MUST have a SAFE_MODE design.

If a new configuration causes repeated failures, EdgeBox must prioritize restoring basic Internet connectivity over maintaining the data plane.

SAFE_MODE should conceptually:

1. stop the data-plane transport
2. remove EdgeBox-owned policy routing
3. remove EdgeBox-owned nftables state
4. preserve the normal WAN default route
5. preserve reverse SSH
6. preserve edge-control connectivity
7. report degraded/safe-mode state

Never intentionally reboot-loop a device because the data plane is unhealthy.

---

# 7. Configuration Must Be Transactional

Configuration changes must follow:

validate
->
prepare
->
apply
->
verify
->
commit

If verification fails:

rollback

Maintain at least:

* factory/default state
* previous known-good configuration
* current configuration

Do not leave partially applied network state after an error.

Remote network configuration is considered a high-risk operation.

---

# 8. Software Architecture

Primary language:

Go

Target environment:

Debian / Armbian Linux

Primary architecture:

ARM64

But code must not unnecessarily depend on ARM64.

Core binary:

edge-agent

Future binaries may include:

edge-relay
edge-control

Do not use Kubernetes.

Do not introduce containers unless there is a concrete technical reason.

Do not build microservices for the EdgeBox.

Prefer a small number of boring, understandable Linux services.

---

# 9. edge-agent Internal Boundaries

The logical internal architecture is:

config
identity
control
management
transport
dataplane
network
health
telemetry
updater
state

These are architectural boundaries, not a requirement to create empty packages.

Avoid directory architecture for its own sake.

---

# 10. Transport Abstraction

WireGuard is NOT the EdgeBox architecture.

WireGuard is Transport V1.

Future candidates include:

* MASQUE CONNECT-IP over HTTP/3
* other QUIC-based transports
* TLS/TCP fallback transports

Transport-specific implementation must not leak into routing and NAT logic.

Do not place nftables manipulation inside a WireGuard package.

Do not place policy routing inside a MASQUE implementation.

Transport should expose a generic packet/data-plane endpoint.

Do not force every future transport to look exactly like a Linux WireGuard interface.

---

# 11. Data Plane

Conceptually:

Transport
->
Packet Endpoint
->
Data Plane
->
Network Policy
->
NAT
->
Physical WAN

A transport implementation may internally use:

* Linux network interface
* TUN
* userspace packet queues

The higher-level architecture must not care.

---

# 12. WireGuard V1

WireGuard is used initially because it provides a simple and reliable baseline.

Do not optimize the entire architecture around WireGuard.

WireGuard-specific configuration must remain isolated.

Typical WireGuard-specific values include:

* private/public keys
* peer endpoint
* allowed IPs
* persistent keepalive
* transport-specific MTU

Other packages should not require these concepts.

---

# 13. Future Transport Direction

Do NOT implement a custom QUIC VPN merely because QUIC exists.

Before developing a custom protocol, evaluate standardized approaches.

The preferred V2 research direction is:

MASQUE CONNECT-IP over HTTP/3

The transport abstraction should make this possible without redesigning:

* routing
* NAT
* health
* configuration
* telemetry
* management
* control plane

Do not implement MASQUE in v0.1.

---

# 14. Linux Networking

Use:

* nftables
* iproute2
* policy routing
* conntrack
* tc where needed

Do NOT use iptables for new functionality.

EdgeBox-owned nftables rules must live in an isolated table such as:

inet edgebox

Never execute:

nft flush ruleset

Never delete unrelated firewall rules.

All network operations must be:

* idempotent
* reversible
* observable
* testable

---

# 15. Policy Routing

Do not globally redirect all EdgeBox traffic into the data transport.

Separate forwarded egress traffic from local device management traffic.

Use dedicated routing tables and marks where appropriate.

A broken forwarded-data rule must not destroy:

* SSH
* DNS
* HTTPS control connectivity
* package downloads

---

# 16. NAT

v0.1 supports IPv4 residential egress.

Traffic delivered from the relay to EdgeBox should:

receive appropriate routing policy
->
leave the physical WAN
->
be SNAT/MASQUERADE translated
->
reach the Internet

Return traffic must follow the corresponding reverse path.

IPv6 egress is not required for the first milestone.

Do not block future IPv6 support in the architecture.

---

# 17. MTU

MTU is a first-class network parameter.

Do not permanently assume 1420 is universally correct.

The architecture should support:

* configured MTU
* detected/effective MTU
* transport-specific overhead

Avoid hidden MTU constants spread throughout the codebase.

---

# 18. Bandwidth Control

EdgeBox must support an explicit egress bandwidth ceiling.

The goal is operational stability and predictable resource use.

The bandwidth limiter belongs to the network/data-plane layer, not a specific transport.

v0.1 may implement a simple hard limit.

More advanced adaptive shaping is future work.

---

# 19. Identity

Each EdgeBox has a persistent unique identity.

Example:

device_id

Device identity is not geographic identity.

Do not encode mutable assumptions into the permanent identity.

Location such as:

country
city
ISP
ASN

is deployment metadata.

Private device credentials must remain local whenever possible.

Private keys must not be committed to Git.

---

# 20. Configuration Model

Use two conceptual configuration layers.

## Bootstrap configuration

Local and stable.

Example location:

/etc/edgebox/bootstrap.yaml

Contains only what is required to find and authenticate with central infrastructure.

## Runtime configuration

May later be fetched from edge-control.

Stored under:

/var/lib/edgebox/

Do not store secrets in ordinary human-readable configuration files unless unavoidable.

Secret files should have restrictive permissions.

---

# 21. State Machine

edge-agent should have an explicit state model.

Example:

BOOT
NETWORK_WAIT
REGISTERING
CONFIGURING
CONNECTING
ONLINE
DEGRADED
RECOVERING
SAFE_MODE

Avoid scattered restart logic.

Do not solve every error with reboot.

---

# 22. Health Model

Health should distinguish at least:

* system health
* WAN health
* management health
* control-plane health
* transport health
* data-plane health
* Internet egress health

"Internet reachable" is not equivalent to "system healthy".

Return structured health information.

---

# 23. Telemetry

Design telemetry from the beginning.

Useful metrics include:

* device uptime
* edge-agent uptime
* CPU
* RAM
* temperature
* WAN RX/TX
* data-plane RX/TX
* transport RX/TX
* relay RTT
* packet loss
* reconnect count
* current public IPv4
* current public IPv6
* current transport
* current relay
* last configuration version
* SAFE_MODE state

Do not perform frequent high-volume speed tests automatically.

---

# 24. Logging

Use structured logging.

Logs should include context such as:

* device_id
* component
* event
* transport
* relay
* config version
* error

Logs should work well with journald.

Do not log:

* private keys
* authentication tokens
* sensitive credential material

---

# 25. systemd

Use systemd for service supervision.

Expected services may eventually include:

edge-agent.service
edge-management.service

Prefer edge-agent to supervise logical application state rather than creating many tiny systemd services.

Management access must be independently restartable from the data plane.

Use appropriate:

Restart=
RestartSec=
network-online.target

Apply reasonable systemd sandboxing/capability restrictions.

---

# 26. Failure Model

Assume all of these can occur:

* WAN cable unplugged
* DHCP lease changes
* public IP changes
* DNS temporarily fails
* relay disappears
* VPS restarts
* WireGuard loses handshake
* UDP quality degrades
* packet loss increases
* configuration is invalid
* nftables apply fails
* edge-agent crashes
* system reboots
* power is abruptly removed

The system must recover without physical intervention whenever reasonably possible.

---

# 27. Testing Philosophy

Networking behavior must be testable.

Important scenarios include:

* cold boot
* repeated apply
* bad configuration rollback
* WAN disconnect/reconnect
* DHCP renewal
* transport failure
* relay restart
* agent crash
* management tunnel reconnect
* nftables idempotency
* policy-routing rollback
* SAFE_MODE entry
* SAFE_MODE recovery

The most important invariant test is:

A data-plane failure must not unnecessarily destroy remote management access.

---

# 28. v0.1 Scope

Implement only what is needed for a reliable first appliance.

v0.1 includes:

* edge-agent
* configuration
* explicit state machine
* WireGuard Transport V1
* IPv4 egress
* nftables NAT
* policy routing
* basic bandwidth limiting
* reverse SSH management
* health
* basic telemetry
* systemd integration
* configuration rollback
* SAFE_MODE

v0.1 explicitly does NOT require:

* MASQUE
* custom QUIC transport
* automatic protocol switching
* automatic relay optimization
* Web UI
* multi-user billing
* geo-routing
* Kubernetes
* complex control-plane backend
* full IPv6 egress
* A/B root filesystem updates

---

# 29. Remote Safety Rule

Codex must treat changes to:

* routes
* ip rules
* nftables
* SSH
* network interfaces
* DNS
* systemd networking

as potentially destructive on a remote device.

Before applying destructive networking changes:

1. inspect current state
2. preserve the current working configuration
3. create rollback capability
4. ensure management connectivity has a survival path
5. apply the smallest required change
6. verify connectivity
7. rollback automatically if verification fails

Do not assume physical access is available.

---

# 30. Development Principles

Prefer:

* simple
* boring
* explicit
* testable
* reversible
* observable

over:

* clever
* magical
* highly abstract
* dependency-heavy

Do not preserve bad legacy abstractions merely for compatibility.

This project is not yet in production.

Backward compatibility with old experimental shell scripts is NOT required.

Existing scripts/configurations may be used as behavioral references only.

---

# 31. Codex Working Rules

Before major implementation:

* inspect the repository
* inspect existing documentation
* state assumptions
* identify risks
* update architecture documentation when architecture changes

When modifying code:

* keep changes scoped
* add or update tests
* run relevant tests
* report remaining risks

Never invent:

* network interfaces
* IP addresses
* WireGuard keys
* public addresses
* relay addresses
* SSH credentials

when they are not present in the repository or task context.

Do not perform dangerous network changes merely to complete a task.

Reliability and recoverability are more important than appearing finished.
