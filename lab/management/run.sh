#!/usr/bin/env bash
set -euo pipefail

renderer=${1:-}
if [[ -z "${renderer}" || ! -x "${renderer}" ]]; then
  echo "usage: $0 /absolute/path/to/linux-edge-management-render" >&2
  exit 2
fi
for command in ssh sftp sshd ssh-keygen ssh-keyscan systemd-analyze ss curl timeout useradd userdel passwd; do
  command -v "${command}" >/dev/null || {
    echo "missing lab prerequisite: ${command}" >&2
    exit 2
  }
done
sshd_bin=$(command -v sshd)

lab_root=/run/edgebox-management-lab
tunnel_user=edgebox_lab_tunnel
edge_user=edgebox_lab_edge
bastion_port=22220
edge_port=22221
assigned_port=22201
other_port=22202
local_forward_port=22203
dynamic_forward_port=22204
created_tunnel_user=0
created_edge_user=0
bastion_pid=
edge_pid=
supervisor_pid=

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "PASS: $*"
}

stop_process() {
  local pid=${1:-}
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
}

stop_bastion() {
  if [[ -n "${bastion_pid}" ]] && kill -0 "${bastion_pid}" 2>/dev/null; then
    pkill -TERM -P "${bastion_pid}" 2>/dev/null || true
    kill "${bastion_pid}" 2>/dev/null || true
    wait "${bastion_pid}" 2>/dev/null || true
  fi
  bastion_pid=
}

cleanup() {
  set +e
  stop_process "${supervisor_pid}"
  stop_bastion
  stop_process "${edge_pid}"
  pkill -TERM -u "${tunnel_user}" sshd 2>/dev/null || true
  pkill -TERM -u "${edge_user}" sshd 2>/dev/null || true
  if [[ ${created_tunnel_user} -eq 1 ]]; then userdel "${tunnel_user}" 2>/dev/null || true; fi
  if [[ ${created_edge_user} -eq 1 ]]; then userdel "${edge_user}" 2>/dev/null || true; fi
  if [[ "${lab_root}" == /run/edgebox-management-lab ]]; then
    rm -rf -- /run/edgebox-management-lab
  fi
}
trap cleanup EXIT

[[ ${EUID} -eq 0 ]] || fail "the isolated sshd lab requires root inside the disposable lab"
[[ ! -e "${lab_root}" ]] || fail "lab directory already exists: ${lab_root}"
getent passwd "${tunnel_user}" >/dev/null && fail "lab account already exists: ${tunnel_user}"
getent passwd "${edge_user}" >/dev/null && fail "lab account already exists: ${edge_user}"
for port in "${bastion_port}" "${edge_port}" "${assigned_port}" "${other_port}" "${local_forward_port}" "${dynamic_forward_port}"; do
  if ss -H -ltn | grep -Eq "(^|[[:space:]])(127\.0\.0\.1|0\.0\.0\.0|\[::\]):${port}([[:space:]]|$)"; then
    fail "required lab port is already in use: ${port}"
  fi
done

install -d -m 0755 "${lab_root}" /run/sshd
useradd --system --no-create-home --home-dir "${lab_root}/tunnel-home" --shell /usr/sbin/nologin "${tunnel_user}"
created_tunnel_user=1
useradd --system --no-create-home --home-dir "${lab_root}/edge-home" --shell /bin/sh "${edge_user}"
created_edge_user=1
passwd -d "${edge_user}" >/dev/null

install -d -o "${tunnel_user}" -g "${tunnel_user}" -m 0700 "${lab_root}/tunnel-home" "${lab_root}/tunnel-home/.ssh"
install -d -o "${edge_user}" -g "${edge_user}" -m 0700 "${lab_root}/edge-home" "${lab_root}/edge-home/.ssh"

