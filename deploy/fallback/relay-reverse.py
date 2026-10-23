#!/usr/bin/python3
"""Add Xray VLESS reverse fallback (data + management) to the relay entry.
Run as root on the VPS. Idempotent. Backs up the current config, validates
with `xray run -test`, and never prints credentials."""
import json, os, pathlib, shutil, subprocess, sys, time, uuid
import ipaddress

relay_host = os.environ.get('EDGEBOX_RELAY_HOST', '')
if not relay_host or not ipaddress.IPv4Address(relay_host).is_global:
    raise SystemExit('Set EDGEBOX_RELAY_HOST to the actual VPS public IPv4 address')

os.umask(0o077)
assert os.geteuid() == 0
etc = pathlib.Path('/etc/edgebox/xray-relay.json')
state = pathlib.Path('/var/lib/edgebox/dataplane')
creds = state / 'n1-reverse.json'
xray = '/opt/edgebox-network/xray'

cfg = json.loads(etc.read_text())
backup = state / ('xray-relay.json.bak-' + time.strftime('%Y%m%d%H%M%S'))
shutil.copy2(etc, backup)

if creds.exists():
    c = json.loads(creds.read_text())
else:
    client = json.loads((state / 'client.json').read_text())
    c = {'bridge_id': str(uuid.uuid4()), 'mgmt_id': str(uuid.uuid4()),
         'public_key': client['public_key'], 'short_id': client['short_id'],
         'server': relay_host, 'port': 443, 'sni': 'www.cloudflare.com'}
    creds.write_text(json.dumps(c))
    os.chmod(creds, 0o600)

inb = next(i for i in cfg['inbounds'] if i.get('tag') == 'clients')
clients = [x for x in inb['settings']['clients'] if x.get('email') not in ('n1-bridge', 'n1-mgmt')]
for i, x in enumerate(clients):
    x.setdefault('email', 'client-%d' % (i + 1))
clients += [
    {'id': c['bridge_id'], 'flow': 'xtls-rprx-vision', 'email': 'n1-bridge', 'reverse': {'tag': 'n1-rev'}},
    {'id': c['mgmt_id'], 'flow': 'xtls-rprx-vision', 'email': 'n1-mgmt', 'reverse': {'tag': 'n1-mgmt'}},
]
inb['settings']['clients'] = clients
client_emails = [x['email'] for x in clients if x['email'] not in ('n1-bridge', 'n1-mgmt')]

cfg['inbounds'] = [inb, {
    'tag': 'mgmt-alt', 'listen': '127.0.0.1', 'port': 22011, 'protocol': 'tunnel',
    'settings': {'address': '127.0.0.1', 'port': 22, 'network': 'tcp'}}]

wg = next(o for o in cfg['outbounds'] if o.get('tag') in ('n1-only', 'n1-wg'))
wg['tag'] = 'n1-wg'
# First outbound is the default: unmatched traffic must never leave the VPS.
cfg['outbounds'] = [{'tag': 'blocked', 'protocol': 'blackhole'}, wg]

cfg['dns'] = {'tag': 'dns-internal', 'servers': ['1.1.1.1', '8.8.8.8'], 'queryStrategy': 'UseIPv4'}
private = ['0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8', '169.254.0.0/16',
           '172.16.0.0/12', '192.168.0.0/16', '224.0.0.0/3', '::/0']
cfg['routing'] = {'domainStrategy': 'IPIfNonMatch', 'rules': [
    {'type': 'field', 'inboundTag': ['mgmt-alt'], 'outboundTag': 'n1-mgmt'},
    {'type': 'field', 'ip': private, 'outboundTag': 'blocked'},
    {'type': 'field', 'inboundTag': ['clients', 'dns-internal'], 'balancerTag': 'n1'},
], 'balancers': [
    {'tag': 'n1', 'selector': ['n1-wg'], 'fallbackTag': 'n1-rev', 'strategy': {'type': 'leastPing'}},
]}
cfg['observatory'] = {'subjectSelector': ['n1-wg'], 'probeUrl': 'http://1.1.1.1/cdn-cgi/trace',
                      'probeInterval': '10s', 'enableConcurrency': True}

candidate = etc.with_name('xray-relay.next.json')
candidate.write_text(json.dumps(cfg, indent=2))
shutil.chown(candidate, user='root', group='edge-proxy')
os.chmod(candidate, 0o640)
r = subprocess.run([xray, 'run', '-test', '-config', str(candidate)], capture_output=True, text=True)
if r.returncode != 0:
    print('VALIDATION FAILED:', (r.stdout + r.stderr)[-1500:].replace(c['bridge_id'], '<id>').replace(c['mgmt_id'], '<id>'))
    candidate.unlink()
    sys.exit(1)
os.replace(candidate, etc)
print(json.dumps({'validated': True, 'backup': str(backup), 'clients_kept': client_emails}))
