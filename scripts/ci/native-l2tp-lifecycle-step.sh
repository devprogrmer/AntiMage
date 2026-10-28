#!/usr/bin/env bash
# Body of the "Run native L2TP and PPP lifecycle" step, extracted so the step can
# bound itself from the inside.
#
# Why this is a script and not inline YAML: when the GitHub step timeout fires,
# the runner kills the step body immediately, so nothing after the hanging
# command runs - no provision log, no driver log, no annotation, no reap. If the
# job then also exhausts its own 45-minute cap while the capture/upload steps
# run, GitHub discards the job's log and artifacts completely. The last three
# native-l2tp hangs produced exactly one artifact of evidence: the check-run note
# "The job has exceeded the maximum execution time of 45m0s".
#
# The caller runs this script under its own `timeout` and then reaps, prints and
# annotates whatever happened. Every phase below is additionally bounded, so the
# script normally returns with a real status and a complete log long before that
# outer bound is reached.
#
# Nothing here changes what the native test proves: the driver still runs
# `modprobe l2tp_ppp` and the real lifecycle assertions.
set -uo pipefail

emit_failure_annotation() {
  local log="$1" title="$2" body
  [ -s "$log" ] || return 0
  body="$(command tail -c 3000 "$log" | command sed -e 's/%/%25/g' -e 's/\r/%0D/g' | command awk 'BEGIN{ORS="%0A"}{print}')"
  printf '::error title=%s::%s\n' "$title" "$body"
}

# Bounds are per phase and come out of one step budget, so a slow runner fails
# with a named phase instead of an unattributed step timeout.
step_budget="${ANTIMAGE_L2TP_STEP_SECONDS:-1500}"
step_started_at="$(date +%s)"
reserve_seconds="${ANTIMAGE_L2TP_STEP_RESERVE_SECONDS:-120}"

remaining_budget() {
  local left=$(( step_started_at + step_budget - $(date +%s) ))
  [ "$left" -lt 1 ] && left=1
  printf '%s' "$left"
}

# Run one phase under the smaller of its own limit and what is left, so the
# script always keeps enough budget to print its logs and exit by itself.
#
# Privileged phases keep `timeout` on the root side of `sudo`. `timeout` only
# signals its direct child, so wrapping `sudo` from the outside would leave the
# root-owned driver and its grandchildren (xl2tpd, pppd, charon) running with
# nothing left to reap them.
run_phase() {
  local label="$1" limit="$2" privilege="$3"
  shift 3
  local left allowed
  left="$(remaining_budget)"
  left=$(( left - reserve_seconds ))
  [ "$left" -lt 5 ] && left=5
  allowed=$(( limit < left ? limit : left ))
  echo "=== phase ${label} (limit ${allowed}s of ${limit}s) ==="
  if [ "$privilege" = "root" ]; then
    sudo timeout --signal=TERM --kill-after=30s "${allowed}s" "$@"
  else
    timeout --signal=TERM --kill-after=30s "${allowed}s" "$@"
  fi
}

provision_log="$RUNNER_TEMP/native-l2tp-provision.log"
driver_log="$RUNNER_TEMP/native-l2tp.log"
: >"$provision_log"
: >"$driver_log"

provision_status=0
run_phase "kernel-index-resolver" 180 user \
  python3 scripts/ci/l2tp-kernel-module-resolver-test.py </dev/null >>"$provision_log" 2>&1 || provision_status=$?
if [ "$provision_status" -eq 0 ]; then
  run_phase "reap-driver-tree-self-test" 300 root \
    python3 scripts/ci/l2tp-reap-driver-tree-test.py </dev/null >>"$provision_log" 2>&1 || provision_status=$?
fi
if [ "$provision_status" -eq 0 ]; then
  # ensure-l2tp-kernel-module.sh carries its own provisioning budget; this bound
  # only stops the whole phase from stalling past its share of the step.
  run_phase "kernel-module-provisioning" 900 user \
    bash scripts/ci/ensure-l2tp-kernel-module.sh </dev/null >>"$provision_log" 2>&1 || provision_status=$?
fi

cat "$provision_log"
if [ "$provision_status" -ne 0 ]; then
  emit_failure_annotation "$provision_log" "native L2TP kernel provisioning failed"
  exit "$provision_status"
fi

# The binaries are built here because the driver consumes them; each compile is
# bounded so a wedged toolchain cannot consume the driver's share of the budget.
nodeagent_test="$RUNNER_TEMP/antimage-nodeagent.test"
nodecontroller_test="$RUNNER_TEMP/antimage-nodecontroller.test"
api_test="$RUNNER_TEMP/antimage-api.test"
node_helper="$RUNNER_TEMP/antimage-node-helper"

build_status=0
run_phase "build-nodeagent-test" 420 user go test -c -o "$nodeagent_test" ./internal/app/nodeagent || build_status=$?
if [ "$build_status" -eq 0 ]; then
  run_phase "build-nodecontroller-test" 420 user go test -c -o "$nodecontroller_test" ./internal/app/nodecontroller || build_status=$?
fi
if [ "$build_status" -eq 0 ]; then
  run_phase "build-api-test" 420 user go test -c -o "$api_test" ./internal/app/api || build_status=$?
fi
if [ "$build_status" -eq 0 ]; then
  run_phase "build-node-helper" 420 user go build -o "$node_helper" ./cmd/antimage_node || build_status=$?
fi
if [ "$build_status" -ne 0 ]; then
  printf '::error title=native L2TP test binaries::%s\n' "a Go build phase failed or timed out"
  exit "$build_status"
fi

# The driver bounds itself as well; the extra margin covers start-up and the
# reap that follows it.
driver_budget="${ANTIMAGE_L2TP_DRIVER_SECONDS:-720}"
status=0
run_phase "native-lifecycle-driver" "$driver_budget" root \
  env RUNNER_TEMP="$RUNNER_TEMP" \
    ANTIMAGE_L2TP_TEST_BINARY="$nodeagent_test" \
    ANTIMAGE_L2TP_PANEL_TEST_BINARY="$nodecontroller_test" \
    ANTIMAGE_L2TP_API_TEST_BINARY="$api_test" \
    ANTIMAGE_NODE_HELPER_BINARY="$node_helper" \
    bash scripts/ci/native-l2tp-driver.sh </dev/null >"$driver_log" 2>&1 || status=$?

bash scripts/ci/l2tp-reap-driver-tree.sh || true
cat "$driver_log"
if [ "$status" -ne 0 ]; then
  emit_failure_annotation "$driver_log" "native L2TP driver failed"
fi
exit "$status"
