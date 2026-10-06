#!/usr/bin/env bash
set -euo pipefail
: "${ANTIMAGE_L2TP_TEST_BINARY:?missing compiled nodeagent test binary}"
: "${ANTIMAGE_L2TP_PANEL_TEST_BINARY:?missing compiled nodecontroller test binary}"
: "${ANTIMAGE_L2TP_API_TEST_BINARY:?missing compiled API test binary}"
: "${ANTIMAGE_NODE_HELPER_BINARY:?missing compiled node helper binary}"

if [ "${ANTIMAGE_L2TP_PRIVATE_MOUNT:-0}" != 1 ]; then
  export ANTIMAGE_L2TP_PRIVATE_MOUNT=1
  exec unshare --mount --fork --propagation private bash "$0" "$@"
fi

ROOT="${ROOT:-$(mktemp -d "${RUNNER_TEMP:-/tmp}/antimage-l2tp-XXXXXX") }"
ROOT="${ROOT% }"
NS="antimage-l2tp-client"
VETH_HOST="altp-vh"
VETH_NS="altp-vn"
CLIENT_CHARON="charon"
CLIENT_IPSEC_RUNDIR=""
CLIENT_STRONGSWAN_CONF=""
SERVER_STRONGSWAN_CONF=""
PIDS=()
forget_pid() {
  local target="$1" pid
  local -a remaining=()
  for pid in "${PIDS[@]}"; do [ "$pid" = "$target" ] || remaining+=("$pid"); done
  PIDS=("${remaining[@]}")
}
stop_pid() {
  local pid="$1" state
  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do
    state="$(ps -o stat= -p "$pid" 2>/dev/null | tr -d ' ' || true)"
    if [ -z "$state" ] || [[ "$state" == Z* ]]; then wait "$pid" 2>/dev/null || true; forget_pid "$pid"; return 0; fi
    sleep .1
  done
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  forget_pid "$pid"
}
cleanup() {
  local rc=$?
  set +e
  if [ "$rc" -ne 0 ]; then
    mkdir -p "$RUNNER_TEMP/antimage-l2tp-failure"
    cp -a "$ROOT/." "$RUNNER_TEMP/antimage-l2tp-failure/" 2>/dev/null || true
    chmod -R a+rX "$RUNNER_TEMP/antimage-l2tp-failure" 2>/dev/null || true
    if [ -s "$ROOT/quota-watch.log" ]; then
      echo '=== captured L2TP quota worker output ===' >&2
      cat "$ROOT/quota-watch.log" >&2
    fi
    { date -u; ip -details addr show; ip route show table all; ip netns list; ip netns exec "$NS" ip -details addr show; ps -ef; } \
      >"$RUNNER_TEMP/antimage-l2tp-failure/network-state.txt" 2>&1
    chmod a+r "$RUNNER_TEMP/antimage-l2tp-failure/network-state.txt" 2>/dev/null || true
    {
      echo '=== host XFRM states (key-bearing lines omitted) ==='
      ip -s xfrm state | sed -E '/^[[:space:]]+(auth|auth-trunc|enc|aead|comp) /d' || true
      echo '=== host XFRM policies ==='
      ip -s xfrm policy || true
      echo '=== client XFRM states (key-bearing lines omitted) ==='
      ip netns exec "$NS" ip -s xfrm state | sed -E '/^[[:space:]]+(auth|auth-trunc|enc|aead|comp) /d' || true
      echo '=== client XFRM policies ==='
      ip netns exec "$NS" ip -s xfrm policy || true
    } >"$RUNNER_TEMP/antimage-l2tp-failure/xfrm-state.txt" 2>&1
    chmod a+r "$RUNNER_TEMP/antimage-l2tp-failure/xfrm-state.txt" 2>/dev/null || true
    echo "L2TP native evidence retained at $RUNNER_TEMP/antimage-l2tp-failure" >&2
  fi
  for pid in "${PIDS[@]}"; do stop_pid "$pid"; done
  if ip netns list | grep -q "^$NS[[:space:]]"; then
    if [ -n "$CLIENT_IPSEC_RUNDIR" ] && [ -s "$CLIENT_STRONGSWAN_CONF" ]; then
      client_ipsec stop >/dev/null 2>&1 || true
    fi
  fi
  ipsec stop >/dev/null 2>&1 || true
  if [ -n "${ANTIMAGE_L2TP_TEST_BINARY:-}" ] && [ -d "$ROOT/l2tp-state" ]; then
    env ANTIMAGE_L2TP_NATIVE_STATE="$ROOT/l2tp-state" ANTIMAGE_L2TP_NATIVE_ACTION=cleanup \
      "$ANTIMAGE_L2TP_TEST_BINARY" -test.run='^TestL2TPNativeAccountingStage$' -test.v >/dev/null 2>&1 || true
  fi
  ip link del "$VETH_HOST" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  if [ "$rc" -eq 0 ]; then rm -rf "$ROOT"; fi
}
trap cleanup EXIT

