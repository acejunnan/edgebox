"""Deployment safety tests; no root or real networking required."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('edgeboxctl', Path(__file__).with_name('edgeboxctl.py'))
ctl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ctl)


class InstallSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.stage = self.root / 'stage'
        self.stage.mkdir()
        (self.stage / 'role').write_text('edge')
        self.patches = [patch.object(ctl, 'ROOT', self.root), patch.object(ctl, 'STAGE', self.stage),
                        patch.object(ctl, 'SYSCTL', self.root / 'sysctl.conf'),
                        patch.object(ctl.os, 'geteuid', return_value=0, create=True)]
        for p in self.patches:
            p.start()
        snap = self.root / 'before-test'
        snap.mkdir()
        (snap / 'protected.json').write_text('{}')

    def tearDown(self):
        for p in reversed(self.patches):
            p.stop()
        self.tmp.cleanup()

    def test_failed_start_has_independent_timer_and_rolls_back(self):
        calls = []

        def run(*args):
            calls.append(args)
            if args == ('/sbin/sysctl', '-n', 'net.ipv4.ip_forward'):
                return '0'
            if args == ('systemctl', 'start', 'edge-dataplane.service'):
                raise RuntimeError('apply failure')
            return ''

        with patch.object(ctl, 'run', side_effect=run), patch.object(ctl.subprocess, 'call') as rollback, patch('sys.argv', ['edgeboxctl', 'start']):
            with self.assertRaisesRegex(RuntimeError, 'apply failure'):
                ctl.main()
            rollback.assert_called_once_with([str(self.root / 'rollback-install.sh')])
        arm = next(i for i, c in enumerate(calls) if c[0] == 'systemd-run')
        change = calls.index(('/sbin/sysctl', '-w', 'net.ipv4.ip_forward=1'))
        self.assertLess(arm, change)

    def test_offline_transport_cannot_commit(self):
        (self.root / 'activation.json').write_text('{}')
        (self.root / 'status.json').write_text(json.dumps({'state': 'SAFE_MODE'}))
        with patch.object(ctl, 'run', return_value='active') as run, patch('sys.argv', ['edgeboxctl', 'commit', '--verified']):
            with self.assertRaisesRegex(RuntimeError, 'not ONLINE'):
                ctl.main()
        self.assertFalse(any('enable' in c.args for c in run.call_args_list))
        self.assertFalse(ctl.SYSCTL.exists())

    def test_modified_management_file_blocks_start(self):
        target = self.root / 'management.conf'
        target.write_text('changed')
        (self.root / 'before-test/protected.json').write_text(json.dumps({str(target): 'wrong-hash'}))
        with patch.object(ctl, 'run') as run, patch('sys.argv', ['edgeboxctl', 'start']):
            with self.assertRaisesRegex(RuntimeError, 'protected management'):
                ctl.main()
        run.assert_not_called()

    def test_cleanup_is_scoped_and_restores_forwarding(self):
        script = ctl.rollback_script('edge', '0')
        self.assertIn('net.ipv4.ip_forward=0', script)
        self.assertIn('edge-dataplane --cleanup', script)
        for forbidden in ('flush ruleset', 'default route', 'edge-management', 'sshd', 'reboot'):
            self.assertNotIn(forbidden, script)


if __name__ == '__main__':
    unittest.main()
