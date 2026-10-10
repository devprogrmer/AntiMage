#!/usr/bin/env bash

# This function is embedded in the installed panel and node scripts. The
# embedding is checked by the integration harness so installed scripts do not
# depend on downloading another mutable installer helper.
download_resolved_build() {
    python3 - "$1" "$2" "$3" "$4" "${5:-}" "${6:-}" <<'PY'
import fcntl, hashlib, json, os, pathlib, re, shutil, signal, struct, sys, tarfile, tempfile, time, urllib.parse, urllib.request
target = json.loads(sys.argv[1])
directory = pathlib.Path(sys.argv[2])
arch, kind = sys.argv[3:5]
operation, app = sys.argv[5:7]
if target.get('os') != 'linux' or target.get('arch') != arch:
    raise SystemExit('Resolved artifact platform mismatch')
size = target.get('size')
digest = target.get('sha256', '').lower()
if not isinstance(size, int) or size <= 0 or size > 2147483648:
    raise SystemExit('Invalid resolved artifact size')
if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
    raise SystemExit('Invalid resolved artifact checksum')
url = target.get('download_url', '')
parsed = urllib.parse.urlparse(url)
if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.fragment:
    raise SystemExit('Resolved artifact requires HTTPS')
class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlparse(newurl).scheme != 'https':
            raise ValueError('Artifact redirect must remain HTTPS')
        return super().redirect_request(req, fp, code, msg, headers, newurl)
def expire(*_): raise TimeoutError('Absolute artifact phase deadline expired')
signal.signal(signal.SIGALRM, expire)
signal.alarm(180)
def sync_directory(path):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)
def atomic_json(path,value):
    with tempfile.NamedTemporaryFile(mode='w',dir=path.parent,prefix='.artifact-state-',delete=False) as output:
        name=output.name
        json.dump(value,output);output.flush();os.fsync(output.fileno())
    try: os.replace(name,path);sync_directory(path.parent)
    finally:
        if os.path.exists(name): os.unlink(name)
cache=directory
if operation:
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}',operation) or not app:
        raise ValueError('Operation-owned artifact identity is required')
    root=pathlib.Path(app).resolve()/'.maintenance-artifacts'
    if root.is_symlink(): raise ValueError('Artifact root cannot be a symlink')
    root.mkdir(mode=0o700,exist_ok=True)
    cache=root/operation
    if cache.is_symlink(): raise ValueError('Artifact operation directory cannot be a symlink')
    cache.mkdir(mode=0o700,exist_ok=True)
lock=os.open(cache/'.artifact.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
fcntl.flock(lock,fcntl.LOCK_EX)
archive=cache/'resolved-artifact'
partial=cache/'resolved-artifact.partial'
record=cache/'target.json'
for path in (archive,partial,record):
    if path.is_symlink(): raise ValueError('Artifact state cannot be a symlink')
identity={'operation_id':operation,'target':target,'kind':kind}
if record.exists():
    state=json.loads(record.read_text())
    if any(state.get(key)!=value for key,value in identity.items()):
        raise ValueError('Persisted immutable artifact target differs')
else:
    state=dict(identity,download_deadline_at=time.time()+180)
    atomic_json(record,state)
def valid_artifact(path):
    if not path.is_file() or path.stat().st_size!=size: return False
    hasher=hashlib.sha256()
    with path.open('rb') as source:
        while True:
            chunk=source.read(1048576)
            if not chunk: break
            hasher.update(chunk)
    return hasher.hexdigest()==digest
if archive.exists():
    # A completed marker never overrides actual bytes. Reject corruption rather
    # than silently downloading and installing a replacement.
    if not valid_artifact(archive): raise ValueError('Cached artifact size or checksum mismatch')
else:
    if partial.exists():
        if valid_artifact(partial):
            os.replace(partial,archive);sync_directory(cache)
        else:
            # Only this verified operation directory and exact owned file.
            partial.unlink();sync_directory(cache)
    if not archive.exists():
        remaining=state['download_deadline_at']-time.time()
        if remaining<=0: raise TimeoutError('Persisted artifact download deadline expired')
        signal.setitimer(signal.ITIMER_REAL,remaining)
        opener=urllib.request.build_opener(HTTPSRedirect())
        count=0
        with opener.open(url,timeout=min(120,remaining)) as response, partial.open('xb') as output:
            if response.status!=200: raise ValueError('Artifact download was not successful')
            while True:
                chunk=response.read(min(1048576,size+1-count))
                if not chunk: break
                count+=len(chunk)
                if count>size: raise ValueError('Artifact exceeds resolved size')
                output.write(chunk)
            output.flush();os.fsync(output.fileno())
        if not valid_artifact(partial): raise ValueError('Resolved artifact size or checksum mismatch')
        os.replace(partial,archive);sync_directory(cache)
state['artifact_ready']=True
atomic_json(record,state)
machines = {'amd64': 62, '386': 3, 'arm64': 183, 'armv5': 40, 'armv6': 40,
            'armv7': 40, 'arm': 40, 'ppc64le': 21, 's390x': 22, 'riscv64': 243}
def check_elf(path):
    with path.open('rb') as stream: header = stream.read(20)
    if len(header) != 20 or header[:4] != b'\x7fELF' or header[5] not in (1, 2):
        raise ValueError('Artifact contains no ELF executable')
    endian = '<' if header[5] == 1 else '>'
    if struct.unpack(endian + 'H', header[18:20])[0] != machines.get(arch):
        raise ValueError('ELF architecture mismatch')
if kind == 'node':
    check_elf(archive)
    with archive.open('rb') as source, (directory/'antimage-node').open('xb') as output:
        shutil.copyfileobj(source,output);output.flush();os.fsync(output.fileno())
elif kind == 'panel':
    required = {'antimage-server', 'antimage-cli'}
    seen = set()
    with tarfile.open(archive, 'r:gz') as package:
        for member in package:
            name = member.name.removeprefix('./')
            if name not in required: continue
            if name in seen or not member.isfile() or member.size <= 0 or member.size > 1073741824:
                raise ValueError('Invalid or duplicate binary archive member')
            seen.add(name)
            path = directory / name
            with package.extractfile(member) as source, path.open('xb') as output:
                while True:
                    chunk = source.read(1048576)
                    if not chunk: break
                    output.write(chunk)
                output.flush()
                os.fsync(output.fileno())
            check_elf(path)
    if seen != required: raise ValueError('Resolved panel archive is incomplete')
else:
    raise ValueError('Unsupported target type')
print('Artifact verified: ' + target['version'], flush=True)
PY
}