mkdir -p "$ROOT" "$ROOT/etc/ppp/ip-pre-up.d" "$ROOT/etc/xl2tpd" "$ROOT/etc/run"
mkdir -p "$ROOT/l2tp-state"
# The package may auto-start a host strongSwan daemon before the test mount is
# installed. Stop it so the production ipsec start command loads this test's
# generated L2TP connection from the isolated /etc/ipsec.conf.
systemctl stop strongswan-starter.service >/dev/null 2>&1 || true
systemctl stop strongswan.service >/dev/null 2>&1 || true
ipsec stop >/dev/null 2>&1 || true
# strongSwan's bypass-lan plugin otherwise installs higher-priority cleartext
# policies for the directly connected test subnet, shadowing UDP/1701 ESP.
SERVER_STRONGSWAN_CONF="$ROOT/server-strongswan.conf"
cat >"$SERVER_STRONGSWAN_CONF" <<'EOF'
include /etc/strongswan.d/*.conf
charon {
    plugins {
        bypass-lan {
            interfaces_ignore = altp-vh
        }
    }
}
EOF
mount --bind "$SERVER_STRONGSWAN_CONF" /etc/strongswan.conf
# Current kernels expose the PPP-over-L2TP driver as l2tp_ppp. Try the legacy
# pppol2tp module name too, but do not fail when that alias is not shipped.
modprobe l2tp_ppp
modprobe pppol2tp 2>/dev/null || true
# Keep pppd's distro-provided dispatcher in the private /etc/ppp mount. The
# production admission hook is installed into ip-pre-up.d; hiding this wrapper
# would let pppd bring up PPP without invoking any pre-up hooks.
if [ ! -x /etc/ppp/ip-pre-up ]; then
  echo "ppp package did not provide executable /etc/ppp/ip-pre-up dispatcher" >&2
  exit 1
fi
cp -a /etc/ppp/ip-pre-up "$ROOT/etc/ppp/ip-pre-up"
for file in "$ROOT/etc/ipsec.conf" "$ROOT/etc/ipsec.secrets" "$ROOT/etc/ppp/options"; do : >"$file"; done
mount --bind "$ROOT/etc/ppp" /etc/ppp
mount --bind "$ROOT/etc/xl2tpd" /etc/xl2tpd
mount --bind "$ROOT/etc/ipsec.conf" /etc/ipsec.conf
mount --bind "$ROOT/etc/ipsec.secrets" /etc/ipsec.secrets

quota_bytes="${ANTIMAGE_L2TP_QUOTA_BYTES:-52428800}"
stage() {
  local action="$1" quota="${2:-$quota_bytes}"
  env ANTIMAGE_L2TP_NATIVE_STATE="$ROOT/l2tp-state" \
    ANTIMAGE_L2TP_NATIVE_ACTION="$action" \
    ANTIMAGE_L2TP_QUOTA_BYTES="$quota" \
    ANTIMAGE_L2TP_SESSION_CALLBACK_URL="$callback_url" \
    ANTIMAGE_L2TP_SESSION_CALLBACK_TOKEN="$callback_token" \
    ANTIMAGE_NODE_HELPER_BINARY="$ANTIMAGE_NODE_HELPER_BINARY" \
    "$ANTIMAGE_L2TP_TEST_BINARY" -test.run='^TestL2TPNativeAccountingStage$' -test.v
}
panel_replay() {
  env ANTIMAGE_L2TP_NATIVE_STATE="$ROOT/l2tp-state" \
    ANTIMAGE_NATIVE_PANEL_REQUIRE_FINAL="${1:-0}" \
    "$ANTIMAGE_L2TP_PANEL_TEST_BINARY" -test.run='^TestL2TPNativePanelDB$' -test.v
}
set_session_panel_usage() {
  python3 - "$1" "$2" <<'PY'
import json, os, sys
path, used = sys.argv[1], int(sys.argv[2])
with open(path, encoding='utf-8') as f:
    cfg = json.load(f)
matches = [name for name, uid in cfg['users'].items() if int(uid) == 7]
if len(matches) != 1 or matches[0] not in cfg.get('policies', {}):
    raise SystemExit(f'expected one L2TP panel policy for user 7 in {path}')
cfg['policies'][matches[0]]['used_traffic'] = used
tmp = path + '.tmp'
with open(tmp, 'w', encoding='utf-8') as f:
    json.dump(cfg, f)
    f.flush()
    os.fsync(f.fileno())
os.replace(tmp, path)
PY
}

wait_for() {
  local name="$1"; shift
  for _ in $(seq 1 300); do "$@" >/dev/null 2>&1 && return 0; sleep .1; done
  echo "timeout waiting for $name" >&2
  for log in "$ROOT"/*.log; do [ -f "$log" ] && { echo "--- $log ---"; cat "$log"; }; done
  return 1
}
wait_for_gone() {
  local name="$1"; shift
  for _ in $(seq 1 200); do if ! "$@" >/dev/null 2>&1; then return 0; fi; sleep .1; done
  echo "timeout waiting for $name to stop" >&2
  return 1
}
env ANTIMAGE_L2TP_API_STATE="$ROOT/l2tp-state" \
  "$ANTIMAGE_L2TP_API_TEST_BINARY" -test.run='^TestL2TPNativePanelSessionServer$' -test.v >"$ROOT/panel-api.log" 2>&1 &
API_PID=$!; PIDS+=("$API_PID")
wait_for 'production Panel session-event endpoint' test -s "$ROOT/l2tp-state/api-callback.txt"
mapfile -t callback_lines <"$ROOT/l2tp-state/api-callback.txt"
callback_url="${callback_lines[0]:-}"
callback_token="${callback_lines[1]:-}"
if [ -z "$callback_url" ] || [ -z "$callback_token" ]; then
  cat "$ROOT/panel-api.log" >&2
  echo 'Panel session-event endpoint did not publish a callback URL and token' >&2
  exit 1
fi
start_server() {
  rm -f "$ROOT/server.pid" "$ROOT/server.control"
  xl2tpd -D -c /etc/xl2tpd/xl2tpd.conf -p "$ROOT/server.pid" -C "$ROOT/server.control" >"$ROOT/xl2tpd-server.log" 2>&1 &
  SERVER_PID=$!; PIDS+=("$SERVER_PID")
  wait_for 'server UDP 1701 listener' sh -c 'ss -lun "( sport = :1701 )" | grep -q 1701'
}
write_client_files() {
  cat >"$ROOT/client-options" <<EOF
noauth
name native-l2tp
password native-l2tp-secret
refuse-eap
refuse-pap
refuse-mschap
nomagic
noipdefault
nodefaultroute
mtu 1200
mru 1200
debug
logfile $ROOT/client-pppd.log
EOF
  cat >"$ROOT/client.conf" <<EOF
[global]
port = 1701
access control = no

[lac antimage]
lns = 10.251.0.1
pppoptfile = $ROOT/client-options
autodial = no
redial = no
EOF
  cat >"$ROOT/client-ipsec.conf" <<EOF
config setup
    uniqueids=no
conn l2tp-client
    auto=add
    keyexchange=ikev1
    authby=secret
    type=transport
    left=%defaultroute
    leftprotoport=17/1701
    right=10.251.0.1
    rightprotoport=17/1701
    rekey=no
    forceencaps=yes
    fragmentation=yes
EOF
}
start_client_ipsec() {
  CLIENT_IPSEC_RUNDIR="$ROOT/client-ipsec-run"
  CLIENT_STRONGSWAN_CONF="$ROOT/client-strongswan.conf"
  mkdir -p "$CLIENT_IPSEC_RUNDIR"
  cat >"$CLIENT_STRONGSWAN_CONF" <<EOF
include /etc/strongswan.d/*.conf
charon {
    plugins {
        bypass-lan {
            interfaces_ignore = altp-vn
        }
    }
    filelog {
        l2tp-client {
            path = $ROOT/client-charon.log
            default = 1
            flush_line = yes
        }
    }
}
EOF
  : >"$ROOT/client-charon.log"
  wait_for 'server strongSwan control socket' sh -c 'ipsec status >/dev/null 2>&1'
  client_ipsec_daemon >"$ROOT/client-ipsec.log" 2>&1 &
  CLIENT_IPSEC_PID=$!; PIDS+=("$CLIENT_IPSEC_PID")
  if ! wait_for 'client strongSwan control socket' sh -c 'test -S "$1/charon.ctl" && test -s "$1/charon.pid"' _ "$CLIENT_IPSEC_RUNDIR"; then
    client_ipsec status >"$ROOT/client-ipsec-status.log" 2>&1 || true
    ls -la "$CLIENT_IPSEC_RUNDIR" >"$ROOT/client-ipsec-rundir.txt" 2>&1 || true
    cat "$ROOT/client-ipsec-status.log" "$ROOT/client-ipsec-rundir.txt" "$ROOT/client-charon.log" >&2
    return 1
  fi
  if ! client_ipsec up l2tp-client >"$ROOT/client-ipsec-up.log" 2>&1; then
    client_ipsec statusall >"$ROOT/client-ipsec-statusall.log" 2>&1 || true
    journalctl -b --no-pager | grep -E 'charon|strongSwan' | tail -n 200 >"$ROOT/client-charon-journal.log" || true
    ip netns exec "$NS" ip xfrm state >"$ROOT/client-ipsec-xfrm-state.txt" 2>&1 || true
    ls -la "$CLIENT_IPSEC_RUNDIR" >"$ROOT/client-ipsec-rundir.txt" 2>&1 || true
    cat "$ROOT/client-ipsec.log" "$ROOT/client-ipsec-up.log" \
      "$ROOT/client-ipsec-statusall.log" "$ROOT/client-charon-journal.log" \
      "$ROOT/client-ipsec-xfrm-state.txt" >&2
    return 1
  fi
  wait_for 'client IPsec transport policy' sh -c 'ip netns exec "$1" ip xfrm state | grep -q "proto esp"' _ "$NS"
  wait_for 'server IPsec transport policy' sh -c 'ip xfrm state | grep -q "proto esp"'
  tcpdump -U -n -i "$VETH_HOST" 'udp port 4500 or udp port 1701' \
    -w "$ROOT/l2tp-ipsec.pcap" >"$ROOT/l2tp-ipsec-capture.log" 2>&1 &
  IPSEC_CAPTURE_PID=$!; PIDS+=("$IPSEC_CAPTURE_PID")
  wait_for 'L2TP IPsec packet capture' sh -c 'test -s "$1"' _ "$ROOT/l2tp-ipsec.pcap"
}
client_ipsec() {
  ip netns exec "$NS" unshare --mount --fork --propagation private bash -c '
    mount --bind "$1" /run
    mount --bind "$2" /etc/strongswan.conf
    shift 2
    exec "$@"
  ' _ "$CLIENT_IPSEC_RUNDIR" "$CLIENT_STRONGSWAN_CONF" env \
    IPSEC_PIDDIR="$CLIENT_IPSEC_RUNDIR" \
    IPSEC_STARTER_PID="$CLIENT_IPSEC_RUNDIR/starter.pid" \
    IPSEC_CHARON_PID="$CLIENT_IPSEC_RUNDIR/charon.pid" \
    DAEMON_NAME=charon ipsec "$@"
}
client_ipsec_daemon() {
  ip netns exec "$NS" unshare --mount --fork --propagation private bash -c '
    mount --bind "$1" /run
    mount --bind "$2" /etc/strongswan.conf
    shift 2
    exec "$@"
  ' _ "$CLIENT_IPSEC_RUNDIR" "$CLIENT_STRONGSWAN_CONF" env \
    IPSEC_PIDDIR="$CLIENT_IPSEC_RUNDIR" \
    IPSEC_STARTER_PID="$CLIENT_IPSEC_RUNDIR/starter.charon.pid" \
    IPSEC_CHARON_PID="$CLIENT_IPSEC_RUNDIR/charon.pid" \
    DAEMON_NAME="$CLIENT_CHARON" /usr/lib/ipsec/starter \
    --daemon "$CLIENT_CHARON" --conf "$ROOT/client-ipsec.conf" --nofork
}
start_client() {
  rm -f "$ROOT/client.control"
  ip netns exec "$NS" xl2tpd -D -c "$ROOT/client.conf" -p "$ROOT/client.pid" -C "$ROOT/client.control" >"$ROOT/xl2tpd-client.log" 2>&1 &
  CLIENT_DAEMON_PID=$!; PIDS+=("$CLIENT_DAEMON_PID")
  wait_for 'client control pipe' test -p "$ROOT/client.control"
  printf 'c antimage\n' >"$ROOT/client.control"
  wait_for 'client PPP interface' ip netns exec "$NS" ip link show ppp0
  wait_for 'server PPP interface' sh -c 'ip -o link show | grep -q "ppp[0-9]"'
  wait_for 'durable production session-start hook' sh -c 'find "$1" -path "*/ppp-accounting/active/ppp*.json" -print -quit | grep -q .' _ "$ROOT/l2tp-state"
}
wait_for_empty_session_outbox() {
  for _ in $(seq 1 300); do
    if ! find "$ROOT/l2tp-state/native-session-outbox" -type f -name '*.json' -print -quit 2>/dev/null | grep -q .; then return 0; fi
    sleep .1
  done
  echo 'durable L2TP Panel session events were not replayed' >&2
  cat "$ROOT/panel-api.log" >&2
  return 1
}

