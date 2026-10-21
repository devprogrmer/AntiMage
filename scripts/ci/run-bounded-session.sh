#!/usr/bin/env bash
# Run a command inside its own session/process group and guarantee that the
# entire process tree is terminated when the command finishes, when a bounded
# timeout expires, or when this wrapper is asked to stop.
#
# Why this exists: "timeout --kill-after" only signals its direct child. The
# native drivers re-exec through "unshare --mount --fork", so the direct child
# is the unshare monitor while the real driver shell and the pppd/xl2tpd/charon
# daemons it spawns live on as grandchildren. Those orphans keep the CI step's
# process group (and its stdio pipes) open, so the runner never marks the step
# done and the job hangs until the job-level timeout. Owning a dedicated
# session lets us signal the whole tree instead of only the direct child.
#
# Usage: run-bounded-session.sh <timeout-seconds> <command> [args...]
set -uo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <timeout-seconds> <command> [args...]" >&2
  exit 64
fi

timeout_s="$1"; shift
grace_s="${ANTIMAGE_SESSION_KILL_GRACE:-10}"

own_pgid="$(ps -o pgid= -p "$$" 2>/dev/null | tr -d ' ')"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/antimage-session-XXXXXX")"
pgid_file="$work_dir/pgid"
status_file="$work_dir/status"

# Start the command as a brand new session leader. The leader publishes both
# its process-group id (its own pid) and, once the command returns, its exit
# status. Using a status file instead of "$!" keeps this correct whether or not
# "setsid" had to fork, and lets us report the real command exit code.
setsid bash -c '
  pgid_file="$1"; status_file="$2"; shift 2
  printf "%s\n" "$$" >"$pgid_file"
  "$@"
  rc=$?
  printf "%s\n" "$rc" >"$status_file"
  exit "$rc"
' _ "$pgid_file" "$status_file" "$@" &

pgid=""
for _ in $(seq 1 200); do
  if [ -s "$pgid_file" ]; then
    pgid="$(cat "$pgid_file" 2>/dev/null || true)"
    [ -n "$pgid" ] && break
  fi
  sleep 0.05
done

killable_group() { [ -n "$pgid" ] && [ "$pgid" != "$own_pgid" ]; }

group_alive() {
  killable_group && kill -0 -- "-$pgid" 2>/dev/null
}

descendants() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    descendants "$child"
    echo "$child"
  done
}

kill_tree() {
  local signal="$1" pid
  if killable_group; then kill "-$signal" -- "-$pgid" 2>/dev/null || true; fi
  if [ -n "$pgid" ]; then
    for pid in $(descendants "$pgid"); do kill "-$signal" "$pid" 2>/dev/null || true; done
    kill "-$signal" "$pgid" 2>/dev/null || true
  fi
}

terminate_tree() {
  kill_tree TERM
  local i=0
  while [ "$i" -lt "$(( grace_s * 10 ))" ]; do
    group_alive || return 0
    sleep 0.1
    i=$(( i + 1 ))
  done
  kill_tree KILL
  i=0
  while [ "$i" -lt 100 ]; do
    group_alive || return 0
    sleep 0.1
    i=$(( i + 1 ))
  done
  return 0
}

on_signal() {
  terminate_tree
  trap - TERM INT HUP
  exit 143
}
trap on_signal TERM INT HUP

rc=0
max_ticks=$(( timeout_s * 10 ))
i=0
while :; do
  if [ -s "$status_file" ]; then
    rc="$(cat "$status_file" 2>/dev/null || echo 0)"
    break
  fi
  if ! group_alive; then
    # The leader is gone; give the status file one more chance, else the
    # command died without reporting (for example killed by a signal).
    sleep 0.2
    if [ -s "$status_file" ]; then
      rc="$(cat "$status_file" 2>/dev/null || echo 0)"
    else
      rc=1
    fi
    break
  fi
  if [ "$i" -ge "$max_ticks" ]; then
    echo "run-bounded-session: timeout after ${timeout_s}s; terminating process tree" >&2
    terminate_tree
    rc=124
    break
  fi
  sleep 0.1
  i=$(( i + 1 ))
done

# Even on a clean exit, make sure no orphaned daemon survived and kept the
# step's descriptors open.
terminate_tree
rm -rf "$work_dir"
trap - TERM INT HUP
exit "$rc"
