#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d "${RUNNER_TEMP:-/tmp}/antimage-pptp-XXXXXX") }"
NS="antimage-pptp-client"
VETH_HOST="apptp-vh"
VETH_NS="apptp-vn"
PIDS=()
forget_pid() {
  local target="$1" pid
  local -a remaining=()
  for pid in "${PIDS[@]}"; do
    [ "$pid" = "$target" ] || remaining+=("$pid")
  done
  PIDS=("${remaining[@]}")
}
stop_pid() {
  local pid="$1" state
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do
    state="$(ps -o stat= -p "$pid" 2>/dev/null | tr -d ' ' || true)"
    if [ -z "$state" ] || [[ "$state" == Z* ]]; then
      wait "$pid" 2>/dev/null || true
      forget_pid "$pid"
      return 0
    fi
    sleep .1
  done
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  forget_pid "$pid"
}
cleanup() {
  local rc=$?
  set +e
  if [ "$rc" -ne 0 ]; then
    mkdir -p "$RUNNER_TEMP/antimage-pptp-failure"
    cp -a "$ROOT/." "$RUNNER_TEMP/antimage-pptp-failure/" || true
    {
      date -u
      ip -details addr show || true
      ip route show table all || true
      ip netns list || true
      ip netns exec "$NS" ip -details addr show || true
      ip netns exec "$NS" ip route show table all || true
      ps -ef || true
    } >"$RUNNER_TEMP/antimage-pptp-failure/network-state.txt" 2>&1
    echo "PPTP native evidence retained at $RUNNER_TEMP/antimage-pptp-failure" >&2
  fi
  for pid in "${PIDS[@]}"; do stop_pid "$pid"; done
  if [ -n "${ANTIMAGE_PPTP_TEST_BINARY:-}" ] && [ -d "$ROOT/pptp-state" ]; then
    env ANTIMAGE_PPTP_NATIVE_STATE="$ROOT/pptp-state" ANTIMAGE_PPTP_NATIVE_ACTION=cleanup \
      "$ANTIMAGE_PPTP_TEST_BINARY" -test.run='^TestPPTPNativeAccountingStage$' -test.v >/dev/null 2>&1 || true
  fi
  ip link del "$VETH_HOST" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  if [ "$rc" -eq 0 ]; then
    rm -rf "$ROOT"
  fi
}
trap cleanup EXIT

ROOT="${ROOT% }"
mkdir -p "$ROOT"
quota_bytes="${ANTIMAGE_PPTP_QUOTA_BYTES:-$((50 * 1024 * 1024))}"
server_config="$ROOT/pptpd.conf"

stage() {
  local action="$1" state="$2" quota="${3:-$quota_bytes}"
  env ANTIMAGE_PPTP_NATIVE_STATE="$state" \
    ANTIMAGE_PPTP_NATIVE_ACTION="$action" \
    ANTIMAGE_PPTP_QUOTA_BYTES="$quota" \
    ANTIMAGE_NODE_HELPER_BINARY="$ANTIMAGE_NODE_HELPER_BINARY" \
    "$ANTIMAGE_PPTP_TEST_BINARY" -test.run='^TestPPTPNativeAccountingStage$' -test.v
}

panel_replay() {
  env ANTIMAGE_PPTP_NATIVE_STATE="$ROOT/pptp-state" \
    ANTIMAGE_NATIVE_PANEL_REQUIRE_FINAL="${1:-0}" \
    "$ANTIMAGE_PPTP_PANEL_TEST_BINARY" -test.run='^TestPPTPNativePanelDB$' -test.v
}

set_session_panel_usage() {
  python3 - "$1" "$2" <<'PY'
import json, os, sys
path, used = sys.argv[1], int(sys.argv[2])
with open(path, encoding='utf-8') as f:
    cfg = json.load(f)
matches = [name for name, uid in cfg['users'].items() if int(uid) == 7]
if len(matches) != 1 or matches[0] not in cfg.get('policies', {}):
    raise SystemExit(f'expected one PPTP panel policy for user 7 in {path}')
cfg['policies'][matches[0]]['used_traffic'] = used
tmp = path + '.tmp'
with open(tmp, 'w', encoding='utf-8') as f:
    json.dump(cfg, f)
    f.flush()
    os.fsync(f.fileno())
os.replace(tmp, path)
PY
}

wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 200); do "$@" >/dev/null 2>&1 && return 0; sleep .1; done
  echo "timeout waiting for $name" >&2
  for log in "$ROOT"/*.log; do [ -f "$log" ] && { echo "--- $log ---"; cat "$log"; }; done
  return 1
}

wait_for_gone() {
  local name="$1"; shift
  for _ in $(seq 1 200); do
    if ! "$@" >/dev/null 2>&1; then return 0; fi
    sleep .1
  done
  echo "timeout waiting for $name to stop" >&2
  for log in "$ROOT"/*.log; do [ -f "$log" ] && { echo "--- $log ---"; cat "$log"; }; done
  return 1
}

capture_ppp_state() {
  local label="$1"
  {
    echo "=== $label $(date -u +%FT%TZ) ==="
    echo '--- host processes ---'
    ps -ef | grep -E '[p]ptpd|[p]ppd' || true
    pgrep -a pptpd || true
    pgrep -a pppd || true
    echo '--- host addresses, links, and routes ---'
    ip -details addr show || true
    ip link show || true
    ip route show table all || true
    echo '--- client namespace addresses, links, and routes ---'
    ip netns exec "$NS" ip -details addr show || true
    ip netns exec "$NS" ip link show || true
    ip netns exec "$NS" ip route show table all || true
    echo '--- PPP pid files ---'
    ls -la /run/ppp* /var/run/ppp* 2>/dev/null || true
    cat /run/ppp*.pid /var/run/ppp*.pid 2>/dev/null || true
    echo '--- PPP native interface counters ---'
    for iface in /sys/class/net/ppp*; do
      [ -d "$iface" ] || continue
      echo "${iface##*/} rx=$(cat "$iface/statistics/rx_bytes" 2>/dev/null || echo unavailable) tx=$(cat "$iface/statistics/tx_bytes" 2>/dev/null || echo unavailable)"
    done
    echo '--- active session records ---'
    find "$ROOT/pptp-state" -path '*/ppp-accounting/active/*.json' -type f -print -exec cat {} \; 2>/dev/null || true
    echo '--- final session records ---'
    find "$ROOT/pptp-state" -path '*/ppp-accounting/final/*.json' -type f -print -exec cat {} \; 2>/dev/null || true
    echo '--- admission denial records ---'
    find "$ROOT/pptp-state" -path '*/ppp-accounting/admission-denials/*.json' -type f -print -exec cat {} \; 2>/dev/null || true
  } >>"$ROOT/quota-session-state.log" 2>&1
}

start_server() {
  pptpd -f -c "$server_config" >"$ROOT/pptpd.log" 2>&1 &
  SERVER_PID="$!"
  PIDS+=("$SERVER_PID")
  wait_for 'PPTP TCP control listener' sh -c 'ss -lnt "( sport = :1723 )" | grep -q 1723'
}

start_client() {
  ip netns exec "$NS" pppd nodetach noauth name native-pptp password native-pptp-secret \
    refuse-eap refuse-pap refuse-chap refuse-mschap \
    require-mppe-128 noipdefault nodefaultroute mtu 1200 mru 1200 \
    pty "$PPTP_CLIENT --nolaunchpppd 10.251.0.1 --loglevel 0" \
    >"$ROOT/pppd-client.log" 2>&1 &
  CLIENT_PID="$!"
  PIDS+=("$CLIENT_PID")
  wait_for 'client PPP interface' ip netns exec "$NS" ip link show ppp0
  wait_for 'server PPP interface' sh -c 'ip -o link show | grep -q "ppp[0-9]"'
  wait_for 'durable server session-start hook' sh -c 'find "$1" -path "*/ppp-accounting/active/ppp*.json" -print -quit | grep -q .' _ "$ROOT/pptp-state"
}

ip netns add "$NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"
ip link set "$VETH_NS" netns "$NS"
ip addr add 10.251.0.1/24 dev "$VETH_HOST"
ip link set "$VETH_HOST" up
ip netns exec "$NS" ip addr add 10.251.0.2/24 dev "$VETH_NS"
ip netns exec "$NS" ip link set lo up
ip netns exec "$NS" ip link set "$VETH_NS" up

echo '=== PPTP production configuration and real PPP tunnel ==='
stage prepare "$ROOT/pptp-state"
config_path="$(cat "$ROOT/pptp-state/pptpd-config-path")"
cp "$config_path" "$server_config"
PPTP_CLIENT="$(command -v pptp)"
start_server
start_client
wait_for 'first PPTP tunnel traffic' ip netns exec "$NS" ping -c 1 -W 1 10.68.0.1
stage collect-first "$ROOT/pptp-state"
panel_replay
stage ack "$ROOT/pptp-state"

