#!/usr/bin/env python3
"""Run installed command acceptance/job guards as independent Linux processes."""
import pathlib,subprocess,tempfile,time

root=pathlib.Path(__file__).resolve().parents[2]
helper=(root/'scripts/antimage/node-command-fence.sh').read_text()
body=helper[helper.index('# Embedded'):].strip()
installed=(root/'scripts/antimage/antimage-node-binary.sh').read_text()
start=installed.index('# Embedded in the installed CLI.')
end=installed.index('rollback_command() {',start)
assert installed[start:end].strip()==body,'Installed command guard diverged'
with tempfile.TemporaryDirectory(prefix='antimage-node-fence-') as app:
    common='set -e\n'+body+'\nAPP_DIR="$1"; FENCE_OPERATION_ID="$2"; LEASE_GENERATION="$3"; COMMAND_ID="$4"; RESOURCE_GENERATION="$6"; RESOURCE_ID="$7"; '
    def invoke(generation,command,mode,success=True,operation='operation-a',resource_generation=None):
        action='node_command_fence "$5"' if mode!='job' else 'lock_node_command; write_action() { printf "action\\n" >> "$APP_DIR/actions"; }; node_guarded_boundary write_action; finish_node_command'
        result=subprocess.run(['bash','-c',common+action,'test',app,operation,str(generation),command,mode,str(resource_generation if resource_generation is not None else generation),'7'],capture_output=True,text=True,timeout=25)
        assert (result.returncode==0)==success,(generation,command,mode,result.stdout,result.stderr)
    invoke(10,'command-a','accept')
    invoke(11,'command-b','accept')
    invoke(10,'command-a','accept',False)
    invoke(10,'command-a','job',False)
    assert not (pathlib.Path(app)/'actions').exists(),'Obsolete queued job executed'
    invoke(11,'command-b','job')
    invoke(11,'command-b','job',False)
    invoke(12,'command-b','accept',False)
    invoke(11,'command-b','check',False)
    assert (pathlib.Path(app)/'actions').read_text()=='action\n','Destructive guard executed more than once'
    invoke(10,'late-command','accept',False)
    invoke(13,'new-command','accept')
    invoke(13,'new-command','check')
    invoke(13,'../escape','accept',False)
    invoke(0,'new-command','accept',False)
    # A different operation starts its lease generation at 1; the node resource
    # generation still advances, rejecting old commands even with large leases.
    invoke(1,'rollback-b','accept',operation='operation-b',resource_generation=15)
    invoke(99,'late-update-a','accept',False,resource_generation=13)
    invoke(1,'rollback-b','job',operation='operation-b',resource_generation=15)
    invoke(1,'paused-old','accept',operation='operation-old',resource_generation=20)
    paused=common+'lock_node_command; touch "$APP_DIR/paused"; until [ -f "$APP_DIR/resume" ]; do sleep 0.02; done; activate_old() { printf "old\\n" >> "$APP_DIR/activation"; }; node_guarded_boundary activate_old; finish_node_command'
    child=subprocess.Popen(['bash','-c',paused,'test',app,'operation-old','1','paused-old','job','20','7'],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    try:
        deadline=time.monotonic()+5
        while not (pathlib.Path(app)/'paused').exists():
            assert child.poll() is None,'Paused job exited unexpectedly'
            assert time.monotonic()<deadline,'Job did not reach pause boundary'
            time.sleep(0.02)
        invoke(1,'winning-new','accept',operation='operation-new',resource_generation=21)
        (pathlib.Path(app)/'resume').touch()
        output,error=child.communicate(timeout=5)
        assert child.returncode!=0,(output,error)
        assert not (pathlib.Path(app)/'activation').exists(),'Superseded job activated binary'
        invoke(1,'winning-new','job',operation='operation-new',resource_generation=21)
        assert (pathlib.Path(app)/'actions').read_text().count('action\n')==3,'Wrong winning action count'
    finally:
        if child.poll() is None: child.kill();child.wait(timeout=5)
    dispatch_start=installed.index('dispatch_command() {')
    dispatch_end=installed.index('\nif [ -z "${COMMAND:-}"',dispatch_start)
    dispatch=installed[dispatch_start:dispatch_end]
    stubs='\nensure_script_matches_installed_mode() { :; }; restart_command() { printf "cli\\n" >> "$APP_DIR/cli-actions"; }; update_core_command() { restart_command; }; '
    def cli(generation,command,operation,resource_generation,success):
        script=common+stubs+dispatch+'\ndispatch_command restart'
        result=subprocess.run(['bash','-c',script,'test',app,operation,str(generation),command,'cli',str(resource_generation),'7'],capture_output=True,text=True,timeout=25)
        assert (result.returncode==0)==success,(result.stdout,result.stderr)
    invoke(1,'api-update','accept',operation='api-update',resource_generation=200)
    cli(0,'unowned-cli','root-cli',0,False)
    assert not (pathlib.Path(app)/'cli-actions').exists(),'Unowned direct CLI bypassed API ownership'
    invoke(1,'cli-owned','accept',operation='cli-operation',resource_generation=210)
    invoke(1,'api-rollback','accept',operation='api-rollback',resource_generation=211)
    cli(1,'cli-owned','cli-operation',210,False)
    cli(1,'api-rollback','api-rollback',211,True)
    cli(1,'api-rollback','api-rollback',211,False)
    assert (pathlib.Path(app)/'cli-actions').read_text()=='cli\n','Direct CLI repeated or stale mutation executed'
print('Resource-global fencing: cross-operation stale rejection and paused-job boundary fencing passed')
