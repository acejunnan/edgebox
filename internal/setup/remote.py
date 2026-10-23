"""Fixed operations used by the workstation wizard. No arbitrary command API."""
import ipaddress
import json
import os
from pathlib import Path
import pwd
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.request

os.umask(0o077)
assert os.geteuid() == 0
request = json.load(__import__('sys').stdin)
mode = request['mode']
root = Path('/var/lib/edgebox/setup')


def run(*args, **kw):
    return subprocess.check_output(args, text=True, **kw).strip()


def write(path, data, mode=0o600):
    path = Path(path)
    with path.open('x') as output:
        output.write(data)
    path.chmod(mode)


def ssh_service():
    for name in ('ssh.service', 'sshd.service'):
        if subprocess.call(['systemctl', 'is-active', '--quiet', name]) == 0:
            return name
    raise RuntimeError('active SSH service required')


if mode == 'preflight':
    assert Path('/etc/debian_version').exists() and Path('/run/systemd/system').exists()
    for p in ('/opt/edgebox-install', '/etc/edgebox/dataplane.json', '/etc/edgebox/edge-management.ssh_config',
              '/etc/ssh/sshd_config.d/90-edgebox-tunnel.conf', '/etc/sysctl.d/80-edgebox-forward.conf',
              '/var/lib/edgebox/identity/device-id'):
        assert not Path(p).exists(), 'existing installation: ' + p
    for account in ('edge-tunnel', 'edge-management'):
        try:
            pwd.getpwnam(account)
        except KeyError:
            pass
        else:
            raise RuntimeError('existing management account')
    if request['role'] == 'vps':
        assert 'Include' in Path('/etc/ssh/sshd_config').read_text()
    root.mkdir(parents=True, exist_ok=False)
    root.chmod(0o700)
    for name, args in {'addresses.json': ['ip', '-j', 'address'],
                       'routes.json': ['ip', '-j', '-4', 'route', 'show', 'table', 'all'],
                       'rules.json': ['ip', '-j', '-4', 'rule'],
                       'wan.json': ['ip', '-j', '-4', 'route', 'show', 'table', 'main', 'default']}.items():
        write(root / name, run(*args))
    if Path('/usr/sbin/nft').exists():
        write(root / 'firewall.nft', run('/usr/sbin/nft', 'list', 'ruleset'))
    write(root / 'sshd_config', Path('/etc/ssh/sshd_config').read_text())
    assert json.loads((root / 'wan.json').read_text()), 'WAN default required'
    print('ready')
elif mode == 'edge-key':
    run('useradd', '--system', '--home-dir', '/var/lib/edgebox/management', '--shell', '/usr/sbin/nologin', 'edge-management')
    run('install', '-d', '-o', 'edge-management', '-g', 'edge-management', '-m', '0700', '/var/lib/edgebox/management')
    run('runuser', '-u', 'edge-management', '--', 'ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', '/var/lib/edgebox/management/id_ed25519')
    print(Path('/var/lib/edgebox/management/id_ed25519.pub').read_text().strip())
elif mode == 'bastion':
    port = int(request['port'])
    assert 1024 <= port <= 65535
    assert not run('ss', '-H', '-ltn', 'sport = :' + str(port))
    svc = ssh_service()
    operator = request['operator']
    before = run('/usr/sbin/sshd', '-T', '-C', 'user=' + operator + ',host=localhost,addr=127.0.0.1')
    rollback = root / 'rollback-management.sh'
    write(rollback, '#!/bin/sh\nset -eu\n'
          'usermod -L edge-tunnel || true\n'
          'if [ -f /var/lib/edgebox-tunnel/.ssh/authorized_keys ]; then truncate -s 0 /var/lib/edgebox-tunnel/.ssh/authorized_keys; fi\n'
          'rm -f /etc/ssh/sshd_config.d/90-edgebox-tunnel.conf\n'
          '/usr/sbin/sshd -t\nsystemctl reload ' + svc + '\n', 0o700)
    run('systemd-run', '--unit=edgebox-setup-management-rollback', '--on-active=10m', str(rollback))
    run('useradd', '--system', '--create-home', '--home-dir', '/var/lib/edgebox-tunnel', '--shell', '/usr/sbin/nologin', 'edge-tunnel')
    # Unknown per-install random password unlocks public-key auth on Debian;
    # account-scoped sshd policy disallows all password authentication.
    subprocess.run(['chpasswd'], input='edge-tunnel:' + secrets.token_urlsafe(48) + '\n', text=True, check=True, stdout=subprocess.DEVNULL)
    run('install', '-d', '-o', 'edge-tunnel', '-g', 'edge-tunnel', '-m', '0700', '/var/lib/edgebox-tunnel/.ssh')
    write('/var/lib/edgebox-tunnel/.ssh/authorized_keys', request['authorized_keys'])
    run('chown', 'edge-tunnel:edge-tunnel', '/var/lib/edgebox-tunnel/.ssh/authorized_keys')
    Path('/etc/ssh/sshd_config.d').mkdir(exist_ok=True)
    write('/etc/ssh/sshd_config.d/90-edgebox-tunnel.conf', request['policy'])
    run('/usr/sbin/sshd', '-t')
    after = run('/usr/sbin/sshd', '-T', '-C', 'user=' + operator + ',host=localhost,addr=127.0.0.1')
    assert before == after, 'operator SSH policy changed'
    effective = run('/usr/sbin/sshd', '-T', '-C', 'user=edge-tunnel,host=localhost,addr=127.0.0.1').splitlines()
    for setting in ('authenticationmethods publickey', 'passwordauthentication no', 'kbdinteractiveauthentication no',
                    'gatewayports no', 'allowtcpforwarding remote', 'permitopen none', 'maxsessions 0'):
        assert setting in effective, 'missing restriction: ' + setting
    write(root / 'port-registry.json', json.dumps({'device_id': request['device_id'], 'port': port}))
    run('systemctl', 'reload', svc)
    print('ready')
