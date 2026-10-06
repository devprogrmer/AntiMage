#!/usr/bin/env bash
set -euo pipefail
: "${ANTIMAGE_AWG_TEST_BINARY:?missing compiled nodeagent test binary}"
: "${ANTIMAGE_AWG_PANEL_TEST_BINARY:?missing compiled nodecontroller test binary}"

ROOT="${ROOT:-$(mktemp -d "${RUNNER_TEMP:-/tmp}/antimage-awg-XXXXXX")}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
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
panel_replay() {
  env ANTIMAGE_AWG_NATIVE_STATE="$ROOT/awg-state" \
    ANTIMAGE_NATIVE_PANEL_REQUIRE_FINAL="${1:-0}" \
    "$ANTIMAGE_AWG_PANEL_TEST_BINARY" -test.run='^TestAmneziaWGNativePanelDB$' -test.v
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

source "$SCRIPT_DIR/native-speed-test.sh"

wait_for 'AmneziaWG handshake' ip netns exec "$NS" ping -c 1 -W 1 10.74.0.1
timeout 30 nc -l -p 19090 >"$ROOT/initial-received" 2>&1 &
PIDS+=("$!")
wait_for 'initial AWG receiver' sh -c 'ss -lnt "( sport = :19090 )" | grep -q 19090'
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=2 2>/dev/null | nc -N -w 3 10.74.0.1 19090'
stage collect-first "$ROOT/awg-state"
panel_replay
stage ack "$ROOT/awg-state"

echo '=== AmneziaWG production stop/apply restart and counter continuity ==='
stage restart "$ROOT/awg-state"
wait_for 'post-restart AWG handshake' ip netns exec "$NS" ping -c 1 -W 1 10.74.0.1
stage collect-next "$ROOT/awg-state"
panel_replay
stage ack "$ROOT/awg-state"
native_speed_policy_stage "$ANTIMAGE_AWG_TEST_BINARY" "$IFACE" 10.74.0.2
measure_native_tunnel_speed amneziawg 10.74.0.1 10.74.0.2
previous_effective="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["effective_total"])' "$ROOT/awg-state/native-panel-receipt.json")"
if [ "$previous_effective" -ge "$quota_bytes" ]; then
  echo "AWG pre-quota traffic already exhausted quota: effective=${previous_effective} quota=${quota_bytes}" >&2
  exit 1
fi
remaining_effective="$((quota_bytes - previous_effective))"
raw_quota_bytes="$((remaining_effective / 3))"
python3 - "$ROOT/awg-state" "$client_pub" "$previous_effective" <<'PY'
import glob, json, os, sys
state, key, used = sys.argv[1], sys.argv[2], int(sys.argv[3])
paths = glob.glob(os.path.join(state, 'amneziawg', 'runtime', '*', 'usage-helper.json'))
if len(paths) != 1:
    raise SystemExit(f'expected one AWG usage helper, found {len(paths)}')
path = paths[0]
with open(path, encoding='utf-8') as f:
    cfg = json.load(f)
if key not in cfg.get('policies', {}):
    raise SystemExit(f'AWG policy missing peer {key}')
cfg['policies'][key]['used_traffic'] = used
tmp = path + '.tmp'
with open(tmp, 'w', encoding='utf-8') as f:
    json.dump(cfg, f)
    f.flush()
    os.fsync(f.fileno())
os.replace(tmp, path)
PY

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
stage collect-final "$ROOT/awg-state"
panel_replay 1
stage ack "$ROOT/awg-state"
received="$(wc -c <"$ROOT/quota-received")"
if [ "$received" -ge "$((raw_quota_bytes + 2 * 1024 * 1024))" ] || [ "$received" -lt "$((raw_quota_bytes * 4 / 5))" ]; then
  echo "AWG quota traffic outside enforcement window: ${received} bytes" >&2
  exit 1
fi
echo "AmneziaWG: effective quota=${quota_bytes} bytes, coefficients=1.5x2, delivered raw payload=${received} bytes"
