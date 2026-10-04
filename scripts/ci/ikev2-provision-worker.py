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
subprocess.run(['mount', '-t', 'tmpfs', 'tmpfs', '/run'], check=True)
(root/'private-ipsec.d').mkdir(exist_ok=True)
subprocess.run(['mount', '--bind', str(root/'private-ipsec.d'), '/etc/ipsec.d'], check=True)
for name,initial in [('ipsec.conf','config setup\n    uniqueids=no\n'), ('ipsec.secrets','')]:
    source = root/('private-'+name)
    source.write_text(initial)
    subprocess.run(['mount','--bind',str(source),'/etc/'+name],check=True)
pathlib.Path('/run/antimage-ikev2-isolated').write_text(str(root))
env = {**os.environ, 'ANTIMAGE_IKEV2_PROVISION_ROOT':str(root)}
subprocess.run([os.environ['ANTIMAGE_IKEV2_TEST_BINARY'],'-test.run=^TestIKEv2NativeProvision$','-test.v'],env=env,check=True)
(root/'provision-ready').write_text('ready')
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
while True:
    signal.pause()
