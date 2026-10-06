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
start_reconnect_attempt() {
  local output="$1"
  ip netns exec "$NS" openconnect --protocol=anyconnect --user=native-user \
    --passwd-on-stdin --reconnect-timeout=1 --servercert "$SERVERCERT" \
    --no-dtls --script "$VPNSCRIPT" --interface=vpn-native \
    https://10.253.0.1:4433 >"$output" 2>&1 <<< 'native-password' &
  client_pid=$!
  PIDS+=("$client_pid")
}
run_accounting() {
  local action="$1" quota_bytes="${2:-52428800}"
  env ANTIMAGE_ANYCONNECT_ACTION="$action" \
    ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" \
    ANTIMAGE_ANYCONNECT_NATIVE_STATE="$ROOT/anyconnect-accounting" \
    ANTIMAGE_ANYCONNECT_NATIVE_PID="$ocserv_pid" \
    ANTIMAGE_ANYCONNECT_QUOTA_BYTES="$quota_bytes" \
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
start_speed_client() {
  ip netns exec "$NS" openconnect --protocol=anyconnect --user=native-speed \
    --passwd-on-stdin --reconnect-timeout=1 --servercert "$SERVERCERT" \
    --no-dtls --script "$VPNSCRIPT" --interface=vpn-speed \
    https://10.253.0.1:4433 >"$ROOT/openconnect-speed.log" 2>&1 <<< 'native-password' &
  speed_client_pid=$!
  PIDS+=("$speed_client_pid")
  for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-speed >/dev/null 2>&1 && break; sleep .25; done
  ip netns exec "$NS" ip -4 addr show dev vpn-speed | grep -q '192.0.2.3/'
}
measure_speed_upload() {
  local result="$ROOT/anyconnect-speed-upload.json"
  python3 - "$result" <<'PY' &
import json, socket, sys, time
expected = 16 * 1024 * 1024
with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("0.0.0.0", 19092))
    server.listen(1)
    conn, _ = server.accept()
    started = time.monotonic()
    total = 0
    windows = {}
    with conn:
        while total < expected:
            block = conn.recv(65536)
            if not block:
                break
            total += len(block)
            second = int(time.monotonic() - started)
            windows[second] = windows.get(second, 0) + len(block)
    elapsed = time.monotonic() - started
if total != expected:
    raise SystemExit(f"upload receiver got {total} bytes, want {expected}")
mbps = total * 8 / elapsed / 1_000_000
peak = max(windows.values()) * 8 / 1_000_000
json.dump({"configured_mbps": 4, "bytes": total, "seconds": elapsed, "average_mbps": mbps, "peak_1s_mbps": peak}, open(sys.argv[1], "w"))
print(f"AnyConnect upload speed configured=4 Mbps bytes={total} seconds={elapsed:.2f} average={mbps:.2f} Mbps peak_1s={peak:.2f} Mbps")
if not 2.5 <= mbps <= 5.5 or peak > 8:
    raise SystemExit(f"AnyConnect upload speed outside tolerance: {mbps:.2f} Mbps")
PY
  local receiver_pid=$!
  PIDS+=("$receiver_pid")
  wait_for 'AnyConnect speed upload receiver' sh -c 'ss -lnt "( sport = :19092 )" | grep -q 19092'
  ip netns exec "$NS" python3 - <<'PY'
import socket, time
expected = 16 * 1024 * 1024
with socket.create_connection(("192.0.2.1", 19092), timeout=10) as conn:
    block = b"u" * 65536
    sent = 0
    while sent < expected:
        part = block[:min(len(block), expected - sent)]
        conn.sendall(part)
        sent += len(part)
    conn.shutdown(socket.SHUT_WR)
PY
  wait "$receiver_pid"
}
measure_speed_download() {
  local client_ip=192.0.2.3 result="$ROOT/anyconnect-speed-download.json"
  ip netns exec "$NS" python3 - "$client_ip" "$result" <<'PY' &
import json, socket, sys, time
expected = 16 * 1024 * 1024
with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((sys.argv[1], 19093))
    server.listen(1)
    conn, _ = server.accept()
    started = time.monotonic()
    total = 0
    windows = {}
    with conn:
        while total < expected:
            block = conn.recv(65536)
            if not block:
                break
            total += len(block)
            second = int(time.monotonic() - started)
            windows[second] = windows.get(second, 0) + len(block)
    elapsed = time.monotonic() - started
if total != expected:
    raise SystemExit(f"download receiver got {total} bytes, want {expected}")
mbps = total * 8 / elapsed / 1_000_000
peak = max(windows.values()) * 8 / 1_000_000
json.dump({"configured_mbps": 6, "bytes": total, "seconds": elapsed, "average_mbps": mbps, "peak_1s_mbps": peak}, open(sys.argv[2], "w"))
print(f"AnyConnect download speed configured=6 Mbps bytes={total} seconds={elapsed:.2f} average={mbps:.2f} Mbps peak_1s={peak:.2f} Mbps")
if not 4.0 <= mbps <= 8.0 or peak > 12:
    raise SystemExit(f"AnyConnect download speed outside tolerance: {mbps:.2f} Mbps")
PY
  local receiver_pid=$!
  PIDS+=("$receiver_pid")
  wait_for 'AnyConnect speed download receiver' ip netns exec "$NS" sh -c 'ss -lnt "( sport = :19093 )" | grep -q 19093'
  python3 - "$client_ip" <<'PY'
import socket, sys
expected = 16 * 1024 * 1024
with socket.create_connection((sys.argv[1], 19093), timeout=10) as conn:
    block = b"d" * 65536
    sent = 0
    while sent < expected:
        part = block[:min(len(block), expected - sent)]
        conn.sendall(part)
        sent += len(part)
    conn.shutdown(socket.SHUT_WR)
PY
  wait "$receiver_pid"
}
run_native_speed_stage() {
  ANTIMAGE_ANYCONNECT_NATIVE_ROOT="$ROOT" \
    "$ANTIMAGE_ANYCONNECT_TEST_BINARY" -test.run='^TestAnyConnectNativeSpeedConfigStage$' -test.v
}
run_quota_payload() {
  local received="$ROOT/quota-received"
  python3 - "$received" <<'PY' &
import socket, sys
with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("0.0.0.0", 19091))
    server.listen(1)
    conn, _ = server.accept()
    with conn, open(sys.argv[1], "wb") as output:
        # A VPN quota disconnect can drop the route without delivering FIN to
        # the tunneled TCP peer. Stop the receiver after an idle interval so
        # the driver can finish collecting the native cutoff result.
        conn.settimeout(10)
        while True:
            try:
                data = conn.recv(65536)
            except ConnectionResetError:
                break
            except socket.timeout:
                break
            if not data:
                break
            output.write(data)
