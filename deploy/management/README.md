# Management asset rendering

This directory contains only fake examples. It contains no production address, assignment, credential, or host key. Rendering stages files in a caller-selected directory and never installs them or writes to `/etc`.

## Per-device key preparation

Generate the unique key on the device being provisioned. Never generate one image-wide key and clone it:

```sh
install -d -o edge-management -g edge-management -m 0700 /var/lib/edgebox/management
sudo -u edge-management ssh-keygen -q -t ed25519 -N '' \
  -C '<device-id>' \
  -f /var/lib/edgebox/management/id_ed25519
chmod 0600 /var/lib/edgebox/management/id_ed25519
chmod 0644 /var/lib/edgebox/management/id_ed25519.pub
```

These commands are for a reviewed future deployment procedure. Do not execute them on the audited appliance during Phase 2A.

Obtain the bastion Ed25519 host public key through an authenticated administrator channel. `ssh-keyscan` output alone is not proof of identity. Compare the SHA-256 fingerprint out of band before rendering:

```sh
ssh-keygen -lf bastion-ssh_host_ed25519_key.pub
```

## Renderer

Build and run the renderer on a staging workstation:

```sh
go build -o ./edge-management-render ./cmd/edge-management-render
./edge-management-render \
  -registry ./deploy/management/port-registry.example.json \
  -device-id lab-device-a \
  -bastion-host bastion.lab.invalid \
  -bastion-ssh-port 22 \
  -device-public-key ./lab-device-a.pub \
  -bastion-host-key ./bastion-ssh_host_ed25519_key.pub \
  -output ./rendered-lab-device-a
```

The output is deliberately not installed. It contains:

- `edge-management.ssh_config`: strict OpenSSH client configuration and the loopback-only remote forward;
- `edge-management.service`: independently supervised unprivileged systemd unit;
- `tunnel-authorized_keys`: one per-device public-key restriction with an exact `permitlisten`;
- `edge-tunnel-sshd.conf`: bastion tunnel-account restrictions;
- `known_hosts`: the pinned bastion host key at the configured SSH port.

Review all five files before installation. On a Linux staging system, validate the unit and bastion configuration with `systemd-analyze verify` and `sshd -t -f` using a complete lab configuration. Never weaken host verification to make validation pass.

## Debian/Ubuntu bastion tunnel account

The reviewed lab procedure creates a system account with `/usr/sbin/nologin`, installs the account-scoped `Match User` policy and reviewed public keys, validates the complete sshd configuration, and then removes Debian/Ubuntu's initial shadow lock marker with:

```sh
passwd -d edge-tunnel
```

On the tested Debian/Ubuntu configuration, the empty shadow field prevents sshd from rejecting the account as locked before public-key authentication. It does not provide a usable password path: the account's `Match User` block requires `AuthenticationMethods publickey`, enables `PubkeyAuthentication`, disables password and keyboard-interactive authentication, and the effective configuration retains `PermitEmptyPasswords no`. The non-interactive shell and `MaxSessions 0` prevent session use independently.

Do not copy this account-state step to another distribution without verification. On the selected bastion OS, inspect the shadow state, run `sshd -T -C user=edge-tunnel,host=localhost,addr=127.0.0.1`, prove a forced password-only login fails, and prove the reviewed key can establish only its TCP remote forward. Apply and validate the account-scoped sshd policy before removing the initial lock marker.

## Ownership at the future EdgeBox

Use a static system account such as `edge-management` with a non-login shell. The account needs read/traverse access only:

| Path | Owner | Mode |
| --- | --- | --- |
| `/var/lib/edgebox/management/` | `edge-management:edge-management` | `0700` |
| `id_ed25519` | `edge-management:edge-management` | `0600` |
| `known_hosts` | `edge-management:edge-management` | `0644` or stricter |
| `/etc/edgebox/edge-management.ssh_config` | `root:edge-management` | `0640` |
| `edge-management.service` | `root:root` | `0644` |

The account requires no `CAP_NET_ADMIN`, privileged port, writable home, or membership in an administration group. Local TCP access to `127.0.0.1:22` and outbound TCP access to the bastion are sufficient.
