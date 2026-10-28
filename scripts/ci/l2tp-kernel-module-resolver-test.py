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

MODINFO_STUB = """#!/bin/sh
if [ "$1" = "-F" ] && [ "$2" = "vermagic" ]; then
  echo "${STUB_VERMAGIC:-$STUB_KERNEL SMP mod_unload modversions}"
  exit 0
fi
exit 1
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
        self._stub("modinfo", MODINFO_STUB)

    def _stub(self, name, body):
        path = self.bin / name
        path.write_text(body)
        path.chmod(path.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)

    def _run(self, sources, vermagic=None):
        import json

        (self.fixture / "config.json").write_text(json.dumps({"sources": sources}))
        env = os.environ.copy()
        if vermagic is not None:
            env["STUB_VERMAGIC"] = vermagic
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

    def test_refuses_a_module_built_for_another_kernel(self):
        """A mismatched l2tp_ppp can hang the runner when modprobe loads it."""
        result = self._run(
            [{"base": "http://azure.archive.ubuntu.com", "filename": LIVE_FILE}],
            vermagic="6.5.0-99-generic SMP mod_unload modversions",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match kernel", result.stderr)

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
    STEP_SCRIPT = (
        pathlib.Path(__file__).resolve().parents[2]
        / "scripts"
        / "ci"
        / "native-l2tp-lifecycle-step.sh"
    )


    def _l2tp_step(self):
        text = self.WORKFLOW.read_text(encoding="utf-8")
        marker = "Run native L2TP and PPP lifecycle"
        start = text.index(marker)
        # The step ends at the next step definition at the same indentation.
        rest = text[start:]
        end = rest.index("\n      - name: ")
        return rest[:end]

    def _l2tp_step_body(self):
        """Return the step's `run:` block with its ten-space indent removed."""
        step = self._l2tp_step()
        marker = "\n        run: |\n"
        start = step.index(marker) + len(marker)
        body = []
        for line in step[start:].split("\n"):
            if line.strip() == "":
                body.append("")
                continue
            if not line.startswith("          "):
                break
            body.append(line[10:])
        return "\n".join(body)

    def test_step_delegates_to_a_self_bounding_script(self):
        step = self._l2tp_step()
        # The body was extracted so a hanging phase still reaches its own reap,
        # log dump and annotation.
        self.assertIn('setsid bash -c ', step)
        self.assertIn(
            "bash scripts/ci/native-l2tp-lifecycle-step.sh </dev/null || rc=$?",
            step,
            "the step must delegate to the self-bounding script",
        )
        # The lifecycle must never run on the step's captured stdout: a leaked
        # privileged descendant would otherwise hold the pipe open and the step
        # could not finish even after its own body returned.
        self.assertIn('>"$step_log" 2>&1 </dev/null &', step)
        self.assertNotIn("| tee", step)
        # The step has to keep going after the lifecycle fails, so it must not
        # run under `set -e` before it has reaped, printed and annotated.
        self.assertIn("set -uo pipefail", step)
        self.assertNotIn("set -euo pipefail", step)
        # The step's bound must sit between the lifecycle's own bound and the
        # 45-minute job cap, leaving room for the capture and upload steps.
        self.assertIn("timeout-minutes: 30", step)
        self.assertNotIn("timeout-minutes: 35", step)
        self.assertIn('status_file="$RUNNER_TEMP/native-l2tp-lifecycle-step.status"', step)
        # The step polls for the lifecycle and never waits on it, so a process
        # stuck in an uninterruptible kernel path cannot hold the step open.
        self.assertIn("while [ ! -s \"$status_file\" ]; do", step)
        self.assertNotIn("wait \"$lifecycle_pid\"", step)

    def test_driver_and_evidence_are_safe_under_cancellation(self):
        step = self._l2tp_step()
        # The logs are still surfaced so failures remain diagnosable even though
        # every producer writes to a file rather than the captured step stdout.
        for log in ("native-l2tp-provision.log", "native-l2tp.log"):
            self.assertIn(log, step)
        workflow = self.WORKFLOW.read_text(encoding="utf-8")
        # Evidence must survive a cancelled run (a wedged step is cancelled).
        self.assertEqual(workflow.count("if: failure() || cancelled()"), 2)

    def test_driver_tree_is_reaped_and_failures_are_annotated(self):
        step = self._l2tp_step()
        # `timeout` only signals the direct child; the privileged grandchildren
        # (xl2tpd/pppd/charon) have to be reaped explicitly or the runner stays
        # wedged and the job never ends, so no log or artifact is ever produced.
        self.assertIn("bash scripts/ci/l2tp-reap-driver-tree.sh || true", step)
        # The failure reason must be surfaced as a check-run annotation, which
        # survives cancellation when the job logs do not.
        self.assertIn("::error title=native L2TP lifecycle step failed::", step)
        self.assertIn('exit "$status"', step)

    def test_extracted_script_bounds_every_phase(self):
        script = self.STEP_SCRIPT.read_text(encoding="utf-8")
        # One shared step budget, with a reserve kept back so the script can
        # always print its logs and annotate before its own bound is reached.
        self.assertIn('step_budget="${ANTIMAGE_L2TP_STEP_SECONDS:-1500}"', script)
        self.assertIn('reserve_seconds="${ANTIMAGE_L2TP_STEP_RESERVE_SECONDS:-120}"', script)
        # 25 minutes of script budget must stay inside the step's 30 minutes.
        self.assertIn("1500", script)
        # Privileged phases keep `timeout` on the root side of sudo: wrapping
        # sudo from the outside would leave the root-owned driver and its
        # grandchildren with nothing left to bound or reap them.
        self.assertIn(
            'sudo timeout --signal=TERM --kill-after=30s "${allowed}s" "$@"',
            script,
        )
        self.assertIn('if [ "$privilege" = "root" ]; then', script)
        # The driver and its privileged self-test are the two phases that must
        # run this way.
        self.assertIn('run_phase "native-lifecycle-driver" "$driver_budget" root', script)
        self.assertIn('run_phase "reap-driver-tree-self-test" 300 root', script)

    def test_extracted_script_keeps_producers_off_the_captured_pipe(self):
        script = self.STEP_SCRIPT.read_text(encoding="utf-8")
        # Each helper detaches stdin and writes to a file, never to the step's
        # captured stdout, so a surviving privileged child cannot pin the pipe.
        for helper in (
            'python3 scripts/ci/l2tp-kernel-module-resolver-test.py </dev/null >>"$provision_log" 2>&1',
            'python3 scripts/ci/l2tp-reap-driver-tree-test.py </dev/null >>"$provision_log" 2>&1',
            'bash scripts/ci/ensure-l2tp-kernel-module.sh </dev/null >>"$provision_log" 2>&1',
            'bash scripts/ci/native-l2tp-driver.sh </dev/null >"$driver_log" 2>&1',
        ):
            self.assertIn(helper, script)
        # The logs are printed afterwards so the step output stays diagnostic.
        self.assertIn('cat "$provision_log"', script)
        self.assertIn('cat "$driver_log"', script)
        self.assertIn('emit_failure_annotation "$provision_log" "native L2TP kernel provisioning failed"', script)
        self.assertIn('emit_failure_annotation "$driver_log" "native L2TP driver failed"', script)
        # The tree is always reaped and the real status is always propagated.
        self.assertIn("bash scripts/ci/l2tp-reap-driver-tree.sh || true", script)
        self.assertIn('exit "$status"', script)
        self.assertIn('exit "$provision_status"', script)
        # The reaper is regression-tested from this same step.
        self.assertIn("python3 scripts/ci/l2tp-reap-driver-tree-test.py", script)


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


