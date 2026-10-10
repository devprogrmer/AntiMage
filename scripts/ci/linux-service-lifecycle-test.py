#!/usr/bin/env python3
"""Real shipped binaries, TLS artifact IO, file transactions and systemd runtime.

Run as root in an isolated Linux test environment. Controller recovery matrices
are separate Go tests; this harness supplies actual external lifecycle evidence.
"""
import hashlib
import http.server
import io
import json
import os
import pathlib
import platform
import shutil
import socket
import ssl
import sqlite3
import subprocess
import tarfile
import tempfile
import threading
import textwrap
import time
import urllib.parse
import urllib.request
import uuid

REPO = pathlib.Path(__file__).resolve().parents[2]
ARCH = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
assert os.geteuid() == 0 and ARCH, 'Root Linux systemd test environment required'

def command(args, **kwargs):
    result = subprocess.run(args, capture_output=True, text=True, timeout=kwargs.pop('timeout', 180), **kwargs)
    if result.returncode:
        raise RuntimeError('Isolated command failed: ' + args[0] + '\n' + result.stderr[-6000:])
    return result.stdout.strip()

def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]

def wait_for(fn, label):
    deadline = time.monotonic() + 45
    last = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except Exception as error:
            last = error
        time.sleep(.1)
    raise AssertionError(label + ': ' + str(last))

probe_source = r'''package main
import("context";"encoding/json";"fmt";"os";"time";"github.com/antimage/antimage/internal/app/nodeclient";nodev1 "github.com/antimage/antimage/internal/proto/node/v1";"google.golang.org/grpc")
func main(){ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel();cfg,err:=nodeclient.LoadClientTLS(nodeclient.TLSConfig{ClientCertFile:os.Args[2],ClientKeyFile:os.Args[3],ServerCertFile:os.Args[2]});if err!=nil{panic(err)};client,err:=nodeclient.Dial(ctx,os.Args[1],cfg,grpc.WithBlock());if err!=nil{panic(err)};defer client.Close();response,err:=client.Control().Health(ctx,&nodev1.HealthRequest{});if err!=nil{panic(err)};state:=response.GetRuntime();if !state.GetStarted(){identity:=fmt.Sprintf("lifecycle-runtime-%d",state.GetProcessStartedAtUnixNano());_,err=client.Runtime().StartRuntime(ctx,&nodev1.RuntimeConfigRequest{OperationId:identity,ConfigJson:"{\"inbounds\":[],\"outbounds\":[]}",Fence:&nodev1.DestructiveFence{OperationId:identity,CommandId:identity,ResourceId:"lifecycle-node",ResourceGeneration:time.Now().UnixNano(),LeaseGeneration:1}});if err!=nil{panic(err)};response,err=client.Control().Health(ctx,&nodev1.HealthRequest{});if err!=nil{panic(err)};state=response.GetRuntime()};json.NewEncoder(os.Stdout).Encode(map[string]any{"version":state.GetNodeVersion(),"started_at":state.GetProcessStartedAtUnixNano(),"sampled_at":state.GetSampledAtUnixNano(),"runtime_started":state.GetStarted(),"core_started_at":state.GetCoreProcessStartedAtUnixNano()})}
'''