echo '=== production L2TP daemon configuration and real xl2tpd/PPP tunnel ==='
stage prepare
config_path="$(cat "$ROOT/l2tp-state/xl2tpd-config-path")"
ppp_options_path="$(awk -F ' = ' '/^pppoptfile = / {print $2}' "$config_path")"
printf '\ndebug\nlogfile %s\n' "$ROOT/server-pppd.log" >>"$ppp_options_path"
cp "$config_path" /etc/xl2tpd/xl2tpd.conf
write_client_files
ip netns add "$NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"
ip link set "$VETH_NS" netns "$NS"
ip addr add 10.251.0.1/24 dev "$VETH_HOST"
ip link set "$VETH_HOST" up
ip netns exec "$NS" ip addr add 10.251.0.2/24 dev "$VETH_NS"
ip netns exec "$NS" ip link set lo up
ip netns exec "$NS" ip link set "$VETH_NS" up
stage ipsec-start
start_client_ipsec
start_server
start_client
wait_for 'assigned production VPN address' sh -c 'ip netns exec "$1" ip -o -4 addr show dev ppp0 | grep -q "10.67.0.2 peer 10.67.0.1"' _ "$NS"
ip netns exec "$NS" ping -c 2 -W 2 10.67.0.1
timeout 20 ip netns exec "$NS" nc -l -p 19092 >"$ROOT/downlink-received" 2>&1 &
DOWNLINK_PID=$!; PIDS+=("$DOWNLINK_PID")
wait_for 'downlink receiver' sh -c 'ip netns exec "$1" ss -lnt "( sport = :19092 )" | grep -q 19092' _ "$NS"
dd if=/dev/zero bs=64K count=16 status=none | nc -N -w 5 10.67.0.2 19092
wait "$DOWNLINK_PID"; forget_pid "$DOWNLINK_PID"
stop_pid "$IPSEC_CAPTURE_PID"
esp_packets="$(tcpdump -nn -r "$ROOT/l2tp-ipsec.pcap" 'udp port 4500' 2>/dev/null | wc -l)"
clear_l2tp_packets="$(tcpdump -nn -r "$ROOT/l2tp-ipsec.pcap" 'udp port 1701' 2>/dev/null | wc -l)"
if [ "$esp_packets" -lt 4 ] || [ "$clear_l2tp_packets" -ne 0 ]; then
  tcpdump -nn -r "$ROOT/l2tp-ipsec.pcap" >&2 || true
  echo "L2TP traffic bypassed IPsec: ESP/UDP packets=${esp_packets}, clear UDP/1701 packets=${clear_l2tp_packets}" >&2
  exit 1