class BoundedL2TPRunnerTest(unittest.TestCase):
    """The shared bounded runner must never outlive its deadline."""

    RUNNER = (
        pathlib.Path(__file__).resolve().parents[2]
        / "scripts"
        / "ci"
        / "l2tp-bounded-run.sh"
    )

    def _fixture(self):
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="l2tp-bounded-run-"))
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        ci_dir = tmp / "scripts" / "ci"
        ci_dir.mkdir(parents=True)
        # Hermetic stand-in for the real reaper, which signals daemons by name.
        (ci_dir / "l2tp-reap-driver-tree.sh").write_text(
            "#!/usr/bin/env bash\necho reaped\n"
        )
        return tmp

    def test_a_wedged_command_is_reported_as_a_timeout(self):
        tmp = self._fixture()
        log = tmp / "run.log"
        status = tmp / "run.status"
        started = time.monotonic()
        result = subprocess.run(
            ["bash", str(self.RUNNER), str(log), str(status), "6", "sleep", "600"],
            cwd=str(tmp),
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=120,
        )
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 90, "the runner must not wait for a wedged command")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertIn("reaped", result.stdout)
        subprocess.run(["pkill", "-9", "-f", "sleep 600"], check=False, capture_output=True)

    def test_the_command_status_is_forwarded(self):
        tmp = self._fixture()
        result = subprocess.run(
            [
                "bash", str(self.RUNNER), str(tmp / "run.log"), str(tmp / "run.status"),
                "60", "bash", "-c", "echo from-driver; exit 3",
            ],
            cwd=str(tmp),
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=120,
        )
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertIn("from-driver", result.stdout)

