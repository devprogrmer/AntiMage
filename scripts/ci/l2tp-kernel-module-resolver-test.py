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
import signal
import stat
import subprocess
import tempfile
import time
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


class WorkflowIsolationTest(unittest.TestCase):
    """Guards the native-l2tp step against a leaked-stdio wedge.

    The runner only finalizes a workflow step once the step's captured stdout
    pipe reaches EOF. `sudo timeout ... apt-get` (and the `dpkg` it drives) can
    leave privileged method children (http/gpgv) alive after the parent is
    killed; if those children inherit the step's pipe they hold it open, so the
    step body finishes but the step never completes. That is exactly how the
    native-l2tp job hung ~55 minutes on two attempts of a SHA whose third
    attempt passed, and why the step's own timeout could not end it.

    The provisioning helpers must therefore write to a file, never to the step's
    captured stdout.
    """

    WORKFLOW = (
        pathlib.Path(__file__).resolve().parents[2]
        / ".github"
        / "workflows"
        / "native-protocol-e2e.yml"
    )

    def _l2tp_step(self):
        text = self.WORKFLOW.read_text(encoding="utf-8")
        marker = "Run native L2TP and PPP lifecycle"
        start = text.index(marker)
        # The step ends at the next step definition at the same indentation.
        rest = text[start:]
        end = rest.index("\n      - name: ")
        return rest[:end]

    def test_provisioning_output_is_redirected_off_the_captured_stdout(self):
        step = self._l2tp_step()
        self.assertIn(
            'python3 scripts/ci/l2tp-kernel-module-resolver-test.py </dev/null >"$provision_log" 2>&1',
            step,
            "provisioning must not write to the captured step stdout",
        )
        self.assertIn(
            'bash scripts/ci/ensure-l2tp-kernel-module.sh </dev/null >>"$provision_log" 2>&1',
            step,
            "kernel-module provisioning must not write to the captured step stdout",
        )
        # The log is still surfaced so failures remain diagnosable.
        self.assertIn('cat "$provision_log"', step)
        self.assertIn('[ "$provision_status" -eq 0 ] || exit "$provision_status"', step)

    def test_driver_and_evidence_are_safe_under_cancellation(self):
        step = self._l2tp_step()
        self.assertIn("bash scripts/ci/native-l2tp-driver.sh \\\n", step)
        self.assertIn('</dev/null >"$RUNNER_TEMP/native-l2tp.log" 2>&1', step)
        workflow = self.WORKFLOW.read_text(encoding="utf-8")
        # Evidence must survive a cancelled run (a wedged step is cancelled).
        self.assertEqual(workflow.count("if: failure() || cancelled()"), 2)


class CapturedPipeWedgeTest(unittest.TestCase):
    """Proves, at process level, why the provisioning stdout had to move.

    The runner finalizes a step only when the step's captured stdout pipe hits
    EOF. A bounded wrapper such as `timeout .. sudo apt-get install ..` kills
    apt-get, but a privileged child it spawned (http/gpgv/dpkg) can survive and
    still hold the inherited pipe write end. The shell then returns while the
    pipe stays open, so the step never finishes and any step-level timeout is
    powerless - it is waiting on a pipe, not on a process. Sending the helper's
    output to a file instead closes that path.

    The parent is SIGKILLed directly (rather than through `timeout`) so the test
    exercises the orphaned-child mechanism itself and does not depend on a
    particular timeout implementation's process-group behaviour.
    """

    LEAKER = """#!/usr/bin/env bash
# Stands in for a privileged apt/dpkg parent: it spawns a child that inherits
# stdout and outlives this shell when the bounded wrapper kills it.
( exec sleep 120 ) &
echo "leaker spawned an inherited-stdout child"
sleep 120
"""

    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="l2tp-pipe-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.leaker = self.tmp / "leaker.sh"
        self.leaker.write_text(self.LEAKER)
        self.leaker.chmod(0o755)

    def _pipe_stays_open(self, capture_helper_to_pipe):
        """Kill the helper's parent and report whether the pipe ever EOFs."""
        fifo = self.tmp / "capture.pipe"
        os.mkfifo(fifo)
        captured = self.tmp / "captured.log"
        reader = subprocess.Popen(
            ["bash", "-c", f"cat {fifo} > {captured}"],
            start_new_session=True,
        )
        writer = None
        child = None
        helper_log = None
        try:
            writer = os.open(fifo, os.O_WRONLY)
            if capture_helper_to_pipe:
                child = subprocess.Popen(["bash", str(self.leaker)], stdout=writer, stderr=writer)
            else:
                helper_log = (self.tmp / "helper.log").open("wb")
                child = subprocess.Popen(
                    ["bash", str(self.leaker)], stdout=helper_log, stderr=helper_log
                )
            time.sleep(0.7)  # let the helper spawn its inherited-stdout child
            os.kill(child.pid, signal.SIGKILL)  # the bounded wrapper killing the parent
            child.wait(timeout=10)
            child = None
            os.close(writer)
            writer = None
            # Give the reader a bounded window to observe EOF.
            for _ in range(40):
                if reader.poll() is not None:
                    return False
                time.sleep(0.1)
            return reader.poll() is None
        finally:
            if child is not None:
                child.kill()
                child.wait(timeout=10)
            if helper_log is not None:
                helper_log.close()
            if writer is not None:
                os.close(writer)
            if reader.poll() is None:
                reader.kill()
            reader.wait(timeout=10)
            subprocess.run(
                ["pkill", "-9", "-f", str(self.leaker)],
                check=False,
                capture_output=True,
            )
            subprocess.run(
                ["pkill", "-9", "-f", "sleep 120"],
                check=False,
                capture_output=True,
            )

    def test_captured_pipe_is_pinned_when_helper_writes_to_it(self):
        self.assertTrue(
            self._pipe_stays_open(capture_helper_to_pipe=True),
            "expected the leaked child to keep the captured pipe open",
        )

    def test_captured_pipe_closes_when_helper_writes_to_a_file(self):
        self.assertFalse(
            self._pipe_stays_open(capture_helper_to_pipe=False),
            "redirecting the helper to a file must let the captured pipe close",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
