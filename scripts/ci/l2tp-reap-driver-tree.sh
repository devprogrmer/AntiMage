#!/usr/bin/env bash
# Kill everything the native L2TP driver may have left behind.
#
# `timeout` only signals the direct child, so the driver's privileged
# grandchildren (xl2tpd, pppd, charon) survive it. They keep their veth pairs
# and client namespaces and leave the host networking half-configured, which
# wedges the runner: the step body finishes but neither the step nor the job
# can end, so the job emits no log and no artifact. The last native-l2tp
# attempt sat 36+ minutes past its own 45-minute cap for exactly this reason.
#
# Reaping is by exact daemon name plus the driver's own command line and
# RUNNER_TEMP prefix, so it can only ever match processes this job started.
set -u

self_pid="$$"
self_ppid="$PPID"

# Collect matches first and signal them separately. `pkill -f` would match the
# `sudo` process that is running this very script, because its command line
# contains the pattern, so the reaper would kill its own wrapper mid-run.
reap_matches() {
  local finder="$1" pattern="$2" pid
  local -a victims=()
  while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    [ "$pid" = "$self_pid" ] && continue
    [ "$pid" = "$self_ppid" ] && continue
    victims+=("$pid")
  done < <(pgrep "$finder" -- "$pattern" 2>/dev/null || true)
  if [ "${#victims[@]}" -gt 0 ]; then
    sudo kill -KILL "${victims[@]}" 2>/dev/null || true
  fi
}

reap_matches -f 'scripts/ci/native-l2tp-driver.sh'
reap_matches -f "${RUNNER_TEMP:-/tmp}/antimage-l2tp-"

for name in xl2tpd pppd charon; do
  reap_matches -x "$name"
done

for ns in $(sudo ip netns list 2>/dev/null | awk '{print $1}' | grep -E '^antimage-l2tp' || true); do
  sudo timeout 10s ip netns delete "$ns" 2>/dev/null || true
done

exit 0
