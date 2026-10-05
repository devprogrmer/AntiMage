#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d "${RUNNER_TEMP:-/tmp}/antimage-awg-XXXXXX")}"
NS="antimage-awg-client"
VETH_HOST="aawg-vh"
VETH_NS="aawg-vn"
IFACE=""
PIDS=()
cleanup() {
  local rc=$?
  set +e
  if [ "$rc" -ne 0 ]; then
    mkdir -p "$RUNNER_TEMP/antimage-awg-failure"
    cp -a "$ROOT/." "$RUNNER_TEMP/antimage-awg-failure/" || true
    {
      date -u
      ip -details addr show || true
      ip route show table all || true
      ip netns list || true
      ip netns exec "$NS" ip -details addr show || true
      ip netns exec "$NS" ip route show table all || true
      sudo dmesg | tail -250 || true
      ps -ef || true
    } >"$RUNNER_TEMP/antimage-awg-failure/network-state.txt" 2>&1
    echo "AmneziaWG native evidence retained at $RUNNER_TEMP/antimage-awg-failure" >&2
  fi
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  [ -z "$IFACE" ] || ip link del "$IFACE" 2>/dev/null || true
  ip link del "$VETH_HOST" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  [ "$rc" -ne 0 ] || rm -rf "$ROOT"
}
trap cleanup EXIT

mkdir -p "$ROOT"
server_priv="$(wg genkey)"
client_priv="$(wg genkey)"
server_pub="$(printf '%s' "$server_priv" | wg pubkey)"
client_pub="$(printf '%s' "$client_priv" | wg pubkey)"
quota_bytes="${ANTIMAGE_AWG_QUOTA_BYTES:-$((50 * 1024 * 1024))}"

stage() {
  local action="$1" state="$2" quota="${3:-$quota_bytes}"
  env ANTIMAGE_AWG_NATIVE_STATE="$state" \
    ANTIMAGE_AWG_NATIVE_ACTION="$action" \
    ANTIMAGE_AWG_SERVER_PRIVATE="$server_priv" \
    ANTIMAGE_AWG_CLIENT_PUBLIC="$client_pub" \
    ANTIMAGE_AWG_QUOTA_BYTES="$quota" \
    "$ANTIMAGE_AWG_TEST_BINARY" -test.run='^TestAmneziaWGNativeAccountingStage$' -test.v
}
AWG_TOOL="${ANTIMAGE_AWG_TOOL:-awg}"

ip netns add "$NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"
ip link set "$VETH_NS" netns "$NS"
ip addr add 10.251.0.1/24 dev "$VETH_HOST"
ip link set "$VETH_HOST" up
ip netns exec "$NS" ip addr add 10.251.0.2/24 dev "$VETH_NS"
ip netns exec "$NS" ip link set lo up
ip netns exec "$NS" ip link set "$VETH_NS" up

echo '=== AmneziaWG production DKMS provisioning/apply and native tunnel ==='
stage apply "$ROOT/awg-state"
IFACE="$(cat "$ROOT/awg-state/interface")"
ip netns exec "$NS" ip link add awg-client type amneziawg
ip netns exec "$NS" ip addr add 10.74.0.2/24 dev awg-client
ip netns exec "$NS" "$AWG_TOOL" set awg-client listen-port 51822 private-key <(printf '%s\n' "$client_priv") \
  jc 4 jmin 8 jmax 80 s1 77 s2 90 h1 12345 h2 23456 h3 34567 h4 45678 \
  peer "$server_pub" endpoint 10.251.0.1:51821 allowed-ips 10.74.0.1/32 persistent-keepalive 1
ip netns exec "$NS" ip link set awg-client up

wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 150); do "$@" >/dev/null 2>&1 && return 0; sleep .1; done
  echo "timeout waiting for $name" >&2
  for log in "$ROOT"/*.log; do [ -f "$log" ] && { echo "--- $log ---"; cat "$log"; }; done
  return 1
}

wait_for 'AmneziaWG handshake' ip netns exec "$NS" ping -c 1 -W 1 10.74.0.1
timeout 30 nc -l -p 19090 >"$ROOT/initial-received" 2>&1 &
PIDS+=("$!")
wait_for 'initial AWG receiver' sh -c 'ss -lnt "( sport = :19090 )" | grep -q 19090'
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=2 2>/dev/null | nc -N -w 3 10.74.0.1 19090'
stage collect "$ROOT/awg-state"

echo '=== AmneziaWG production stop/apply restart and counter continuity ==='
stage restart "$ROOT/awg-state"
wait_for 'post-restart AWG handshake' ip netns exec "$NS" ping -c 1 -W 1 10.74.0.1
stage collect "$ROOT/awg-state"
stage ack "$ROOT/awg-state"

echo '=== AmneziaWG production offline quota worker on real 50 MiB traffic ==='
timeout 180 nc -l -p 19091 >"$ROOT/quota-received" 2>&1 &
listener_pid="$!"
PIDS+=("$listener_pid")
wait_for 'AWG quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
ip netns exec "$NS" tc qdisc replace dev "$VETH_NS" root tbf rate 12mbit burst 32kb latency 400ms
stage quota-watch "$ROOT/awg-state" "$quota_bytes" >"$ROOT/quota-watch.log" 2>&1 &
watch_pid="$!"
PIDS+=("$watch_pid")
sleep .2
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=64K count=1024 2>/dev/null | nc -N -w 5 10.74.0.1 19091' >"$ROOT/quota-sender.log" 2>&1 &
sender_pid="$!"
PIDS+=("$sender_pid")
wait "$watch_pid"
if ip netns exec "$NS" ping -c 1 -W 1 10.74.0.1 >/dev/null 2>&1; then
  echo 'AWG quota-exhausted peer reconnected after production removal' >&2
  exit 1
fi
wait "$sender_pid" || true
wait "$listener_pid" || true
cat "$ROOT/quota-watch.log"
received="$(wc -c <"$ROOT/quota-received")"
if [ "$received" -ge "$((64 * 1024 * 1024))" ] || [ "$received" -lt "$((quota_bytes * 4 / 5))" ]; then
  echo "AWG quota traffic outside enforcement window: ${received} bytes" >&2
  exit 1
fi
echo "AmneziaWG: quota=${quota_bytes} bytes stopped native peer; delivered payload=${received} bytes"