echo '=== PPTP daemon restart and durable accounting recovery ==='
stop_pid "$SERVER_PID"
stop_pid "$CLIENT_PID"
wait_for_gone 'client PPP interface shutdown' ip netns exec "$NS" ip link show ppp0
wait_for_gone 'server PPP interface shutdown' sh -c 'ip -o link show | grep -q "ppp[0-9]"'
start_server
start_client
wait_for 'post-restart PPTP tunnel traffic' ip netns exec "$NS" ping -c 1 -W 1 10.68.0.1
stage collect-next "$ROOT/pptp-state"
panel_replay
stage ack "$ROOT/pptp-state"
previous_effective="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["effective_total"])' "$ROOT/pptp-state/native-panel-receipt.json")"
if [ "$previous_effective" -ge "$quota_bytes" ]; then
  echo "PPTP pre-quota traffic already exhausted quota: effective=${previous_effective} quota=${quota_bytes}" >&2
  exit 1
fi
remaining_effective="$((quota_bytes - previous_effective))"
raw_quota_bytes="$((remaining_effective / 3))"
session_config="$(find "$ROOT/pptp-state/pptp" -name session-helper.json -print -quit)"
set_session_panel_usage "$session_config" "$previous_effective"

echo '=== PPTP production offline quota enforcement on real 50 MiB transfer ==='
timeout 180 nc -l -p 19091 >"$ROOT/quota-received" 2>&1 &
listener_pid="$!"
PIDS+=("$listener_pid")
wait_for 'PPTP quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
ip netns exec "$NS" tc qdisc replace dev "$VETH_NS" root tbf rate 12mbit burst 32kb latency 400ms
stage quota-watch "$ROOT/pptp-state" "$quota_bytes" >"$ROOT/quota-watch.log" 2>&1 &
watch_pid="$!"
PIDS+=("$watch_pid")
sleep .2
capture_ppp_state 'before quota cutoff'
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=64K count=1024 2>/dev/null | nc -N -w 5 10.68.0.1 19091' >"$ROOT/quota-sender.log" 2>&1 &
sender_pid="$!"
PIDS+=("$sender_pid")
wait "$watch_pid"
forget_pid "$watch_pid"
capture_ppp_state 'after server quota cutoff'
wait_for_gone 'quota-exhausted client PPP interface' ip netns exec "$NS" ip link show ppp0 || {
  capture_ppp_state 'client PPP interface failed to disappear'
  echo 'PPTP quota-exhausted client PPP interface remained connected' >&2
  exit 1
}
wait "$sender_pid" || true
forget_pid "$sender_pid"
wait "$listener_pid" || true
forget_pid "$listener_pid"
cat "$ROOT/quota-watch.log"

echo '=== PPTP same-credential reconnect must be denied during PPP pre-up ==='
find "$ROOT/pptp-state" -path '*/ppp-accounting/admission-denials/7.json' -delete
timeout 20s ip netns exec "$NS" pppd nodetach maxfail 1 noauth name native-pptp password native-pptp-secret \
  refuse-eap refuse-pap refuse-chap refuse-mschap \
  require-mppe-128 noipdefault nodefaultroute mtu 1200 mru 1200 \
  pty "$PPTP_CLIENT --nolaunchpppd 10.251.0.1 --loglevel 0" \
  >"$ROOT/pptpd-reconnect.log" 2>&1 &
reconnect_pid="$!"
PIDS+=("$reconnect_pid")
wait_for 'durable PPTP reconnect quota denial' sh -c 'find "$1" -path "*/ppp-accounting/admission-denials/7.json" -print -quit | grep -q .' _ "$ROOT/pptp-state"
wait_for_gone 'reconnect PPP interface before usable traffic' ip netns exec "$NS" ip link show ppp0
set +e
wait "$reconnect_pid"
reconnect_rc=$?
set -e
forget_pid "$reconnect_pid"
if [ "$reconnect_rc" -eq 0 ] || [ "$reconnect_rc" -eq 124 ]; then
  cat "$ROOT/pptpd-reconnect.log" >&2
  echo "quota-exhausted PPTP reconnect exited unexpectedly with status $reconnect_rc" >&2
  exit 1
fi
if ip netns exec "$NS" ip link show ppp0 >/dev/null 2>&1; then
  cat "$ROOT/pptpd-reconnect.log" >&2
  echo 'quota-exhausted PPTP reconnect exposed a usable PPP interface' >&2
  exit 1
fi
capture_ppp_state 'after rejected reconnect'
cat "$ROOT/pptpd-reconnect.log"
stage collect-final "$ROOT/pptp-state"
panel_replay 1
stage ack "$ROOT/pptp-state"
received="$(wc -c <"$ROOT/quota-received")"
if [ "$received" -ge "$((raw_quota_bytes + 2 * 1024 * 1024))" ] || [ "$received" -lt "$((raw_quota_bytes * 4 / 5))" ]; then
  echo "PPTP quota traffic outside enforcement window: ${received} bytes" >&2
  exit 1
fi
echo "PPTP: effective quota=${quota_bytes} bytes, coefficients=1.5x2, delivered raw payload=${received} bytes"
