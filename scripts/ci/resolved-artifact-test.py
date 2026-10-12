#!/usr/bin/env python3
"""Run the embedded production downloader against real TLS artifact responses."""
import hashlib
import http.server
import io
import json
import pathlib
import ssl
import signal
import subprocess
import tarfile
import tempfile
import threading

ROOT = pathlib.Path(__file__).resolve().parents[2]
helper = (ROOT / 'scripts/antimage/resolved-build.sh').read_text()
function = helper[helper.index('download_resolved_build()'):].strip()
for name in ('antimage-binary.sh', 'antimage-node-binary.sh'):
    script = (ROOT / 'scripts/antimage' / name).read_text()
    start = script.index('download_resolved_build()')
    end = script.index('\n}\n', start) + 2
    assert script[start:end].strip() == function, 'Installed helper diverged: ' + name

payloads = {}
downloads = {}
paused_downloads = {}
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        downloads[self.path] = downloads.get(self.path, 0) + 1
        data = payloads.get(self.path)
        self.send_response(200 if data is not None else 404)
        self.end_headers()
        if data is not None:
            pause = paused_downloads.get(self.path)
            try:
                if pause is not None:
                    self.wfile.write(data[:1048576])
                    self.wfile.flush()
                    pause.wait(timeout=15)
                    self.wfile.write(data[1048576:])
                else:
                    self.wfile.write(data)
            except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                pass  # The crash harness deliberately kills the downloader.
    def log_message(self, *_): pass