ssh-keygen -q -t ed25519 -N '' -C lab-device-a -f "${lab_root}/device_key"
ssh-keygen -q -t ed25519 -N '' -C wrong-device -f "${lab_root}/wrong_device_key"
ssh-keygen -q -t ed25519 -N '' -C lab-operator -f "${lab_root}/operator_key"
ssh-keygen -q -t ed25519 -N '' -C bastion-host -f "${lab_root}/bastion_host_key"
ssh-keygen -q -t ed25519 -N '' -C wrong-host -f "${lab_root}/wrong_host_key"
ssh-keygen -q -t ed25519 -N '' -C edge-host -f "${lab_root}/edge_host_key"
chmod 0600 "${lab_root}/device_key" "${lab_root}/wrong_device_key" "${lab_root}/operator_key" "${lab_root}/bastion_host_key" "${lab_root}/wrong_host_key" "${lab_root}/edge_host_key"

cat >"${lab_root}/registry.json" <<EOF
{
  "schema_version": 1,
  "allowed_port_range": {"start": 22000, "end": 22999},
  "reserved_ports": [22000, 22999],
  "assignments": [{"device_id": "lab-device-a", "port": ${assigned_port}}]
}
EOF

"${renderer}" \
  -registry "${lab_root}/registry.json" \
  -device-id lab-device-a \
  -bastion-host 127.0.0.1 \
  -bastion-ssh-port "${bastion_port}" \
  -tunnel-user "${tunnel_user}" \
  -service-user "${tunnel_user}" \
  -service-group "${tunnel_user}" \
  -private-key-path "${lab_root}/device_key" \
  -known-hosts-path "${lab_root}/rendered/known_hosts" \
  -client-config-path "${lab_root}/rendered/edge-management.ssh_config" \
  -local-ssh-port "${edge_port}" \
  -server-alive-interval 1 \
  -server-alive-count-max 2 \
  -connect-timeout 2 \
  -device-public-key "${lab_root}/device_key.pub" \
  -bastion-host-key "${lab_root}/bastion_host_key.pub" \
  -output "${lab_root}/rendered"

install -o "${tunnel_user}" -g "${tunnel_user}" -m 0600 "${lab_root}/rendered/tunnel-authorized_keys" "${lab_root}/tunnel-home/.ssh/authorized_keys"
install -o "${edge_user}" -g "${edge_user}" -m 0600 "${lab_root}/operator_key.pub" "${lab_root}/edge-home/.ssh/authorized_keys"

cat >"${lab_root}/bastion_sshd_config" <<EOF
Port ${bastion_port}
ListenAddress 127.0.0.1
HostKey ${lab_root}/bastion_host_key
PidFile ${lab_root}/bastion_sshd.pid
AuthorizedKeysFile .ssh/authorized_keys
UsePAM no
StrictModes yes
PermitRootLogin no
# Deliberately permissive fixture values prove that the rendered Match block
# restricts only the tunnel account and does not rewrite unrelated policy.
PasswordAuthentication yes
KbdInteractiveAuthentication yes
PubkeyAuthentication yes
GatewayPorts clientspecified
AllowUsers ${tunnel_user}
LogLevel VERBOSE
Subsystem sftp internal-sftp
EOF
cat "${lab_root}/rendered/edge-tunnel-sshd.conf" >>"${lab_root}/bastion_sshd_config"

cat >"${lab_root}/edge_sshd_config" <<EOF
Port ${edge_port}
ListenAddress 127.0.0.1
HostKey ${lab_root}/edge_host_key
PidFile ${lab_root}/edge_sshd.pid
AuthorizedKeysFile .ssh/authorized_keys
UsePAM no
StrictModes yes
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowUsers ${edge_user}
AllowTcpForwarding no
X11Forwarding no
LogLevel VERBOSE
Subsystem sftp internal-sftp
EOF

"${sshd_bin}" -t -f "${lab_root}/bastion_sshd_config"
"${sshd_bin}" -t -f "${lab_root}/edge_sshd_config"
# Debian/Ubuntu system accounts start with a locked shadow marker that sshd may
# reject before public-key authentication. Remove it only after the complete
# account-scoped policy is installed and syntax-checked. Password methods and
# empty passwords remain disabled by the effective sshd policy.
passwd -d "${tunnel_user}" >/dev/null
[[ -z "$(getent shadow "${tunnel_user}" | cut -d: -f2)" ]] || fail "tunnel account remains shadow-locked"
systemd-analyze verify "${lab_root}/rendered/edge-management.service"
pass "rendered sshd and systemd configurations validate"

