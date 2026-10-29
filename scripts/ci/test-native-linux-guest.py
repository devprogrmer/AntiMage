#!/usr/bin/env python3
"""Exercise the real guest-owner cleanup without downloading or booting a VM."""
import pathlib
import os
import signal
import subprocess
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("native-linux-guest.sh")


class GuestOwnerTest(unittest.TestCase):
    def owner(self, cancellation):
        source = SCRIPT.read_text()
        cleanup = source[source.index("cleanup() {"):source.index("image='")]
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            for name in ("key", "key.pub", "disk.qcow2", "seed.img", "base.img"):
                (root / name).touch()
            body = ('root="$1"; evidence="$root/evidence"; mkdir "$evidence"; '
                    'sleep 300 & vm_pid=$!; sleep 300 & test_pid=$!;\n' + cleanup +
                    '\nprintf "ready\\n"; ' + ('wait' if cancellation else 'exit 7'))
            proc = subprocess.Popen(["bash", "-c", body, "test", str(root)],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                    start_new_session=True)
            try:
                self.assertEqual(proc.stdout.readline(), "ready\n")
                if cancellation:
                    proc.send_signal(signal.SIGTERM)
                stdout, stderr = proc.communicate(timeout=8)
                self.assertEqual(proc.returncode, 143 if cancellation else 7, stderr)
                # Descendants inherited the pipe: communicate returning also
                # proves no surviving VM/SSH process keeps stdout open.
                self.assertFalse((root / "key").exists())
                self.assertFalse((root / "disk.qcow2").exists())
                self.assertTrue((root / "evidence").is_dir())
            finally:
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                if proc.poll() is None:
                    proc.kill()
                    proc.communicate(timeout=8)

    def test_normal_failure_reaps_vm_and_ssh_and_keeps_status(self):
        self.owner(cancellation=False)

    def test_cancellation_reaps_vm_and_ssh_and_closes_pipes(self):
        self.owner(cancellation=True)


if __name__ == "__main__":
    unittest.main()
