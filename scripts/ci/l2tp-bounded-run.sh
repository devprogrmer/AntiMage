#!/usr/bin/env bash
# Run the native L2TP driver so that a wedged driver cannot wedge the step.
#
# `timeout` only signals its direct child, and a process parked in an
# uninterruptible kernel path cannot be signalled away at all. When that
# happens the step body never returns, the job runs to its cap, GitHub cancels
# it and both the log and every artifact are discarded - which is why earlier
# native-l2tp failures produced no diagnosable output.
#
# The driver therefore runs in its own session with stdout and stderr on a
# file, never on the caller's captured stdout, and this script waits for it
# with its own deadline instead of waiting on the process itself.
#
# Usage: l2tp-bounded-run.sh <log> <status-file> <deadline-seconds> <command...>
set -uo pipefail

if [ "$#" -lt 4 ]; then
  echo "usage: $0 <log> <status-file> <deadline-seconds> <command...>" >&2
  exit 2
fi

log="$1"
status_file="$2"
deadline_seconds="$3"
shift 3

case "$deadline_seconds" in
  ''|*[!0-9]*)
    echo "deadline must be a whole number of seconds, got '$deadline_seconds'" >&2
    exit 2
    ;;
esac

rm -f "$status_file"
: >"$log"

# `bash -c` re-executes the caller's command unchanged. Detaching the session
# keeps the caller's stdout pipe out of the driver's process tree, and detaching
# stdin stops the privileged helpers from holding a terminal.
L2TP_STATUS_FILE="$status_file" setsid bash -c '
  rc=0
  "$@" || rc=$?
  printf "%s" "$rc" > "$L2TP_STATUS_FILE"
' l2tp-bounded-run "$@" >"$log" 2>&1 </dev/null &
pid=$!

deadline=$(( $(date +%s) + deadline_seconds ))
while [ ! -s "$status_file" ]; do
  kill -0 "$pid" 2>/dev/null || break
  [ "$(date +%s)" -lt "$deadline" ] || break
  sleep 5
done

# Reap before reading the log so no surviving daemon still holds it open.
bash scripts/ci/l2tp-reap-driver-tree.sh || true

status=0
if [ -s "$status_file" ]; then
  status="$(command head -c 16 "$status_file" | command tr -dc '0-9')"
  [ -n "$status" ] || status=1
else
  # Still running past the deadline: stop the whole session and report a
  # timeout. The test is not skipped or weakened, only bounded.
  sudo kill -KILL -- "-$pid" 2>/dev/null || true
  kill -KILL "$pid" 2>/dev/null || true
  status=124
fi

command tail -c 200000 "$log" || true
exit "$status"
