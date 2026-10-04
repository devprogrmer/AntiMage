#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d)}"
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

wireguard_native_stage() {
  local action="$1" state="$2"
  [ -n "${ANTIMAGE_WIREGUARD_TEST_BINARY:-}" ] || return 0
  env ANTIMAGE_WIREGUARD_NATIVE_STATE="$state" \
    ANTIMAGE_WIREGUARD_NATIVE_INTERFACE=wg-native \
    ANTIMAGE_WIREGUARD_NATIVE_PEER="$client_pub" \
    ANTIMAGE_WIREGUARD_ACTION="$action" \
    "$ANTIMAGE_WIREGUARD_TEST_BINARY" -test.run='^TestWireGuardNativeAccountingStage$' -test.v
}

run_wireguard() {
  echo '=== WireGuard native handshake/traffic/restart ==='
  local server_priv client_priv server_pub client_pub
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
  wireguard_native_stage collect "$ROOT/wireguard-accounting"
  wireguard_native_stage ack "$ROOT/wireguard-accounting"

  timeout 15 nc -l -p 19091 >/dev/null 2>&1 &
  local quota_listener_pid=$!
  PIDS+=("$quota_listener_pid")
  wait_for 'WireGuard quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
  ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=2 2>/dev/null | nc -N -w 5 10.200.0.1 19091' >"$ROOT/wireguard-quota-sender.log" 2>&1 &
  local quota_sender_pid=$!
  PIDS+=("$quota_sender_pid")
  wait "$quota_sender_pid" || true
  wait "$quota_listener_pid" || true
  wireguard_native_stage quota "$ROOT/wireguard-quota"
  echo 'WireGuard: handshake, traffic, and interface restart passed'
}

run_openvpn() {
  echo '=== OpenVPN native tun/traffic/restart ==='
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
    env ANTIMAGE_OPENVPN_ACTION=quota ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" \
      ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" \
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
    env ANTIMAGE_OPENVPN_NATIVE_ROOT="$ROOT" ANTIMAGE_OPENVPN_NATIVE_STATE="$ROOT/openvpn-accounting" \
      ANTIMAGE_OPENVPN_NATIVE_PID="$server_pid" \
      "$ANTIMAGE_OPENVPN_TEST_BINARY" -test.run='^TestOpenVPNNativeAccountingStage$' -test.v
  fi
  echo 'OpenVPN: tun session, traffic, and server restart passed'
}

run_wireguard
run_openvpn
