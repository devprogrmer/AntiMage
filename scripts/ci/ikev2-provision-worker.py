#!/usr/bin/env python3
"""Private mount/PID namespace worker; never invoke without unshare."""
import os
import pathlib
import signal
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
if os.getpid() != 1:
    raise SystemExit('requires unshare --pid --fork --mount-proc')
subprocess.run(['mount', '--make-rprivate', '/'], check=True)
# The parent native driver already isolates /run from the host. Keep that
# private filesystem shared here so VICI sockets remain reachable by the driver.
# Build a private copy of /etc rather than bind-mounting the individual ipsec
# files: the NodeAgent deliberately updates them with atomic rename, which
# cannot replace a file that is itself a mountpoint (EBUSY).
private_etc = root/'private-etc'
private_etc.mkdir(exist_ok=True)
# Copy only the configuration consumed by strongSwan and the test tools. A
# recursive copy of all of /etc includes large mounted trees on CI and can
# outlive the worker's startup window.
for entry in ('ssl', 'ca-certificates', 'default', 'alternatives'):
    source = pathlib.Path('/etc')/entry
    if source.exists():
        subprocess.run(['cp', '-a', str(source), str(private_etc)], check=True)
for entry in ('hosts', 'resolv.conf', 'nsswitch.conf', 'passwd', 'group', 'services', 'protocols'):
    source = pathlib.Path('/etc')/entry
    if source.exists() or source.is_symlink():
        target = private_etc/entry
        if source.is_symlink():
            target.parent.mkdir(parents=True, exist_ok=True)
            target.symlink_to(os.readlink(source))
        else:
            subprocess.run(['cp', '-a', str(source), str(target)], check=True)
subprocess.run(['mount', '--bind', str(private_etc), '/etc'], check=True)
(private_etc/'strongswan.d'/'charon').mkdir(parents=True, exist_ok=True)
(private_etc/'strongswan.d'/'charon'/'antimage-native.conf').write_text('''charon {
    plugins {
        kernel-netlink { load = yes }
        socket-default { load = yes }
        vici { load = yes }\n        stroke { load = yes }
        openssl { load = yes }
        x509 { load = yes }
        pubkey { load = yes }
        pem { load = yes }
        pkcs12 { load = yes }
        random { load = yes }
        nonce { load = yes }
        eap-identity { load = yes }
        eap-mschapv2 { load = yes }
    }
}
''')
private_ipsec_d = private_etc/'ipsec.d'
private_ipsec_d.mkdir(parents=True, exist_ok=True)
for source in (root/'ipsec.d').glob('*.p12'):
    target = private_ipsec_d/source.name
    target.write_bytes(source.read_bytes())
# The production `ipsec start` launcher reads strongswan.conf for VICI and
# plugin configuration; STRONGSWAN_CONF in the parent is not a substitute for
# installing that config at the daemon's standard path inside this namespace.
(private_etc/'strongswan.conf').write_bytes((root/'server.conf').read_bytes())
for name,initial in [('ipsec.conf','config setup\n    uniqueids=no\n'), ('ipsec.secrets','')]:
    (private_etc/name).write_text(initial)
pathlib.Path('/run/antimage-ikev2-isolated').write_text(str(root))
env = {**os.environ, 'ANTIMAGE_IKEV2_PROVISION_ROOT':str(root)}
subprocess.run([os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeProvision$','-test.v'],env=env,check=True)
# The CI PID namespace has no systemd. The production `ipsec start` command
# therefore cannot launch starter through systemctl; start the same native
# starter directly after provisioning so rereadsecrets/reload and VICI use the
# real strongSwan runtime in this namespace.
starter_log = open(root/'starter.stdout', 'w')
starter = subprocess.Popen(['/usr/lib/ipsec/starter', '--daemon', 'charon', '--nofork'], stdout=starter_log, stderr=subprocess.STDOUT)
for _ in range(100):
    if pathlib.Path('/run/charon.vici').exists():
        break
    if starter.poll() is not None and starter.returncode != 0:
        raise SystemExit('starter exited with code '+str(starter.returncode))
    import time; time.sleep(0.1)
else:
    diagnostics = []
    for command in (['ps', '-eo', 'pid,ppid,stat,comm,args'], ['ipsec', 'statusall'], ['find', '/run', '-maxdepth', '2', '-ls'], ['find', '/var/run', '-maxdepth', '2', '-ls']):
        try:
            result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=8, check=False)
            diagnostics.append('$ '+' '.join(command)+'\n'+result.stdout)
        except Exception as exc:
            diagnostics.append('$ '+' '.join(command)+'\n'+repr(exc))
    try:
        diagnostics.append('--- starter stdout ---\n'+(root/'starter.stdout').read_text())
    except Exception as exc:
        diagnostics.append('--- starter stdout unavailable ---\n'+repr(exc))
    (root/'starter-diagnostics.txt').write_text('\n'.join(diagnostics))
    raise SystemExit('starter VICI socket did not become available')
(root/'provision-ready').write_text('ready')
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
while True:
    signal.pause()
