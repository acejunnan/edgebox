#!/usr/bin/env python3
"""Explicit deployment lifecycle. Root only. Management remains independent."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

STAGE = Path('/opt/edgebox-install')
ROOT = Path('/var/lib/edgebox/dataplane')
SYSCTL = Path('/etc/sysctl.d/80-edgebox-forward.conf')


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def rollback_script(role, forwarding):
    return ('#!/bin/sh\nset -eu\n' +
            ('systemctl disable --now edge-proxy.service || true\n' if role == 'relay' else '') +
            'systemctl disable --now edge-dataplane.service || true\n'
            '/opt/edgebox-network/edge-dataplane --cleanup\n' +
            (f'rm -f /etc/sysctl.d/80-edgebox-forward.conf\n/sbin/sysctl -w net.ipv4.ip_forward={forwarding}\n' if role == 'edge' else '') +
            'echo "SAFE_MODE: data plane removed; management preserved"\n')


def verify_protected():
    snapshots = sorted(ROOT.glob('before-*/protected.json'), key=lambda p: p.stat().st_mtime)
    if not snapshots:
        raise RuntimeError('missing pre-install snapshot')
    for name, digest in json.loads(snapshots[-1].read_text()).items():
        if hashlib.sha256(Path(name).read_bytes()).hexdigest() != digest:
            raise RuntimeError('protected management/firewall file changed: ' + name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    config = sub.add_parser('configure')
    config.add_argument('--peer', required=True)
    config.add_argument('--relay-host', required=True)
    config.add_argument('--mtu', type=int, default=1380)
    config.add_argument('--bandwidth-mbit', type=int, default=10)
    for name in ('start', 'status', 'safe-mode'):
        sub.add_parser(name)
    commit = sub.add_parser('commit')
    commit.add_argument('--verified', action='store_true', required=True, help='operator has verified fresh reverse SSH and real client egress')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('root required')
    role = (STAGE / 'role').read_text().strip()
    if role not in ('edge', 'relay'):
        raise RuntimeError('invalid installed role')
    if args.action == 'configure':
        if role == 'edge':
            run('systemctl', 'is-active', 'edge-management.service')
            # A fresh install may establish management after binary preparation.
            # Add those files to the protected baseline before data-plane staging.
            verify_protected()
            snapshot = max(ROOT.glob('before-*/protected.json'), key=lambda p: p.stat().st_mtime)
            hashes = json.loads(snapshot.read_text())
            for name in ('/etc/edgebox/edge-management.ssh_config', '/etc/systemd/system/edge-management.service'):
                hashes[name] = hashlib.sha256(Path(name).read_bytes()).hexdigest()
            snapshot.write_text(json.dumps(hashes, indent=2))
        run(sys.executable, str(STAGE / 'install.py'), role, args.peer, str(STAGE),
            '--relay-host', args.relay_host, '--mtu', str(args.mtu), '--bandwidth-mbit', str(args.bandwidth_mbit))
        print('Configuration staged; rollback armed. Next: edgeboxctl start')
    elif args.action == 'start':
        if (ROOT / 'activation.json').exists():
            raise RuntimeError('activation already recorded; use systemctl for committed service restarts')
        verify_protected()
        if role == 'edge':
            run('systemctl', 'is-active', 'edge-management.service')
            if SYSCTL.exists():
                raise RuntimeError('forward sysctl file already owned; refusing overwrite')
        before = run('/sbin/sysctl', '-n', 'net.ipv4.ip_forward')
        rollback = ROOT / 'rollback-install.sh'
        rollback.write_text(rollback_script(role, before))
        rollback.chmod(0o700)
        # Timer survives controller exit and SSH disconnect. Arm before mutations.
        run('systemctl', 'stop', 'edgebox-data-install-rollback.timer')
        run('systemd-run', '--unit=edgebox-data-activation-rollback', '--on-active=30m',
            '--timer-property=AccuracySec=1s', str(rollback))
        (ROOT / 'activation.json').write_text(json.dumps({'role': role, 'ip_forward_before': before}))
        try:
            if role == 'edge':
                run('/sbin/sysctl', '-w', 'net.ipv4.ip_forward=1')
            run('systemctl', 'start', 'edge-dataplane.service')
            if role == 'relay':
                run('systemctl', 'start', 'edge-proxy.service')
            verify_protected()
            if role == 'edge':
                run('systemctl', 'is-active', 'edge-management.service')
        except Exception:
            subprocess.call([str(rollback)])
            raise
        print('Started with 30-minute automatic rollback. Verify both hosts and client egress, then commit on BOTH hosts.')
    elif args.action == 'commit':
        verify_protected()
        if not (ROOT / 'activation.json').exists():
            raise RuntimeError('start first')
        run('systemctl', 'is-active', 'edge-dataplane.service')
        state = json.loads((ROOT / 'status.json').read_text())
        if state.get('state') != 'ONLINE':
            raise RuntimeError('transport not ONLINE; cannot commit')
        if role == 'edge':
            run('systemctl', 'is-active', 'edge-management.service')
            SYSCTL.write_text('net.ipv4.ip_forward=1\n')
            SYSCTL.chmod(0o644)
        else:
            run('systemctl', 'is-active', 'edge-proxy.service')
        run('systemctl', 'enable', 'edge-dataplane.service')
        if role == 'relay':
            run('systemctl', 'enable', 'edge-proxy.service')
        run('systemctl', 'stop', 'edgebox-data-activation-rollback.timer')
        print('Committed. Private client link on VPS: /var/lib/edgebox/dataplane/client-link.txt')
    elif args.action == 'safe-mode':
        run(str(ROOT / 'rollback-install.sh'))
        print('SAFE_MODE; data plane disabled, management preserved.')
    else:
        print((ROOT / 'status.json').read_text() if (ROOT / 'status.json').exists() else 'Not started')
        print(run('systemctl', 'is-active', 'edge-dataplane.service'))


if __name__ == '__main__':
    main()