elif mode == 'edge-management':
    Path('/etc/edgebox').mkdir(exist_ok=True)
    identity = Path('/var/lib/edgebox/identity')
    identity.mkdir(mode=0o700, exist_ok=True)
    write(identity / 'device-id', request['device_id'] + '\n')
    for name, dest in {'edge-management.ssh_config': '/etc/edgebox/edge-management.ssh_config',
                       'known_hosts': '/var/lib/edgebox/management/known_hosts',
                       'edge-management.service': '/etc/systemd/system/edge-management.service'}.items():
        write(dest, request['files'][name], 0o644 if name.endswith('.service') else 0o600)
    run('chown', 'edge-management:edge-management', '/var/lib/edgebox/management/known_hosts')
    run('chown', 'root:edge-management', '/etc/edgebox/edge-management.ssh_config')
    Path('/etc/edgebox/edge-management.ssh_config').chmod(0o640)
    run('systemd-analyze', 'verify', '/etc/systemd/system/edge-management.service')
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', 'edge-management.service')
    print('ready')
elif mode == 'management-commit':
    run('systemctl', 'stop', 'edgebox-setup-management-rollback.timer')
    print('ready')
elif mode == 'public-key':
    print(Path('/var/lib/edgebox/dataplane/wireguard.pub').read_text().strip())
elif mode == 'configure':
    run('/usr/local/sbin/edgeboxctl', 'configure', '--peer', request['peer'], '--relay-host', request['relay'])
    print('ready')
elif mode in ('start', 'commit', 'safe-mode'):
    run('/usr/local/sbin/edgeboxctl', mode, *(['--verified'] if mode == 'commit' else []))
    print('ready')
elif mode == 'healthy':
    assert json.loads(Path('/var/lib/edgebox/dataplane/status.json').read_text())['state'] == 'ONLINE'
    assert json.loads((root / 'wan.json').read_text()) == json.loads(run('ip', '-j', '-4', 'route', 'show', 'table', 'main', 'default'))
    print('ready')
elif mode == 'egress':
    def public_ip(proxy=None):
        args = ['curl', '-4', '--fail', '--silent', '--show-error', '--max-time', '20']
        if proxy:
            args += ['--proxy', proxy]
        value = run(*args, 'https://checkip.amazonaws.com')
        assert ipaddress.IPv4Address(value).is_global
        return value
    if request.get('role') == 'edge':
        print(public_ip())
    else:
        # Test the actual VLESS -> relay -> WireGuard -> edge path, not just a handshake.
        client = json.loads(Path('/var/lib/edgebox/dataplane/client.json').read_text())
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0))
            port = s.getsockname()[1]
        config = {'log': {'loglevel': 'none'},
                  'inbounds': [{'listen': '127.0.0.1', 'port': port, 'protocol': 'socks', 'settings': {'auth': 'noauth', 'udp': False}}],
                  'outbounds': [{'protocol': 'vless', 'settings': {'vnext': [{'address': '127.0.0.1', 'port': 443,
                                  'users': [{'id': client['id'], 'encryption': 'none', 'flow': 'xtls-rprx-vision'}]}]},
                                 'streamSettings': {'network': 'tcp', 'security': 'reality',
                                  'realitySettings': {'serverName': 'www.cloudflare.com', 'fingerprint': 'chrome',
                                  'publicKey': client['public_key'], 'shortId': client['short_id']}}}]}
        with tempfile.TemporaryDirectory(prefix='verify-', dir=root) as tmp:
            cfg = Path(tmp) / 'client.json'
            cfg.write_text(json.dumps(config))
            process = subprocess.Popen(['/opt/edgebox-network/xray', 'run', '-config', str(cfg)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(50):
                    try:
                        with socket.create_connection(('127.0.0.1', port), timeout=0.2):
                            break
                    except OSError:
                        if process.poll() is not None:
                            raise RuntimeError('test client failed')
                        time.sleep(0.1)
                print(public_ip('socks5h://127.0.0.1:' + str(port)))
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
elif mode == 'client-link':
    print(Path('/var/lib/edgebox/dataplane/client-link.txt').read_text().strip())
else:
    raise RuntimeError('unsupported operation')
