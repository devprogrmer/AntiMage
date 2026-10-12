#!/usr/bin/env bash

managed_binary_backup() {
    python3 - "$@" <<'PY'
import hashlib, json, os, pathlib, platform, re, shutil, stat, sys, tempfile, time, fcntl, signal
action, application, identity, target_type, target_id = sys.argv[1:6]
application = pathlib.Path(application).resolve()
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}', identity):
    raise SystemExit('Invalid backup identity')
root = application / '.update-backups'
if root.is_symlink(): raise SystemExit('Backup root cannot be a symlink')
root.mkdir(mode=0o700, exist_ok=True)
architecture = {'x86_64':'amd64','i386':'386','i686':'386','aarch64':'arm64','armv7l':'arm','armv6l':'arm','ppc64le':'ppc64le','s390x':'s390x','riscv64':'riscv64'}.get(platform.machine(), platform.machine())
backup = root / identity
def sync_file(path):
    with path.open('rb') as stream: os.fsync(stream.fileno())
def sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)
def checksum(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while True:
            chunk = stream.read(1048576)
            if not chunk: break
            digest.update(chunk)
    return digest.hexdigest()
def contained(path):
    path = pathlib.Path(path)
    if path.is_symlink() or application not in path.resolve().parents:
        raise ValueError('Backup destination escapes application directory')
    return path
if action == 'create':
    if backup.exists(): raise SystemExit('Backup identity already exists')
    staged = pathlib.Path(tempfile.mkdtemp(prefix='.preparing-', dir=root))
    try:
        metadata = application / '.binary-release.json'
        build = json.loads(metadata.read_text()) if metadata.exists() else {}
        manifest = {'identity': identity, 'target_type': target_type, 'target_id': target_id,
                    'version': build.get('tag', ''), 'commit': build.get('commit', ''),
                    'created_at': int(time.time()), 'source_operation_id': identity,
                    'os': platform.system().lower(), 'architecture': architecture,
                    'schema_version': build.get('schema_version'), 'files': []}
        for number, source in enumerate(sys.argv[6:]):
            source = contained(source)
            if not source.exists(): continue
            attributes = source.stat()
            if not stat.S_ISREG(attributes.st_mode): raise ValueError('Backup requires regular files')
            copy = staged / str(number)
            shutil.copy2(source, copy)
            os.chown(copy, attributes.st_uid, attributes.st_gid)
            sync_file(copy)
            manifest['files'].append({'reference': str(number), 'destination': str(source),
                                      'sha256': checksum(copy), 'size': copy.stat().st_size,
                                      'mode': stat.S_IMODE(attributes.st_mode),
                                      'uid': attributes.st_uid, 'gid': attributes.st_gid})
        if not manifest['files']: raise ValueError('No recoverable files exist')
        path = staged / 'manifest.json'
        path.write_text(json.dumps(manifest))
        sync_file(path)
        sync_dir(staged)
        os.rename(staged, backup)
        sync_dir(root)
    except BaseException:
        shutil.rmtree(staged)
        raise
elif action in ('verify', 'restore'):
    if backup.is_symlink(): raise ValueError('Backup directory cannot be a symlink')
    manifest = json.loads((backup / 'manifest.json').read_text())
    if manifest['identity'] != identity or manifest['target_type'] != target_type or str(manifest['target_id']) != target_id:
        raise ValueError('Backup identity does not match target')
    if manifest.get('os') != platform.system().lower() or manifest.get('architecture') != architecture:
        raise ValueError('Backup platform metadata is incompatible')
    lock_path = backup / '.restore.lock'
    if lock_path.is_symlink(): raise ValueError('Restore lock cannot be a symlink')
    lock = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError('Restore deadline exceeded')))
    signal.alarm(180)
    fcntl.flock(lock, fcntl.LOCK_EX)
    records = []
    destinations = set()
    references = set()
    for record in manifest['files']:
        if not str(record['reference']).isdigit(): raise ValueError('Invalid backup reference')
        source = backup / record['reference']
        destination = contained(record['destination'])
        if str(destination) in destinations or str(record['reference']) in references:
            raise ValueError('Duplicate backup destination or reference')
        destinations.add(str(destination)); references.add(str(record['reference']))
        if source.is_symlink() or source.stat().st_size != record['size'] or checksum(source) != record['sha256']:
            raise ValueError('Backup integrity verification failed')
        if record['mode'] & 0o111:
            with source.open('rb') as stream: header = stream.read(20)
            expected = {'amd64':62,'386':3,'arm64':183,'arm':40,'ppc64le':21,'s390x':22,'riscv64':243}.get(architecture)
            if len(header) < 20 or header[:4] != b'\x7fELF' or header[5] not in (1,2) or int.from_bytes(header[18:20], 'little' if header[5] == 1 else 'big') != expected:
                raise ValueError('Backup executable architecture is incompatible')
        records.append((record, source, destination))
    if action == 'restore':
        transaction_path = backup / 'restore-state.json'
        if transaction_path.is_symlink(): raise ValueError('Restore transaction cannot be a symlink')
        fingerprint = checksum(backup / 'manifest.json')
        transaction = json.loads(transaction_path.read_text()) if transaction_path.exists() else {'identity':identity,'manifest_sha256':fingerprint,'phase':'rollback_prepared','deadline_at_unix_nanos':time.time_ns()+180_000_000_000}
        if transaction.get('identity') != identity or transaction.get('manifest_sha256') != fingerprint:
            raise ValueError('Restore transaction identity changed')
        phases = ['rollback_prepared','backup_verified','restore_ready','restore_committed','rollback_restart_required']
        if transaction.get('phase') not in phases: raise ValueError('Invalid restore phase')
        if phases.index(transaction['phase']) < phases.index('restore_committed'):
            remaining = int(transaction.get('deadline_at_unix_nanos',0))-time.time_ns()
            if remaining <= 0: raise TimeoutError('Persisted restore deadline expired')
            signal.alarm(max(1,(remaining+999_999_999)//1_000_000_000))
        def persist_phase(phase):
            if phases.index(transaction['phase']) > phases.index(phase): return
            transaction['phase'] = phase
            descriptor, name = tempfile.mkstemp(prefix='.restore-state-', dir=backup)
            try:
                with os.fdopen(descriptor,'w') as stream:
                    json.dump(transaction,stream); stream.flush(); os.fsync(stream.fileno())
                os.replace(name,transaction_path); sync_dir(backup)
            finally:
                pathlib.Path(name).unlink(missing_ok=True)
        def matches(record,destination):
            if not destination.is_file() or destination.is_symlink(): return False
            attributes=destination.stat()
            return attributes.st_size==record['size'] and checksum(destination)==record['sha256'] and stat.S_IMODE(attributes.st_mode)==record['mode'] and attributes.st_uid==record['uid'] and attributes.st_gid==record['gid']
        if phases.index(transaction['phase']) >= phases.index('restore_committed'):
            if not all(matches(record,destination) for record,_,destination in records):
                raise ValueError('Committed restore production identity changed; manual recovery required')
        else:
            persist_phase('rollback_prepared')
            persist_phase('backup_verified')
            prepared=[]
            for record,source,destination in records:
                if matches(record,destination): continue
                staged=destination.parent / ('.restore-'+identity+'-'+record['reference'])
                if staged.is_symlink(): raise ValueError('Restore staging cannot be a symlink')
                if not staged.exists() or not matches(record,staged):
                    if staged.exists(): staged.unlink()
                    descriptor=os.open(staged,os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW,0o600)
                    with os.fdopen(descriptor,'wb') as stream, source.open('rb') as original:
                        shutil.copyfileobj(original,stream); stream.flush(); os.fsync(stream.fileno())
                        os.fchmod(stream.fileno(),record['mode']); os.fchown(stream.fileno(),record['uid'],record['gid']); os.fsync(stream.fileno())
                    sync_dir(destination.parent)
                prepared.append((record,staged,destination))
            persist_phase('restore_ready')
            for record,staged,destination in prepared:
                if not matches(record,staged): raise ValueError('Restore staged identity changed')
                os.replace(staged,destination); sync_dir(destination.parent)
            if not all(matches(record,destination) for record,_,destination in records): raise ValueError('Restored production identity verification failed')
            persist_phase('restore_committed')
        persist_phase('rollback_restart_required')
    signal.alarm(0)
    os.close(lock)
else:
    raise ValueError('Unsupported backup action')
print(json.dumps(manifest), flush=True)
PY
}
