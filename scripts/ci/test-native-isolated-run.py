#!/usr/bin/env python3
"""Check real Linux namespace ownership, exit codes and cancellation cleanup."""
import os
import pathlib
import signal
import subprocess
import sys
import time
import unittest
import uuid

SCRIPT = pathlib.Path(__file__).with_name("native-isolated-run.py")


def marker_pids(marker):
    found = []
    for entry in pathlib.Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            first_arg = (entry / "cmdline").read_bytes().split(b"\0")[0]
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        if first_arg == marker.encode():
            found.append(int(entry.name))
    return found


class NativeIsolationTest(unittest.TestCase):
    def test_exit_status_and_private_network_and_run(self):
        host_net = os.readlink("/proc/self/ns/net")
        host_run = os.stat("/run").st_dev
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "bash", "-c",
             'readlink /proc/self/ns/net; stat -c %d /run; exit 7'],
            capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 7, result.stderr)
        net, run = result.stdout.splitlines()
        self.assertNotEqual(net, host_net)
        self.assertNotEqual(int(run), host_run)
        self.assertEqual(os.readlink("/proc/self/ns/net"), host_net)
        self.assertEqual(os.stat("/run").st_dev, host_run)

    def launch(self, wait):
        marker = "antimage-isolation-" + uuid.uuid4().hex
        # The orphan keeps stdout/stderr open and starts a separate session.
        # Killing a process group alone cannot clean up this descendant.
        body = ('setsid bash -c \'exec -a "$1" sleep 300\' child "$1" & '
                'printf "ready\\n"; ' + ('wait' if wait else 'sleep .3; exit 7'))
        proc = subprocess.Popen(
            [sys.executable, str(SCRIPT), "bash", "-c", body, "test", marker],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        return proc, marker

    def test_network_interfaces_are_visible_in_private_sysfs_only(self):
        name = "audit" + uuid.uuid4().hex[:8]
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "bash", "-euc",
             'ip link add "$1" type dummy; test -s "/sys/class/net/$1/ifindex"; '
             'cat "/sys/class/net/$1/ifindex"', "test", name],
            capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertGreater(int(result.stdout), 0)
        self.assertFalse(pathlib.Path("/sys/class/net", name).exists())

    def assert_clean(self, marker):
        self.assertEqual(marker_pids(marker), [], "orphan still holds runtime resources")

    def test_normal_exit_reaps_daemonized_pipe_holders(self):
        proc, marker = self.launch(wait=False)
        try:
            stdout, stderr = proc.communicate(timeout=5)
            self.assertEqual(proc.returncode, 7, stderr)
            self.assertIn("ready", stdout)
            self.assert_clean(marker)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.communicate(timeout=5)

    def test_cancellation_reaps_all_descendants_and_closes_pipes(self):
        self.check_cancellation(signal.SIGTERM)

    def test_supervisor_death_reaps_all_descendants_and_closes_pipes(self):
        self.check_cancellation(signal.SIGKILL)

    def check_cancellation(self, signum):
        proc, marker = self.launch(wait=True)
        try:
            deadline = time.monotonic() + 5
            while not marker_pids(marker) and time.monotonic() < deadline:
                time.sleep(.02)
            self.assertTrue(marker_pids(marker), "fixture never launched its descendant")
            proc.send_signal(signum)
            stdout, stderr = proc.communicate(timeout=5)
            self.assertNotEqual(proc.returncode, 0, stderr)
            self.assertIn("ready", stdout)
            self.assert_clean(marker)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