effective=$("${sshd_bin}" -T -f "${lab_root}/bastion_sshd_config" -C "user=${tunnel_user},host=localhost,addr=127.0.0.1")
for required in \
  "authenticationmethods publickey" \
  "pubkeyauthentication yes" \
  "passwordauthentication no" \
  "kbdinteractiveauthentication no" \
  "permitemptypasswords no" \
  "allowtcpforwarding remote" \
  "allowstreamlocalforwarding no" \
  "allowagentforwarding no" \
  "x11forwarding no" \
  "permittty no" \
  "gatewayports no" \
  "maxsessions 0" \
  "forcecommand /bin/false"; do
  grep -Fxq "${required}" <<<"${effective}" || fail "effective bastion configuration missing: ${required}"
done
operator_effective=$("${sshd_bin}" -T -f "${lab_root}/bastion_sshd_config" -C "user=unrelated-operator,host=localhost,addr=127.0.0.1")
for unchanged in \
  "passwordauthentication yes" \
  "kbdinteractiveauthentication yes" \
  "gatewayports clientspecified"; do
  grep -Fxq "${unchanged}" <<<"${operator_effective}" || fail "rendered fragment changed unrelated account policy: ${unchanged}"
done
pass "effective Match policy is tunnel-account-scoped and denies sessions and unauthorized forwarding"

start_bastion() {
  "${sshd_bin}" -D -e -f "${lab_root}/bastion_sshd_config" >>"${lab_root}/bastion.log" 2>&1 &
  bastion_pid=$!
  for _ in $(seq 1 50); do
    if ! kill -0 "${bastion_pid}" 2>/dev/null; then
      tail -n 20 "${lab_root}/bastion.log" >&2 || true
      fail "bastion sshd exited"
    fi
    if ssh-keyscan -T 1 -p "${bastion_port}" 127.0.0.1 >/dev/null 2>&1; then return; fi
    sleep 0.1
  done
  fail "bastion sshd did not become ready"
}

"${sshd_bin}" -D -e -f "${lab_root}/edge_sshd_config" >>"${lab_root}/edge.log" 2>&1 &
edge_pid=$!
start_bastion

wrong_host_data=$(cut -d' ' -f1,2 "${lab_root}/wrong_host_key.pub")
printf '[127.0.0.1]:%d %s\n' "${bastion_port}" "${wrong_host_data}" >"${lab_root}/wrong_known_hosts"
edge_host_data=$(cut -d' ' -f1,2 "${lab_root}/edge_host_key.pub")
printf '[127.0.0.1]:%d %s\n' "${assigned_port}" "${edge_host_data}" >"${lab_root}/edge_known_hosts"

