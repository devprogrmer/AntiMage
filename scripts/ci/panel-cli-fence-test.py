#!/usr/bin/env python3
"""Installed Panel CLI journal + production Go DB guard, in fresh Bash processes."""
import hashlib
import json
import os
import pathlib
import sqlite3
import subprocess
import tempfile
import time

repo = pathlib.Path(__file__).resolve().parents[2]
with tempfile.TemporaryDirectory(prefix='antimage-panel-fence-') as temporary:
    root = pathlib.Path(temporary)
    binary = root / 'antimage-cli'
    subprocess.run(['go', 'build', '-o', str(binary), './cmd/antimage_cli'], cwd=repo, check=True, timeout=180)
    database = root / 'operations.sqlite'
    db = sqlite3.connect(database)
    db.executescript('''
CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT);
CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT,PRIMARY KEY(target_type,target_id));
CREATE TABLE operation_executor_leases(operation_id TEXT PRIMARY KEY,executor_id TEXT,acquired_at BIGINT,expires_at BIGINT,generation BIGINT,revision BIGINT);
CREATE TABLE operation_resource_fences(target_type TEXT,target_id TEXT,generation BIGINT,owner_operation_id TEXT,revision BIGINT,PRIMARY KEY(target_type,target_id));
''')
    operation = 'panel-test-operation'
    command = 'panel-command-' + hashlib.sha256((operation + '|update').encode()).hexdigest()[:32]
    now = int(time.time() * 1000)
    db.execute("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", (operation, 'update', 'panel', 'panel', 'local-test-admin', 'isolated-request', 'running', 'installing', 50, now//1000, now//1000, now//1000, None, '', json.dumps({'origin':'cli'})))
    db.execute("INSERT INTO operation_locks VALUES('panel','panel',?)", (operation,))
    db.execute("INSERT INTO operation_executor_leases VALUES(?,'first',?,?,1,1)", (operation, now, now+180000))
    db.execute("INSERT INTO operation_resource_fences VALUES('panel','panel',1,?,1)", (operation,))
    db.commit()
    script = r'''
set -eo pipefail
source "$1/scripts/antimage/antimage-binary.sh"
set -u
APP_DIR="$2"; ENV_FILE="$2/unused.env"; APP_NAME=isolated
# Capture fixture paths before function arguments shadow positional parameters.
TEST_DATABASE="$3"; TEST_CLI="$4"
antimage_cli() { SQLALCHEMY_DATABASE_URL="sqlite:///$TEST_DATABASE" "$TEST_CLI" "$@"; }
update_command() {
  touch "$APP_DIR/ready"
  for ((i=0;i<1000;i++)); do [ -f "$APP_DIR/resume" ] && break; sleep .01; done
  [ -f "$APP_DIR/resume" ] || return 1
  panel_owned_boundary bash -c 'printf committed > "$1"' fixture "$APP_DIR/production"
}
panel_route_destructive_command update --fence-operation-id "$5" --executor-id first --lease-generation 1 --resource-generation 1 --command-id "$6"
'''
    env = dict(os.environ, ANTIMAGE_SOURCE_ONLY='1')
    worker = subprocess.Popen(['bash','-c',script,'test',str(repo),str(root),str(database),str(binary),operation,command], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    deadline = time.monotonic()+20
    while not (root/'ready').exists() and worker.poll() is None and time.monotonic()<deadline:
        time.sleep(.01)
    if not (root/'ready').exists():
        worker.kill(); out,err=worker.communicate(timeout=5)
        raise AssertionError('CLI did not reach paused production boundary: '+out+err)
    # Independent DB owner takes over while the old CLI is paused before commit.
    db.execute("UPDATE operation_executor_leases SET executor_id='second',generation=2,expires_at=?,revision=2 WHERE operation_id=?", (now+180000,operation))
    db.execute("UPDATE operation_resource_fences SET generation=2,revision=2 WHERE target_type='panel' AND target_id='panel'")
    db.commit()
    (root/'resume').touch()
    out,err = worker.communicate(timeout=20)
    assert worker.returncode != 0 and not (root/'production').exists(), 'stale CLI committed after DB takeover: '+out+err
    assert 'stale or expired Panel command rejected' in err, out+err
    journal=json.loads((root/'.maintenance-fences'/('operation-'+operation+'.json')).read_text())
    assert journal['commands'][command]=='started', 'unknown command receipt was discarded'
    db.close()
print('Panel production CLI: persisted owner check, paused stale boundary rejection, and unknown journal retention passed')
