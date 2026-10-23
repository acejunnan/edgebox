#!/usr/bin/python3
"""Install the N1 TCP/443 fallback paths (Xray VLESS reverse, outbound only).

  edge-bridge.service    data fallback: relay -> N1 -> local WAN (public IPv4 only)
  edge-mgmt-alt.service  management fallback: relay 127.0.0.1:22011 -> N1 127.0.0.1:22

Both are ordinary locally originated connections in the main routing table and
are independent from edge-dataplane and edge-management. Idempotent; run as root:
  install-edge.py <credentials.json> <Xray-linux-arm64-v8a.zip>
"""
import hashlib, json, os, pathlib, shutil, subprocess, sys, zipfile

XRAY_SHA256 = '4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c'  # v26.3.27 arm64-v8a
os.umask(0o077)
assert os.geteuid() == 0
creds_src, archive = map(pathlib.Path, sys.argv[1:3])
c = json.loads(creds_src.read_text())
etc = pathlib.Path('/etc/edgebox')
net = pathlib.Path('/opt/edgebox-network')
net.mkdir(parents=True, exist_ok=True)
os.chmod(net, 0o755)

assert hashlib.sha256(archive.read_bytes()).hexdigest() == XRAY_SHA256, 'xray archive hash mismatch'
binary = net / 'xray'
tmp = net / '.xray.new'
with zipfile.ZipFile(archive) as z:
    tmp.write_bytes(z.read('xray'))
os.chmod(tmp, 0o755)
os.replace(tmp, binary)

if subprocess.run(['id', 'edge-proxy'], capture_output=True).returncode:
    subprocess.check_call(['useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', 'edge-proxy'])

creds = etc / 'xray-reverse.json'
if creds_src.resolve() != creds.resolve():
    shutil.copyfile(creds_src, creds)
os.chmod(creds, 0o600)

def relay(user_id, tag):
    return {'tag': 'to-relay', 'protocol': 'vless',
            'settings': {'address': c['server'], 'port': c['port'], 'id': user_id, 'encryption': 'none',
                         'flow': 'xtls-rprx-vision', 'reverse': {'tag': tag}},
            'streamSettings': {'network': 'tcp', 'security': 'reality',
                               'realitySettings': {'serverName': c['sni'], 'fingerprint': 'chrome',
                                                   'publicKey': c['public_key'], 'shortId': c['short_id']}}}

nonpublic = ['0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8', '169.254.0.0/16', '172.16.0.0/12',
             '192.0.0.0/24', '192.0.2.0/24', '192.168.0.0/16', '198.18.0.0/15', '198.51.100.0/24',
             '203.0.113.0/24', '224.0.0.0/3', '::/0']
bridge = {
    'log': {'loglevel': 'warning', 'access': 'none'},
    'outbounds': [{'tag': 'blocked', 'protocol': 'blackhole'},
                  {'tag': 'direct', 'protocol': 'freedom', 'settings': {'domainStrategy': 'UseIPv4'}},
                  relay(c['bridge_id'], 'from-relay')],
    'routing': {'domainStrategy': 'IPIfNonMatch', 'rules': [
        {'type': 'field', 'inboundTag': ['from-relay'], 'ip': nonpublic, 'outboundTag': 'blocked'},
        {'type': 'field', 'inboundTag': ['from-relay'], 'outboundTag': 'direct'}]},
}
mgmt = {
    'log': {'loglevel': 'warning', 'access': 'none'},
    'outbounds': [{'tag': 'blocked', 'protocol': 'blackhole'},
                  {'tag': 'ssh-local', 'protocol': 'freedom',
                   'settings': {'redirect': '127.0.0.1:22',
                                'finalRules': [{'action': 'allow', 'network': 'tcp', 'ip': ['127.0.0.1'], 'port': '22'}]}},
                  relay(c['mgmt_id'], 'from-relay-mgmt')],
    'routing': {'rules': [{'type': 'field', 'inboundTag': ['from-relay-mgmt'], 'outboundTag': 'ssh-local'}]},
}
unit = """[Unit]
Description={desc}
Wants=network.target
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=edge-proxy
Group=edge-proxy
ExecStart=/opt/edgebox-network/xray run -config {conf}
Restart=always
RestartSec=10
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
MemoryMax={mem}
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
"""
for name, conf, desc, mem in [
    ('edge-bridge', bridge, 'EdgeBox data fallback over TCP/443 (outbound VLESS reverse)', '160M'),
    ('edge-mgmt-alt', mgmt, 'EdgeBox management fallback over TCP/443 (independent of data plane)', '96M'),
]:
    path = etc / f'xray-{name}.json'
    nxt = etc / f'xray-{name}.next.json'
    nxt.write_text(json.dumps(conf, indent=2))
    shutil.chown(nxt, user='root', group='edge-proxy')
    os.chmod(nxt, 0o640)
    r = subprocess.run([str(binary), 'run', '-test', '-config', str(nxt)], capture_output=True, text=True)
    if r.returncode:
        out = r.stdout + r.stderr
        for secret in (c['bridge_id'], c['mgmt_id'], c['public_key'], c['short_id']):
            out = out.replace(secret, '<redacted>')
        sys.exit(f'{name} validation failed: {out[-1200:]}')
    os.replace(nxt, path)
    up = pathlib.Path(f'/etc/systemd/system/{name}.service')
    up.write_text(unit.format(desc=desc, conf=path, mem=mem))
    os.chmod(up, 0o644)
subprocess.check_call(['systemctl', 'daemon-reload'])
print(json.dumps({'installed': ['edge-bridge', 'edge-mgmt-alt'], 'started': False}))