with tempfile.TemporaryDirectory(prefix='antimage-artifacts-') as temporary:
    temporary = pathlib.Path(temporary)
    cert, key = temporary / 'cert.pem', temporary / 'key.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '1',
                    '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'],
                   check=True, capture_output=True)
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    binary = pathlib.Path('/bin/true').read_bytes()
    payloads['/node'] = binary
    archive = io.BytesIO()
    with tarfile.open(fileobj=archive, mode='w:gz') as package:
        for name in ('antimage-server', 'antimage-cli'):
            member = tarfile.TarInfo(name)
            member.size = len(binary)
            package.addfile(member, io.BytesIO(binary))
    payloads['/panel'] = archive.getvalue()
    import os
    environment = dict(os.environ, SSL_CERT_FILE=str(cert))
    for kind in ('node', 'panel'):
        data = payloads['/' + kind]
        target = {'version': 'dev-abcdef1', 'channel': 'dev', 'os': 'linux', 'arch': 'amd64',
                  'download_url': f'https://localhost:{server.server_port}/{kind}',
                  'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
        def run(case, item, succeeds):
            destination = temporary / (kind + '-' + case)
            destination.mkdir()
            result = subprocess.run(['bash', '-c', function + '\ndownload_resolved_build "$@"',
                                     'test', json.dumps(item), str(destination), 'amd64', kind],
                                    env=environment, capture_output=True, text=True, timeout=20)
            assert (result.returncode == 0) == succeeds, (case, result.stdout, result.stderr)
            if succeeds:
                names = ['antimage-node'] if kind == 'node' else ['antimage-server', 'antimage-cli']
                for name in names: assert (destination / name).read_bytes() == binary
            print(kind + ': ' + case + ' passed')
        run('exact', target, True)
        run('checksum', dict(target, sha256='0' * 64), False)
        run('truncated', dict(target, size=len(data) + 1), False)
        run('zero', dict(target, size=0), False)
        run('metadata-arch', dict(target, arch='arm64'), False)
        run('missing', dict(target, download_url=f'https://localhost:{server.server_port}/missing'), False)
        payloads['/bad-elf'] = b'not an executable'
        bad = dict(target, download_url=f'https://localhost:{server.server_port}/bad-elf',
                   sha256=hashlib.sha256(payloads['/bad-elf']).hexdigest(), size=len(payloads['/bad-elf']))
        run('bad-elf', bad, False)

        # Each invocation starts a fresh production downloader process. Cache
        # identity and bytes survive independently of the temporary extraction.
        app = temporary / (kind + '-app')
        app.mkdir()
        def recover(case, operation, item=target, succeeds=True):
            destination = temporary / (kind + '-recovery-' + case)
            destination.mkdir()
            result = subprocess.run(['bash', '-c', function + '\ndownload_resolved_build "$@"',
                                     'test', json.dumps(item), str(destination), 'amd64', kind,
                                     operation, str(app)], env=environment, capture_output=True,
                                    text=True, timeout=20)
            assert (result.returncode == 0) == succeeds, (case, result.stdout, result.stderr)
            if succeeds:
                for name in (['antimage-node'] if kind == 'node' else ['antimage-server', 'antimage-cli']):
                    assert (destination / name).read_bytes() == binary
            else:
                assert not any((destination / name).exists() for name in ['antimage-node', 'antimage-server', 'antimage-cli'])
            print(kind + ': recovery ' + case + ' passed')
        before = downloads['/' + kind]
        recover('first', 'cache-full')
        recover('full-reuse', 'cache-full')
        assert downloads['/' + kind] == before + 1, 'Complete cache was downloaded twice'
        full = app / '.maintenance-artifacts/cache-full'
        # Simulate a crash before the atomic rename of an already fsynced file.
        (full / 'resolved-artifact').rename(full / 'resolved-artifact.partial')
        recover('complete-partial-promoted', 'cache-full')
        assert downloads['/' + kind] == before + 1
        (full / 'resolved-artifact').write_bytes(b'corrupted completed artifact')
        recover('corrupt-completed-rejected', 'cache-full', succeeds=False)
        assert downloads['/' + kind] == before + 1
        recover('immutable-target-rejected', 'cache-full', dict(target, version='dev-abcdef2'), False)
        partial = app / '.maintenance-artifacts/cache-partial'
        partial.mkdir()
        import time
        (partial / 'target.json').write_text(json.dumps({'operation_id': 'cache-partial', 'kind': kind,
                                                       'target': target, 'download_deadline_at': time.time() + 180}))
        (partial / 'resolved-artifact.partial').write_bytes(data[:len(data)//2])
        unrelated = partial / 'unrelated-file'
        unrelated.write_text('preserve')
        sibling = partial.parent / 'other-operation'
        sibling.mkdir()
        (sibling / 'resolved-artifact.partial').write_text('preserve sibling')
        recover('partial-redownload', 'cache-partial')
        assert downloads['/' + kind] == before + 2
        assert unrelated.read_text() == 'preserve'
        assert (sibling / 'resolved-artifact.partial').read_text() == 'preserve sibling'
        expired = app / '.maintenance-artifacts/cache-expired'
        expired.mkdir()
        (expired / 'target.json').write_text(json.dumps({'operation_id': 'cache-expired', 'kind': kind,
                                                       'target': target, 'download_deadline_at': time.time() - 1}))
        recover('expired-deadline', 'cache-expired', succeeds=False)
        assert downloads['/' + kind] == before + 2
    # Kill the actual production downloader after it writes a partial artifact.
    # Its next process must recover the owned cache and fetch the exact target.
    import time
    crash_data = binary + os.urandom(3 * 1048576)
    payloads['/crash-node'] = crash_data
    paused_downloads['/crash-node'] = threading.Event()
    crash_app = temporary / 'crash-app'
    crash_app.mkdir()
    crash_output = temporary / 'crash-first'
    crash_output.mkdir()
    crash_target = dict(target, download_url=f'https://localhost:{server.server_port}/crash-node',
                        size=len(crash_data), sha256=hashlib.sha256(crash_data).hexdigest())
    worker = subprocess.Popen(['bash', '-c', function + '\ndownload_resolved_build "$@"',
                               'test', json.dumps(crash_target), str(crash_output), 'amd64',
                               'node', 'crash-download', str(crash_app)], env=environment,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    partial = crash_app / '.maintenance-artifacts/crash-download/resolved-artifact.partial'
    deadline = time.monotonic() + 10
    try:
        while time.monotonic() < deadline and (not partial.exists() or partial.stat().st_size < 1048576):
            assert worker.poll() is None, worker.communicate()
            time.sleep(.01)
        assert partial.exists() and partial.stat().st_size >= 1048576, 'No real partial download observed'
        os.killpg(worker.pid, signal.SIGKILL)
        worker.communicate(timeout=5)
    finally:
        paused_downloads.pop('/crash-node').set()
        if worker.poll() is None:
            os.killpg(worker.pid, signal.SIGKILL)
            worker.communicate(timeout=5)
    recovered = temporary / 'crash-recovered'
    recovered.mkdir()
    result = subprocess.run(['bash', '-c', function + '\ndownload_resolved_build "$@"',
                             'test', json.dumps(crash_target), str(recovered), 'amd64',
                             'node', 'crash-download', str(crash_app)], env=environment,
                            capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, (result.stdout, result.stderr)
    assert (recovered / 'antimage-node').read_bytes() == crash_data
    assert downloads['/crash-node'] == 2
    print('node: actual downloader termination and partial recovery passed')
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
print('Production resolved artifact verification: 14 validation + 14 recovery invocations + downloader crash recovery passed')
