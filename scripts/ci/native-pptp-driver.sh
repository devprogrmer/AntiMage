#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d "${RUNNER_TEMP:-/tmp}/antimage-pptp-XXXXXX") }"
NS="antimage-pptp-client"
VETH_HOST="apptp-vh"
VETH_NS="apptp-vn"
PIDS=()
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
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
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

wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 200); do "$@" >/dev/null 2>&1 && return 0; sleep .1; done
  echo "timeout waiting for $name" >&2
  for log in "$ROOT"/*.log; do [ -f "$log" ] && { echo "--- $log ---"; cat "$log"; }; done
  return 1
}

start_server() {
  pptpd -f -c "$server_config" >"$ROOT/pptpd.log" 2>&1 &
  SERVER_PID="$!"
  PIDS+=("$SERVER_PID")
  wait_for 'PPTP TCP control listener' sh -c 'ss -lnt "( sport = :1723 )" | grep -q 1723'
}

start_client() {
  ip netns exec "$NS" pppd nodetach noauth name native-pptp password native-pptp-secret \
    remotename antimage-pptp refuse-eap refuse-pap refuse-chap refuse-mschap \
    require-mschap-v2 require-mppe-128 noipdefault nodefaultroute mtu 1200 mru 1200 \
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

echo '=== PPTP daemon restart and durable accounting recovery ==='
kill "$SERVER_PID"
wait "$SERVER_PID" || true
wait "$CLIENT_PID" || true
start_server
start_client
wait_for 'post-restart PPTP tunnel traffic' ip netns exec "$NS" ping -c 1 -W 1 10.68.0.1

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
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=64K count=1024 2>/dev/null | nc -N -w 5 10.68.0.1 19091' >"$ROOT/quota-sender.log" 2>&1 &
sender_pid="$!"
PIDS+=("$sender_pid")
wait "$watch_pid"
if ip netns exec "$NS" ip link show ppp0 >/dev/null 2>&1; then
  echo 'PPTP quota-exhausted PPP session remained connected' >&2
  exit 1
fi
wait "$sender_pid" || true
wait "$listener_pid" || true
cat "$ROOT/quota-watch.log"
stage ack "$ROOT/pptp-state"
received="$(wc -c <"$ROOT/quota-received")"
if [ "$received" -ge "$((64 * 1024 * 1024))" ] || [ "$received" -lt "$((quota_bytes * 4 / 5))" ]; then
  echo "PPTP quota traffic outside enforcement window: ${received} bytes" >&2
  exit 1
fi
echo "PPTP: quota=${quota_bytes} bytes stopped native PPP session; delivered payload=${received} bytes"
