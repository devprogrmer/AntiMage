#!/usr/bin/env bash

# Embedded in the installed CLI. Accepted generations and queued job checks use
# the same persistent file and execution lock, including after agent restart.
node_command_fence() {
    python3 - "$1" "$APP_DIR" "$FENCE_OPERATION_ID" "$LEASE_GENERATION" "$COMMAND_ID" "$RESOURCE_GENERATION" "$RESOURCE_ID" <<'PY'
import fcntl,json,os,pathlib,re,signal,sys,tempfile
mode,app,operation,generation,command,resource_generation,resource_id=sys.argv[1:]
pattern=r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}'
if not re.fullmatch(pattern,operation) or not re.fullmatch(pattern,command) or not re.fullmatch(pattern,resource_id):
    raise SystemExit('Invalid command fencing identity')
if not generation.isdigit() or not 0<int(generation)<2**63:
    raise SystemExit('Positive command fencing generation is required')
generation=int(generation)
if not resource_generation.isdigit() or not 0<int(resource_generation)<2**63:
    raise SystemExit('Positive resource generation is required')
resource_generation=int(resource_generation)
root=pathlib.Path(app).resolve()/'.maintenance-fences'
if root.is_symlink(): raise SystemExit('Fence root cannot be a symlink')
root.mkdir(mode=0o700,exist_ok=True)
lock=root/'.generation.lock'
fd=os.open(lock,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
def expired(*_): raise TimeoutError('Node execution lock deadline expired')
signal.signal(signal.SIGALRM,expired)
signal.alarm(15)
if mode.endswith('-held'):
    execution=(root/'.execution.lock').stat()
    if os.fstat(9).st_ino!=execution.st_ino or os.fstat(9).st_dev!=execution.st_dev:
        raise SystemExit('Inherited execution lock is unavailable')
    fcntl.flock(9,fcntl.LOCK_EX|fcntl.LOCK_NB)
if mode.endswith('-boundary-held'):
    if os.fstat(8).st_ino!=os.fstat(fd).st_ino or os.fstat(8).st_dev!=os.fstat(fd).st_dev:
        raise SystemExit('Destructive boundary lock is unavailable')
    fcntl.flock(8,fcntl.LOCK_EX|fcntl.LOCK_NB)
else:
    fcntl.flock(fd,fcntl.LOCK_EX)
def persist(path,state):
    temporary=None
    try:
        with tempfile.NamedTemporaryFile(mode='w',dir=root,prefix='.fence-',delete=False) as output:
            temporary=output.name
            json.dump(state,output)
            output.flush();os.fsync(output.fileno())
        os.replace(temporary,path)
        parent=os.open(root,os.O_RDONLY|os.O_DIRECTORY)
        try: os.fsync(parent)
        finally: os.close(parent)
    finally:
        if temporary and os.path.exists(temporary): os.unlink(temporary)
resource_record=root/'resource.json'
if resource_record.is_symlink(): raise SystemExit('Resource record cannot be a symlink')
resource=json.loads(resource_record.read_text()) if resource_record.exists() else {'generation':0,'owner_operation_id':'','resource_id':resource_id}
if resource['resource_id']!=resource_id: raise SystemExit('Resource identity mismatch')
if resource_generation<resource['generation']: raise SystemExit('Stale resource generation rejected')
if mode.startswith('accept'):
    if resource_generation==resource['generation'] and resource['owner_operation_id']!=operation:
        raise SystemExit('Resource generation belongs to another operation')
    if resource_generation>resource['generation']:
        resource={'generation':resource_generation,'owner_operation_id':operation,'resource_id':resource_id}
        persist(resource_record,resource)
elif resource_generation!=resource['generation'] or resource['owner_operation_id']!=operation:
    raise SystemExit('Job has been superseded by another resource owner')
record=root/('operation-'+operation+'.json')
if record.is_symlink(): raise SystemExit('Fence record cannot be a symlink')
state=json.loads(record.read_text()) if record.exists() else {'generation':0,'commands':{}}
if generation<state['generation']: raise SystemExit('Stale command generation rejected')
if mode.startswith('accept'):
    if generation>state['generation']:
        state={'generation':generation,'commands':{key:value for key,value in state['commands'].items() if value in ('completed','started')},'command_resource_generations':state.get('command_resource_generations',{})}
    if state['commands'].get(command)=='started':
        raise SystemExit('Command outcome unknown; reconcile target state before replay')
    completed=state['commands'].get(command)=='completed'
    if not completed: state['commands'][command]='accepted'
elif mode.startswith('complete'):
    if generation!=state['generation'] or state['commands'].get(command) not in ('accepted','started'):
        raise SystemExit('Completion belongs to an obsolete command')
    completed=False
    state['commands'][command]='completed'
elif mode.startswith('start'):
    if generation!=state['generation'] or state['commands'].get(command)!='accepted':
        raise SystemExit('Command already dispatched; reconcile before replay')
    completed=False
    state['commands'][command]='started'
    state.setdefault('command_resource_generations',{})[command]=resource_generation
elif mode.startswith('check'):
    if generation!=state['generation'] or state['commands'].get(command) not in ('accepted','started'):
        raise SystemExit('Queued command no longer owns accepted generation')
else: raise SystemExit('Unknown fencing mode')
if mode.startswith(('accept','complete','start')):
    persist(record,state)
    if completed: raise SystemExit('Command already executed; reconcile target state')
print('Command generation verified',flush=True)
PY
}

finish_node_command() {
    [ -n "${LEASE_GENERATION:-}" ] || return 0
    node_command_fence complete-held
}

lock_node_command() {
    # Installed destructive callers must first establish persistent ownership.
    # Bootstrap callers without a managed operation never enter this helper.
    [ -n "${LEASE_GENERATION:-}" ] || return 0
    [ ! -L "$APP_DIR/.maintenance-fences" ] || return 1
    mkdir -p -m 700 "$APP_DIR/.maintenance-fences"
    [ ! -L "$APP_DIR/.maintenance-fences/.execution.lock" ] || return 1
    exec 9>"$APP_DIR/.maintenance-fences/.execution.lock"
    flock -x -w 15 9 || return 1
    # Persist dispatch before the first destructive boundary. A crash keeps the
    # started receipt, so another process cannot execute this command again.
    node_command_fence start-held
}

require_node_command_ownership() {
    if [ -z "${LEASE_GENERATION:-}" ] || [ -z "${RESOURCE_GENERATION:-}" ] || [ -z "${COMMAND_ID:-}" ]; then
        echo "Destructive CLI command requires an operation authorized by the Panel; root does not bypass maintenance ownership" >&2
        return 1
    fi
    node_command_fence check
}

node_guarded_boundary() {
    [ -n "${LEASE_GENERATION:-}" ] || { "$@"; return $?; }
    [ ! -L "$APP_DIR/.maintenance-fences/.generation.lock" ] || return 1
    exec 8>"$APP_DIR/.maintenance-fences/.generation.lock"
    flock -x -w 15 8 || return 1
    node_command_fence check-boundary-held || { flock -u 8; return 1; }
    local result=0
    if "$@"; then result=0; else result=$?; fi
    flock -u 8
    return "$result"
}
