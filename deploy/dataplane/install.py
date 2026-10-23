#!/usr/bin/python3
"""Explicit first installation; credentials remain local. Run under root."""
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import uuid
import zipfile
import argparse
import ipaddress

def run(*args):
    return subprocess.check_output(args, text=True)

os.umask(0o077)
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('role', choices=['edge', 'relay'])
parser.add_argument('peer', help='other host WireGuard public key')
parser.add_argument('stage', help='directory containing built binary and service files')
parser.add_argument('--relay-host', required=True, help='actual VPS public IPv4 address')
parser.add_argument('--mtu', type=int, default=1380)
parser.add_argument('--bandwidth-mbit', type=int, default=10)
parser.add_argument('--resume-prepared', action='store_true')
args = parser.parse_args()
if not ipaddress.IPv4Address(args.relay_host).is_global:
    parser.error('relay-host must be the actual public VPS IPv4 address')
if not 1280 <= args.mtu <= 1500 or not 1 <= args.bandwidth_mbit <= 10000:
    parser.error('invalid MTU or bandwidth ceiling')
role, peer, stage = args.role, args.peer, args.stage
resume = args.resume_prepared
assert role in ('edge', 'relay') and re.fullmatch(r'[A-Za-z0-9+/]{43}=', peer)
assert os.geteuid() == 0
stage = pathlib.Path(stage)
etc = pathlib.Path('/etc/edgebox')
etc.mkdir(exist_ok=True)
root = pathlib.Path('/var/lib/edgebox/dataplane')
assert (root / 'wireguard.key').exists()
for p in ['/etc/edgebox/dataplane.json', '/etc/systemd/system/edge-dataplane.service', '/opt/edgebox-network/edge-dataplane']:
    assert resume or not pathlib.Path(p).exists(), 'refusing to overwrite existing installation: ' + p
if resume:
    assert subprocess.run(['systemctl', 'is-active', '--quiet', 'edge-dataplane']).returncode != 0
    assert subprocess.run(['systemctl', 'is-active', '--quiet', 'edge-proxy']).returncode != 0
network_dir = pathlib.Path('/opt/edgebox-network')
network_dir.mkdir(exist_ok=True)
os.chmod(network_dir, 0o755)
shutil.copyfile(stage / 'edge-dataplane', network_dir / 'edge-dataplane')
os.chmod(network_dir / 'edge-dataplane', 0o755)
private = (root / 'wireguard.key').read_text().strip()
lines = ['[Interface]', 'PrivateKey = ' + private]
if role == 'relay':
    lines += ['ListenPort = 51820']
lines += ['[Peer]', 'PublicKey = ' + peer, 'AllowedIPs = ' + ('10.77.0.1/32' if role == 'edge' else '0.0.0.0/0')]
if role == 'edge':
    lines += [f'Endpoint = {args.relay_host}:51820', 'PersistentKeepalive = 25']