fi
echo "L2TP IPsec transport verified on veth: UDP/4500 packets=${esp_packets}, clear UDP/1701 packets=${clear_l2tp_packets}"
downlink_bytes="$(wc -c <"$ROOT/downlink-received")"
if [ "$downlink_bytes" -ne "$((16 * 64 * 1024))" ]; then
  echo "L2TP server-to-client transfer mismatch: received=${downlink_bytes}" >&2
  exit 1
fi
stage collect-first
if ! find "$ROOT/l2tp-state/native-session-outbox" -type f -name '*.json' -print -quit | grep -q .; then
  echo 'L2TP session start was not durably queued while Panel was offline' >&2
  exit 1
fi

echo '=== runtime restart, durable node-state reload, and reconnect ==='
stop_pid "$SERVER_PID"
stop_pid "$CLIENT_DAEMON_PID"
ip netns exec "$NS" pkill -TERM pppd 2>/dev/null || true
pkill -TERM pppd 2>/dev/null || true
wait_for_gone 'client PPP interface shutdown' ip netns exec "$NS" ip link show ppp0
wait_for_gone 'server PPP interface shutdown' sh -c 'ip -o link show | grep -q "ppp[0-9]"'
wait_for 'durable L2TP disconnect event while Panel is offline' \
  sh -c 'test "$(find "$1/native-session-outbox" -type f -name "*.json" | wc -l)" -ge 2' _ "$ROOT/l2tp-state"
