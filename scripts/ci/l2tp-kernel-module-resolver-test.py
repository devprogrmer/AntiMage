#!/usr/bin/env python3
"""Regression tests for scripts/ci/ensure-l2tp-kernel-module.sh.

The provisioning script originally reused the single pool path apt resolved
against the runner's own cached package index and swapped only the mirror host.
When Ubuntu rotated the kernel package, the superseded path returned 404 from
every mirror, provisioning failed, and native-l2tp never executed even though a
valid replacement was published.

These tests stub the network and package manager so they stay hermetic. They
prove the script now:

1. resolves the pool filename from each source's own package index;
2. fails over to the next source when one returns 404;
3. reports failure, rather than success, when no source publishes the package.
"""

import gzip
import os
import pathlib
import shutil
import stat
import subprocess
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "ci" / "ensure-l2tp-kernel-module.sh"

KERNEL = "6.8.0-1064-azure"
PACKAGE = f"linux-modules-extra-{KERNEL}"
POOL_DIR = "pool/main/l/linux-azure-6.8"
LIVE_FILE = (
    f"{POOL_DIR}/linux-modules-extra-6.8.0-1064-azure_6.8.0-1064.72~22.04.1_amd64.deb"
)
# The superseded build apt's stale local index still pointed at.
ROTATED_FILE = (
    f"{POOL_DIR}/linux-modules-extra-6.8.0-1064-azure_6.8.0-1064.70~22.04.1_amd64.deb"
)
PAYLOAD = b"M" * 65536

CURL_STUB = r'''#!/usr/bin/env python3
import gzip, json, os, sys

fixture = os.environ["STUB_FIXTURE"]
config = json.load(open(os.path.join(fixture, "config.json")))
args = sys.argv[1:]
url = args[-1]
PAYLOAD = b"M" * 65536

def emit(body):
    if "-o" in args:
        with open(args[args.index("-o") + 1], "wb") as fh:
            fh.write(body)
    else:
        sys.stdout.buffer.write(body)
    sys.exit(0)

def index_for(base):
    for entry in config["sources"]:
        if entry["base"] == base:
            return entry.get("filename")
    return None

for entry in config["sources"]:
    base = entry["base"]
    if url.startswith(base + "/ubuntu/dists/") and url.endswith("Packages.gz"):
        filename = entry.get("filename")
        if not filename:
            sys.exit(22)
        body = (
            "Package: {pkg}\n"
            "Version: 0\n"
            "Filename: {fn}\n"
            "Size: {size}\n\n"
        ).format(pkg=os.environ["STUB_PACKAGE"], fn=filename, size=len(PAYLOAD))
        emit(gzip.compress(body.encode()))

for entry in config["sources"]:
    base = entry["base"]
    filename = entry.get("filename")
    if not filename or not entry.get("serves_download", True):
        continue
    if url == base + "/ubuntu/" + filename:
        emit(PAYLOAD)

if url.startswith("https://snapshot.ubuntu.com/"):
    for entry in config["sources"]:
        filename = entry.get("filename")
        if filename and url.endswith("/" + filename):
            emit(PAYLOAD)

sys.exit(22)
'''

SUDO_STUB = """#!/bin/sh
exec env "$@"
"""

DPKG_STUB = """#!/bin/sh
if [ "$1" = "-i" ] && [ -s "$2" ]; then
  touch "$STUB_FIXTURE/installed"
fi
exit 0
"""

APT_GET_STUB = """#!/bin/sh
exit 100
"""

UNAME_STUB = """#!/bin/sh
if [ "$1" = "-r" ]; then
  echo "$STUB_KERNEL"
  exit 0
fi
exec /usr/bin/uname "$@"
"""

FIND_STUB = """#!/bin/sh
if [ -f "$STUB_FIXTURE/installed" ]; then
  echo "/lib/modules/$STUB_KERNEL/kernel/net/l2tp/l2tp_ppp.ko"
fi
exit 0
"""


class ProvisioningTest(unittest.TestCase):
    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="l2tp-resolver-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        self.fixture = self.tmp / "fixture"
        self.fixture.mkdir()
        self._stub("curl", CURL_STUB)
        self._stub("sudo", SUDO_STUB)
        self._stub("dpkg", DPKG_STUB)
        self._stub("apt-get", APT_GET_STUB)
        self._stub("uname", UNAME_STUB)
        self._stub("find", FIND_STUB)

    def _stub(self, name, body):
        path = self.bin / name
        path.write_text(body)
        path.chmod(path.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)

    def _run(self, sources):
        import json

        (self.fixture / "config.json").write_text(json.dumps({"sources": sources}))
        env = os.environ.copy()
        env.update(
            {
                "PATH": f"{self.bin}{os.pathsep}{env['PATH']}",
                "STUB_FIXTURE": str(self.fixture),
                "STUB_KERNEL": KERNEL,
                "STUB_PACKAGE": PACKAGE,
                "ANTIMAGE_L2TP_PROVISION_SECONDS": "120",
                "ANTIMAGE_L2TP_APT_ATTEMPT_SECONDS": "1",
                "ANTIMAGE_L2TP_MIRROR_SECONDS": "30",
            }
        )
        return subprocess.run(
            ["bash", str(SCRIPT)],
            capture_output=True,
            text=True,
            env=env,
            timeout=240,
        )

    def test_resolves_published_pool_path(self):
        """The live filename must come from the index, not a stale apt path."""
        result = self._run(
            [
                {
                    "base": "http://azure.archive.ubuntu.com",
                    "filename": LIVE_FILE,
                }
            ]
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(LIVE_FILE, result.stdout)
        self.assertIn("installed from", result.stdout)
        # The stub only marks the module present when a non-empty .deb landed.
        self.assertTrue((self.fixture / "installed").exists())

    def test_fails_over_when_a_mirror_404s(self):
        result = self._run(
            [
                {
                    "base": "http://azure.archive.ubuntu.com",
                    "filename": ROTATED_FILE,
                    "serves_download": False,
                },
                {
                    "base": "http://archive.ubuntu.com",
                    "filename": LIVE_FILE,
                },
            ]
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(ROTATED_FILE, result.stdout)
        self.assertIn("archive.ubuntu.com", result.stdout)
        self.assertIn("installed from", result.stdout)

    def test_fails_when_no_source_publishes_the_package(self):
        result = self._run([])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("could not install", result.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