(etc / 'wireguard-v1.conf').write_text('\n'.join(lines) + '\n')
os.chmod(etc / 'wireguard-v1.conf', 0o600)
config = {
    'transport': {'interface': 'ebwg0', 'address': ('10.77.0.2/32' if role == 'edge' else '10.77.0.1/32'), 'mtu': args.mtu, 'file': '/etc/edgebox/wireguard-v1.conf'},
    'network': {'role': role, 'interface': 'ebwg0', 'source': '10.77.0.1', 'table': 177, 'priority': 10777, 'bandwidth_mbit': args.bandwidth_mbit},
}
(etc / 'dataplane.json').write_text(json.dumps(config, indent=2))
run(str(network_dir / 'edge-dataplane'), '--check')
shutil.copyfile(stage / 'edge-dataplane.service', '/etc/systemd/system/edge-dataplane.service')
os.chmod('/etc/systemd/system/edge-dataplane.service', 0o644)
if role == 'relay':
    archive = stage / 'Xray-linux-64.zip'
    assert hashlib.sha256(archive.read_bytes()).hexdigest() == '23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae'
    binary = network_dir / 'xray'
    with zipfile.ZipFile(archive) as z:
        binary.write_bytes(z.read('xray'))
    os.chmod(binary, 0o755)
    key_output = run(str(binary), 'x25519')
    pairs = dict(line.split(': ', 1) for line in key_output.strip().splitlines() if ': ' in line)
    private_key = pairs.get('PrivateKey', pairs.get('Private key'))
    public_key = pairs.get('Password (PublicKey)', pairs.get('Password', pairs.get('PublicKey', pairs.get('Public key'))))
    assert private_key and public_key, 'unrecognized key generator output'
    ident = str(uuid.uuid4())
    short = os.urandom(8).hex()
    xray = {
        'log': {'loglevel': 'warning', 'access': 'none'},
        'dns': {'servers': ['223.5.5.5', '1.1.1.1'], 'queryStrategy': 'UseIPv4'},
        'inbounds': [{'tag': 'clients', 'listen': '0.0.0.0', 'port': 443, 'protocol': 'vless',
            'settings': {'clients': [{'id': ident, 'flow': 'xtls-rprx-vision'}], 'decryption': 'none'},
            'streamSettings': {'network': 'tcp', 'security': 'reality', 'realitySettings': {
                'show': False, 'dest': 'www.cloudflare.com:443', 'xver': 0, 'serverNames': ['www.cloudflare.com'],
                'privateKey': private_key, 'shortIds': [short]}}}],
        'outbounds': [{'tag': 'n1-only', 'protocol': 'freedom', 'sendThrough': '10.77.0.1',
            'settings': {'domainStrategy': 'ForceIPv4'}, 'streamSettings': {'sockopt': {'interface': 'ebwg0'}}},
            {'tag': 'blocked', 'protocol': 'blackhole'}],
        'routing': {'domainStrategy': 'IPIfNonMatch', 'rules': [
            {'type': 'field', 'ip': ['geoip:private'], 'outboundTag': 'blocked'}]},
    }
    # Use explicit ranges, so geodata downloads are not an operational dependency.
    xray['routing']['rules'][0]['ip'] = ['0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8', '169.254.0.0/16', '172.16.0.0/12', '192.168.0.0/16', '224.0.0.0/3', '::/0']
    (etc / 'xray-relay.json').write_text(json.dumps(xray, indent=2))
    os.chmod(etc / 'xray-relay.json', 0o640)
    if subprocess.run(['id', 'edge-proxy'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
        subprocess.check_call(['useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', 'edge-proxy'])
    shutil.chown(etc / 'xray-relay.json', user='root', group='edge-proxy')
    run(str(binary), 'run', '-test', '-config', str(etc / 'xray-relay.json'))
    shutil.copyfile(stage / 'edge-proxy.service', '/etc/systemd/system/edge-proxy.service')
    os.chmod('/etc/systemd/system/edge-proxy.service', 0o644)
    link = f'vless://{ident}@{args.relay_host}:443?encryption=none&security=reality&sni=www.cloudflare.com&fp=chrome&pbk={public_key}&sid={short}&type=tcp&flow=xtls-rprx-vision#EdgeBox-N1'
    (root / 'client-link.txt').write_text(link + '\n')
    # Public-key client data includes its bearer credential; never print it.
    (root / 'client.json').write_text(json.dumps({'id': ident, 'public_key': public_key, 'short_id': short}))
rollback = root / 'rollback-install.sh'
rollback.write_text('#!/bin/sh\nset -eu\n' +
    ('systemctl disable --now edge-proxy.service || true\n' if role == 'relay' else '') +
    'systemctl disable --now edge-dataplane.service || true\n' +
    '/opt/edgebox-network/edge-dataplane --cleanup\n' +
    'printf "SAFE_MODE: deployment rolled back; management untouched\\n"\n')
os.chmod(rollback, 0o700)
run('systemctl', 'daemon-reload')
run('systemd-analyze', 'verify', '/etc/systemd/system/edge-dataplane.service')
run('systemd-run', '--unit=edgebox-data-install-rollback', '--on-active=30m', '--timer-property=AccuracySec=1s', str(rollback))
print(json.dumps({'role': role, 'configuration_validated': True, 'rollback_armed': True, 'services_started': False}))
