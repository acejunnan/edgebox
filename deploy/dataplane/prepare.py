#!/usr/bin/python3
"""Prepare local credentials and snapshots, without activating networking."""
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import ipaddress

def run(*args):
    return subprocess.check_output(args, text=True)

os.umask(0o077)
role = sys.argv[1]
assert role in ('edge', 'relay')
assert os.geteuid() == 0
root = pathlib.Path('/var/lib/edgebox/dataplane')
root.mkdir(parents=True, exist_ok=True)
os.chmod(root, 0o755)
snapshot = pathlib.Path(tempfile.mkdtemp(prefix='before-', dir=root))
for name, args in {
    'addresses.json': ['ip', '-j', 'address'],
    'routes.json': ['ip', '-j', '-4', 'route', 'show', 'table', 'all'],
    'rules.json': ['ip', '-j', '-4', 'rule'],
    'firewall.nft': ['/usr/sbin/nft', 'list', 'ruleset'],
}.items():
    (snapshot / name).write_text(run(*args))
rules = json.loads((snapshot / 'rules.json').read_text())
assert not any(int(r.get('priority', -1)) == 10777 for r in rules), 'policy priority occupied'
routes = json.loads((snapshot / 'routes.json').read_text())
assert not any(str(r.get('table')) == '177' for r in routes), 'routing table occupied'
reserved = ipaddress.ip_network('10.77.0.0/24')
assert not any(r.get('dst', 'default') != 'default' and
               ipaddress.ip_network(r['dst'], strict=False).overlaps(reserved)
               for r in routes if r.get('dst')), 'tunnel route collision'
if role == 'relay':
    assert not run('ss', '-H', '-ltn', 'sport = :443').strip(), 'TCP/443 occupied'
    assert not run('ss', '-H', '-lun', 'sport = :51820').strip(), 'UDP/51820 occupied'
assert not any(a['ifname'] == 'ebwg0' for a in json.loads((snapshot / 'addresses.json').read_text())), 'interface occupied'
assert 'table inet edgebox {' not in (snapshot / 'firewall.nft').read_text(), 'nft table occupied'
for a in json.loads((snapshot / 'addresses.json').read_text()):
    for addr in a.get('addr_info', []):
        assert not addr['local'].startswith('10.77.'), 'tunnel subnet collision'
protected = ['/etc/nftables.conf', '/etc/ssh/sshd_config']
if role == 'edge':
    protected += ['/etc/edgebox/edge-management.ssh_config', '/etc/systemd/system/edge-management.service']
else:
    protected += ['/etc/edgebox/bastion-tunnel.conf']
hashes = {p: hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in protected if pathlib.Path(p).exists()}
(snapshot / 'protected.json').write_text(json.dumps(hashes, indent=2))
key = root / 'wireguard.key'
if not key.exists():
    key.write_text(run('wg', 'genkey'))
os.chmod(key, 0o600)
with key.open() as f:
    public = subprocess.check_output(['wg', 'pubkey'], stdin=f, text=True).strip()
(root / 'wireguard.pub').write_text(public + '\n')
print(json.dumps({'role': role, 'snapshot': str(snapshot), 'public_key': public}))
