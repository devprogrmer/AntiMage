#!/usr/bin/env bash

managed_binary_install() {
    managed_binary_backup verify "$1" "$2" "$3" "$4" >/dev/null || return 1
    python3 - "$@" <<'PY'
import fcntl, hashlib, json, os, pathlib, platform, re, shutil, signal, stat, sys, tempfile, time
app, identity, kind, target = sys.argv[1:5]
app = pathlib.Path(app).resolve()
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}', identity): raise ValueError('Invalid transaction identity')
arguments = sys.argv[5:]
if not arguments or len(arguments) % 2: raise ValueError('Source/destination pairs required')
root = app / '.update-transactions'
if root.is_symlink(): raise ValueError('Transaction root cannot be a symlink')
root.mkdir(mode=0o700, exist_ok=True)
directory = root / identity
if directory.is_symlink(): raise ValueError('Transaction directory cannot be a symlink')
directory.mkdir(mode=0o700, exist_ok=True)
def sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)
def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        while True:
            chunk = stream.read(1048576)
            if not chunk: break
            value.update(chunk)
    return value.hexdigest()
lock = os.open(directory / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError('Install deadline exceeded')))
signal.alarm(180)
fcntl.flock(lock, fcntl.LOCK_EX)
records = []
seen = set()
for index in range(0, len(arguments), 2):
    source, destination = map(pathlib.Path, arguments[index:index + 2])
    if destination.is_symlink() or app not in destination.resolve().parents: raise ValueError('Installation escapes application directory')
    if str(destination) in seen: raise ValueError('Duplicate production destination')
    seen.add(str(destination))
    if source.is_symlink() or not source.is_file(): raise ValueError('Installation requires regular artifacts')
    with source.open('rb') as stream:
        header = stream.read(20)
        expected = {'x86_64':62,'i386':3,'i686':3,'aarch64':183,'armv7l':40,'armv6l':40,'ppc64le':21,'s390x':22,'riscv64':243}.get(platform.machine())
        if len(header)<20 or header[:4]!=b'\x7fELF' or header[5] not in (1,2) or int.from_bytes(header[18:20], 'little' if header[5]==1 else 'big')!=expected: raise ValueError('Executable artifact architecture is incompatible')
    attributes = destination.stat() if destination.exists() else source.stat()
    records.append({'source': str(source), 'destination': str(destination), 'sha256': digest(source), 'size': source.stat().st_size, 'mode': 0o755, 'uid': attributes.st_uid, 'gid': attributes.st_gid, 'reference': str(index // 2)})
state_path = directory / 'state.json'
if state_path.is_symlink(): raise ValueError('Installation state cannot be a symlink')
phases = ['artifact_ready', 'backup_verified', 'replacement_ready', 'replacement_committed', 'runtime_restart_required']
identity_record = {'operation_id': identity, 'target_type': kind, 'target_id': target, 'files': [{key:value for key,value in record.items() if key != 'source'} for record in records]}
state = json.loads(state_path.read_text()) if state_path.exists() else {**identity_record, 'phase': 'artifact_ready', 'deadline_at_unix_nanos':time.time_ns()+180_000_000_000}
if any(state.get(key) != value for key,value in identity_record.items()): raise ValueError('Immutable installation target changed')
if state.get('phase') not in phases: raise ValueError('Invalid installation phase')
if phases.index(state['phase']) < phases.index('replacement_committed'):
    remaining = int(state.get('deadline_at_unix_nanos',0))-time.time_ns()
    if remaining <= 0: raise TimeoutError('Persisted installation deadline expired')
    signal.alarm(max(1,(remaining+999_999_999)//1_000_000_000))
def persist(phase):
    if phases.index(state['phase']) > phases.index(phase): return
    state['phase'] = phase
    descriptor, temporary = tempfile.mkstemp(prefix='.state-', dir=directory)
    try:
        with os.fdopen(descriptor,'w') as stream:
            json.dump(state,stream); stream.flush(); os.fsync(stream.fileno())
        os.replace(temporary,state_path); sync_dir(directory)
    finally: pathlib.Path(temporary).unlink(missing_ok=True)
def matches(record, path):
    if not path.is_file() or path.is_symlink(): return False
    attributes = path.stat()
    return attributes.st_size == record['size'] and digest(path) == record['sha256'] and stat.S_IMODE(attributes.st_mode) == record['mode'] and attributes.st_uid == record['uid'] and attributes.st_gid == record['gid']
if phases.index(state['phase']) >= phases.index('replacement_committed'):
    if not all(matches(record,pathlib.Path(record['destination'])) for record in records): raise ValueError('Committed production identity changed; manual recovery required')
else:
    persist('artifact_ready'); persist('backup_verified')
    staged = []
    for record in records:
        destination = pathlib.Path(record['destination'])
        if matches(record,destination): continue
        temporary = destination.parent / ('.install-' + identity + '-' + record['reference'])
        if temporary.is_symlink(): raise ValueError('Staged executable cannot be a symlink')
        if not matches(record,temporary):
            temporary.unlink(missing_ok=True)
            descriptor = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            with os.fdopen(descriptor,'wb') as stream, pathlib.Path(record['source']).open('rb') as original:
                shutil.copyfileobj(original,stream); stream.flush(); os.fchmod(stream.fileno(),record['mode']); os.fchown(stream.fileno(),record['uid'],record['gid']); os.fsync(stream.fileno())
            sync_dir(destination.parent)
        staged.append((record,temporary,destination))
    persist('replacement_ready')
    for record,temporary,destination in staged:
        if not matches(record,temporary): raise ValueError('Staged executable identity changed')
        os.replace(temporary,destination); sync_dir(destination.parent)
    if not all(matches(record,pathlib.Path(record['destination'])) for record in records): raise ValueError('Production installation identity verification failed')
    persist('replacement_committed')
persist('runtime_restart_required')
signal.alarm(0)
os.close(lock)
print(json.dumps(state),flush=True)
PY
}