class StepBodyBoundingTest(unittest.TestCase):
    """The workflow step must return even when the lifecycle never does.

    This is the regression that the previous attempts were missing. A process
    stuck in an uninterruptible kernel path cannot be signalled away, so if the
    step waits for the lifecycle it never finishes: the job then runs to its
    45-minute cap, GitHub cancels it, and the log and artifacts are discarded.
    The body below is the real one from the workflow, run against a lifecycle
    stub that never exits.
    """

    def _step_body(self):
        return WorkflowIsolationTest()._l2tp_step_body()

    def test_step_returns_and_reports_a_timeout_when_the_lifecycle_wedges(self):
        body = self._step_body()
        self.assertIn("setsid", body)
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="l2tp-step-body-"))
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        ci_dir = tmp / "scripts" / "ci"
        ci_dir.mkdir(parents=True)
        # Stands in for a lifecycle that is wedged for good.
        (ci_dir / "native-l2tp-lifecycle-step.sh").write_text(
            "#!/usr/bin/env bash\nsleep 600\n"
        )
        (ci_dir / "l2tp-reap-driver-tree.sh").write_text("#!/usr/bin/env bash\nexit 0\n")
        env = dict(os.environ)
        env["RUNNER_TEMP"] = str(tmp)
        env["ANTIMAGE_L2TP_STEP_DEADLINE_SECONDS"] = "8"
        started = time.monotonic()
        result = subprocess.run(
            ["bash", "-c", body],
            cwd=str(tmp),
            env=env,
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=120,
        )
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 90, "the step must not wait for the lifecycle")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertIn("=== lifecycle step log ===", result.stdout)
        self.assertIn("::error title=native L2TP lifecycle step failed::", result.stdout)
        subprocess.run(
            ["pkill", "-9", "-f", "sleep 600"],
            check=False,
            capture_output=True,
        )

class ExtractedStepBoundingTest(unittest.TestCase):
    """The extracted step must bound itself and always return.

    A step that hands its lifetime to the runner produces no log, no artifact and
    no annotation: once the job passes its cap GitHub discards both. The script
    therefore gives every phase its own limit out of one shared step budget, so a
    wedged phase ends the script with a real status instead of the runner.
    """

    def test_a_wedged_phase_is_bounded_and_the_script_exits(self):
        script = (
            pathlib.Path(__file__).resolve().parents[2]
            / "scripts"
            / "ci"
            / "native-l2tp-lifecycle-step.sh"
        )
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="l2tp-bound-"))
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        stub = tmp / "bin"
        stub.mkdir()
        # Stands in for a wedged provisioning helper: it never returns, so only
        # the phase's own bound can end it.
        wedged = stub / "python3"
        wedged.write_text("#!/usr/bin/env bash\nsleep 600\n")
        wedged.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = f"{stub}:/usr/bin:/bin"
        env["RUNNER_TEMP"] = str(tmp)
        env["ANTIMAGE_L2TP_STEP_SECONDS"] = "12"
        env["ANTIMAGE_L2TP_STEP_RESERVE_SECONDS"] = "0"
        started = time.monotonic()
        result = subprocess.run(
            ["bash", str(script)],
            cwd=str(script.parents[2]),
            env=env,
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=60,
        )
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 45, "the script must bound a wedged phase itself")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertIn("=== phase kernel-index-resolver", result.stdout)

if __name__ == "__main__":
    unittest.main(verbosity=2)