PY
  local receiver_pid=$!
  PIDS+=("$receiver_pid")
  wait_for 'AnyConnect native quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
  ip netns exec "$NS" tc qdisc replace dev vpn-native root tbf rate 8mbit burst 32kb latency 400ms
  local start_ns end_ns sender_pid
  start_ns="$(date +%s%N)"
  ip netns exec "$NS" python3 - <<'PY' >"$ROOT/quota-sender.log" 2>&1 &
import socket
sent = 0
try:
    with socket.create_connection(("192.0.2.1", 19091), timeout=10) as client:
        # A quota cutoff can blackhole the VPN route while sendall() is blocked
        # by TCP retransmits. Bound the write so the harness can collect the
        # server-side cutoff counters and finish its quota assertions.
        client.settimeout(5)
        payload = b"q" * 65536
        while sent < 30 * 1024 * 1024:
            client.sendall(payload)
            sent += len(payload)
        client.shutdown(socket.SHUT_WR)
except OSError as exc:
    print(f"sender stopped at {sent} bytes after tunnel cutoff: {exc}")
print(f"client payload submitted={sent} bytes")
PY
  sender_pid=$!
  PIDS+=("$sender_pid")
  wait "$sender_pid" || true
  end_ns="$(date +%s%N)"
  wait "$receiver_pid"
  local received_bytes elapsed_ms
  received_bytes="$(wc -c <"$received")"
  elapsed_ms="$(((end_ns - start_ns) / 1000000))"
  test "$received_bytes" -gt 0
  printf '%s %s\n' "$received_bytes" "$elapsed_ms" >"$ROOT/quota-payload-result.txt"
}
report_quota_result() {
  local received_bytes elapsed_ms
  read -r received_bytes elapsed_ms <"$ROOT/quota-payload-result.txt"
  python3 - "$ROOT/native-quota-result.json" "$received_bytes" "$elapsed_ms" <<'PY'
import json, sys
result = json.load(open(sys.argv[1], encoding="utf-8"))
result["tunnel_payload_bytes"] = int(sys.argv[2])
result["disconnect_elapsed_ms"] = int(sys.argv[3])
if result["limit_bytes"] != 52_428_800 or result["effective_after"] < result["limit_bytes"]:
    raise SystemExit(f"native AnyConnect quota did not reach its exact 50 MiB threshold: {result}")
print("AnyConnect native quota: " + json.dumps(result, sort_keys=True))
PY
  echo "AnyConnect quota receiver got ${received_bytes} tunnel bytes; disconnect elapsed=${elapsed_ms} ms"
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
printf 'native-password\nnative-password\n' | ocpasswd -c "$ROOT/ocpasswd" native-speed >/dev/null
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
config-per-user = $ROOT/users
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
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  run_native_speed_stage
fi
start_client "$ROOT/openconnect.log"
ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
if [ -n "${ANTIMAGE_ANYCONNECT_TEST_BINARY:-}" ]; then
  transfer_tunnel_payload initial
  echo '=== ocserv per-user upload/download shaping on native traffic ==='
  start_speed_client
  measure_speed_upload
  measure_speed_download
  kill -TERM "$speed_client_pid" 2>/dev/null || true
  wait "$speed_client_pid" 2>/dev/null || true
  wait_for_gone 'AnyConnect speed test tunnel interface' ip netns exec "$NS" ip link show vpn-speed
  run_accounting collect-first
  panel_replay
  run_accounting ack

  echo '=== ocserv runtime restart, durable node-state reload, and reconnect ==='
  kill -TERM "$ocserv_pid"
  wait "$ocserv_pid" || true
  wait_for 'OpenConnect detects the ocserv runtime restart' grep -Eq \
    'Received server disconnect|Read error|Failed to reconnect' "$ROOT/openconnect.log"
  # The client retries by default and can retain its TUN during backoff. Stop
  # that old client after observing the server loss; the reconnect below is a
  # fresh authenticated process against the restarted daemon.
  kill -TERM "$client_pid" 2>/dev/null || true
  wait "$client_pid" 2>/dev/null || true
  wait_for_gone 'first AnyConnect tunnel interface' ip netns exec "$NS" ip link show vpn-native
  start_server
  start_client "$ROOT/openconnect-restart.log"
  ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
  transfer_tunnel_payload restarted
  run_accounting collect-next
  panel_replay
  run_accounting ack

  run_accounting quota-watch 52428800 >"$ROOT/quota-watch.log" 2>&1 &
  quota_watch_pid=$!; PIDS+=("$quota_watch_pid")
  wait_for 'production AnyConnect offline quota worker readiness' test -s "$ROOT/native-quota-ready"
  run_quota_payload
  wait "$quota_watch_pid"
  cat "$ROOT/quota-watch.log"
  report_quota_result
  wait_for 'OpenConnect receives the native quota disconnect' grep -Fq \
    'Received server disconnect' "$ROOT/openconnect-restart.log"
  kill -TERM "$client_pid" 2>/dev/null || true
  wait "$client_pid" 2>/dev/null || true
  wait_for_gone 'quota disconnected AnyConnect tunnel interface' ip netns exec "$NS" ip link show vpn-native
fi
wait "$client_pid" 2>/dev/null || true
start_reconnect_attempt "$ROOT/openconnect-reconnect.log"
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
