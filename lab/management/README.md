# Isolated reverse-SSH lab

`run.sh` starts two temporary OpenSSH servers on WSL/Linux loopback: a bastion and a simulated EdgeBox sshd. It creates ephemeral users and Ed25519 keys, invokes the production renderer, and removes all lab users/files on exit. It does not contact the audited appliance and does not alter host routes, DNS, firewall, nftables, or network interfaces.

Prerequisites:

- Linux root in a disposable VM or WSL distribution;
- OpenSSH client and server, `systemd-analyze`, `ss`, `curl`, and Python 3;
- a Linux/amd64 build of `cmd/edge-management-render`.

Example from the repository root on Windows with Ubuntu WSL:

```powershell
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
go build -o .\.lab-edge-management-render ./cmd/edge-management-render
wsl.exe -d Ubuntu-22.04 -- bash /mnt/d/to_box/lab/management/run.sh /mnt/d/to_box/.lab-edge-management-render
Remove-Item -LiteralPath .\.lab-edge-management-render
```

The lab uses the documented Debian/Ubuntu account state: a system account with `/usr/sbin/nologin` and an empty, non-locked shadow field. It proves that password authentication remains unavailable while the reviewed public key can establish its assigned forward.

The lab verifies real OpenSSH behavior for loopback binding, per-key `permitlisten`, wrong keys, host-key mismatch, `ssh -N -T` with `MaxSessions 0`, shell/exec/PTY/subsystem denial, effective agent/X11 restrictions, local/dynamic/StreamLocal forwarding denial, end-to-end TCP reverse forwarding, supervised reconnects, bastion restart, transient connection loss, and independence from a simulated edge-agent process. It also gives unrelated accounts deliberately different global fixture settings and confirms the rendered `Match User` block does not alter them.

DNS outage and address renewal are intentionally deferred to a disposable VM/network-namespace extension because this lab makes no host networking changes. The deterministic procedure is documented in `docs/MANAGEMENT_RUNBOOK.md`.
