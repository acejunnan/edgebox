#!/usr/bin/env python3
"""Install source-built tools, prepare unique credentials; never start networking."""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

SOURCE = Path(__file__).resolve().parents[1]
STAGE = Path('/opt/edgebox-install')


def run(*args, **kw):
    return subprocess.check_output(args, text=True, **kw).strip()


def download(url, path, digest):
    with urllib.request.urlopen(url, timeout=120) as response:
        with path.open('wb') as output:
            shutil.copyfileobj(response, output)
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise RuntimeError('download checksum mismatch')


def main():
    if os.geteuid() != 0 or sys.argv[1:] not in (['vps'], ['n1']):
        raise SystemExit('Usage: sudo python3 provision.py vps|n1')
    role = 'relay' if sys.argv[1] == 'vps' else 'edge'
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if arch is None or (role == 'relay' and arch != 'amd64'):
        raise SystemExit('Supported: amd64 VPS; arm64/amd64 edge (64-bit Linux).')
    if STAGE.exists() or Path('/etc/edgebox/dataplane.json').exists():
        raise SystemExit('Existing installation/staging found; refusing overwrite. See install guide.')
    if role == 'edge' and not Path('/etc/ssh/sshd_config').exists():
        raise SystemExit('Configure key-based local SSH and reverse management first.')
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='edgebox-build-') as tmp:
        tmp = Path(tmp)
        # Fetch supported stable Go from its official HTTPS index, verify archive.
        with urllib.request.urlopen('https://go.dev/dl/?mode=json', timeout=60) as response:
            releases = json.load(response)
        release = next(r for r in releases if r['stable'])
        asset = next(f for f in release['files'] if f['os'] == 'linux' and f['arch'] == arch and f['kind'] == 'archive')
        archive = tmp / 'go.tar.gz'
        download('https://go.dev/dl/' + asset['filename'], archive, asset['sha256'])
        with tarfile.open(archive) as tf:
            # Reject paths escaping the build directory even on older Python.
            for member in tf.getmembers():
                target = (tmp / member.name).resolve()
                if not target.is_relative_to(tmp.resolve()) or member.issym() or member.islnk():
                    raise RuntimeError('unsafe Go archive entry')
            tf.extractall(tmp)
        built = tmp / 'stage'
        built.mkdir()
        env = dict(os.environ, CGO_ENABLED='0', GOTOOLCHAIN='local')
        for name in ('edge-agent', 'edge-dataplane', 'edge-web', 'edge-updater', 'edge-management-render', 'edge-ap-render', 'edge-release'):
            subprocess.check_call([str(tmp / 'go/bin/go'), 'build', '-trimpath', '-o', str(built / name), './cmd/' + name], cwd=SOURCE, env=env)
        for name in ('edge-dataplane.service', 'edge-proxy.service', 'install.py', 'prepare.py'):
            shutil.copyfile(SOURCE / 'deploy/dataplane' / name, built / name)
        shutil.copyfile(SOURCE / 'deploy/edgeboxctl.py', built / 'edgeboxctl.py')
        if role == 'relay':
            download('https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip',
                     built / 'Xray-linux-64.zip', '23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae')
        # Prepare checks ownership/collisions and snapshots BEFORE installing files.
        subprocess.check_call([sys.executable, str(built / 'prepare.py'), role])
        shutil.copytree(built, STAGE)
        for path in STAGE.iterdir():
            path.chmod(0o755 if path.suffix not in ('.py', '.zip', '.service') else 0o600)
        (STAGE / 'role').write_text(role)
        (STAGE / 'source-commit').write_text(run('git', '-C', str(SOURCE), 'rev-parse', 'HEAD') + '\n')
    shutil.copyfile(STAGE / 'edgeboxctl.py', '/usr/local/sbin/edgeboxctl')
    Path('/usr/local/sbin/edgeboxctl').chmod(0o700)
    print('Prepared. No data-plane service has been started.')
    print('Public WireGuard key:', Path('/var/lib/edgebox/dataplane/wireguard.pub').read_text().strip())
    print('Next: edgeboxctl configure --peer OTHER_PUBLIC_KEY --relay-host ACTUAL_VPS_IPV4')


if __name__ == '__main__':
    main()
