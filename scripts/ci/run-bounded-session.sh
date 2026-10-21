#!/usr/bin/env bash
# Bound a command's runtime and guarantee that no descendant outlives it.
#
# Why this exists: GitHub Actions cancels a step by signalling the step's whole
# process group. The native drivers re-exec through "unshare --mount --pid
# --fork --mount-proc --kill-child", so the kernel destroys every process in
# that PID namespace when the namespace init (unshare) dies. This wrapper must
# therefore NOT put the command in a new session: if it did, the command would
# escape the runner's process-group kill and survive as an orphan holding the
# step's stdio pipes open - exactly the hang this is meant to prevent.
#
# The wrapper itself only needs to (a) stop the command after a bounded time and
# (b) relay its real exit status. Killing the namespace init is enough to tear
# down the entire namespace, so no descendant can outlive the step.
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
  local i=0
  kill -TERM "$cmd_pid" 2>/dev/null || true
  while [ "$i" -lt "$(( grace_s * 10 ))" ]; do
    kill -0 "$cmd_pid" 2>/dev/null || return 0
    sleep 0.1
    i=$(( i + 1 ))
  done
  # SIGKILLing the namespace init makes the kernel destroy the whole PID
  # namespace, so descendants cannot survive even if they ignore SIGTERM.
  kill -KILL "$cmd_pid" 2>/dev/null || true
  i=0
  while [ "$i" -lt 100 ]; do
    kill -0 "$cmd_pid" 2>/dev/null || return 0
    sleep 0.1
    i=$(( i + 1 ))
  done
  return 0
}

max_ticks=$(( timeout_s * 10 ))
i=0
while :; do
  if ! kill -0 "$cmd_pid" 2>/dev/null; then
    wait "$cmd_pid" 2>/dev/null
    exit $?
  fi
  if [ "$i" -ge "$max_ticks" ]; then
    echo "run-bounded-session: timeout after ${timeout_s}s; terminating command namespace" >&2
    terminate_command
    wait "$cmd_pid" 2>/dev/null || true
    exit 124
  fi
  sleep 0.1
  i=$(( i + 1 ))
done
