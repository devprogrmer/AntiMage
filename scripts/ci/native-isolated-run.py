#!/usr/bin/env python3
"""Own disposable native-test daemons without sharing runner runtime state."""
import ctypes
import os
import signal
import subprocess
import sys


def parent_death_signal():
    # If even the supervisor is SIGKILLed, kill unshare; its --kill-child
    # then kills namespace init and Linux reaps every remaining descendant.
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(1, signal.SIGKILL, 0, 0, 0) != 0:
        raise OSError(ctypes.get_errno(), "PR_SET_PDEATHSIG failed")
    if os.getppid() == 1:
        os.kill(os.getpid(), signal.SIGKILL)


def main():
    if os.geteuid() != 0 or len(sys.argv) < 2:
        print("Usage: sudo python3 native-isolated-run.py COMMAND [ARG ...]", file=sys.stderr)
        return 2
    child = None
    cancelled = 0

    def kill_child():
        if child is not None:
            try:
                child.kill()
            except ProcessLookupError:
                pass

    def cancel(signum, _frame):
        nonlocal cancelled
        cancelled = signum
        # unshare --fork ignores SIGTERM while waiting. Kill its parent
        # explicitly, letting --kill-child and PID namespace teardown finish.
        kill_child()

    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, cancel)
    child = subprocess.Popen(
        ["unshare", "--mount", "--net", "--pid", "--fork",
         "--kill-child=SIGKILL", "--mount-proc", "bash", "-euc",
         'mount --make-rprivate /; mount -t sysfs -o nosuid,nodev,noexec sysfs /sys; '
         'mount -t tmpfs -o mode=755 tmpfs /run; '
         'mkdir -p /run/lock /run/netns; ip link set lo up; exec "$@"',
         "native-isolated-run", *sys.argv[1:]],
        start_new_session=True, preexec_fn=parent_death_signal,
    )
    try:
        if cancelled:
            kill_child()
        result = child.wait()
    finally:
        if child.poll() is None:
            kill_child()
        child.wait()
    return 128 + cancelled if cancelled else (result if result >= 0 else 128 - result)


if __name__ == "__main__":
    sys.exit(main())
