#!/usr/bin/env python3
"""Private mount/PID namespace worker; never invoke without unshare."""
import os
import pathlib
import signal
import subprocess
import sys
import time

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
private_ipsec_d = private_etc/'ipsec.d'
private_ipsec_d.mkdir(parents=True, exist_ok=True)
for source in (root/'ipsec.d').glob('*.p12'):
    target = private_ipsec_d/source.name
    target.write_bytes(source.read_bytes())
# NodeAgent owns ipsec.conf and ipsec.secrets, not strongswan.conf. The native
# driver supplies the private VICI config before production ipsec start.
(private_etc/'strongswan.conf').write_text((root/'server.conf').read_text())
# The driver passes its host-side fixture path in STRONGSWAN_CONF. Once /etc is
# private, all strongSwan processes must read the namespace-local config path.
os.environ['STRONGSWAN_CONF'] = '/etc/strongswan.conf'
private_charon_plugins = private_etc/'strongswan.d'/'charon'
private_charon_plugins.mkdir(parents=True, exist_ok=True)
for plugin in (
    'aes', 'attr', 'constraints', 'eap-identity', 'eap-mschapv2',
    'gmp', 'hmac', 'kernel-netlink', 'nonce', 'openssl', 'pem',
    'pkcs1', 'pkcs8', 'pkcs12', 'pubkey', 'random', 'revocation',
    'sha1', 'sha2', 'socket-default', 'stroke', 'vici', 'x509',
):
    extra = '    socket = unix:///run/charon.vici\n' if plugin == 'vici' else ''
    (private_charon_plugins/(plugin+'.conf')).write_text(
        plugin+' {\n    load = yes\n'+extra+'}\n'
    )
for name,initial in [('ipsec.conf','config setup\n    uniqueids=no\n'), ('ipsec.secrets','')]:
    (private_etc/name).write_text(initial)
pathlib.Path('/run/antimage-ikev2-isolated').write_text(str(root))
env = {**os.environ, 'ANTIMAGE_IKEV2_PROVISION_ROOT':str(root)}
subprocess.run([os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeProvision$','-test.v'],env=env,check=True)
# The production ipsec start must bring up charon; a second starter could hide failure.
for _ in range(100):
    if pathlib.Path('/run/charon.vici').is_socket():
        break
    time.sleep(0.1)
else:
    diagnostics = []
    for command in (['ps', '-eo', 'pid,ppid,stat,comm,args'], ['ipsec', 'statusall'], ['find', '/run', '-maxdepth', '2', '-ls'], ['find', '/var/run', '-maxdepth', '2', '-ls']):
        try:
            result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=8, check=False)
            diagnostics.append('$ '+' '.join(command)+'\n'+result.stdout)
        except Exception as exc:
            diagnostics.append('$ '+' '.join(command)+'\n'+repr(exc))
    try:
        diagnostics.append('--- production provision stdout ---\n'+(root/'server.stdout').read_text())
    except Exception as exc:
        diagnostics.append('--- production provision stdout unavailable ---\n'+repr(exc))
    for label, command in (
        ('effective strongSwan config', ['cat', '/etc/strongswan.conf']),
        ('strongSwan config tree', ['find', '/etc/strongswan.d', '-maxdepth', '3', '-type', 'f', '-print']),
        ('integrity settings', ['sh', '-c', "grep -Rni 'integrity_test' /etc/strongswan.conf /etc/strongswan.d 2>/dev/null || true"]),
        ('strongSwan package versions', ['dpkg-query', '-W', '-f=${Package} ${Version}\\n', 'strongswan', 'strongswan-charon', 'strongswan-starter', 'libstrongswan', 'libstrongswan-standard-plugins']),
        ('strongSwan package verification', ['sh', '-c', 'dpkg -V strongswan-charon strongswan-starter libstrongswan libstrongswan-standard-plugins 2>&1 || true']),
        ('charon linked libraries', ['ldd', '/usr/lib/ipsec/charon']),
        ('strongSwan binary/plugin checksums', ['sh', '-c', 'sha256sum /usr/lib/ipsec/charon /usr/lib/ipsec/libstrongswan.so* /usr/lib/ipsec/libchecksum.so /usr/lib/ipsec/plugins/libstrongswan-hmac.so /usr/lib/ipsec/plugins/libstrongswan-vici.so 2>&1 || true']),
    ):
        try:
            result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=8, check=False)
            diagnostics.append('--- '+label+' (exit '+str(result.returncode)+') ---\n'+result.stdout)
        except Exception as exc:
            diagnostics.append('--- '+label+' unavailable ---\n'+repr(exc))
    # Starter's summary maps several charon startup failures to the same
    # message. Run charon briefly with non-private config/library diagnostics
    # in this disposable namespace to retain the parser's actual error.
    try:
        probe = subprocess.run(
            ['/usr/lib/ipsec/charon', '--debug-cfg', '2', '--debug-lib', '2', '--debug-dmn', '2'],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            timeout=5, check=False,
        )
        diagnostics.append('--- charon config/library diagnostic (exit '+str(probe.returncode)+') ---\n'+probe.stdout)
    except subprocess.TimeoutExpired as exc:
        output = exc.stdout or b''
        if isinstance(output, bytes):
            output = output.decode(errors='replace')
        diagnostics.append('--- charon config/library diagnostic timed out ---\n'+output)
    except Exception as exc:
        diagnostics.append('--- charon config/library diagnostic unavailable ---\n'+repr(exc))
    (root/'starter-diagnostics.txt').write_text('\n'.join(diagnostics))
    print((root/'starter-diagnostics.txt').read_text(), flush=True)
    raise SystemExit('starter VICI socket did not become available')
(root/'provision-ready').write_text('ready')
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
while True:
    signal.pause()