base_ssh=(ssh -F /dev/null -T -p "${bastion_port}" -l "${tunnel_user}" -i "${lab_root}/device_key" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" -o ConnectTimeout=2)

if ssh -vv -F /dev/null -T -p "${bastion_port}" -l "${tunnel_user}" \
  -o BatchMode=yes -o PreferredAuthentications=password -o PubkeyAuthentication=no \
  -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" \
  127.0.0.1 true >"${lab_root}/password-auth.log" 2>&1; then
  fail "tunnel account authenticated with a password method"
fi
grep -Fq 'Permission denied (publickey)' "${lab_root}/password-auth.log" || fail "tunnel account offered an unexpected authentication method"
pass "non-locked tunnel account remains public-key-only"

if ssh -F /dev/null -T -p "${bastion_port}" -l "${tunnel_user}" -i "${lab_root}/wrong_device_key" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" 127.0.0.1 true >/dev/null 2>&1; then
  fail "wrong device key authenticated"
fi
pass "wrong device key is rejected"

if "${base_ssh[@]}" -o "UserKnownHostsFile=${lab_root}/wrong_known_hosts" 127.0.0.1 true >/dev/null 2>&1; then
  fail "mismatched bastion host key was accepted"
fi
pass "host-key mismatch is rejected with strict checking"

if timeout 5 "${base_ssh[@]}" 127.0.0.1 </dev/null >"${lab_root}/shell.out" 2>/dev/null; then
  fail "tunnel account opened a shell session"
fi
if "${base_ssh[@]}" 127.0.0.1 'printf useful-exec' >"${lab_root}/exec.out" 2>/dev/null; then
  fail "tunnel account executed a command"
fi
if grep -Fq 'useful-exec' "${lab_root}/exec.out"; then
  fail "tunnel account executed the requested shell command"
fi
if "${base_ssh[@]}" -tt 127.0.0.1 true >/dev/null 2>&1; then
  fail "tunnel account obtained a PTY"
fi
if sftp -b /dev/null -F /dev/null -P "${bastion_port}" -i "${lab_root}/device_key" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" \
  "${tunnel_user}@127.0.0.1" >"${lab_root}/subsystem.log" 2>&1; then
  fail "tunnel account opened an SFTP subsystem"
fi
pass "MaxSessions=0 rejects shell, exec, PTY, and subsystem channels"

if timeout 5 "${base_ssh[@]}" -N -o ExitOnForwardFailure=yes -R "127.0.0.1:${other_port}:127.0.0.1:${edge_port}" 127.0.0.1 >/dev/null 2>&1; then
  fail "device key claimed another device port"
fi
if timeout 5 "${base_ssh[@]}" -N -o ExitOnForwardFailure=yes -R "0.0.0.0:${assigned_port}:127.0.0.1:${edge_port}" 127.0.0.1 >/dev/null 2>&1; then
  fail "device key claimed a wildcard listener"
fi
pass "permitlisten rejects another port and wildcard binding"

streamlocal_path="${lab_root}/tunnel-home/forbidden.sock"
if timeout 5 "${base_ssh[@]}" -N -o ExitOnForwardFailure=yes \
  -R "${streamlocal_path}:127.0.0.1:${edge_port}" 127.0.0.1 >/dev/null 2>&1; then
  fail "tunnel account created a remote Unix-domain listener"
fi
[[ ! -e "${streamlocal_path}" ]] || fail "rejected StreamLocal forwarding left a socket behind"
pass "remote StreamLocal forwarding is rejected without a residual socket"

cp "${lab_root}/bastion_sshd_config" "${lab_root}/malformed_sshd_config"
echo 'DefinitelyNotAnOpenSSHDirective yes' >>"${lab_root}/malformed_sshd_config"
if "${sshd_bin}" -t -f "${lab_root}/malformed_sshd_config" >/dev/null 2>&1; then
  fail "malformed sshd configuration was accepted"
fi
pass "malformed sshd configuration fails closed"

tunnel_supervisor() {
  while true; do
    ssh -N -T -F "${lab_root}/rendered/edge-management.ssh_config" edgebox-management-bastion >>"${lab_root}/client.log" 2>&1 || true
    sleep 0.5
  done
}
tunnel_supervisor &
supervisor_pid=$!

edge_command() {
  ssh -F /dev/null -T -p "${assigned_port}" -l "${edge_user}" -i "${lab_root}/operator_key" \
    -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
    -o "UserKnownHostsFile=${lab_root}/edge_known_hosts" -o ConnectTimeout=1 \
    127.0.0.1 'printf edge-ok' 2>/dev/null
}

wait_for_tunnel() {
  for _ in $(seq 1 80); do
    if [[ $(edge_command || true) == edge-ok ]]; then return; fi
    sleep 0.25
  done
  fail "reverse tunnel did not become usable"
}
wait_for_tunnel
listener=$(ss -H -ltn "sport = :${assigned_port}")
grep -Eq "127\.0\.0\.1:${assigned_port}([[:space:]]|$)" <<<"${listener}" || fail "assigned listener is not on IPv4 loopback"
if grep -Eq "(0\.0\.0\.0|\[::\]|\*):${assigned_port}" <<<"${listener}"; then
  fail "assigned listener is exposed on a wildcard address"
fi
if ss -H -ltn "sport = :${other_port}" | grep -q .; then fail "unassigned listener exists"; fi
pass "correct key creates only its loopback listener and reaches simulated EdgeBox sshd"

ssh -vv -F /dev/null -N -T -p "${bastion_port}" -l "${tunnel_user}" -i "${lab_root}/device_key" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" \
  -L "127.0.0.1:${local_forward_port}:127.0.0.1:${edge_port}" 127.0.0.1 >"${lab_root}/local-forward.log" 2>&1 &
local_pid=$!
sleep 0.5
python3 - "${local_forward_port}" <<'PY' || true
import socket
import sys
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=1)
s.settimeout(1)
try:
    data = s.recv(64)
    if data.startswith(b"SSH-"):
        raise SystemExit("unauthorized local forward reached sshd")
