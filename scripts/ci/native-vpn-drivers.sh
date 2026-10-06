#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d)}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-antimage-vpn-client}"
VETH_HOST="avpn-vh"
VETH_NS="avpn-vn"
PIDS=()
cleanup() {
  local rc=$?
  set +e
  if [ "$rc" -ne 0 ]; then
    for log in "$ROOT"/*.log; do
      [ -f "$log" ] || continue
      echo "--- $log ---" >&2
      cat "$log" >&2 || true
    done
    if [ -n "${RUNNER_TEMP:-}" ]; then
      local evidence="$RUNNER_TEMP/antimage-native-vpn-failure"
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
      echo "Native VPN failure evidence retained at $evidence" >&2
    fi
  fi
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  ip link del wg-native 2>/dev/null || true
  ip link del tun-native 2>/dev/null || true
  ip link del "$VETH_HOST" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  rm -rf "$ROOT"
}
trap cleanup EXIT

mkdir -p "$ROOT"
ip netns add "$NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"
ip link set "$VETH_NS" netns "$NS"
ip addr add 10.250.0.1/24 dev "$VETH_HOST"
ip link set "$VETH_HOST" up
  ip netns exec "$NS" ip addr add 10.250.0.2/24 dev "$VETH_NS"
  ip netns exec "$NS" ip link set lo up
  ip netns exec "$NS" ip link set "$VETH_NS" up
  ip netns exec "$NS" ip route add default via 10.250.0.1
  # Keep tunnel destinations from falling through the underlay default route
  # after OpenVPN removes its more-specific connected /32 on quota rejection.
  ip netns exec "$NS" ip route add blackhole 10.210.0.0/24 metric 250
  sysctl -q -w net.ipv4.ip_forward=1

wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 100); do "$@" >/dev/null 2>&1 && return 0; sleep .1; done
  echo "timeout waiting for $name" >&2
  for log in "$ROOT"/*.log; do
    [ -f "$log" ] || continue
    echo "--- $log ---" >&2
    cat "$log" >&2
  done
  return 1
}

source "$SCRIPT_DIR/native-speed-test.sh"

wireguard_native_stage() {
  local action="$1" state="$2" quota_bytes="${3:-1048576}"
  [ -n "${ANTIMAGE_WIREGUARD_TEST_BINARY:-}" ] || return 0
  env ANTIMAGE_WIREGUARD_NATIVE_STATE="$state" \
    ANTIMAGE_WIREGUARD_NATIVE_INTERFACE=wg-native \
    ANTIMAGE_WIREGUARD_NATIVE_PEER="$client_pub" \
    ANTIMAGE_WIREGUARD_NATIVE_QUOTA_BYTES="$quota_bytes" \
    ANTIMAGE_WIREGUARD_ACTION="$action" \
    "$ANTIMAGE_WIREGUARD_TEST_BINARY" -test.run='^TestWireGuardNativeAccountingStage$' -test.v
}

run_wireguard() {
  echo '=== WireGuard native handshake/traffic/restart ==='
  local server_priv client_priv server_pub client_pub
  local quota_bytes="${ANTIMAGE_WIREGUARD_QUOTA_BYTES:-$((50 * 1024 * 1024))}"
  server_priv="$(wg genkey)"; client_priv="$(wg genkey)"
  server_pub="$(printf '%s' "$server_priv" | wg pubkey)"
  client_pub="$(printf '%s' "$client_priv" | wg pubkey)"
  ip link add wg-native type wireguard
  ip addr add 10.200.0.1/24 dev wg-native
  wg set wg-native listen-port 51820 private-key <(printf '%s\n' "$server_priv") \
    peer "$client_pub" allowed-ips 10.200.0.2/32
  ip link set wg-native up

  ip netns exec "$NS" ip link add wg-native type wireguard
  ip netns exec "$NS" ip addr add 10.200.0.2/24 dev wg-native
  ip netns exec "$NS" wg set wg-native listen-port 51820 private-key <(printf '%s\n' "$client_priv") \
    peer "$server_pub" endpoint 10.250.0.1:51820 allowed-ips 10.200.0.1/32 persistent-keepalive 1
  ip netns exec "$NS" ip link set wg-native up

  wait_for 'WireGuard handshake' ip netns exec "$NS" ping -c 1 -W 1 10.200.0.1
  local hs
  hs="$(wg show wg-native latest-handshakes | awk 'NR==1 {print $2}')"
  test "${hs:-0}" -gt 0
  ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=2 2>/dev/null | nc -N -w 2 10.200.0.1 19090' &
  local nc_pid=$!
  (nc -l -p 19090 >/dev/null 2>&1 || nc -l 19090 >/dev/null 2>&1) &
  PIDS+=("$nc_pid" "$!")
  wait "$nc_pid" || true
  wireguard_native_stage collect "$ROOT/wireguard-accounting"

  ip link del wg-native
  ip link add wg-native type wireguard
  ip addr add 10.200.0.1/24 dev wg-native
  wg set wg-native listen-port 51820 private-key <(printf '%s\n' "$server_priv") peer "$client_pub" allowed-ips 10.200.0.2/32
  ip link set wg-native up
  wait_for 'WireGuard post-restart handshake' ip netns exec "$NS" ping -c 1 -W 1 10.200.0.1
  echo 'WireGuard server peers after interface restart:'
  wg show wg-native peers
  wireguard_native_stage collect "$ROOT/wireguard-accounting"
  echo 'WireGuard server peers after native collect:'
  wg show wg-native peers
  wireguard_native_stage ack "$ROOT/wireguard-accounting"
  echo 'WireGuard server peer state after ACK:'
  wg show wg-native
  echo 'WireGuard client peer state after ACK:'
  ip netns exec "$NS" wg show wg-native
  echo 'WireGuard client route after ACK:'
  ip netns exec "$NS" ip route get 10.200.0.1
  wait_for 'WireGuard post-ACK peer traffic' ip netns exec "$NS" ping -c 1 -W 1 10.200.0.1
  echo 'WireGuard peer configuration immediately before quota:'
  wg show wg-native peers

  native_speed_policy_stage "$ANTIMAGE_WIREGUARD_TEST_BINARY" wg-native 10.200.0.2
  measure_native_tunnel_speed wireguard 10.200.0.1 10.200.0.2 "$ANTIMAGE_WIREGUARD_TEST_BINARY" wg-native
  local prior_raw_quota_usage
  prior_raw_quota_usage="$(wg show wg-native transfer | awk -v peer="$client_pub" '$1 == peer { print $2 + $3; found=1 } END { if (!found) print 0 }')"
  local remaining_raw_quota="$((quota_bytes - prior_raw_quota_usage))"
  if [ "$remaining_raw_quota" -le 0 ]; then
    echo "WireGuard speed-test traffic exhausted quota before cutoff test: used=${prior_raw_quota_usage} quota=${quota_bytes}" >&2
    return 1
  fi
  echo "WireGuard quota remaining after lifecycle and speed traffic: ${remaining_raw_quota} raw bytes (used=${prior_raw_quota_usage})"

  timeout 120 nc -l -p 19091 >"$ROOT/wireguard-quota-received" 2>&1 &
  local quota_listener_pid=$!
  PIDS+=("$quota_listener_pid")
  wait_for 'WireGuard quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
  # Shape the encrypted client transport so the real 50 MiB quota worker has
  # repeated 100 ms enforcement opportunities instead of a single bulk sample.
  ip netns exec "$NS" tc qdisc replace dev "$VETH_NS" root tbf rate 12mbit burst 32kb latency 400ms
  wireguard_native_stage quota-watch "$ROOT/wireguard-quota" "$quota_bytes" >"$ROOT/wireguard-quota-watch.log" 2>&1 &
  local quota_watch_pid=$!
  PIDS+=("$quota_watch_pid")
  sleep .2
  ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=64K count=1024 2>/dev/null | nc -N -w 5 10.200.0.1 19091' >"$ROOT/wireguard-quota-sender.log" 2>&1 &
  local quota_sender_pid=$!
  PIDS+=("$quota_sender_pid")
  wait "$quota_watch_pid"
  if ip netns exec "$NS" ping -c 1 -W 1 10.200.0.1 >/dev/null 2>&1; then
    echo 'WireGuard exhausted peer reconnected after offline quota removal' >&2
    exit 1
  fi
  if wg show wg-native peers | grep -Fq "$client_pub"; then
    echo 'WireGuard quota peer unexpectedly returned after reconnect attempt' >&2
    exit 1
  fi
  wait "$quota_sender_pid" || true
  wait "$quota_listener_pid" || true
  cat "$ROOT/wireguard-quota-watch.log"
  local quota_received
  quota_received="$(wc -c <"$ROOT/wireguard-quota-received")"
  if [ "$quota_received" -ge "$((remaining_raw_quota + 2 * 1024 * 1024))" ] || [ "$quota_received" -lt "$((remaining_raw_quota * 4 / 5))" ]; then
    echo "WireGuard quota traffic outside expected remaining-quota window: received=${quota_received} remaining=${remaining_raw_quota}" >&2
    exit 1
  fi
  echo "WireGuard: quota=${quota_bytes} bytes stopped the peer; prior raw=${prior_raw_quota_usage}, remaining=${remaining_raw_quota}, delivered=${quota_received} bytes"
}

run_openvpn() {
  echo '=== OpenVPN native tun/traffic/restart ==='
  local quota_bytes="${ANTIMAGE_OPENVPN_QUOTA_BYTES:-$((50 * 1024 * 1024))}"
  local ca_key="$ROOT/ca.key" ca_cert="$ROOT/ca.crt"
  local server_key="$ROOT/server.key" server_csr="$ROOT/server.csr" server_cert="$ROOT/server.crt"
  local client_key="$ROOT/client.key" client_csr="$ROOT/client.csr" client_cert="$ROOT/client.crt"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=antimage-native-ca' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -keyout "$ca_key" -out "$ca_cert" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -subj '/CN=antimage-native-server' \
    -keyout "$server_key" -out "$server_csr" >/dev/null 2>&1
  openssl x509 -req -days 1 -in "$server_csr" -CA "$ca_cert" -CAkey "$ca_key" \
    -CAcreateserial -extfile <(printf 'keyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=IP:10.250.0.1\n') \
    -out "$server_cert" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -subj '/CN=antimage-native-client' \
    -keyout "$client_key" -out "$client_csr" >/dev/null 2>&1
  openssl x509 -req -days 1 -in "$client_csr" -CA "$ca_cert" -CAkey "$ca_key" \
    -CAcreateserial -extfile <(printf 'keyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n') \
    -out "$client_cert" >/dev/null 2>&1
  cat >"$ROOT/openvpn-connect.sh" <<EOF
#!/bin/sh
exec env ANTIMAGE_OPENVPN_ACTION=admission ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" ANTIMAGE_OPENVPN_NATIVE_PID="\${daemon_pid}" "${ANTIMAGE_OPENVPN_TEST_BINARY:-/bin/true}" -test.run='^TestOpenVPNNativeAccountingStage$' -test.v
EOF
  chmod 700 "$ROOT/openvpn-connect.sh"
  cat >"$ROOT/server.conf" <<EOF
tls-server
ca $ca_cert
cert $server_cert
key $server_key
dh none
remote-cert-tls client
dev tun-native
proto udp
port 11940
server 10.210.0.0 255.255.255.0
persist-tun
status $ROOT/openvpn.status 1
status-version 3
management 127.0.0.1 11941
script-security 2
client-connect $ROOT/openvpn-connect.sh
data-ciphers AES-256-GCM
ping 1
ping-restart 3
verb 3
EOF
  cat >"$ROOT/client.conf" <<EOF
client
tls-client
ca $ca_cert
cert $client_cert
key $client_key
remote-cert-tls server
remote 10.250.0.1 11940
dev tun-native
proto udp
persist-tun
data-ciphers AES-256-GCM
ping 1
ping-restart 3
verb 3
EOF
  openvpn --config "$ROOT/server.conf" >"$ROOT/openvpn-server.log" 2>&1 &
  local server_pid=$!
  PIDS+=("$server_pid")
  ip netns exec "$NS" openvpn --config "$ROOT/client.conf" >/"$ROOT/openvpn-client.log" 2>&1 &
  PIDS+=("$!")
  wait_for 'OpenVPN server tun' ip link show tun-native
  wait_for 'OpenVPN client tun' ip netns exec "$NS" ip link show tun-native
  wait_for 'OpenVPN initial handshake' grep -Fq 'Initialization Sequence Completed' "$ROOT/openvpn-client.log"
  wait_for 'OpenVPN initial traffic' ip netns exec "$NS" ping -c 1 -W 1 10.210.0.1
  wait_for 'OpenVPN status-v3 client accounting' grep -Fq 'antimage-native-client' "$ROOT/openvpn.status"
  if [ -n "${ANTIMAGE_OPENVPN_TEST_BINARY:-}" ]; then
    env ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" \
      ANTIMAGE_OPENVPN_NATIVE_PID="$server_pid" \
      "$ANTIMAGE_OPENVPN_TEST_BINARY" -test.run='^TestOpenVPNNativeAccountingStage$' -test.v
  fi
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
  openvpn --config "$ROOT/server.conf" >"$ROOT/openvpn-server-restart.log" 2>&1 &
  server_pid=$!
  PIDS+=("$server_pid")
  wait_for 'OpenVPN server restart' kill -0 "$server_pid"
  wait_for 'OpenVPN post-restart tun' ip link show tun-native
  wait_for 'OpenVPN client reconnection' sh -c 'test "$(grep -cF "Initialization Sequence Completed" "$1")" -ge 2' sh "$ROOT/openvpn-client.log"
  wait_for 'OpenVPN post-restart traffic' ip netns exec "$NS" ping -c 1 -W 1 10.210.0.1
  if [ -n "${ANTIMAGE_OPENVPN_TEST_BINARY:-}" ]; then
    local openvpn_client_ip
    openvpn_client_ip="$(ip netns exec "$NS" ip -4 -o addr show dev tun-native | awk '{ split($4, address, "/"); print address[1]; exit }')"
    if [ -z "$openvpn_client_ip" ]; then
      echo 'OpenVPN client tunnel has no assigned IPv4 address' >&2
      return 1
    fi
    echo "OpenVPN speed test assigned client IPv4: $openvpn_client_ip"
    native_speed_policy_stage "$ANTIMAGE_OPENVPN_TEST_BINARY" tun-native "$openvpn_client_ip"
    measure_native_tunnel_speed openvpn 10.210.0.1 "$openvpn_client_ip" "$ANTIMAGE_OPENVPN_TEST_BINARY" tun-native
    env ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" \
      ANTIMAGE_OPENVPN_NATIVE_PID="$server_pid" \
      "$ANTIMAGE_OPENVPN_TEST_BINARY" -test.run='^TestOpenVPNNativeAccountingStage$' -test.v
    local previous_raw_quota_usage
    previous_raw_quota_usage="$(python3 - "$ROOT/openvpn-accounting/openvpn/usage-state.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    state = json.load(source)
key = '\0offline-total\0' + '7' + '\0native'
print(int(state.get('baseline', {}).get(key, 0)))
PY
)"
    local remaining_raw_quota="$((quota_bytes - previous_raw_quota_usage))"
    if [ "$remaining_raw_quota" -le 0 ]; then
      echo "OpenVPN lifecycle and speed traffic exhausted quota before cutoff test: used=${previous_raw_quota_usage} quota=${quota_bytes}" >&2
      return 1
    fi
    echo "OpenVPN quota remaining after lifecycle and speed traffic: ${remaining_raw_quota} raw bytes (used=${previous_raw_quota_usage})"
    local successful_handshakes_before_quota
    successful_handshakes_before_quota="$(grep -cF 'Initialization Sequence Completed' "$ROOT/openvpn-client.log" || true)"
    timeout 120 nc -l -p 19092 >"$ROOT/openvpn-quota-received" 2>&1 &
    local quota_listener_pid=$!
    PIDS+=("$quota_listener_pid")
    wait_for 'OpenVPN quota receiver' sh -c 'ss -lnt "( sport = :19092 )" | grep -q 19092'
    ip netns exec "$NS" tc qdisc replace dev "$VETH_NS" root tbf rate 12mbit burst 32kb latency 400ms
    env ANTIMAGE_OPENVPN_ACTION=quota-watch ANTIMAGE_OPENVPN_QUOTA_BYTES="$quota_bytes" \
      ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" \
      ANTIMAGE_OPENVPN_NATIVE_PID="$server_pid" \
      "$ANTIMAGE_OPENVPN_TEST_BINARY" -test.run='^TestOpenVPNNativeAccountingStage$' -test.v >"$ROOT/openvpn-quota-watch.log" 2>&1 &
    local quota_watch_pid=$!
    PIDS+=("$quota_watch_pid")
    sleep .2
    ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=64K count=1024 2>/dev/null | nc -N -w 5 10.210.0.1 19092' >"$ROOT/openvpn-quota-sender.log" 2>&1 &
    local quota_sender_pid=$!
    PIDS+=("$quota_sender_pid")
    wait "$quota_watch_pid"
    wait_for 'OpenVPN quota reconnect denied by the local admission hook' grep -Fq \
      'openvpn admission denied: data limit reached' "$ROOT/openvpn-server-restart.log"
    wait_for 'OpenVPN native client-connect hook rejection' grep -Fq \
      'WARNING: Failed running command (--client-connect)' "$ROOT/openvpn-server-restart.log"
    local successful_handshakes
    successful_handshakes="$(grep -cF 'Initialization Sequence Completed' "$ROOT/openvpn-client.log" || true)"
    if [ "$successful_handshakes" -gt "$successful_handshakes_before_quota" ]; then
      echo "OpenVPN established a new tunnel after quota rejection (before=${successful_handshakes_before_quota}, after=${successful_handshakes})" >&2
      exit 1
    fi
    if ip netns exec "$NS" ping -c 1 -W 1 10.210.0.1 >/dev/null 2>&1; then
      echo 'OpenVPN quota-denied reconnect still carried tunnel traffic' >&2
      exit 1
    fi
    wait "$quota_sender_pid" || true
    wait "$quota_listener_pid" || true
    cat "$ROOT/openvpn-quota-watch.log"
    local quota_received
    quota_received="$(wc -c <"$ROOT/openvpn-quota-received")"
    if [ "$quota_received" -ge "$((remaining_raw_quota + 2 * 1024 * 1024))" ] || [ "$quota_received" -lt "$((remaining_raw_quota * 4 / 5))" ]; then
      echo "OpenVPN quota traffic outside expected remaining-quota window: received=${quota_received} remaining=${remaining_raw_quota}" >&2
      exit 1
    fi
    echo "OpenVPN: quota=${quota_bytes} bytes stopped the native/reconnected session; prior raw=${previous_raw_quota_usage}, remaining=${remaining_raw_quota}, delivered=${quota_received} bytes"
  fi
  echo 'OpenVPN: tun session, traffic, and server restart passed'
}

if [ "${ANTIMAGE_NATIVE_RUN_WIREGUARD:-1}" = 1 ]; then
  run_wireguard
fi
if [ "${ANTIMAGE_NATIVE_RUN_OPENVPN:-1}" = 1 ]; then
  run_openvpn
fi

if [ "${ANTIMAGE_NATIVE_RUN_PANEL:-1}" = 1 ] && [ -n "${ANTIMAGE_VPN_PANEL_TEST_BINARY:-}" ]; then
  # Copy only real collector state, preserving helpers and all durable batches.
  mkdir -p "$ROOT/combined-node"
  cp -a "$ROOT/wireguard-quota/wireguard" "$ROOT/combined-node/"
  cp -a "$ROOT/openvpn-accounting/openvpn" "$ROOT/combined-node/"
  env ANTIMAGE_VPN_NATIVE_STATE="$ROOT/combined-node" \
    "$ANTIMAGE_VPN_PANEL_TEST_BINARY" -test.run='^TestNativeVPNCombinedPanelDBLostACK$' -test.v
fi