with tempfile.TemporaryDirectory(prefix='antimage-real-lifecycle-') as temporary:
    root = pathlib.Path(temporary)
    builds = root / 'builds'
    builds.mkdir()
    generated = REPO / '.codex-cache' / ('lifecycle-probe-' + uuid.uuid4().hex)
    generated.mkdir(parents=True)
    units = []
    artifact_server = None
    try:
        (generated / 'main.go').write_text(probe_source)
        probe = builds / 'node-probe'
        command(['go', 'build', '-buildvcs=false', '-o', str(probe), str(generated / 'main.go')], cwd=REPO)
        cli = builds / 'antimage-cli'
        command(['go', 'build', '-buildvcs=false', '-o', str(cli), './cmd/antimage_cli'], cwd=REPO)
        for kind, package, module in [('node', 'antimage_node', 'nodeagent'), ('panel', 'antimage_gateway', 'system')]:
            for version in ['v1.0.0', 'v2.0.0']:
                destination = builds / (kind + '-' + version)
                flags = '-X github.com/antimage/antimage/internal/app/' + module + '.BuildVersion=' + version
                command(['go', 'build', '-buildvcs=false', '-ldflags', flags, '-o', str(destination), './cmd/' + package], cwd=REPO)

        cert, key = root / 'cert.pem', root / 'key.pem'
        command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key), '-out', str(cert), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1'])
        payloads, downloads = {}, {}
        class Artifacts(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                data = payloads[self.path]
                downloads[self.path] = downloads.get(self.path, 0) + 1
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            def log_message(self, *_):
                pass
        artifact_server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Artifacts)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        artifact_server.socket = tls.wrap_socket(artifact_server.socket, server_side=True)
        threading.Thread(target=artifact_server.serve_forever, daemon=True).start()
        helpers = '\n'.join((REPO / 'scripts/antimage' / name).read_text() for name in ['resolved-build.sh', 'managed-backup.sh', 'transactional-install.sh'])
        helper_env = dict(os.environ, SSL_CERT_FILE=str(cert))

        for kind in ['node', 'panel']:
            app = root / kind
            binary_dir = app / 'bin'
            binary_dir.mkdir(parents=True)
            executable = binary_dir / ('antimage-node' if kind == 'node' else 'antimage-server')
            shutil.copy2(builds / (kind + '-v1.0.0'), executable)
            executable.chmod(0o755)
            destinations = [executable]
            if kind == 'panel':
                shutil.copy2(cli, binary_dir / 'antimage-cli')
                destinations.append(binary_dir / 'antimage-cli')
            (app / '.binary-release.json').write_text(json.dumps({'tag': 'v1.0.0', 'install_mode': 'binary'}))
            port = free_port()
            unit = 'antimage-lifecycle-' + uuid.uuid4().hex
            unit_path = pathlib.Path('/run/systemd/system') / (unit + '.service')
            assert not unit_path.exists()
            environment = dict(ANTIMAGE_APP_DIR=str(app), ANTIMAGE_INSTALL_MODE='binary', ANTIMAGE_SERVICE_NAME=unit)
            if kind == 'node':
                worker = app / 'runtime-worker'
                worker.write_text("#!/bin/sh\nif [ \"$1\" = -version ]; then echo \"Xray 1.0.0\"; exit 0; fi\nexec sleep 600\n")
                worker.chmod(0o755)
                environment.update(XRAY_EXECUTABLE_PATH=str(worker), ANTIMAGE_NODE_NAME=unit, ANTIMAGE_NODE_APP_NAME=unit, ANTIMAGE_NODE_APP_DIR=str(app), ANTIMAGE_NODE_DATA_DIR=str(app / 'data'), SERVICE_HOST='127.0.0.1', SERVICE_PORT=str(port), SSL_CERT_FILE=str(cert), SSL_KEY_FILE=str(key))
            else:
                environment.update(SQLALCHEMY_DATABASE_URL='sqlite:///' + str(app / 'panel.sqlite'), ANTIMAGE_GATEWAY_ADDR='127.0.0.1:' + str(port), ANTIMAGE_CERT_BASE=str(app / 'certificates'), ANTIMAGE_EXTERNAL_APPS_BASE=str(app / 'external-apps'))
            unit_path.write_text('[Unit]\nDescription=Isolated AntiMage lifecycle test\n[Service]\nType=exec\nRestart=no\nPrivateNetwork=yes\nRuntimeMaxSec=180\nTimeoutStopSec=10\nKillMode=control-group\nWorkingDirectory=' + str(app) + '\n' + ''.join('Environment="' + name + '=' + value + '"\n' for name, value in environment.items()) + 'ExecStart=' + str(executable) + '\n')
            units.append((unit, unit_path))
            command(['systemctl', 'daemon-reload'])
            command(['systemctl', 'start', unit])
            panel_token = None
            def namespace_command(args, **kwargs):
                pid = int(command(['systemctl', 'show', unit, '-p', 'MainPID', '--value']))
                assert pid > 0
                return command(['nsenter', '--target', str(pid), '--net'] + args, **kwargs)
            def runtime_health(expected):
                if kind == 'node':
                    evidence = json.loads(namespace_command([str(probe), '127.0.0.1:' + str(port), str(cert), str(key), expected], timeout=10))
                    return evidence if evidence['version'] == expected and evidence['started_at'] > 0 and evidence['runtime_started'] and evidence['core_started_at'] > 0 and evidence['sampled_at'] >= evidence['started_at'] else None
                origin = 'http://127.0.0.1:' + str(port)
                if not panel_token:
                    return namespace_command(['curl', '--silent', '--fail', '--max-time', '5', origin + '/__antimage_go/api_healthz']) == 'ok'
                info = json.loads(namespace_command(['curl', '--silent', '--fail', '--max-time', '15', '-H', 'Authorization: Bearer ' + panel_token, origin + '/api/maintenance/info'], timeout=20))
                stats = json.loads(namespace_command(['curl', '--silent', '--fail', '--max-time', '15', '-H', 'Authorization: Bearer ' + panel_token, origin + '/api/system'], timeout=20))
                return info.get('panel', {}).get('running_version') == expected and info['panel'].get('process_started_at_unix_nano', 0) > 0 and stats.get('version') == expected
            wait_for(lambda: runtime_health('v1.0.0'), kind + ' A health')
            if kind == 'panel':
                cli_environment = dict(os.environ, **environment)
                command([str(cli), 'admin', 'create', '--username', 'isolated-lifecycle-owner', '--role', 'full_access', '--password', 'isolated-fixture-password'], env=cli_environment)
                login = json.loads(namespace_command(['curl', '--silent', '--fail', '--max-time', '15', '--data', urllib.parse.urlencode(dict(username='isolated-lifecycle-owner', password='isolated-fixture-password', grant_type='password')), 'http://127.0.0.1:' + str(port) + '/api/admin/token']))
                panel_token = login['access_token']
                wait_for(lambda: runtime_health('v1.0.0'), 'Panel running A version from authenticated API')
            first_pid = int(command(['systemctl', 'show', unit, '-p', 'MainPID', '--value']))
            assert pathlib.Path('/proc/' + str(first_pid) + '/exe').read_bytes() == executable.read_bytes()
            checkpoints = ['artifact_ready', 'backup_verified', 'replacement_ready', 'first_production_commit', 'replacement_committed', 'runtime_restart_required']
            for iteration, checkpoint in enumerate(checkpoints):
                operation = 'lifecycle-' + kind + '-' + checkpoint
                command(['bash', '-c', helpers + '\nmanaged_binary_backup "$@"', 'fixture', 'create', str(app), operation, kind, unit] + list(map(str, destinations)))
                target_binary = (builds / (kind + '-v2.0.0')).read_bytes()
                archive = target_binary
                if kind == 'panel':
                    buffer = io.BytesIO()
                    with tarfile.open(fileobj=buffer, mode='w:gz') as package:
                        for name, data in [('antimage-server', target_binary), ('antimage-cli', cli.read_bytes())]:
                            member = tarfile.TarInfo(name)
                            member.size, member.mode = len(data), 0o755
                            package.addfile(member, io.BytesIO(data))
                    archive = buffer.getvalue()
                payloads['/' + kind] = archive
                target = dict(version='v2.0.0', os='linux', arch=ARCH, size=len(archive), sha256=hashlib.sha256(archive).hexdigest(), download_url='https://localhost:' + str(artifact_server.server_port) + '/' + kind)
                stage = app / ('stage-' + checkpoint)
                stage.mkdir()
                command(['bash', '-c', helpers + '\ndownload_resolved_build "$@"', 'fixture', json.dumps(target), str(stage), ARCH, kind, operation, str(app)], env=helper_env)
                assert downloads['/' + kind] == iteration + 1
                command(['systemctl', 'stop', unit])
                pairs = [str(path) for destination in destinations for path in [stage / destination.name, destination]]
                arguments = [str(app), operation, kind, unit] + pairs
                body = (REPO / 'scripts/antimage/transactional-install.sh').read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
                injection = textwrap.dedent('''
    import os, pathlib, json
    _real_replace = os.replace
    def interrupted_replace(source, destination):
        _real_replace(source, destination)
        path = pathlib.Path(destination)
        if path.name == 'state.json' and json.loads(path.read_text())['phase'] == CHECKPOINT:
            os._exit(92)
        if CHECKPOINT == 'first_production_commit' and path.name == EXECUTABLE:
            os._exit(92)
    os.replace = interrupted_replace
    ''')
                interrupted = subprocess.run(['python3', '-c', 'CHECKPOINT=' + repr(checkpoint) + '\nEXECUTABLE=' + repr(executable.name) + '\n' + injection + body] + arguments, capture_output=True, timeout=30)
                assert interrupted.returncode == 92, (kind, checkpoint, interrupted.stderr.decode()[-2000:])
                state_path = app / '.update-transactions' / operation / 'state.json'
                original_deadline = json.loads(state_path.read_text())['deadline_at_unix_nanos']
                command(['bash', '-c', helpers + '\nmanaged_binary_install "$@"', 'fixture'] + arguments)
                assert json.loads(state_path.read_text())['deadline_at_unix_nanos'] == original_deadline
                identities = [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations]
                command(['bash', '-c', helpers + '\nmanaged_binary_install "$@"', 'fixture'] + arguments)
                assert identities == [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations], 'Lost ACK repeated replacement'
                command(['systemctl', 'start', unit])
                wait_for(lambda: runtime_health('v2.0.0'), kind + ' B health')
                second_pid = int(command(['systemctl', 'show', unit, '-p', 'MainPID', '--value']))
                assert second_pid != first_pid and pathlib.Path('/proc/' + str(second_pid) + '/exe').read_bytes() == target_binary, 'No actual fresh target process'
                command(['systemctl', 'stop', unit])
                restore_checkpoints = ['rollback_prepared', 'backup_verified', 'restore_ready', 'first_production_commit', 'restore_committed', 'rollback_restart_required']
                restore_checkpoint = restore_checkpoints[iteration]
                restore_body = (REPO / 'scripts/antimage/managed-backup.sh').read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
                restore_injection = injection.replace("'state.json'", "'restore-state.json'")
                restore_arguments = ['restore', str(app), operation, kind, unit]
                interrupted = subprocess.run(['python3', '-c', 'CHECKPOINT=' + repr(restore_checkpoint) + '\nEXECUTABLE=' + repr(executable.name) + '\n' + restore_injection + restore_body] + restore_arguments, capture_output=True, timeout=30)
                assert interrupted.returncode == 92, (kind, restore_checkpoint, interrupted.stderr.decode()[-2000:])
                restore_state = app / '.update-backups' / operation / 'restore-state.json'
                restore_deadline = json.loads(restore_state.read_text())['deadline_at_unix_nanos']
                command(['bash', '-c', helpers + '\nmanaged_binary_backup "$@"', 'fixture'] + restore_arguments)
                assert json.loads(restore_state.read_text())['deadline_at_unix_nanos'] == restore_deadline
                restored_identities = [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations]
                command(['bash', '-c', helpers + '\nmanaged_binary_backup "$@"', 'fixture'] + restore_arguments)
                assert restored_identities == [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations], 'Lost restore ACK repeated replacement'
                if kind == 'panel':
                    now_ns = time.time_ns()
                    snapshot = dict(id=operation, target_type='panel', action='rollback', phase='outcome_unknown', running=True, restarting=True, started_at=now_ns//1_000_000_000, started_at_unix_nano=now_ns, phase_started_at_unix_nano=now_ns, updated_at=now_ns//1_000_000_000, desired_version='v1.0.0', requested_version='v1.0.0', previous_version='v2.0.0', target_previous_version='v1.0.0', backup_identity=operation, request_id='real-lifecycle-rollback', requested_by='isolated-lifecycle-owner', logs=[])
                    with sqlite3.connect(app / 'panel.sqlite') as database:
                        database.execute('INSERT INTO operations(id,operation_type,target_type,target_id,requested_by,request_id,state,phase,created_at,updated_at,error,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)', (operation,'rollback','panel','panel','isolated-lifecycle-owner','real-lifecycle-rollback','waiting','outcome_unknown',now_ns//1_000_000_000,now_ns//1_000_000_000,'',json.dumps(dict(snapshot=snapshot))))
                        database.execute('INSERT INTO operation_locks(target_type,target_id,operation_id) VALUES (?,?,?)', ('panel','panel',operation))
                command(['systemctl', 'start', unit])
                wait_for(lambda: runtime_health('v1.0.0'), kind + ' rollback A health')
                if kind == 'panel':
                    def recovered_panel():
                        observed = json.loads(namespace_command(['curl', '--silent', '--fail', '--max-time', '15', '-H', 'Authorization: Bearer ' + panel_token, 'http://127.0.0.1:' + str(port) + '/api/maintenance/status'], timeout=20))
                        return observed.get('id') == operation and observed.get('phase') == 'completed' and observed.get('running_version') == 'v1.0.0' and not observed.get('running')
                    wait_for(recovered_panel, 'Panel controller startup consumed actual restored files and persisted operation')
                    with sqlite3.connect(app / 'panel.sqlite') as database:
                        assert database.execute('SELECT state,request_id FROM operations WHERE id=?', (operation,)).fetchone() == ('completed','real-lifecycle-rollback')
                        assert database.execute('SELECT COUNT(*) FROM operation_locks WHERE operation_id=?', (operation,)).fetchone()[0] == 0
                    assert restored_identities == [(destination.stat().st_ino, destination.stat().st_mtime_ns) for destination in destinations], 'Startup controller replayed completed restore'
                third_pid = int(command(['systemctl', 'show', unit, '-p', 'MainPID', '--value']))
                assert third_pid not in [first_pid, second_pid] and pathlib.Path('/proc/' + str(third_pid) + '/exe').read_bytes() == (builds / (kind + '-v1.0.0')).read_bytes(), 'Rollback did not activate original executable'
                assert downloads['/' + kind] == iteration + 1, 'Restore downloaded again'
                print(kind + ' [' + checkpoint + ']: real A → verified TLS B → backup → stop → install → fresh B health → restore → fresh A health passed', flush=True)
    finally:
        for unit, path in units:
            subprocess.run(['systemctl', 'stop', unit], capture_output=True, timeout=20)
            path.unlink(missing_ok=True)
        if units:
            subprocess.run(['systemctl', 'daemon-reload'], capture_output=True, timeout=20)
        if artifact_server:
            artifact_server.shutdown()
            artifact_server.server_close()
        shutil.rmtree(generated)
