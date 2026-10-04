#!/usr/bin/env python3
"""Isolated Linux strongSwan datapath + opt-in AntiMage component lifecycle.

Requires root and strongSwan charon-systemd/charon-cmd/swanctl, openssl,
iproute2, iputils-ping and nftables. No system daemon config is modified.
Set ANTIMAGE_IKEV2_TEST_BINARY and ANTIMAGE_IKEV2_PANEL_BINARY to compiled
nodeagent/nodecontroller test executables for accounting/DB coverage.
This is NOT a full running Panel HTTP/gRPC/browser deployment test.
"""
import os, pathlib, signal, subprocess as sp, tempfile, time, json, hashlib, re
CONN='antimage-ikev2-'+hashlib.sha256(b'native').hexdigest()[:16]
expected=0
temp_parent=os.environ.get('ANTIMAGE_IKEV2_TEMP_DIR','/var/tmp')
pathlib.Path(temp_parent).mkdir(parents=True,exist_ok=True)
R=pathlib.Path(tempfile.mkdtemp(prefix='antimage-ikev2-', dir=temp_parent))
socket_parent=os.environ.get('ANTIMAGE_IKEV2_SOCKET_DIR')
SOCKET_DIR=pathlib.Path(socket_parent) if socket_parent else R
SOCKET_DIR.mkdir(parents=True,exist_ok=True)
S='amikes'+str(os.getpid()); C='amikec'+str(os.getpid()); C2=C+'b'
processes=[]
def run(args, ns=None, check=True):
    p=sp.run((['ip','netns','exec',ns] if ns else [])+list(map(str,args)),text=True,stdout=sp.PIPE,stderr=sp.STDOUT,timeout=30)
    if check and p.returncode: raise RuntimeError(str(args)+'\n'+p.stdout)
    return p.stdout
def write(name,text):
    p=R/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(text);return p
def install_swanctl_wrapper():
    wrapper=write('bin/swanctl', '#!/bin/sh\nexec /usr/sbin/swanctl "$@" --uri unix://'+str(SOCKET_DIR/'server.vici')+'\n')
    wrapper.chmod(0o755)
    return wrapper.parent
def start(args,ns,conf,log):
    f=open(R/log,'w');p=sp.Popen(['ip','netns','exec',ns]+list(map(str,args)),env={**os.environ,'PATH':str(R/'bin')+':'+os.environ['PATH'],'STRONGSWAN_CONF':str(R/conf)},stdout=f,stderr=sp.STDOUT,start_new_session=True);f.close();processes.append(p);return p
def stop(p,timeout=15):
    if p.poll() is not None:return
    try:os.killpg(p.pid,signal.SIGTERM)
    except ProcessLookupError:pass
    try:p.wait(timeout=timeout)
    except sp.TimeoutExpired:
        try:os.killpg(p.pid,signal.SIGKILL)
        except ProcessLookupError:pass
        p.wait(timeout=5)
def wait(fn,desc):
    for _ in range(100):
        if fn():return
        time.sleep(.2)
    raise RuntimeError('Timeout: '+desc)