start_server
rm -f "$ROOT/l2tp-state/panel-down"
start_client
wait_for_empty_session_outbox
ip netns exec "$NS" ping -c 2 -W 2 10.67.0.1
panel_replay
stage ack
# A fresh collector process reloads the acknowledged production state and
# emits a new immutable delta batch; SQLite then retries X and Y across reopen.
stage collect-next
panel_replay
stage ack
previous_effective="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["effective_total"])' "$ROOT/l2tp-state/native-panel-receipt.json")"
if [ "$previous_effective" -ge "$quota_bytes" ]; then
  echo "L2TP pre-quota traffic already exhausted quota: effective=${previous_effective} quota=${quota_bytes}" >&2
  exit 1
fi
remaining_effective="$((quota_bytes - previous_effective))"
raw_quota_bytes="$((remaining_effective / 3))"
session_config="$(find "$ROOT/l2tp-state/l2tp" -name session-helper.json -print -quit)"
set_session_panel_usage "$session_config" "$previous_effective"

echo '=== native L2TP effective 50 MiB quota cutoff with coefficient accounting ==='
timeout 180 nc -l -p 19091 >"$ROOT/quota-received" 2>&1 &
LISTENER_PID=$!; PIDS+=("$LISTENER_PID")
wait_for 'quota receiver' sh -c 'ss -lnt "( sport = :19091 )" | grep -q 19091'
ip netns exec "$NS" tc qdisc replace dev "$VETH_NS" root tbf rate 12mbit burst 32kb latency 400ms
stage quota-watch "$quota_bytes" >"$ROOT/quota-watch.log" 2>&1 &
WATCH_PID=$!; PIDS+=("$WATCH_PID")
sleep .2
ip netns exec "$NS" sh -c 'dd if=/dev/zero bs=1M count=50 status=none | nc -N -w 10 10.67.0.1 19091' >"$ROOT/quota-sender.log" 2>&1 &
SENDER_PID=$!; PIDS+=("$SENDER_PID")
wait "$WATCH_PID"; forget_pid "$WATCH_PID"
wait_for_gone 'quota-exhausted client PPP interface' ip netns exec "$NS" ip link show ppp0
wait "$SENDER_PID" || true; forget_pid "$SENDER_PID"
wait "$LISTENER_PID" || true; forget_pid "$LISTENER_PID"
cat "$ROOT/quota-watch.log"

