#!/usr/bin/env python3
"""Process-level regression test for the native L2TP tree reaper.

Reproduces the hang that costs the whole job: a driver descendant that ignores
SIGTERM (which is what `timeout --signal=TERM` sends) plus a privileged daemon
left running. If those survive, the step's captured pipe never closes and the
job cannot be finished or timed out, which is why the native-l2tp job produced
no logs and no artifact.

The reaper in scripts/ci/l2tp-reap-driver-tree.sh must kill both, so this test
starts them, runs the reaper, and asserts nothing survives. It needs root
because the reaper uses sudo, and skips cleanly where that is unavailable.
"""

from __future__ import annotations

import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
REAPER = REPO_ROOT / "scripts" / "ci" / "l2tp-reap-driver-tree.sh"


def have_root() -> bool:
    if os.geteuid() == 0:
        return True
    return subprocess.run(["sudo", "-n", "true"], capture_output=True).returncode == 0


def wait_gone(procs: list[subprocess.Popen], deadline_s: float = 20.0) -> list[int]:
    # poll() reaps the child, so a killed process counts as gone once it is
    # collected; os.kill(pid, 0) would keep reporting a zombie as alive.
    end = time.monotonic() + deadline_s
    while time.monotonic() < end:
        remaining = [proc.pid for proc in procs if proc.poll() is None]
        if not remaining:
            return []
        time.sleep(0.2)
    return [proc.pid for proc in procs if proc.poll() is None]


def main() -> int:
    if not have_root():
        print("l2tp reap test: skipped (needs root)")
        return 0

    with tempfile.TemporaryDirectory(prefix="l2tp-reap-") as work:
        work_path = pathlib.Path(work)

        # A driver descendant that refuses SIGTERM, named so the reaper's
        # command-line match must catch it.
        stubborn = subprocess.Popen(
            ["bash", "-c", 'trap "" TERM; exec -a scripts/ci/native-l2tp-driver.sh sleep 300'],
            start_new_session=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        # A "daemon" whose process name is exactly xl2tpd. It has to be set
        # through prctl: copying a coreutils binary to that name fails because
        # coreutils dispatches programs by argv[0].
        daemon = subprocess.Popen(
            [
                sys.executable,
                "-c",
                "import ctypes, time;"
                "ctypes.CDLL(None).prctl(15, b'xl2tpd', 0, 0, 0);"
                "time.sleep(300)",
            ],
            start_new_session=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        time.sleep(0.5)
        comm = pathlib.Path(f"/proc/{daemon.pid}/comm").read_text().strip()
        assert comm == "xl2tpd", f"test setup failed: daemon process name is {comm!r}"

        try:
            subprocess.run(
                ["sudo", "bash", str(REAPER)],
                check=True,
                env={**os.environ, "RUNNER_TEMP": str(work_path)},
                timeout=60,
            )
            survivors = wait_gone([stubborn, daemon])
            assert not survivors, f"reaper left processes alive: {survivors}"
        finally:
            for proc in (stubborn, daemon):
                if proc.poll() is None:
                    try:
                        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
                    except (ProcessLookupError, PermissionError):
                        pass
                proc.wait(timeout=10)

    print("l2tp reap test: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
