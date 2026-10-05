#!/usr/bin/env bash
set -euo pipefail
ROOT="${ROOT:-$(mktemp -d)}"
NS="${NS:-antimage-anyconnect-client}"
PIDS=()
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    for log in "$ROOT"/*.log; do
      [ -f "$log" ] || continue
      echo "--- $log ---" >&2
      cat "$log" >&2 || true
    done
  fi
  set +e
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  ip netns del "$NS" 2>/dev/null || true
  rm -rf "$ROOT"
}
trap cleanup EXIT
mkdir -p "$ROOT"
ip netns add "$NS"
ip link add aoc-vh type veth peer name aoc-vn
ip link set aoc-vn netns "$NS"
ip addr add 10.253.0.1/24 dev aoc-vh
ip link set aoc-vh up
ip netns exec "$NS" ip addr add 10.253.0.2/24 dev aoc-vn
ip netns exec "$NS" ip link set lo up
ip netns exec "$NS" ip link set aoc-vn up
test -c /dev/net/tun
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=antimage-ocserv' -keyout "$ROOT/key.pem" -out "$ROOT/cert.pem" >/dev/null 2>&1
SERVERCERT="pin-sha256:$(openssl x509 -in "$ROOT/cert.pem" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | openssl dgst -sha256 -binary | base64 -w0)"
VPNSCRIPT="${VPNSCRIPT:-/usr/share/vpnc-scripts/vpnc-script}"
test -x "$VPNSCRIPT"
printf 'native-password\nnative-password\n' | ocpasswd -c "$ROOT/ocpasswd" native-user >/dev/null
cat >"$ROOT/ocserv.conf" <<EOF
auth = plain[passwd=$ROOT/ocpasswd]
device = vpns
server-cert = $ROOT/cert.pem
server-key = $ROOT/key.pem
tcp-port = 4433
udp-port = 4433
run-as-user = root
run-as-group = root
socket-file = $ROOT/ocserv-worker.sock
occtl-socket-file = $ROOT/ocserv.sock
use-occtl = true
ipv4-network = 192.0.2.0
ipv4-netmask = 255.255.255.0
dns = 1.1.1.1
max-clients = 4
EOF
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  cat >"$ROOT/quota-admission.sh" <<EOF
#!/bin/sh
exec env ANTIMAGE_ANYCONNECT_ACTION=admission ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" ANTIMAGE_ANYCONNECT_NATIVE_PID_FILE="$ROOT/ocserv.pid" "$ANTIMAGE_ANYCONNECT_TEST_BINARY" -test.run='^TestAnyConnectNativeAccountingStage$' -test.v
EOF
  chmod 700 "$ROOT/quota-admission.sh"
  printf '\nconnect-script = %s\n' "$ROOT/quota-admission.sh" >>"$ROOT/ocserv.conf"
fi
ocserv --foreground --config="$ROOT/ocserv.conf" >"$ROOT/ocserv.log" 2>&1 &
ocserv_pid=$!
printf '%s\n' "$ocserv_pid" >"$ROOT/ocserv.pid"
PIDS+=("$ocserv_pid")
for _ in $(seq 1 80); do ss -lnt '( sport = :4433 )' | grep -q 4433 && break; sleep .25; done
ss -lnt '( sport = :4433 )' | grep -q 4433
ip netns exec "$NS" sh -c "printf '%s\n' native-password | openconnect --protocol=anyconnect --user=native-user --passwd-on-stdin --servercert '$SERVERCERT' --no-dtls --script '$VPNSCRIPT' --interface=vpn-native https://10.253.0.1:4433" >"$ROOT/openconnect.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
ip netns exec "$NS" ip link show vpn-native >/dev/null
for _ in $(seq 1 80); do ip netns exec "$NS" ip -4 addr show dev vpn-native | grep -q '192.0.2.' && break; sleep .25; done
ip netns exec "$NS" ip addr show vpn-native
ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  env ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" \
    ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" \
    ANTIMAGE_ANYCONNECT_NATIVE_PID="$ocserv_pid" \
    "$ANTIMAGE_ANYCONNECT_TEST_BINARY" -test.run='^TestAnyConnectNativeAccountingStage$' -test.v
  env ANTIMAGE_ANYCONNECT_ACTION=quota \
    ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" \
    ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" \
    ANTIMAGE_ANYCONNECT_NATIVE_PID="$ocserv_pid" \
    "$ANTIMAGE_ANYCONNECT_TEST_BINARY" -test.run='^TestAnyConnectNativeAccountingStage$' -test.v
fi
client_pid="${PIDS[1]}"
for _ in $(seq 1 80); do kill -0 "$client_pid" 2>/dev/null || break; sleep .25; done
if kill -0 "$client_pid" 2>/dev/null; then
  echo 'openconnect did not exit after the server disconnected the over-quota session' >&2
  exit 1
fi
wait "$client_pid" 2>/dev/null || true
unset 'PIDS[1]'
ip netns exec "$NS" sh -c "printf '%s\n' native-password | openconnect --protocol=anyconnect --user=native-user --passwd-on-stdin --servercert '$SERVERCERT' --no-dtls --script '$VPNSCRIPT' --interface=vpn-native https://10.253.0.1:4433" >"$ROOT/openconnect-reconnect.log" 2>&1 &
PIDS+=("$!")
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  for _ in $(seq 1 80); do grep -q 'AUTH_FAILED' "$ROOT/openconnect-reconnect.log" && break; sleep .25; done
  if ! grep -q 'AUTH_FAILED' "$ROOT/openconnect-reconnect.log"; then
    echo 'AnyConnect client reconnected after the persisted offline quota was exhausted' >&2
    exit 1
  fi
  if ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1; then
    echo 'AnyConnect quota-denied reconnect still created a tunnel interface' >&2
    exit 1
  fi
else
  for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
  ip netns exec "$NS" ip link show vpn-native >/dev/null
  for _ in $(seq 1 80); do ip netns exec "$NS" ip -4 addr show dev vpn-native | grep -q '192.0.2.' && break; sleep .25; done
  ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
fi
echo 'AnyConnect/ocserv: real authenticated session, quota cutoff, and reconnect policy passed'
