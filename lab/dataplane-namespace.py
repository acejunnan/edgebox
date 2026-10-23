#!/usr/bin/python3
"""Qualify installed edge executor in an isolated network namespace first."""
import json
import os
import signal
import subprocess
import time

name = 'edgebox-qualification'
def run(*a):
    return subprocess.check_output(a, text=True, stderr=subprocess.STDOUT)
def ns(*a):
    return run('ip', 'netns', 'exec', name, *a)

assert os.geteuid() == 0
run('ip', 'netns', 'add', name)
try:
    ns('ip', 'link', 'set', 'lo', 'up')
    ns('ip', 'link', 'add', 'testwan', 'type', 'dummy')
    ns('ip', 'address', 'add', '192.0.2.2/24', 'dev', 'testwan')
    ns('ip', 'link', 'set', 'testwan', 'up')
    ns('ip', 'route', 'add', 'default', 'via', '192.0.2.1', 'dev', 'testwan')
    ns('/usr/sbin/nft', 'add', 'table', 'inet', 'unrelated_marker')
    for attempt in range(2):
        process = subprocess.Popen(['ip', 'netns', 'exec', name, '/opt/edgebox-network/edge-dataplane'], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        try:
            time.sleep(3)
            assert process.poll() is None, process.stdout.read()
            assert 'table inet edgebox' in ns('/usr/sbin/nft', 'list', 'table', 'inet', 'edgebox')
            assert '10777:' in ns('ip', '-4', 'rule')
            assert '192.0.2.1' in ns('ip', '-4', 'route', 'show', 'table', '177')
            assert 'tbf' in ns('tc', 'qdisc', 'show', 'dev', 'ebwg0')
        finally:
            process.send_signal(signal.SIGTERM)
            output = process.communicate(timeout=45)[0]
            print(output, end='')
            assert process.returncode == 0, 'executor failed'
        assert 'edgebox' not in ns('/usr/sbin/nft', 'list', 'tables')
        assert 'unrelated_marker' in ns('/usr/sbin/nft', 'list', 'tables')
        assert '10777:' not in ns('ip', '-4', 'rule')
        assert '192.0.2.1' in ns('ip', '-4', 'route', 'show', 'default')
    print(json.dumps({'namespace_apply_cleanup_repeat': 'passed', 'unrelated_firewall_preserved': True, 'main_default_preserved': True}))
finally:
    run('ip', 'netns', 'del', name)
