# Remote Management Design

## Purpose

Human emergency access uses OpenSSH, not an unauthenticated command API. Each EdgeBox establishes an outbound reverse tunnel to a management bastion, so the design works behind NAT and CGNAT without exposing TCP/22 to the Internet.

```text
EdgeBox sshd 127.0.0.1:22
        ^
        | outbound SSH remote forward
        |
Bastion 127.0.0.1:<assigned-port>
        ^
        | operator SSH session on bastion
        |
Operator
```

The management process is independent of `edge-agent` and the data-plane transport. Its socket must use ordinary locally originated routing through the physical WAN and the main routing table.

## Device identity and credentials

- Give every device a persistent `device_id` and a unique Ed25519 management key pair generated on that device.
- Never clone a private device key into an image or reuse it across devices.
- Store the private key in `/var/lib/edgebox/management/id_ed25519`, owned by root with mode `0600` (or an equally restricted dedicated service user).
- Pin the bastion host key in a root-owned `known_hosts` file. Do not use `StrictHostKeyChecking=no`.
- Install only the public key on the bastion, tied to that device's assigned loopback port.
- Do not place passwords, private keys, tokens, relay addresses, or invented port assignments in this repository.

Port assignment may remain a reviewed static registry for v0.1, for example a deployment-only mapping from `device_id` to bastion loopback port. Dynamic allocation is out of scope.

## Edge service template

Phase 2A uses `cmd/edge-management-render` to render a validated OpenSSH client configuration and a unit. Concrete hosts, ports, public keys, and file paths are inputs; the renderer neither accepts private-key contents nor installs its output. These are design examples, not instructions to apply on the audited host yet.

The service runs as a static unprivileged `edge-management` account. Its persistent key directory is owned by that account and mode `0700`; the private key is mode `0600`. A static account is preferred over `DynamicUser` because the device key must retain a stable owner across upgrades. The account needs no login shell, writable home, or Linux capabilities.

```ini
# /etc/systemd/system/edge-management.service
[Unit]
Description=EdgeBox outbound reverse SSH management tunnel
Wants=network.target
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=edge-management
Group=edge-management
ExecStart=/usr/bin/ssh -N -T -F /etc/edgebox/edge-management.ssh_config edgebox-management-bastion
Restart=always
RestartSec=10s
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectSystem=strict
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
```

The rendered OpenSSH configuration carries `BatchMode`, `ExitOnForwardFailure`, keepalives, bounded connect timeout, explicit key and known-host files, strict host checking, disabled agent/X11/TTY behavior, and the exact loopback remote forward. Avoiding environment-variable interpolation in `ExecStart` removes a configuration-injection surface. Hardening must be validated on the target OpenSSH/systemd versions. `edge-agent.service` stopping or entering SAFE_MODE must not stop this unit.

## Bastion account and SSH restrictions

Use a dedicated non-operator account for tunnel establishment. It must not share credentials with operator accounts. The generated fragment deliberately makes no global authentication or forwarding-policy changes; all required tunnel restrictions are explicit in its account-specific block. Assigned reverse ports must have no Internet firewall exposure.

An OpenSSH configuration fragment equivalent to this is the v0.1 baseline:

```text
Match User edge-tunnel
    AuthenticationMethods publickey
    PubkeyAuthentication yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    GatewayPorts no
    ClientAliveInterval 30
    ClientAliveCountMax 2
    AllowTcpForwarding remote
    AllowStreamLocalForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    PermitTunnel no
    PermitOpen none
    MaxSessions 0
    ForceCommand /bin/false
```

`MaxSessions 0` prevents shell, exec, and subsystem channels while still allowing the `ssh -N -T` TCP remote forward. `AllowStreamLocalForwarding no` excludes Unix-domain-socket forwarding. `ForceCommand` and the PTY restriction remain defense in depth. Confirm the effective account policy and forwarding behavior on the selected bastion release before rollout.

Phase 2C starts the client after `network.target`, not the potentially slow
wait-online target: SSH has bounded connection attempts and systemd retries.
Account-scoped server keepalives retire dead tunnel workers so they cannot
retain the assigned port indefinitely. These do not change operator-account
policy. The actual stale-port incident and fresh-login verification are recorded
in local Phase 2C qualification records, excluded from the public source release.

Constrain each device key in the tunnel account's `authorized_keys` to its allocated listener. A reviewed entry should be equivalent to:

```text
no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,permitlisten="127.0.0.1:<assigned-port>" ssh-ed25519 <device-public-key> <device-id>
```

`AllowTcpForwarding remote` prevents arbitrary local/dynamic forwarding, `permitlisten` limits the key to one reverse listener, and `GatewayPorts no` prevents a requested wildcard listener from becoming public. Where the installed OpenSSH supports a practical account-level `PermitListen`, use it as an additional restriction; per-key restriction remains necessary when one account serves several assigned ports.

The bastion firewall should allow its public SSH port only as operationally required. Reverse listener ports are loopback-only and must not be opened in the public firewall.

## Operator access

After authenticating to the bastion with a separate operator account, connect to the assigned port from the bastion itself:

```sh
ssh operator@<bastion-host>
ssh -p <assigned-port> <edge-admin-user>@127.0.0.1
```

The EdgeBox SSH login must use a tested per-operator or break-glass public key. Password authentication should be disabled only after the reverse tunnel and key login have both been proven in a second concurrent session and an automatic rollback has been armed.

## Routing survival checks

Before and after every future data-plane apply:

1. Resolve the bastion endpoint using the management DNS path.
2. Confirm `ip route get <bastion-address>` selects the physical WAN and main routing behavior, not the transport endpoint.
3. Confirm no EdgeBox policy rule captures locally originated management traffic.
4. Confirm the reverse listener is established on bastion loopback.
5. Open a fresh operator session through the listener before closing the existing recovery session.

Endpoint addresses may change, so hard-coded exception routes are not the primary design. The primary isolation is that only explicitly classified forwarded data-plane traffic enters the dedicated table.

## Migration from the audited host

The audited host currently listens on `0.0.0.0:22` and `[::]:22`, permits root/password login, has no root `authorized_keys`, and has no reverse-management service. Directly disabling password or root login now could remove the only verified access path.

The safe migration order is:

1. establish device identity and a unique device-to-bastion key;
2. provision the restricted bastion key and loopback port;
3. install pinned bastion host keys and `edge-management.service`;
4. verify reconnect after service kill and WAN interruption;
5. provision and verify EdgeBox administrator public-key login through the reverse path;
6. keep an existing recovery session open, arm timed rollback, then disable password authentication;
7. verify a new reverse-path session before committing;
8. separately decide whether root SSH should be prohibited or key-only according to deployment policy.

No step above was performed during Phase 0 or Phase 2A. Historical operational records remain local. For a new deployment, follow the public [installation guide](INSTALL.zh-CN.md); credential rotation and host-key changes require their own reviewed rollback procedure.