echo '=== exhausted L2TP credential reconnect is denied before PPP is usable ==='
find "$ROOT/l2tp-state" -path '*/ppp-accounting/admission-denials/7.json' -delete
printf 'c antimage\n' >"$ROOT/client.control"
wait_for 'durable L2TP reconnect denial' sh -c 'find "$1" -path "*/ppp-accounting/admission-denials/7.json" -print -quit | grep -q .' _ "$ROOT/l2tp-state"
wait_for_gone 'denied reconnect PPP interface' ip netns exec "$NS" ip link show ppp0
stage collect-final
panel_replay 1
stage ack
wait_for_empty_session_outbox
touch "$ROOT/l2tp-state/api-stop"
wait "$API_PID"
forget_pid "$API_PID"
cat "$ROOT/panel-api.log"
received="$(wc -c <"$ROOT/quota-received")"
if [ "$received" -ge "$((raw_quota_bytes + 2 * 1024 * 1024))" ] || [ "$received" -lt "$((raw_quota_bytes * 4 / 5))" ]; then
  echo "L2TP quota traffic outside bound: payload=${received} quota=${quota_bytes}" >&2
  exit 1
fi
echo "L2TP: effective quota=${quota_bytes} bytes, coefficients=1.5x2, raw payload=${received} bytes, expected raw quota about ${raw_quota_bytes} bytes"
