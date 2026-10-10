#!/usr/bin/env python3
"""Process-crash recovery of the exact installed Node/Panel file commit helper."""
import json
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
install = (ROOT / 'scripts/antimage/transactional-install.sh').read_text()
function = install[install.index('managed_binary_install()'):].strip()
backup = (ROOT / 'scripts/antimage/managed-backup.sh').read_text()
for name in ('antimage-binary.sh', 'antimage-node-binary.sh'):
    text = (ROOT / 'scripts/antimage' / name).read_text()
    start = text.index('managed_binary_install()')
    end = text.index('\n}\n', start) + 2
    assert text[start:end].strip() == function.strip(), name + ' helper diverged'
body = function.split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
phases = ['artifact_ready', 'backup_verified', 'replacement_ready', 'first_production_commit',
          'replacement_committed', 'runtime_restart_required']
for kind in ('node', 'panel'):
    for phase in phases:
        with tempfile.TemporaryDirectory(prefix='antimage-install-crash-') as temporary:
            app = pathlib.Path(temporary)
            sources = [app / 'target-server', app / 'target-cli'][:1 if kind == 'node' else 2]
            destinations = [app / 'server', app / 'cli'][:len(sources)]
            for source, destination in zip(sources, destinations):
                source.write_bytes(pathlib.Path('/bin/false').read_bytes())
                destination.write_bytes(pathlib.Path('/bin/true').read_bytes())
                destination.chmod(0o755)
            prefix = [str(app), 'operation-1', kind, kind + '-1']
            subprocess.run(['bash', '-c', backup + '\nmanaged_binary_backup "$@"', 'test', 'create']
                           + prefix + list(map(str, destinations)), check=True, capture_output=True, timeout=20)
            manifest = app / '.update-backups/operation-1/manifest.json'
            good_backup = manifest.read_bytes()
            arguments = prefix + [str(path) for pair in zip(sources, destinations) for path in pair]
            if phase == 'artifact_ready':
                expected_bytes = sources[0].read_bytes()
                wrong_arch = bytearray(expected_bytes)
                wrong_arch[18:20] = b'\xff\xff'
                sources[0].write_bytes(wrong_arch)
                rejected = subprocess.run(['bash', '-c', backup + '\n' + function + '\nmanaged_binary_install "$@"', 'test']
                                          + arguments, capture_output=True, timeout=20)
                assert rejected.returncode != 0, 'Wrong architecture executable was activated'
                assert all(path.read_bytes() == pathlib.Path('/bin/true').read_bytes() for path in destinations)
                sources[0].write_bytes(expected_bytes)
            injection = '''
import os, pathlib, json
_replace = os.replace
def crash_replace(source,destination):
    _replace(source,destination)
    if pathlib.Path(destination).name == 'state.json':
        if json.loads(pathlib.Path(destination).read_text())['phase'] == CHECKPOINT: os._exit(92)
    elif CHECKPOINT == 'first_production_commit' and pathlib.Path(destination).name == 'server': os._exit(92)
os.replace=crash_replace
'''
            crashed = subprocess.run(['python3', '-c', 'CHECKPOINT=' + repr(phase) + '\n' + injection + body]
                                     + arguments, capture_output=True, timeout=20)
            assert crashed.returncode == 92, (kind, phase, crashed.stderr)
            def invoke():
                return subprocess.run(['bash', '-c', backup + '\n' + function + '\nmanaged_binary_install "$@"', 'test']
                                      + arguments, capture_output=True, timeout=20)
            state_path = app / '.update-transactions/operation-1/state.json'
            interrupted_state = json.loads(state_path.read_text())
            original_deadline = interrupted_state['deadline_at_unix_nanos']
            if phase == 'replacement_ready':
                production_before = [path.read_bytes() for path in destinations]
                interrupted_state['deadline_at_unix_nanos'] = 1
                state_path.write_text(json.dumps(interrupted_state))
                assert invoke().returncode != 0, 'Restart reset an expired installation deadline'
                assert production_before == [path.read_bytes() for path in destinations]
                interrupted_state['deadline_at_unix_nanos'] = original_deadline
                state_path.write_text(json.dumps(interrupted_state))
            resumed = invoke()
            assert resumed.returncode == 0, (kind, phase, resumed.stderr)
            assert all(source.read_bytes() == destination.read_bytes() for source, destination in zip(sources, destinations))
            assert manifest.read_bytes() == good_backup, 'Recovery overwrote verified backup'
            state = json.loads((app / '.update-transactions/operation-1/state.json').read_text())
            assert state['phase'] == 'runtime_restart_required'
            assert state['deadline_at_unix_nanos'] == original_deadline, 'Recovery reset original install deadline'
            identities = [(path.stat().st_ino, path.stat().st_mtime_ns) for path in destinations]
            assert invoke().returncode == 0
            assert identities == [(path.stat().st_ino, path.stat().st_mtime_ns) for path in destinations], 'Install lost ACK repeated replacement'
            sources[0].write_bytes(pathlib.Path('/bin/true').read_bytes())
            assert invoke().returncode != 0, 'Recovery accepted a changed immutable target'
print('Production installation transaction: 12 Node/Panel crash boundaries, partial bundle commit, lost-ACK reuse and immutable target rejection passed')