finally:
    s.close()
PY
sleep 0.5
stop_process "${local_pid}"
grep -q 'administratively prohibited' "${lab_root}/local-forward.log" || fail "local forwarding was not explicitly denied"

ssh -vv -F /dev/null -N -T -p "${bastion_port}" -l "${tunnel_user}" -i "${lab_root}/device_key" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o "UserKnownHostsFile=${lab_root}/rendered/known_hosts" \
  -D "127.0.0.1:${dynamic_forward_port}" 127.0.0.1 >"${lab_root}/dynamic-forward.log" 2>&1 &
dynamic_pid=$!
sleep 0.5
curl --silent --show-error --max-time 2 --socks5-hostname "127.0.0.1:${dynamic_forward_port}" "http://127.0.0.1:${edge_port}/" >/dev/null 2>&1 || true
sleep 0.5
stop_process "${dynamic_pid}"
grep -q 'administratively prohibited' "${lab_root}/dynamic-forward.log" || fail "dynamic forwarding was not explicitly denied"
pass "unauthorized local and dynamic forwarding are denied"

old_client=$(pgrep -P "${supervisor_pid}" -x ssh | head -n 1)
[[ -n "${old_client}" ]] || fail "supervisor has no SSH child"
kill "${old_client}"
for _ in $(seq 1 40); do
  new_client=$(pgrep -P "${supervisor_pid}" -x ssh | head -n 1 || true)
  if [[ -n "${new_client}" && "${new_client}" != "${old_client}" ]] && [[ $(edge_command || true) == edge-ok ]]; then break; fi
  sleep 0.25
done
[[ -n "${new_client:-}" && "${new_client}" != "${old_client}" ]] || fail "supervisor did not replace killed SSH client"
pass "supervisor reconnects after SSH client termination"

connection_child=$(pgrep -u "${tunnel_user}" -x sshd | head -n 1 || true)
[[ -n "${connection_child}" ]] || fail "bastion tunnel connection child not found"
kill -9 "${connection_child}"
wait_for_tunnel
pass "temporary server-side connection loss recovers"

stop_bastion
start_bastion
wait_for_tunnel
pass "bastion restart causes eventual reconnect"

sleep 300 &
edge_agent_pid=$!
kill "${edge_agent_pid}"
wait "${edge_agent_pid}" 2>/dev/null || true
[[ $(edge_command || true) == edge-ok ]] || fail "edge-agent exit affected management tunnel"
pass "edge-agent process exit has no effect on management supervision"

grep -Fq 'StrictHostKeyChecking yes' "${lab_root}/rendered/edge-management.ssh_config" || fail "strict host checking missing"
grep -Fq "permitlisten=\"127.0.0.1:${assigned_port}\"" "${lab_root}/rendered/tunnel-authorized_keys" || fail "permitlisten restriction missing"
pass "all isolated reverse-SSH management checks completed"
