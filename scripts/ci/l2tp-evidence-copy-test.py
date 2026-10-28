#!/usr/bin/env python3
"""Regression test for the L2TP failure-evidence copy.

When the native L2TP driver fails, its EXIT trap copies the whole run tree into
``$RUNNER_TEMP/antimage-l2tp-failure`` for the workflow to upload. That tree
contains the control FIFOs created by the driver's private xl2tpd instances
(``client.control`` and ``client-b.control``). ``actions/upload-artifact``
matches ``antimage-*-failure/**`` and reads every entry, and reading a FIFO
blocks until a writer appears, so the upload step stalled forever and the job
died at its 45-minute timeout without logs or an artifact.

This test builds a tree shaped like a failed run -- regular files, a nested
directory, a resolvable symlink, a dangling symlink, FIFOs and a unix socket --
and asserts the copied evidence is strictly uploadable: only regular files,
directories and resolvable symlinks remain, and every remaining entry can be
read to completion under a hard deadline.
"""

from __future__ import annotations

import os
import pathlib
import socket
import stat
import subprocess
import sys
import tempfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
COPY_SCRIPT = REPO_ROOT / "scripts" / "ci" / "l2tp-evidence-copy.sh"

# Reads every entry in the copied tree; a FIFO or socket would block here and
# trip the caller's subprocess timeout.
READER = """
import pathlib, sys
root = pathlib.Path(sys.argv[1])
for path in sorted(root.rglob("*")):
    if path.is_dir():
        continue
    try:
        with open(path, "rb") as handle:
            while handle.read(65536):
                pass
    except OSError:
        continue
"""


def build_source_tree(root: pathlib.Path) -> socket.socket:
    (root / "l2tp-state").mkdir(parents=True)
    (root / "l2tp-state" / "session-helper.json").write_text('{"ok": true}\n')
    (root / "xl2tpd-server.log").write_text("starting xl2tpd\n")
    # The control pipes the driver waits for with `test -p`.
    os.mkfifo(root / "client.control")
    os.mkfifo(root / "client-b.control")
    # strongSwan's control socket, copied verbatim by `cp -a`.
    control = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    control.bind(str(root / "client-ipsec-run.ctl"))
    control.listen(1)
    os.symlink("xl2tpd-server.log", root / "server.log.link")
    os.symlink("removed-after-failure.log", root / "dangling.link")
    return control


def assert_uploadable(dst: pathlib.Path) -> None:
    special: list[str] = []
    dangling: list[str] = []
    for path in dst.rglob("*"):
        mode = path.lstat().st_mode
        if stat.S_ISFIFO(mode) or stat.S_ISSOCK(mode) or stat.S_ISBLK(mode) or stat.S_ISCHR(mode):
            special.append(str(path))
        elif stat.S_ISLNK(mode) and not path.exists():
            dangling.append(str(path))
    assert not special, f"non-uploadable entries survived the evidence copy: {special}"
    assert not dangling, f"dangling symlinks survived the evidence copy: {dangling}"

    try:
        subprocess.run(
            [sys.executable, "-c", READER, str(dst)],
            check=True,
            timeout=20,
        )
    except subprocess.TimeoutExpired as exc:
        raise AssertionError("reading the evidence tree blocked; the upload would hang") from exc

    assert (dst / "l2tp-state" / "session-helper.json").read_text() == '{"ok": true}\n'
    assert (dst / "xl2tpd-server.log").read_text() == "starting xl2tpd\n"
    assert (dst / "server.log.link").read_text() == "starting xl2tpd\n"


def main() -> int:
    with tempfile.TemporaryDirectory(prefix="l2tp-evidence-") as work:
        work_path = pathlib.Path(work)
        src = work_path / "run"
        dst = work_path / "evidence"
        src.mkdir()
        control = build_source_tree(src)
        try:
            subprocess.run(["bash", str(COPY_SCRIPT), str(src), str(dst)], check=True)
            assert_uploadable(dst)
        finally:
            control.close()
    print("l2tp evidence copy: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
