#!/usr/bin/env python3
"""Exercise the embedded production backup/restore path, with no service mocks."""
import json
import os
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
helper = (ROOT / 'scripts/antimage/managed-backup.sh').read_text()
function = helper[helper.index('managed_binary_backup()'):].strip()
for name in ('antimage-binary.sh', 'antimage-node-binary.sh'):
    script = (ROOT / 'scripts/antimage' / name).read_text()
    start = script.index('managed_binary_backup()')
    end = script.index('\n}\n', start) + 2
    assert script[start:end].strip() == function, 'Embedded backup helper diverged: ' + name

with tempfile.TemporaryDirectory(prefix='antimage-backup-') as temporary:
    app = pathlib.Path(temporary)
    binary = app / 'node'
    metadata = app / '.binary-release.json'
    binary.write_bytes(pathlib.Path('/bin/true').read_bytes())
    binary.chmod(0o750)
    old_bytes = binary.read_bytes()
    metadata.write_text(json.dumps({'tag': 'v1.2.3', 'commit': 'a' * 40}))
    def invoke(action, identity='update-1', success=True):
        result = subprocess.run(['bash', '-c', function + '\nmanaged_binary_backup "$@"', 'test',
                                 action, str(app), identity, 'node', 'node-1', str(binary), str(metadata)],
                                capture_output=True, text=True, timeout=20)
        assert (result.returncode == 0) == success, (action, result.stdout, result.stderr)
        return json.loads(result.stdout) if success else None
    inventory = invoke('create')
    assert inventory['version'] == 'v1.2.3' and inventory['commit'] == 'a' * 40
    assert inventory['source_operation_id'] == 'update-1'
    assert inventory['files'][0]['size'] == len(old_bytes)
    invoke('create', success=False)
    binary.write_bytes(b'new target')
    binary.chmod(0o700)
    metadata.write_text('{"tag":"v1.2.4"}')
    invoke('restore')
    assert binary.read_bytes() == old_bytes
    assert binary.stat().st_mode & 0o777 == 0o750
    assert json.loads(metadata.read_text())['tag'] == 'v1.2.3'
    assert binary.stat().st_uid == os.getuid()
    invoke('verify')
    assert (app / '.update-backups/update-1/manifest.json').exists(), 'Recoverable backup was deleted'
    backup = app / '.update-backups/update-1/0'
    backup.write_bytes(b'corrupt backup')
    binary.write_bytes(b'current healthy target')
    invoke('restore', success=False)
    assert binary.read_bytes() == b'current healthy target', 'Invalid backup replaced live binary'
    invoke('restore', identity='../escape', success=False)
    print('Production managed backup: identity, metadata, retention, restore, mode/owner, corruption and traversal passed')

# Execute the identical embedded Python body in fresh processes. Inject sudden
# process exit at real rename syscalls; files, fsync and recovery are production
# code, with no backup/restore implementation substituted by the harness.
body = function.split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
checkpoints = ['rollback_prepared', 'backup_verified', 'restore_ready',
               'first_production_commit', 'restore_committed', 'rollback_restart_required']
for target_type in ('node', 'panel'):
    for checkpoint in checkpoints:
        with tempfile.TemporaryDirectory(prefix='antimage-restore-crash-') as temporary:
            app = pathlib.Path(temporary)
            destinations = [app / 'server', app / 'cli']
            original = pathlib.Path('/bin/true').read_bytes()
            for destination in destinations:
                destination.write_bytes(original)
                destination.chmod(0o750)
            arguments = ['create', str(app), 'crash-operation', target_type, target_type + '-1'] + list(map(str, destinations))
            subprocess.run(['python3', '-c', body] + arguments, check=True, capture_output=True, timeout=20)
            backup = app / '.update-backups/crash-operation'
            manifest_bytes = (backup / 'manifest.json').read_bytes()
            for destination in destinations:
                destination.write_bytes(b'broken replacement')
            injection = '''
import os, pathlib, json
_production_replace = os.replace
def _crash_replace(source, destination):
    _production_replace(source, destination)
    if pathlib.Path(destination).name == 'restore-state.json':
        if json.loads(pathlib.Path(destination).read_text())['phase'] == CRASH_CHECKPOINT:
            os._exit(91)
    elif CRASH_CHECKPOINT == 'first_production_commit' and pathlib.Path(destination).name == 'server':
        os._exit(91)
os.replace = _crash_replace
'''
            crashed = subprocess.run(['python3', '-c', 'CRASH_CHECKPOINT=' + repr(checkpoint) + '\n' + injection + body]
                                     + ['restore'] + arguments[1:], capture_output=True, timeout=20)
            assert crashed.returncode == 91, (target_type, checkpoint, crashed.stderr)
            state_path = backup / 'restore-state.json'
            interrupted_state = json.loads(state_path.read_text())
            original_deadline = interrupted_state['deadline_at_unix_nanos']
            if checkpoint == 'restore_ready':
                production_before = [path.read_bytes() for path in destinations]
                interrupted_state['deadline_at_unix_nanos'] = 1
                state_path.write_text(json.dumps(interrupted_state))
                expired = subprocess.run(['python3', '-c', body] + ['restore'] + arguments[1:], capture_output=True, timeout=20)
                assert expired.returncode != 0, 'Restart reset an expired restore deadline'
                assert production_before == [path.read_bytes() for path in destinations]
                interrupted_state['deadline_at_unix_nanos'] = original_deadline
                state_path.write_text(json.dumps(interrupted_state))
            restored = subprocess.run(['python3', '-c', body] + ['restore'] + arguments[1:], capture_output=True, timeout=20)
            assert restored.returncode == 0, (target_type, checkpoint, restored.stderr)
            assert (backup / 'manifest.json').read_bytes() == manifest_bytes, 'Recovery modified last good backup'
            assert all(destination.read_bytes() == original for destination in destinations)
            assert json.loads((backup / 'restore-state.json').read_text())['phase'] == 'rollback_restart_required'
            assert json.loads(state_path.read_text())['deadline_at_unix_nanos'] == original_deadline, 'Recovery reset original restore deadline'
            identities = [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations]
            subprocess.run(['python3', '-c', body] + ['restore'] + arguments[1:], check=True, capture_output=True, timeout=20)
            assert identities == [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations], 'Lost ACK recovery repeated completed restore'
print('Production restore transaction: 12 Node/Panel process-crash boundaries and completed-restore reuse passed')
