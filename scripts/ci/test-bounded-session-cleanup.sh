#!/usr/bin/env bash
# Regression test for the native-driver lifecycle.
#
# GitHub Actions cancels a step by signalling the step's whole process group.
# Historically the native driver re-execs through "unshare --mount --fork", so
# that signal reached only the unshare monitor while the driver shell and its
# pppd/xl2tpd/charon daemons survived as orphans, keeping the step's stdio
# pipes open. The runner then never marked the step done and the job hung until
# the job-level timeout with no evidence uploaded.
#
# This test emulates that cancellation against a stub driver that has the exact
# production ownership shape (a dedicated mount + PID namespace, plus daemons
# that ignore SIGTERM). It fails if any descendant outlives the wrapper.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="$SCRIPT_DIR/run-bounded-session.sh"
WORK="$(mktemp -d)"
STUB_PATTERN='AMSTUBXL2TPD|AMSTUBPPPD'

cleanup() {
  pkill -9 -f "$STUB_PATTERN" 2>/dev/null || true
  pkill -9 -f "$WORK/stub-driver.sh" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

assert_no_survivors() {
  local stage="$1" leftover
  leftover="$(pgrep -f "$STUB_PATTERN" 2>/dev/null | tr '\n' ' ' || true)"
  [ -z "$leftover" ] || fail "$stage: daemons survived (pids: $leftover)"
  leftover="$(pgrep -f "$WORK/stub-driver.sh" 2>/dev/null | tr '\n' ' ' || true)"
  [ -z "$leftover" ] || fail "$stage: stub driver survived (pids: $leftover)"
}

cat >"$WORK/stub-driver.sh" <<'DRV'
#!/usr/bin/env bash
set -euo pipefail
if [ "${STUB_NS:-0}" != 1 ]; then
  export STUB_NS=1
  # Same ownership model as the real driver: a dedicated mount AND PID
  # namespace, so no daemon that ignores SIGTERM can outlive the leader.
  exec unshare --mount --pid --fork --mount-proc --kill-child=SIGKILL --propagation private bash "$0" "$@"
fi
# Daemons that ignore SIGTERM and inherit stdio, exactly like pppd/xl2tpd/charon.
bash -c 'trap "" TERM; exec -a AMSTUBXL2TPD sleep 900' &
bash -c 'trap "" TERM; exec -a AMSTUBPPPD   sleep 900' &
sleep 900
DRV
chmod +x "$WORK/stub-driver.sh"

# Start the wrapper in its own process group, the way the runner starts a step.
start_driver_group() {
  local bound="$1" log="$2" pgfile="$3"
  setsid bash -c 'echo $$ >"$1"; shift; exec "$@"' _ "$pgfile" \
    bash "$RUNNER" "$bound" bash "$WORK/stub-driver.sh" >"$log" 2>&1 &
  echo $!
}

echo '=== timeout path: bounded exit with no survivors ===' >&2
pgfile="$WORK/pg.timeout"
start_driver_group 3 "$WORK/timeout.log" "$pgfile" >/dev/null
wait_start="$(date +%s)"
while [ ! -s "$pgfile" ]; do
  [ $(( $(date +%s) - wait_start )) -lt 10 ] || fail "timeout path: driver group never started"
  sleep 0.1
done
pgid="$(cat "$pgfile")"
# The bound must be wall-clock, not "sleep 0.2" iterations: under CI load an
# iteration-counted bound stretched a 900s limit into 35+ minutes and reached
# the job timeout instead of failing fast. Assert the wrapper honours a 3s
# bound within a small margin even while the stub driver ignores SIGTERM.
start="$(date +%s)"
while kill -0 -- "-$pgid" 2>/dev/null; do
  [ $(( $(date +%s) - start )) -lt 25 ] || fail "timeout path was not bounded in wall-clock time"
  sleep 0.2
done
elapsed=$(( $(date +%s) - start ))
[ "$elapsed" -le 22 ] || fail "timeout path took ${elapsed}s for a 3s bound"
echo "timed-out after ${elapsed}s for a 3s bound (wall-clock)" >&2
sleep 1
assert_no_survivors "timeout path"

echo '=== SIGTERM cancellation of the whole group ===' >&2
pgfile="$WORK/pg.term"
start_driver_group 600 "$WORK/term.log" "$pgfile" >/dev/null
wait_start="$(date +%s)"
while [ ! -s "$pgfile" ]; do
  [ $(( $(date +%s) - wait_start )) -lt 10 ] || fail "SIGTERM path: driver group never started"
  sleep 0.1
done
pgid="$(cat "$pgfile")"
sleep 2
kill -TERM -- "-$pgid" 2>/dev/null || true
start="$(date +%s)"
while kill -0 -- "-$pgid" 2>/dev/null; do
  [ $(( $(date +%s) - start )) -lt 30 ] || fail "SIGTERM cancellation did not exit within 30s"
  sleep 0.2
done
sleep 1
assert_no_survivors "SIGTERM path"

echo '=== SIGKILL cancellation of the whole group ===' >&2
pgfile="$WORK/pg.kill"
start_driver_group 600 "$WORK/kill.log" "$pgfile" >/dev/null
wait_start="$(date +%s)"
while [ ! -s "$pgfile" ]; do
  [ $(( $(date +%s) - wait_start )) -lt 10 ] || fail "SIGKILL path: driver group never started"
  sleep 0.1
done
pgid="$(cat "$pgfile")"
sleep 2
kill -KILL -- "-$pgid" 2>/dev/null || true
sleep 2
assert_no_survivors "SIGKILL path"

echo 'bounded-session cleanup regression: PASS' >&2
