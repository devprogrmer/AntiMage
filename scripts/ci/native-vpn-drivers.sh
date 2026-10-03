#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(mktemp -d)}"
NS="${NS:-antimage-vpn-client}"
VETH_HOST="avpn-vh"
VETH_NS="avpn-vn"
PIDS=()
cleanup() {
  set +e
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
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
  ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=2 2>/dev/null | nc -w 2 10.200.0.1 19090' &
  local nc_pid=$!
  (nc -l -p 19090 >/dev/null 2>&1 || nc -l 19090 >/dev/null 2>&1) &
  PIDS+=("$nc_pid" "$!")
  wait "$nc_pid" || true

  ip link del wg-native
  ip link add wg-native type wireguard
  ip addr add 10.200.0.1/24 dev wg-native
  wg set wg-native listen-port 51820 private-key <(printf '%s\n' "$server_priv") peer "$client_pub" allowed-ips 10.200.0.2/32
  ip link set wg-native up
  wait_for 'WireGuard post-restart handshake' ip netns exec "$NS" ping -c 1 -W 1 10.200.0.1
  echo 'WireGuard: handshake, traffic, and interface restart passed'
}

run_openvpn() {
  echo '=== OpenVPN native tun/traffic/restart ==='
  local key="$ROOT/static.key"
  openvpn --genkey secret "$key"
  cat >"$ROOT/server.conf" <<EOF
secret $key
dev tun-native
proto udp
port 11940
ifconfig 10.210.0.1 10.210.0.2
persist-key
persist-tun
cipher AES-256-GCM
data-ciphers AES-256-GCM:CHACHA20-POLY1305
verb 0
EOF
  cat >"$ROOT/client.conf" <<EOF
secret $key
remote 10.250.0.1 11940
dev tun-native
proto udp
ifconfig 10.210.0.2 10.210.0.1
persist-key
persist-tun
cipher AES-256-GCM
data-ciphers AES-256-GCM:CHACHA20-POLY1305
verb 0
EOF
  openvpn --config "$ROOT/server.conf" >/"$ROOT/openvpn-server.log" 2>&1 &
  PIDS+=("$!")
  ip netns exec "$NS" openvpn --config "$ROOT/client.conf" >/"$ROOT/openvpn-client.log" 2>&1 &
  PIDS+=("$!")
  wait_for 'OpenVPN server tun' ip link show tun-native
  wait_for 'OpenVPN client tun' ip netns exec "$NS" ip link show tun-native
  ip netns exec "$NS" ping -c 2 -W 2 10.210.0.1
  kill "${PIDS[0]}" 2>/dev/null || true
  wait_for 'OpenVPN server restart' sh -c 'openvpn --config "$1" >/"$2" 2>&1 &' sh "$ROOT/server.conf" "$ROOT/openvpn-server-restart.log"
  PIDS+=("$!")
  sleep 1
  ip netns exec "$NS" ping -c 2 -W 2 10.210.0.1
  echo 'OpenVPN: tun session, traffic, and server restart passed'
}

run_wireguard
run_openvpn
