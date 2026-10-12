import json
import tempfile
import unittest
from pathlib import Path

from native_l2tp_session_state import denial_has_no_session_state, has_new_active_session, snapshot_processes


class NativeL2TPSessionStateTest(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.active = self.root / "l2tp" / "native" / "ppp-accounting" / "active"
        self.active.mkdir(parents=True)

    def tearDown(self):
        self.tempdir.cleanup()

    def write_session(self, name, process, client_ip):
        (self.active / name).write_text(
            json.dumps({"Process": process, "ClientIP": client_ip, "UserID": 7}),
            encoding="utf-8",
        )

    def test_old_source_session_does_not_satisfy_new_peer_wait(self):
        self.write_session("ppp0-old.json", "boot:100:1", "10.251.0.2")
        before = snapshot_processes(self.root)

        self.assertFalse(has_new_active_session(self.root, before, "10.252.0.2"))

        self.write_session("ppp1-new.json", "boot:200:1", "10.252.0.2")
        self.assertTrue(has_new_active_session(self.root, before, "10.252.0.2"))

    def test_old_same_peer_session_does_not_satisfy_reconnect_wait(self):
        self.write_session("ppp0-old.json", "boot:100:1", "10.252.0.2")
        before = snapshot_processes(self.root)

        self.assertFalse(has_new_active_session(self.root, before, "10.252.0.2"))

        self.write_session("ppp1-new.json", "boot:200:1", "10.252.0.2")
        self.assertTrue(has_new_active_session(self.root, before, "10.252.0.2"))

    def test_denied_peer_must_leave_no_active_or_pending_state(self):
        denial_dir = self.root / "l2tp" / "native" / "ppp-accounting" / "admission-denials"
        denial_dir.mkdir(parents=True)
        (denial_dir / "7.json").write_text(
            json.dumps({"client_ip": "10.252.0.2", "process": "boot:200:1", "reason": "device limit reached"}),
            encoding="utf-8",
        )
        self.assertTrue(denial_has_no_session_state(self.root, "10.252.0.2", "device limit reached"))

        pending = self.root / "ppp-admission" / "pending"
        pending.mkdir(parents=True)
        (pending / "denied.json").write_text(
            json.dumps({"Process": "boot:200:1", "ClientIP": "10.252.0.2"}),
            encoding="utf-8",
        )
        self.assertFalse(denial_has_no_session_state(self.root, "10.252.0.2", "device limit reached"))

        (pending / "denied.json").unlink()
        self.write_session("ppp1-denied.json", "boot:200:1", "10.252.0.2")
        self.assertFalse(denial_has_no_session_state(self.root, "10.252.0.2", "device limit reached"))


if __name__ == "__main__":
    unittest.main()
