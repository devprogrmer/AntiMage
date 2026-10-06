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
    if [ -n "${RUNNER_TEMP:-}" ]; then
      local evidence="$RUNNER_TEMP/antimage-anyconnect-failure"
      mkdir -p "$evidence"
      cp -a "$ROOT/." "$evidence/" || true
      {
        date -u
        ip -details addr show || true
        ip route show table all || true
        ip netns list || true
        ip netns exec "$NS" ip -details addr show || true
        ip netns exec "$NS" ip route show table all || true
        nft list ruleset || true
        tc -s qdisc show || true
        ps -ef || true
      } >"$evidence/network-state.txt" 2>&1
      echo "AnyConnect failure evidence retained at $evidence" >&2
    fi
  fi
  set +e
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  ip netns del "$NS" 2>/dev/null || true
  rm -rf "$ROOT"
}
trap cleanup EXIT
wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 100); do
    "$@" >/dev/null 2>&1 && return 0
    sleep .1
  done
  echo "timeout waiting for $name" >&2
  return 1
}
wait_for_gone() {
  local name="$1"; shift
  for _ in $(seq 1 80); do
    if ! "$@" >/dev/null 2>&1; then return 0; fi
    sleep .1
  done
  echo "timeout waiting for $name to stop" >&2
  return 1
}
start_server() {
  ocserv --foreground --config="$ROOT/ocserv.conf" >"$ROOT/ocserv.log" 2>&1 &
  ocserv_pid=$!
  printf '%s\n' "$ocserv_pid" >"$ROOT/ocserv.pid"
  PIDS+=("$ocserv_pid")
  for _ in $(seq 1 80); do ss -lnt '( sport = :4433 )' | grep -q 4433 && break; sleep .25; done
  ss -lnt '( sport = :4433 )' | grep -q 4433
}
start_client() {
  local output="$1"
  ip netns exec "$NS" openconnect --protocol=anyconnect --user=native-user \
    --passwd-on-stdin --reconnect-timeout=1 --servercert "$SERVERCERT" \
    --no-dtls --script "$VPNSCRIPT" --interface=vpn-native \
    https://10.253.0.1:4433 >"$output" 2>&1 <<< 'native-password' &
  client_pid=$!
  PIDS+=("$client_pid")
  for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
  ip netns exec "$NS" ip link show vpn-native >/dev/null
  for _ in $(seq 1 80); do ip netns exec "$NS" ip -4 addr show dev vpn-native | grep -q '192.0.2.' && break; sleep .25; done
  ip netns exec "$NS" ip addr show vpn-native
}
run_accounting() {
  local action="$1"
  env ANTIMAGE_ANYCONNECT_ACTION="$action" \
    ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" \
    ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" \
    ANTIMAGE_ANYCONNECT_NATIVE_PID="$ocserv_pid" \
    "$ANTIMAGE_ANYCONNECT_TEST_BINARY" -test.run='^TestAnyConnectNativeAccountingStage$' -test.v
}
transfer_tunnel_payload() {
  local label="$1" server_pid received="$ROOT/$1.received"
  python3 - "$received" <<'PY' &
import socket, sys
with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("0.0.0.0", 19090))
    server.listen(1)
    conn, _ = server.accept()
    with conn, open(sys.argv[1], "wb") as output:
        while True:
            data = conn.recv(65536)
            if not data:
                break
            output.write(data)
PY
  server_pid=$!
  PIDS+=("$server_pid")
  ip netns exec "$NS" python3 - <<'PY'
import socket, time
deadline = time.monotonic() + 8
while True:
    try:
        conn = socket.create_connection(("192.0.2.1", 19090), timeout=1)
        break
    except OSError:
        if time.monotonic() >= deadline:
            raise
        time.sleep(0.05)
with conn:
    payload = b"a" * 65536
    for _ in range(16):
        conn.sendall(payload)
    conn.shutdown(socket.SHUT_WR)
PY
  wait "$server_pid"
  test "$(wc -c <"$received")" -eq 1048576
  echo "AnyConnect $label tunnel payload: $(wc -c <"$received") bytes received"
}
panel_replay() {
  env ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" \
    ANTIMAGE_NATIVE_PANEL_REQUIRE_FINAL="${1:-0}" \
    "$ANTIMAGE_ANYCONNECT_PANEL_TEST_BINARY" -test.run='^TestAnyConnectNativePanelDB$' -test.v
}
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
start_server
start_client "$ROOT/openconnect.log"
ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  transfer_tunnel_payload initial
  run_accounting collect-first
  panel_replay
  run_accounting ack

  echo '=== ocserv runtime restart, durable node-state reload, and reconnect ==='
  kill -TERM "$ocserv_pid"
  wait "$ocserv_pid" || true
  wait_for_gone 'first AnyConnect tunnel interface' ip netns exec "$NS" ip link show vpn-native
  for _ in $(seq 1 80); do kill -0 "$client_pid" 2>/dev/null || break; sleep .1; done
  if kill -0 "$client_pid" 2>/dev/null; then
    echo 'openconnect did not exit after ocserv runtime restart' >&2
    exit 1
  fi
  wait "$client_pid" 2>/dev/null || true
  start_server
  start_client "$ROOT/openconnect-restart.log"
  ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
  transfer_tunnel_payload restarted
  run_accounting collect-next
  panel_replay
  run_accounting ack

  run_accounting quota
fi
for _ in $(seq 1 80); do kill -0 "$client_pid" 2>/dev/null || break; sleep .25; done
if kill -0 "$client_pid" 2>/dev/null; then
  echo 'openconnect did not exit after the server disconnected the over-quota session' >&2
  exit 1
fi
wait "$client_pid" 2>/dev/null || true
start_client "$ROOT/openconnect-reconnect.log"
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  wait_for 'AnyConnect local admission hook quota denial' grep -Fq \
    'anyconnect admission denied: data limit reached' "$ROOT/ocserv.log"
  for _ in $(seq 1 80); do grep -Fq 'HTTP/1.1 401 Cookie is not acceptable' "$ROOT/openconnect-reconnect.log" && break; sleep .25; done
  if ! grep -Fq 'HTTP/1.1 401 Cookie is not acceptable' "$ROOT/openconnect-reconnect.log"; then
    echo 'AnyConnect reconnect did not receive the ocserv quota admission rejection' >&2
    exit 1
  fi
  if grep -Eq 'HTTP/1.1 200 CONNECTED|Configured as ' "$ROOT/openconnect-reconnect.log"; then
    echo 'AnyConnect quota-denied reconnect was accepted and configured a tunnel' >&2
    exit 1
  fi
  if ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1; then
    echo 'AnyConnect quota-denied reconnect still created a tunnel interface' >&2
    exit 1
  fi
  run_accounting collect-final
  panel_replay 1
  run_accounting ack
else
  for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
  ip netns exec "$NS" ip link show vpn-native >/dev/null
  for _ in $(seq 1 80); do ip netns exec "$NS" ip -4 addr show dev vpn-native | grep -q '192.0.2.' && break; sleep .25; done
  ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
fi
echo 'AnyConnect/ocserv: real authenticated session, quota cutoff, and reconnect policy passed'