def sw(*args):return run(['swanctl',*args,'--uri','unix://'+str(SOCKET_DIR/'server.vici')],S)
def sas():return sw('--list-sas')
def accounting(action=''):
    install_swanctl_wrapper()
    env={**os.environ,'PATH':str(R/'bin')+':'+os.environ['PATH'],'ANTIMAGE_IKEV2_NATIVE_STATE':str(R/'node-state'),'ANTIMAGE_IKEV2_EXPECTED_BYTES':str(expected),'ANTIMAGE_IKEV2_ACTION':action}
    p=sp.run(['ip','netns','exec',S,os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeAccountingStage$','-test.v'],env=env,text=True,stdout=sp.PIPE,stderr=sp.STDOUT,timeout=60)
    print(p.stdout,flush=True)
    assert p.returncode==0,'AntiMage native accounting failed'
def traffic(label):
    global expected
    state=sas(); assert 'ESTABLISHED' in state and 'INSTALLED' in state,state
    assert '10.80.0.2' in state and '10.82.0.1' in state,state
    print(label+'\n'+state,flush=True)
    print(run(['ping','-I','10.82.0.1','-c','3','-W','2','10.81.0.1'],C),flush=True)
    after=sas();print(after,flush=True)
    write(label+'.sas',after)
    expected+=504
    if os.environ.get('ANTIMAGE_IKEV2_TEST_BINARY'):
        accounting()
print('ARTIFACTS='+str(R),flush=True)
try:
    install_swanctl_wrapper()
    for n in [S,C]:run(['ip','netns','add',n]);run(['ip','link','set','lo','up'],n)
    run(['ip','link','add','ike-s','type','veth','peer','name','ike-c'])
    for dev,n,addr in [('ike-s',S,'10.80.0.1/24'),('ike-c',C,'10.80.0.2/24')]:
        run(['ip','link','set',dev,'netns',n]);run(['ip','addr','add',addr,'dev',dev],n);run(['ip','link','set',dev,'up'],n)
    run(['ip','addr','add','10.81.0.1/32','dev','lo'],S)
    run(['ip','route','add','10.81.0.1/32','via','10.80.0.1'],C)
    for side in ['server','client','client2']:
        write(side+'.conf',f'''include /etc/strongswan.d/*.conf
charon {{
 load_modular = yes
 pid_file = {R}/{side}.pid
 plugins {{
  vici {{
   socket = unix://{SOCKET_DIR}/{side}.vici
  }}
 }}
 filelog {{
  log {{
   path = {R}/{side}.log
   default = 1
   flush_line = yes
  }}
 }}
}}
charon-systemd {{
 journal {{
  default = -1
 }}
}}
charon-cmd : charon {{
}}
''')
    run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-subj','/CN=local-test-ca','-keyout',R/'ca.key','-out',R/'ca.pem'])
    for side in ['server','client','client2']:
        run(['openssl','req','-new','-newkey','rsa:2048','-nodes','-subj','/CN='+side,'-keyout',R/(side+'.key'),'-out',R/(side+'.csr')])
        ext=write(side+'.ext','basicConstraints=critical,CA:FALSE\nsubjectAltName=DNS:'+side+'\n')
        run(['openssl','x509','-req','-in',R/(side+'.csr'),'-CA',R/'ca.pem','-CAkey',R/'ca.key','-CAcreateserial','-days','1','-extfile',ext,'-out',R/(side+'.pem')])
    for identity in ['client','client2']:
        run(['openssl','pkcs12','-export','-in',R/(identity+'.pem'),'-inkey',R/(identity+'.key'),'-certfile',R/'ca.pem','-out',R/(identity+'.p12'),'-passout','pass:'])
    for sub in ['x509','x509ca','private','x509ocsp','x509aa','x509ac','x509crl','pubkey','rsa','ecdsa','pkcs8','pkcs12']:(R/sub).mkdir()
    import shutil
    shutil.copy(R/'server.pem',R/'x509/server.pem');shutil.copy(R/'ca.pem',R/'x509ca/ca.pem');shutil.copy(R/'server.key',R/'private/server.key')
    write('swanctl.conf','''connections {
 CONN {
  version = 2
  local_addrs = 10.80.0.1
  remote_addrs = 10.80.0.0/24
  unique = never
  pools = clients
  proposals = aes256-sha256-modp2048
  local {
   auth = pubkey
   certs = server.pem
   id = server
  }
  remote {
   auth = pubkey
   id = client
  }
  children {
   tunnel {
    local_ts = 10.81.0.1/32
    esp_proposals = aes256-sha256
    rekey_time = 0
   }
  }
 }
}
pools {
 clients {
  addrs = 10.82.0.1-10.82.0.8
 }
}
'''.replace('CONN',CONN))
    def server():
        if os.environ.get('ANTIMAGE_IKEV2_PROVISION'):
            if (R/'provision-ready').exists():
                (R/'provision-ready').rename(R/('provision-ready-'+str(time.time_ns())))
            p=start(['unshare','--mount','--pid','--fork','--mount-proc','--kill-child','python3',pathlib.Path(__file__).with_name('ikev2-provision-worker.py'),R],S,'server.conf','server.stdout')
            wait(lambda:(R/'provision-ready').exists() or p.poll() is not None,'production applyIKEv2Runtimes')
        else:
            p=start(['/usr/lib/ipsec/charon'],S,'server.conf','server.stdout')
        try:
            wait(lambda:(SOCKET_DIR/'server.vici').is_socket() or p.poll() is not None,'server VICI')
        except RuntimeError:
            for diagnostic in [R/'server.stdout', R/'server.log']:
                if diagnostic.exists():
                    print(str(diagnostic)+'\n'+diagnostic.read_text()[-12000:],flush=True)
            raise
        assert p.poll() is None,'responder exited'
        if not os.environ.get('ANTIMAGE_IKEV2_PROVISION'):
            print(sw('--load-all','--file',str(R/'swanctl.conf')),flush=True)
        else:
            print((R/'server.stdout').read_text(),flush=True)
        return p
    def client(ns=C,side='client',host='10.80.0.1',identity='client'):
        p=start(['charon-cmd','--host',host,'--identity',identity,'--remote-identity','server','--p12',R/(identity+'.p12'),'--profile','ikev2-pub','--remote-ts','10.81.0.1/32','--ike-proposal','aes256-sha256-modp2048','--esp-proposal','aes256-sha256'],ns,side+'.conf',side+'.stdout')
        wait(lambda:'INSTALLED' in sas(),'CHILD_SA');return p
    srv=server();cli=client();traffic('initial')
    before=sas();print(sw('--rekey','--child',CONN if os.environ.get('ANTIMAGE_IKEV2_PROVISION') else 'tunnel'),flush=True)
    wait(lambda:sas()!=before,'rekey change');traffic('after-child-rekey')
    print(sw('--rekey','--ike',CONN),flush=True);time.sleep(1);traffic('after-ike-rekey')
    stop(cli);stop(srv)
    srv=server();cli=client();traffic('after-runtime-restart')
    if os.environ.get('ANTIMAGE_IKEV2_TEST_BINARY'):
        # Exact-sized ICMP packets make the plaintext IP accounting oracle
        # independent of swanctl. Each request+reply is 2 * (payload+28).
        for stage,count in [('below-quota',8000),('over-quota',500)]:
            print(run(['ping','-q','-I','10.82.0.1','-c',str(count),'-i','.0005','-s','1000','-W','2','10.81.0.1'],C),flush=True)
            expected += count*2*(1000+28)
            accounting('quota')
            write(stage+'.sas',sas())
        assert 'INSTALLED' not in sas(),'quota left CHILD_SA installed'
        blocked=sp.run(['ip','netns','exec',C,'ping','-I','10.82.0.1','-c','1','-W','1','10.81.0.1'],stdout=sp.PIPE,stderr=sp.STDOUT,text=True)
        assert blocked.returncode!=0,'traffic passed after quota cutoff'
        print('PASS offline 50 MiB coefficient-adjusted quota; batch sampling overshoot='+str(expected*3-(50<<20)),flush=True)
        # Restarted agent/runtime must still deny an exhausted user.
        stop(cli);stop(srv)
        srv=server();cli=client()
        accounting('quota')
        assert 'INSTALLED' not in sas(),'offline restart restored exhausted traffic'
        print('PASS persisted offline quota after runtime and collector restart',flush=True)
        if os.environ.get('ANTIMAGE_IKEV2_PANEL_BINARY'):
            for delivery in range(2):
                env={**os.environ,'ANTIMAGE_IKEV2_NATIVE_STATE':str(R/'node-state')}
                p=sp.run([os.environ['ANTIMAGE_IKEV2_PANEL_BINARY'],'-test.run=^TestIKEv2NativePanelDB$','-test.v'],env=env,text=True,stdout=sp.PIPE,stderr=sp.STDOUT,timeout=60)
                print(p.stdout,flush=True)
                assert p.returncode==0,'Panel repository DB delivery failed'
                accounting('ack')
                # First immutable pending snapshot is 504 bytes; subsequent
                # batch contains all newer real traffic, then nothing remains.
                expected -= 504 if delivery==0 else expected
            receipt=json.loads((R/'node-state/native-panel-receipt.json').read_text())
            assert receipt['raw_total']==17478016,receipt
            assert receipt['effective_total']==52434048,receipt
            print('PASS native collector -> SQLite exact-once -> lost ACK replay -> durable ACK/prune',flush=True)
        # Test each speed direction independently on a fresh connection. This
        # deliberately tests shaping separately from the exhausted quota user.
        stop(cli)
        cli=client()
        for direction in ['upload','download']:
            env={**os.environ,'PATH':str(R/'bin')+':'+os.environ['PATH'],'ANTIMAGE_IKEV2_NATIVE_STATE':str(R/'speed-state'),'ANTIMAGE_IKEV2_ACTION':direction}
            p=sp.run(['ip','netns','exec',S,os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeSpeedStage$','-test.v'],env=env,text=True,stdout=sp.PIPE,stderr=sp.STDOUT,timeout=60)
            print(p.stdout,flush=True)
            assert p.returncode==0,'production speed rule installation failed'
            ping=run(['ping','-q','-I','10.82.0.1','-c','200','-i','.005','-s','1000','-W','1','10.81.0.1'],C,check=False)
            print(ping,flush=True)
            rules=run(['nft','list','table','inet','antimage_ikev2_speed'],S)
            print(rules,flush=True)
            dropped=sum(int(n) for n in re.findall(r'counter packets (\d+)',rules))
            assert dropped>0,direction+' did not drop over-limit native packets'
            loss=re.search(r'([\d.]+)% packet loss',ping)
            assert loss and 0<float(loss.group(1))<100,direction+' must pass some traffic while policing excess'
            run(['nft','delete','table','inet','antimage_ikev2_speed'],S)
        print('PASS independent production upload/download nft policing on native traffic',flush=True)
        run(['ip','netns','add',C2]);run(['ip','link','set','lo','up'],C2)
        run(['ip','link','add','ike-s2','type','veth','peer','name','ike-c2'])
        run(['ip','link','set','ike-s2','netns',S]);run(['ip','link','set','ike-c2','netns',C2])
        run(['ip','addr','add','10.80.1.1/24','dev','ike-s2'],S)
        run(['ip','link','set','ike-s2','up'],S)
        run(['ip','addr','add','10.80.1.2/24','dev','ike-c2'],C2)
        run(['ip','link','set','ike-c2','up'],C2)
        run(['ip','route','add','10.81.0.1/32','via','10.80.1.1'],C2)
        write('client2.conf',(R/'client.conf').read_text().replace('/client.','/client2.'))
        for kind in ['device','ip']:
            cli2=client(C2,'client2','10.80.1.1','client2')
            wait(lambda:sas().count('ESTABLISHED')==2,'two independent clients')
            env={**os.environ,'PATH':str(R/'bin')+':'+os.environ['PATH'],'ANTIMAGE_IKEV2_NATIVE_STATE':str(R/(kind+'-state')),'ANTIMAGE_IKEV2_ACTION':kind}
            p=sp.run(['ip','netns','exec',S,os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeSessionLimitStage$','-test.v'],env=env,text=True,stdout=sp.PIPE,stderr=sp.STDOUT,timeout=60)
            print(p.stdout,flush=True)
            assert p.returncode==0,'native '+kind+' enforcement failed'
            print(run(['ping','-I','10.82.0.1','-c','1','-W','2','10.81.0.1'],C),flush=True)
        stop(cli2)
        print('PASS native device/session and distinct outer-IP limits',flush=True)
    evidence={
        'native_sa_traffic_rekey_restart': True,
        'production_apply_ikev2_runtimes': bool(os.environ.get('ANTIMAGE_IKEV2_PROVISION')),
        'native_collector_quota_speed_session_limits': bool(os.environ.get('ANTIMAGE_IKEV2_TEST_BINARY')),
        'panel_repository_sqlite_ack': bool(os.environ.get('ANTIMAGE_IKEV2_TEST_BINARY') and os.environ.get('ANTIMAGE_IKEV2_PANEL_BINARY')),
        'running_panel_node_transport_browser': False,
    }
    write('evidence.json',json.dumps(evidence,indent=2)+'\n')
    print('PASS implemented IKEv2 harness stages: '+json.dumps(evidence),flush=True)
finally:
    for p in reversed(processes):
        stop(p,timeout=10)
    for n in [S,C,C2]:run(['ip','netns','del',n],check=False)
    if socket_parent:
        try:SOCKET_DIR.rmdir()
        except OSError:pass
    for f in R.glob('*.log'):
        print(str(f)+'\n'+f.read_text()[-2000:],flush=True)
    if (R/'server.stdout').exists():
        print(str(R/'server.stdout')+'\n'+(R/'server.stdout').read_text()[-12000:],flush=True)
    print((R/'client.stdout').read_text()[-16000:] if (R/'client.stdout').exists() else 'no client stdout',flush=True)
    print('Evidence retained at '+str(R),flush=True)
