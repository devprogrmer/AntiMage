#!/usr/bin/env bash
# Regression test: a driver that re-execs through "unshare --fork" and spawns
# daemons that ignore SIGTERM must still be terminated in full by
# run-bounded-session.sh, on the timeout path and the cancellation path.
# Before this harness existed those daemons survived and pinned the CI step
# open until the job timeout.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="$SCRIPT_DIR/run-bounded-session.sh"
WORK="$(mktemp -d)"
STUB_DAEMONS='AMSTUBXL2TPD|AMSTUBPPPD'

cleanup() {
  pkill -9 -f "$STUB_DAEMONS" 2>/dev/null || true
  pkill -9 -f "$WORK/stub-driver.sh" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

assert_no_survivors() {
  local stage="$1" leftover
  leftover="$(pgrep -f "$STUB_DAEMONS" 2>/dev/null | tr '\n' ' ' || true)"
  [ -z "$leftover" ] || fail "$stage: daemons survived (pids: $leftover)"
  leftover="$(pgrep -f "$WORK/stub-driver.sh" 2>/dev/null | tr '\n' ' ' || true)"
  [ -z "$leftover" ] || fail "$stage: stub driver survived (pids: $leftover)"
}

# Mimic the real driver's ownership: re-exec into a mount namespace via
# "unshare --mount --fork", then spawn daemons that ignore SIGTERM and inherit
# the wrapper's stdio (exactly the processes that used to pin the CI step).
cat >"$WORK/stub-driver.sh" <<'DRV'
#!/usr/bin/env bash
set -euo pipefail
if [ "${STUB_PRIV:-0}" != 1 ]; then
  export STUB_PRIV=1
  exec unshare --mount --fork --kill-child=SIGKILL --propagation private bash "$0" "$@"
fi
bash -c 'trap "" TERM; exec -a AMSTUBXL2TPD sleep 900' &
bash -c 'trap "" TERM; exec -a AMSTUBPPPD   sleep 900' &
trap 'echo stub-cleanup >&2' EXIT
sleep 900
DRV
chmod +x "$WORK/stub-driver.sh"

echo '=== timeout path: bounded exit and no survivors ===' >&2
start="$(date +%s)"
status=0
bash "$RUNNER" 3 bash "$WORK/stub-driver.sh" >"$WORK/timeout.log" 2>&1 || status=$?
elapsed=$(( $(date +%s) - start ))
[ "$status" -eq 124 ] || fail "timeout path expected exit 124, got $status"
[ "$elapsed" -lt 25 ] || fail "timeout path was not bounded (took ${elapsed}s)"
assert_no_survivors "timeout path"

echo '=== mid-run cancellation: bounded exit and no survivors ===' >&2
bash "$RUNNER" 600 bash "$WORK/stub-driver.sh" >"$WORK/cancel.log" 2>&1 &
runner=$!
sleep 3
kill -TERM "$runner" 2>/dev/null || true
start="$(date +%s)"
while kill -0 "$runner" 2>/dev/null; do
  [ $(( $(date +%s) - start )) -lt 20 ] || fail "mid-run cancellation did not exit within 20s"
  sleep 0.2
done
wait "$runner" 2>/dev/null || true
assert_no_survivors "cancellation path"

echo 'bounded-session cleanup regression: PASS' >&2
