#!/usr/bin/env bash
# Bound a command's runtime and guarantee that no descendant outlives it.
#
# Why this exists: GitHub Actions cancels a step by signalling the step's whole
# process group. The native drivers re-exec through "unshare --mount --pid
# --fork --mount-proc --kill-child", so the kernel destroys every process in
# that PID namespace when the namespace init (unshare) is killed. This wrapper
# must therefore NOT put the command in a new session: if it did, the command
# would escape the runner's process-group kill and survive as an orphan holding
# the step's stdio pipes open - exactly the hang this is meant to prevent.
#
# The wrapper itself only needs to (a) stop the command after a bounded time and
# (b) relay its real exit status.
#
# Two details matter and were both wrong at first:
#
#   * The bound must be wall-clock. An earlier version counted loop iterations,
#     so a loaded runner stretched a 900s bound into 35+ minutes and blew the
#     job timeout instead of failing fast.
#
#   * The namespace init must be SIGKILLed, not SIGTERMed. Measured against
#     real "unshare --mount --pid --fork": SIGTERM to the monitor leaves the
#     monitor and the whole namespace running (it only works if the inner init
#     cooperates, which a stuck driver does not), while SIGKILL makes the kernel
#     destroy the namespace and the step's stdio pipes finally close. The bound
#     therefore SIGTERMs only briefly and then SIGKILLs.
#
# Usage: run-bounded-session.sh <timeout-seconds> <command> [args...]
set -uo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <timeout-seconds> <command> [args...]" >&2
  exit 64
fi

timeout_s="$1"; shift
grace_s="${ANTIMAGE_SESSION_KILL_GRACE:-15}"

# Start the command as a direct child so it stays inside this step's process
# group and the runner can reach it on cancellation.
"$@" &
cmd_pid=$!

trap 'terminate_command; exit 143' TERM INT HUP

terminate_command() {
  # A cooperative shutdown would be nicer, but the failure mode this wrapper
  # exists for is a driver that is already stuck. SIGTERM to an "unshare --pid"
  # monitor does not tear the namespace down, so ask politely for one instant
  # and then SIGKILL, which the kernel turns into "destroy the whole pid ns".
  kill -TERM "$cmd_pid" 2>/dev/null || true
  local deadline=$(( $(date +%s) + 2 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kill -0 "$cmd_pid" 2>/dev/null || return 0
    sleep 0.05
  done
  # SIGKILLing the namespace init makes the kernel destroy the whole PID
  # namespace, so descendants cannot survive even if they ignore SIGTERM.
  kill -KILL "$cmd_pid" 2>/dev/null || true
  deadline=$(( $(date +%s) + grace_s ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kill -0 "$cmd_pid" 2>/dev/null || return 0
    sleep 0.1
  done
  return 0
}

started_at=$(date +%s)
while :; do
  if ! kill -0 "$cmd_pid" 2>/dev/null; then
    wait "$cmd_pid" 2>/dev/null
    exit $?
  fi
  elapsed=$(( $(date +%s) - started_at ))
  if [ "$elapsed" -ge "$timeout_s" ]; then
    echo "run-bounded-session: timeout after ${timeout_s}s (elapsed ${elapsed}s); terminating command namespace" >&2
    terminate_command
    wait "$cmd_pid" 2>/dev/null || true
    exit 124
  fi
  sleep 0.2
done
