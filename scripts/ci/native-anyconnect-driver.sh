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
SERVERCERT="sha256:$(openssl x509 -in "$ROOT/cert.pem" -noout -fingerprint -sha256 | tr -d ':' | cut -d= -f2)"
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
socket-file = $ROOT/ocserv.sock
ipv4-network = 192.0.2.0
ipv4-netmask = 255.255.255.0
dns = 1.1.1.1
max-clients = 4
EOF
ocserv --foreground --config="$ROOT/ocserv.conf" >"$ROOT/ocserv.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 80); do ss -lnt '( sport = :4433 )' | grep -q 4433 && break; sleep .25; done
ss -lnt '( sport = :4433 )' | grep -q 4433
ip netns exec "$NS" sh -c "printf '%s\n' native-password | openconnect --protocol=anyconnect --user=native-user --passwd-on-stdin --servercert '$SERVERCERT' --no-dtls --interface=vpn-native https://10.253.0.1:4433" >"$ROOT/openconnect.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
ip netns exec "$NS" ip link show vpn-native >/dev/null
ip netns exec "$NS" ip addr show vpn-native
ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
kill "${PIDS[1]}" 2>/dev/null || true
sleep 1
ip netns exec "$NS" sh -c "printf '%s\n' native-password | openconnect --protocol=anyconnect --user=native-user --passwd-on-stdin --servercert '$SERVERCERT' --no-dtls --interface=vpn-native https://10.253.0.1:4433" >"$ROOT/openconnect-reconnect.log" 2>&1 &
PIDS+=("$!")
for _ in $(seq 1 120); do ip netns exec "$NS" ip link show vpn-native >/dev/null 2>&1 && break; sleep .25; done
ip netns exec "$NS" ip link show vpn-native >/dev/null
ip netns exec "$NS" ping -c 3 -W 2 192.0.2.1
echo 'AnyConnect/ocserv: real authenticated session, tun creation, and reconnect passed'
