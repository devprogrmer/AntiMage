#!/usr/bin/env bash
# Read-only production diagnostic collector for an installed AntiMage node.
#
# Works against an ALREADY DEPLOYED antimage-node. It does not need any new
# binary, new instrumentation, or unmerged code, and it never writes to the
# service, its configuration, its accounting state, or any database.
#
# It only ever:
#   * reads /proc, /sys and /etc metadata,
#   * runs explicitly read-only status commands (systemctl show/cat, ss, wg show,
#     sha256sum, strings/readelf/grep on the installed binary),
#   * opens the panel database in read-only mode (sqlite mode=ro + query_only,
#     or mysql in a READ ONLY transaction) and projects aggregate counts only.
#
# It never restarts, reloads, installs, migrates, truncates or flushes anything,
# and it never selects credential columns (passwords, UUIDs, keys, tokens,
# certificates, proxy settings, emails or usernames).

set -u
umask 077

SECONDS_TO_SAMPLE=60
INTERVAL=2
OUTPUT=""
DATA_DIR=""
NODE_PID=""
SQLITE_DB=""
MYSQL_DEFAULTS=""
MYSQL_DATABASE=""
APP_DIR_DEFAULT=/opt/antimage-node
SERVICE_UNIT=antimage-node.service
MAX_OBSERVE=600

usage() {
  cat <<'USAGE'
Usage: production_diagnostic.sh [options]

  --seconds N          process sampling window, 60..600 (default 60)
  --interval N         seconds between samples, 1..30 (default 2)
  --output PATH        report file (default /var/tmp/antimage-diag-<utc>/report.txt)
  --data-dir PATH      node state directory (default: /var/lib/antimage-node)
  --pid PID            sample one specific antimage-node PID
  --sqlite PATH        panel SQLite database (read-only)
  --mysql-defaults P   mysql client defaults file (mode 0600) for the panel DB
  --database NAME      panel database name, required with --mysql-defaults
  -h, --help           this help

The script never modifies anything. Run it as root so it can read /proc/<pid>.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --seconds) SECONDS_TO_SAMPLE="${2:-}"; shift 2 ;;
    --interval) INTERVAL="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --pid) NODE_PID="${2:-}"; shift 2 ;;
    --sqlite) SQLITE_DB="${2:-}"; shift 2 ;;
    --mysql-defaults) MYSQL_DEFAULTS="${2:-}"; shift 2 ;;
    --database) MYSQL_DATABASE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$SECONDS_TO_SAMPLE" in ''|*[!0-9]*) echo "--seconds must be an integer" >&2; exit 2 ;; esac
case "$INTERVAL" in ''|*[!0-9]*) echo "--interval must be an integer" >&2; exit 2 ;; esac
[ "$SECONDS_TO_SAMPLE" -lt 60 ] && SECONDS_TO_SAMPLE=60
[ "$SECONDS_TO_SAMPLE" -gt "$MAX_OBSERVE" ] && SECONDS_TO_SAMPLE=$MAX_OBSERVE
[ "$INTERVAL" -lt 1 ] && INTERVAL=1
[ "$INTERVAL" -gt 30 ] && INTERVAL=30

have() { command -v "$1" >/dev/null 2>&1; }
read_file() { [ -r "$1" ] && cat "$1" 2>/dev/null; }
trim() { printf '%s' "$1" | tr -d '\r\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'; }

# Aggregate rx/tx byte counters for VPN and common physical interfaces.
# /proc/net/dev pads short interface names, so fields must be split on plain
# whitespace (leading blanks skipped) instead of on "[: ]+": with the latter,
# names such as enp3s0 (>= 6 chars, no padding) shift every field by one and
# silently report packet counts as bytes.
read_vpn_net() {
  awk '/^[[:space:]]*(eth|ens|enp|eno|wlan|tun|tap|wg|awg|ppp)[^:]*:/ {rx+=$2; tx+=$10} END {print rx+0, tx+0}' /proc/net/dev 2>/dev/null
}

if [ -z "$OUTPUT" ]; then
  OUTPUT="/var/tmp/antimage-diag-$(date -u +%Y%m%dT%H%M%SZ)/report.txt"
fi
mkdir -p "$(dirname "$OUTPUT")" || { echo "cannot create $(dirname "$OUTPUT")" >&2; exit 1; }
: > "$OUTPUT" || { echo "cannot write $OUTPUT" >&2; exit 1; }

say() { printf '%s\n' "$*" >> "$OUTPUT"; }
section() { say ""; say "=== $* ==="; }
unavailable() { say "$1: unavailable ($2)"; }

NODE_ASSOC=""
discover_node() {
  if [ -n "$NODE_PID" ]; then
    [ -d "/proc/$NODE_PID" ] || return 1
    return 0
  fi
  local pid
  # The installed unit is the most reliable source; fall back to process scans.
  if have systemctl; then
    pid="$(systemctl show "$SERVICE_UNIT" -p MainPID --value 2>/dev/null)"
    case "$pid" in ''|0|*[!0-9]*) : ;; *) [ -d "/proc/$pid" ] && { NODE_PID="$pid"; return 0; } ;; esac
  fi
  for pid in $(pgrep -x antimage-node 2>/dev/null); do
    NODE_PID="$pid"; break
  done
  if [ -z "$NODE_PID" ]; then
    for pid in $(pgrep -f 'antimage-node' 2>/dev/null); do
      [ -r "/proc/$pid/comm" ] || continue
      [ "$(trim "$(read_file "/proc/$pid/comm")")" = "antimage-node" ] || continue
      NODE_PID="$pid"; break
    done
  fi
  [ -n "$NODE_PID" ] && [ -d "/proc/$NODE_PID" ]
}

discover_data_dir() {
  [ -n "$DATA_DIR" ] && return 0
  local candidate
  for candidate in /var/lib/antimage-node /var/lib/AntiMage-node; do
    [ -d "$candidate" ] && { DATA_DIR="$candidate"; return 0; }
  done
  DATA_DIR=/var/lib/antimage-node
}

# ---------------------------------------------------------------- header ----
section "COLLECTOR"
say "collector: production_diagnostic.sh (read-only)"
say "started_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
say "sample_seconds: $SECONDS_TO_SAMPLE"
say "sample_interval: $INTERVAL"
say "hostname: $(trim "$(read_file /proc/sys/kernel/hostname)")"
say "kernel: $(uname -srmo 2>/dev/null || uname -a)"
say "uid: $(id -u 2>/dev/null)"

section "REDACTION"
say "Review before sharing. The report intentionally contains only aggregate counts,"
say "sizes, timings and process/interface names. It never prints user IDs, emails,"
say "usernames, UUIDs, passwords, tokens, certificates, private keys or proxy"
say "settings. Before sharing, still consider redacting: this hostname, the"
say "installed binary sha256, and any local file paths if they are sensitive."
say "To strip those: sed -E 's/(hostname|sha256): .*/\1: <redacted>/' report.txt"

# ------------------------------------------------------------ host specs ----
section "HOST"
CPU_MODEL="$(grep -m1 '^model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^[[:space:]]*//')"
ONLINE_CPUS="$(nproc --all 2>/dev/null || grep -c '^processor' /proc/cpuinfo 2>/dev/null)"
say "cpu_model: ${CPU_MODEL:-unavailable}"
say "logical_cpus: ${ONLINE_CPUS:-unavailable}"
MEM_TOTAL_KB="$(awk '/^MemTotal:/{print $2}' /proc/meminfo 2>/dev/null)"
say "mem_total_kib: ${MEM_TOTAL_KB:-unavailable}"
say "mem_total_bytes: $(awk -v k="${MEM_TOTAL_KB:-0}" 'BEGIN{printf "%d", k*1024}')"
awk '/^(MemFree|MemAvailable|Buffers|Cached|SwapTotal|SwapFree|Dirty|Writeback):/{k=tolower($1); sub(/:$/, "", k); printf "mem_%s_kib: %s\n", k, $2}' /proc/meminfo 2>/dev/null >> "$OUTPUT"
say "loadavg: $(trim "$(read_file /proc/loadavg)")"
say "uptime_seconds: $(awk '{printf "%d", $1}' /proc/uptime 2>/dev/null)"
say "online_cpu_list: $(trim "$(read_file /sys/devices/system/cpu/online)")"
if [ -f /proc/1/cgroup ]; then
  say "cgroup: $(head -n 3 /proc/1/cgroup 2>/dev/null | tr '\n' ';' )"
fi
say "virtualization: $(systemd-detect-virt 2>/dev/null || echo unavailable)"

# ------------------------------------------------------- node process ------
section "NODE_PROCESS"
if discover_node; then
  say "pid: $NODE_PID"
  say "exe: $(readlink -f "/proc/$NODE_PID/exe" 2>/dev/null || echo unavailable)"
  say "comm: $(trim "$(read_file "/proc/$NODE_PID/comm")")"
  say "cmdline: $(tr '\0' ' ' < "/proc/$NODE_PID/cmdline" 2>/dev/null | sed 's/[[:space:]]*$//')"
  say "state: $(awk '{print $3}' "/proc/$NODE_PID/stat" 2>/dev/null)"
  say "threads: $(awk '/^Threads:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null)"
  say "node_process_cgroup: $(trim "$(read_file "/proc/$NODE_PID/cgroup")")"
  START_TICKS="$(awk '{print $22}' "/proc/$NODE_PID/stat" 2>/dev/null)"
  CLK_TCK="$(getconf CLK_TCK 2>/dev/null || echo 100)"
  BTIME="$(awk '/^btime/{print $2}' /proc/stat 2>/dev/null)"
  if [ -n "$START_TICKS" ] && [ -n "$BTIME" ]; then
    say "process_started_utc: $(date -u -d "@$(( BTIME + START_TICKS / CLK_TCK ))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unavailable)"
  fi
  say "ps_lstart: $(ps -o lstart= -p "$NODE_PID" 2>/dev/null | sed 's/^[[:space:]]*//')"
  say "rss_kib: $(awk '/^VmRSS:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null)"
  say "vsz_kib: $(awk '/^VmSize:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null)"
  if [ -r "/proc/$NODE_PID/io" ]; then
    awk '/^(rchar|wchar|read_bytes|write_bytes|syscr|syscw):/{printf "io_%s: %s\n", $1, $2}' "/proc/$NODE_PID/io" >> "$OUTPUT" 2>/dev/null
  else
    say "io_counters: unavailable (/proc/$NODE_PID/io not readable; sampling io columns stay blank)"
  fi
  say "limits_open_files: $(awk '/^Max open files/{print $4}' "/proc/$NODE_PID/limits" 2>/dev/null)"
  AFFINITY="$(awk '/^Cpus_allowed_list:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null)"
  say "affinity_logical_cpus: ${AFFINITY:-unavailable}"
  if [ -n "$AFFINITY" ]; then
    say "affinity_logical_cpu_count: $(awk -v a="$AFFINITY" 'BEGIN{n=0; m=split(a,p,","); for(i=1;i<=m;i++){ if (p[i] ~ /^[0-9]+-[0-9]+$/) { split(p[i],r,"-"); n+=r[2]-r[1]+1 } else if (p[i] ~ /^[0-9]+$/) { n++ } } print n}')"
  fi
  CG_PATH="$(awk -F: '/^0::/{print $3}' "/proc/$NODE_PID/cgroup" 2>/dev/null)"
  if [ -n "$CG_PATH" ] && [ -r "/sys/fs/cgroup$CG_PATH/memory.max" ]; then
    say "cgroup_memory_max_bytes: $(trim "$(read_file "/sys/fs/cgroup$CG_PATH/memory.max")")"
    say "cgroup_memory_current_bytes: $(trim "$(read_file "/sys/fs/cgroup$CG_PATH/memory.current")")"
    say "cgroup_cpu_max: $(trim "$(read_file "/sys/fs/cgroup$CG_PATH/cpu.max")")"
  else
    say "cgroup_limits: unavailable (cgroup v2 memory.max not readable)"
  fi

  section "NODE_BINARY_IDENTITY"
  EXE_PATH="$(readlink -f "/proc/$NODE_PID/exe" 2>/dev/null || true)"
  if [ -n "$EXE_PATH" ] && [ -r "$EXE_PATH" ]; then
    say "binary_path: $EXE_PATH"
    say "binary_size_bytes: $(stat -c %s "$EXE_PATH" 2>/dev/null || echo unavailable)"
    say "binary_mtime_utc: $(date -u -r "$EXE_PATH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unavailable)"
    say "binary_sha256: $(sha256sum "$EXE_PATH" 2>/dev/null | awk '{print $1}')"
    if have strings; then
      VERSION_STRINGS="$(strings -a "$EXE_PATH" 2>/dev/null | grep -E '^dev-[0-9a-f]{7,40}$' | sort -u | paste -sd, -)"
      say "embedded_build_version: ${VERSION_STRINGS:-unavailable}"
      COMMIT_STRINGS="$(strings -a "$EXE_PATH" 2>/dev/null | grep -oE '^[0-9a-f]{40}$' | sort -u | head -n 3 | paste -sd, -)"
      say "embedded_commit_candidates: ${COMMIT_STRINGS:-unavailable}"
      say "has_runtime_metrics_instrumentation: $(strings -a "$EXE_PATH" 2>/dev/null | grep -c 'runtime-metrics.json')"
      say "has_batched_reflection: $(strings -a "$EXE_PATH" 2>/dev/null | grep -c 'localAwaitingReflectionUsageBatchInto')"
      say "has_delivery_index: $(strings -a "$EXE_PATH" 2>/dev/null | grep -c 'localUsageDeliveryIndex')"
      say "has_anchor_memoization: $(strings -a "$EXE_PATH" 2>/dev/null | grep -c 'localUsageAnchorCache')"
    else
      unavailable "embedded_build_version" "strings not installed"
    fi
  else
    unavailable "binary_sha256" "cannot read /proc/$NODE_PID/exe"
  fi
else
  say "pid: unavailable (no antimage-node process found; pass --pid)"
fi

# -------------------------------------------------- service configuration --
section "SERVICE_CONFIGURATION"
if have systemctl; then
  systemctl show "$SERVICE_UNIT" -p MainPID -p ActiveEnterTimestamp -p ExecMainStartTimestamp \
    -p FragmentPath -p User -p Group -p Restart -p EnvironmentFiles 2>/dev/null >> "$OUTPUT"
  if [ "$(systemctl show "$SERVICE_UNIT" -p LoadState --value 2>/dev/null)" != "loaded" ]; then
    say "systemctl_note: $SERVICE_UNIT is not a loaded system unit here; if the node runs as a systemd user unit, read the sampled PID's cgroup above and inspect it with 'systemctl --user status' as that user"
  fi
  UNIT_FILE="$(systemctl show "$SERVICE_UNIT" -p FragmentPath --value 2>/dev/null)"
  if [ -n "$UNIT_FILE" ] && [ -r "$UNIT_FILE" ]; then
    say "unit_file: $UNIT_FILE"
    say "--- ExecStart / Environment lines (values of secret-looking keys redacted) ---"
    grep -E '^(ExecStart|Environment|EnvironmentFile|WorkingDirectory|LimitNOFILE)=' "$UNIT_FILE" 2>/dev/null \
      | sed -E 's/(TOKEN|SECRET|PASSWORD|PASS|KEY|CERT|DSN|URL)=[^ "]*/\1=<redacted>/gI' >> "$OUTPUT"
  fi
  say "--- account-related environment (allowlisted keys only) ---"
  systemctl show "$SERVICE_UNIT" -p Environment --value 2>/dev/null \
    | tr ' ' '\n' | grep -E '^(ANTIMAGE_NODE_(QUOTA|ACCOUNTING|XRAY|WIREGUARD|OPENVPN|L2TP|PPTP|IKEV2|ANYCONNECT)[A-Z_]*|ANTIMAGE_NODE_DATA_DIR)=' \
    | sed -E 's/(TOKEN|SECRET|PASSWORD|KEY)=.*/\1=<redacted>/gI' >> "$OUTPUT" || say "no ANTIMAGE_NODE_* accounting keys in the unit"
else
  unavailable "systemctl" "systemctl not present"
fi

APP_DIR="$(dirname "$(dirname "$(systemctl show "$SERVICE_UNIT" -p ExecStart --value 2>/dev/null | sed -E 's/.*path=([^ ;]+).*/\1/')" 2>/dev/null)" 2>/dev/null)"
case "$APP_DIR" in ''|.|/|*'path='*) APP_DIR="$APP_DIR_DEFAULT" ;; esac
[ -d "$APP_DIR" ] || APP_DIR="$APP_DIR_DEFAULT"
say "app_dir: $APP_DIR"
say "install_mode_file: $(read_file "$APP_DIR/.install-mode" 2>/dev/null | head -n1)"
if [ -r "$APP_DIR/.binary-release.json" ]; then
  say "--- .binary-release.json (allowlisted keys) ---"
  grep -oE '"(version|tag|commit|sha256|built_at|channel)"[[:space:]]*:[[:space:]]*"[^"]{0,80}"' "$APP_DIR/.binary-release.json" 2>/dev/null >> "$OUTPUT" \
    || say "no allowlisted keys found"
else
  unavailable ".binary-release.json" "not present"
fi
if [ -r "$APP_DIR/.env" ]; then
  say "--- node .env (allowlisted accounting keys only) ---"
  grep -E '^(ANTIMAGE_NODE_(QUOTA|ACCOUNTING|XRAY|WIREGUARD|OPENVPN|L2TP|PPTP|IKEV2|ANYCONNECT)[A-Z_]*|ANTIMAGE_NODE_DATA_DIR)=' "$APP_DIR/.env" 2>/dev/null >> "$OUTPUT" \
    || say "no allowlisted keys in .env"
else
  unavailable ".env" "not present at $APP_DIR/.env"
fi

# ------------------------------------------------------ accounting state ---
section "ACCOUNTING_STATE"
discover_data_dir
say "data_dir: $DATA_DIR"
if [ -d "$DATA_DIR" ]; then
  find "$DATA_DIR" -maxdepth 3 -name 'usage-state.json' -printf '%p size=%s mtime=%TY-%Tm-%TdT%TH:%TM:%TSZ\n' 2>/dev/null \
    | sed "s#^$DATA_DIR/##" >> "$OUTPUT" || unavailable "usage-state inventory" "find failed"
  say "accounting_state_bytes_total: $(find "$DATA_DIR" -maxdepth 3 -name 'usage-state.json' -printf '%s\n' 2>/dev/null | awk '{s+=$1} END{printf "%d", s+0}')"
  say "usage_state_file_count: $(find "$DATA_DIR" -maxdepth 3 -name 'usage-state.json' 2>/dev/null | wc -l)"
  if [ -r "$DATA_DIR/accounting/runtime-policy.json" ]; then
    say "runtime_policy_size_bytes: $(stat -c %s "$DATA_DIR/accounting/runtime-policy.json" 2>/dev/null || echo unavailable)"
    say "runtime_policy_mtime_utc: $(date -u -r "$DATA_DIR/accounting/runtime-policy.json" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unavailable)"
    say "runtime_policy_revision: $(grep -oE '"revision"[[:space:]]*:[[:space:]]*[0-9]+' "$DATA_DIR/accounting/runtime-policy.json" 2>/dev/null | head -n1 | grep -oE '[0-9]+$')"
    say "runtime_policy_inbound_count: $(grep -oE '"tag"[[:space:]]*:' "$DATA_DIR/accounting/runtime-policy.json" 2>/dev/null | wc -l)"
  else
    unavailable "runtime_policy_revision" "runtime-policy.json not present"
  fi
  if [ -r "$DATA_DIR/accounting/deliveries.json" ]; then
    DELIV_PATH="$DATA_DIR/accounting/deliveries.json"
    say "deliveries_size_bytes: $(stat -c %s "$DELIV_PATH" 2>/dev/null || echo unavailable)"
    say "deliveries_mtime_utc: $(date -u -r "$DELIV_PATH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unavailable)"
    say "deliveries_ledger_version: $(grep -oE '"version"[[:space:]]*:[[:space:]]*[0-9]+' "$DELIV_PATH" 2>/dev/null | head -n1 | grep -oE '[0-9]+$')"
    say "deliveries_batch_count: $(grep -oE '"batch_id"[[:space:]]*:' "$DELIV_PATH" 2>/dev/null | wc -l)"
    say "deliveries_row_count: $(grep -oE '"raw"[[:space:]]*:' "$DELIV_PATH" 2>/dev/null | wc -l)"
  else
    unavailable "deliveries.json" "not present under $DATA_DIR/accounting"
  fi
  if [ -r "$DATA_DIR/runtime-metrics.json" ]; then
    say "runtime_metrics_file: present (mtime $(date -u -r "$DATA_DIR/runtime-metrics.json" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null))"
    grep -oE '"(goroutine_count|heap_bytes|total_alloc_bytes|gc_cycles|gc_cpu_seconds_micro|accounting_ticks|quota_evaluations|reflection_calls|delivery_calls)"[[:space:]]*:[[:space:]]*[0-9]+' \
      "$DATA_DIR/runtime-metrics.json" 2>/dev/null >> "$OUTPUT" || say "no allowlisted metrics present"
  else
    say "runtime_metrics_file: unavailable (deployed binary predates read-only runtime instrumentation)"
  fi
else
  unavailable "data_dir" "$DATA_DIR does not exist"
fi

# ------------------------------------------------------- protocol runtime --
section "PROTOCOL_RUNTIME"
for proc in xray wg-quick charon strongswan openvpn xl2tpd pppd ocserv accel-ppp; do
  # pgrep -c prints 0 but exits non-zero when nothing matches; keep the count only.
  COUNT="$(pgrep -cx "$proc" 2>/dev/null || true)"
  [ -n "$COUNT" ] || COUNT=0
  say "process_${proc}_count: $COUNT"
done
if have ss; then
  say "listening_sockets_total: $(ss -H -lntu 2>/dev/null | wc -l)"
  say "established_sockets_total: $(ss -H -t state established 2>/dev/null | wc -l)"
else
  unavailable "socket_counts" "ss not installed"
fi
if have wg; then
  WG_IFACES="$(wg show interfaces 2>/dev/null)"
  say "wireguard_interfaces: ${WG_IFACES:-none}"
  for iface in $WG_IFACES; do
    # Peer count only. Never dump peer public keys or endpoints.
    say "wireguard_${iface}_peer_count: $(wg show "$iface" peers 2>/dev/null | wc -l)"
    say "wireguard_${iface}_latest_handshake_count: $(wg show "$iface" latest-handshakes 2>/dev/null | awk '$2+0>0' | wc -l)"
    say "wireguard_${iface}_transfer_lines: $(wg show "$iface" transfer 2>/dev/null | wc -l)"
  done
else
  unavailable "wireguard_interfaces" "wg not installed"
fi
if [ -d /sys/class/net ]; then
  say "network_interfaces: $(ls /sys/class/net 2>/dev/null | paste -sd, -)"
fi

# --------------------------------------------------------- profile tooling --
section "PROFILE_TOOLING"
# Availability only. This collector never starts a sampled capture; the exact
# read-only command is recorded so a symbolized profile can be taken separately.
if have perf; then
  say "perf_version: $(perf --version 2>/dev/null | head -n1)"
  say "perf_event_paranoid: $(trim "$(read_file /proc/sys/kernel/perf_event_paranoid)")"
  say "kptr_restrict: $(trim "$(read_file /proc/sys/kernel/kptr_restrict)")"
  say "perf_capture_command: perf record -F 99 -g -p <node_pid> -o /var/tmp/node-perf.data -- sleep 60"
  say "perf_report_command: perf report --stdio --no-children --percent-limit 0.3 -i /var/tmp/node-perf.data"
  say "perf_note: needs root or perf_event_paranoid <= 1; the .data file may embed process paths and must be reviewed before sharing"
else
  unavailable "perf" "not installed; CPU attribution must come from the sampling section"
fi
if have go; then
  say "go_version: $(go version 2>/dev/null)"
else
  say "go_version: unavailable"
fi

# --------------------------------------------------------------- sampling --
if [ -n "${NODE_PID:-}" ] && [ -d "/proc/$NODE_PID" ]; then
  section "CPU_SAMPLING"
  CLK_TCK="$(getconf CLK_TCK 2>/dev/null || echo 100)"
  say "clk_tck: $CLK_TCK"
  say "note: cpu_percent_one_core is this PID only; child daemons are excluded"
  say "columns: sample_index elapsed_s cpu_pct_one_core cpu_pct_user cpu_pct_system utime_ticks stime_ticks rss_kib threads read_syscalls write_syscalls write_bytes"

  read_cpu() { awk '{print $14, $15}' "/proc/$NODE_PID/stat" 2>/dev/null; }
  read_rss() { awk '/^VmRSS:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null; }
  read_threads() { awk '/^Threads:/{print $2}' "/proc/$NODE_PID/status" 2>/dev/null; }
  read_ios() { awk '/^(syscr|syscw|write_bytes):/{printf "%s ", $2}' "/proc/$NODE_PID/io" 2>/dev/null; }
  # total, idle and busy jiffies for the whole host, to normalize the node CPU
  # against what the machine was actually doing during the same window.
  read_host_cpu() {
    awk '/^cpu /{total=0; for (i=2; i<=NF; i++) total+=$i; printf "%d %d\n", total, $5+$6}' /proc/stat 2>/dev/null
  }

  NET0="$(read_vpn_net)"
  HOST_CPU0="$(read_host_cpu)"

  PREV_CPU="$(read_cpu)"
  PREV_TIME="$(date +%s)"
  SUM_CPU=0; COUNT=0; PEAK=0; SUM_USER=0; SUM_SYS=0
  i=0
  while [ "$i" -lt "$SECONDS_TO_SAMPLE" ]; do
    sleep "$INTERVAL"
    NOW_TIME="$(date +%s)"
    CUR_CPU="$(read_cpu)"
    [ -z "$CUR_CPU" ] && break
    UTIME="$(echo "$CUR_CPU" | awk '{print $1}')"
    STIME="$(echo "$CUR_CPU" | awk '{print $2}')"
    PUTIME="$(echo "$PREV_CPU" | awk '{print $1}')"
    PSTIME="$(echo "$PREV_CPU" | awk '{print $2}')"
    DT=$(( NOW_TIME - PREV_TIME )); [ "$DT" -le 0 ] && DT=1
    DCPU=$(( (UTIME - PUTIME) + (STIME - PSTIME) ))
    PCT="$(awk -v d="$DCPU" -v t="$DT" -v h="$CLK_TCK" 'BEGIN{printf "%.2f", (d/h)/t*100}')"
    PCT_USER="$(awk -v d="$((UTIME - PUTIME))" -v t="$DT" -v h="$CLK_TCK" 'BEGIN{printf "%.2f", (d/h)/t*100}')"
    PCT_SYS="$(awk -v d="$((STIME - PSTIME))" -v t="$DT" -v h="$CLK_TCK" 'BEGIN{printf "%.2f", (d/h)/t*100}')"
    SUM_CPU="$(awk -v a="$SUM_CPU" -v b="$PCT" 'BEGIN{printf "%.2f", a+b}')"
    SUM_USER="$(awk -v a="$SUM_USER" -v b="$PCT_USER" 'BEGIN{printf "%.2f", a+b}')"
    SUM_SYS="$(awk -v a="$SUM_SYS" -v b="$PCT_SYS" 'BEGIN{printf "%.2f", a+b}')"
    PEAK="$(awk -v a="$PEAK" -v b="$PCT" 'BEGIN{printf "%.2f", (b>a)?b:a}')"
    COUNT=$((COUNT + 1))
    say "$COUNT $((i + INTERVAL)) $PCT $PCT_USER $PCT_SYS $UTIME $STIME $(read_rss) $(read_threads) $(read_ios)"
    PREV_CPU="$CUR_CPU"; PREV_TIME="$NOW_TIME"
    i=$((i + INTERVAL))
  done
  if [ "$COUNT" -gt 0 ]; then
    say "--- summary ---"
    say "samples: $COUNT"
    say "cpu_percent_one_core_mean: $(awk -v a="$SUM_CPU" -v n="$COUNT" 'BEGIN{printf "%.3f", a/n}')"
    say "cpu_percent_one_core_user_mean: $(awk -v a="$SUM_USER" -v n="$COUNT" 'BEGIN{printf "%.3f", a/n}')"
    say "cpu_percent_one_core_system_mean: $(awk -v a="$SUM_SYS" -v n="$COUNT" 'BEGIN{printf "%.3f", a/n}')"
    say "cpu_percent_one_core_peak: $PEAK"
    say "cpu_percent_normalized_by_logical_cpus: $(awk -v a="$SUM_CPU" -v n="$COUNT" -v c="${ONLINE_CPUS:-1}" 'BEGIN{printf "%.3f", (a/n)/c}')"
    say "logical_cpus_used_for_normalization: ${ONLINE_CPUS:-unavailable}"
    HOST_CPU1="$(read_host_cpu)"
    say "host_total_cpu_percent: $(awk -v a="$HOST_CPU0" -v b="$HOST_CPU1" 'BEGIN{split(a,x," "); split(b,y," "); dt=y[1]-x[1]; di=y[2]-x[2]; if (dt<=0) {print "unavailable"} else {printf "%.3f", 100*(dt-di)/dt}}')"
    say "host_total_cpu_semantics: all logical CPUs of this host over the same window"
    NET1="$(read_vpn_net)"
    say "traffic_window_seconds: $(( SECONDS_TO_SAMPLE ))"
    say "traffic_rx_bytes_per_second: $(awk -v a="$NET0" -v b="$NET1" -v s="$SECONDS_TO_SAMPLE" 'BEGIN{split(a,x," "); split(b,y," "); printf "%.1f", (y[1]-x[1])/s}')"
    say "traffic_tx_bytes_per_second: $(awk -v a="$NET0" -v b="$NET1" -v s="$SECONDS_TO_SAMPLE" 'BEGIN{split(a,x," "); split(b,y," "); printf "%.1f", (y[2]-x[2])/s}')"
    say "traffic_scope: VPN virtual interfaces + common ethernet prefixes; excludes unrelated host interfaces"
  fi

  section "PER_THREAD_CPU"
  say "note: cumulative CPU seconds per TID, ranking only; no thread names are materialized"
  if [ -d "/proc/$NODE_PID/task" ]; then
    for tid in $(ls "/proc/$NODE_PID/task" 2>/dev/null); do
      awk -v t="$tid" '{ut=$14; st=$15; printf "%d %.2f %.2f\n", t, ut/100, st/100}' "/proc/$NODE_PID/task/$tid/stat" 2>/dev/null
    done | sort -k2,2gr | head -n 12 | awk 'BEGIN{print "tid cpu_seconds_user cpu_seconds_system"}{print}' >> "$OUTPUT"
  else
    unavailable "per_thread_cpu" "task directory not readable"
  fi
else
  section "CPU_SAMPLING"
  unavailable "cpu_sampling" "no antimage-node process to sample"
fi

# ------------------------------------------------------------- panel DB ----
section "PANEL_DATABASE"
SQL_TABLES="users proxies hosts service_hosts exclude_inbounds_association inbounds"
DB_MODE=""

# Raw single-value SQL. Empty output means "could not run" (client missing,
# database unreadable, or the statement failed); callers must treat that as
# unavailable rather than as a zero.
db_scalar() {
  local sql="$1"
  if [ -n "$SQLITE_DB" ]; then
    if have sqlite3 && sqlite3 -readonly "$SQLITE_DB" 'SELECT 1' >/dev/null 2>&1; then
      sqlite3 -readonly "$SQLITE_DB" "$sql" 2>/dev/null
      return 0
    fi
    if have python3; then
      python3 - "$SQLITE_DB" "$sql" <<'PY' 2>/dev/null
import sqlite3, sys
path, sql = sys.argv[1], sys.argv[2]
try:
    con = sqlite3.connect("file:%s?mode=ro" % path, uri=True, timeout=3)
    con.execute("PRAGMA query_only=ON")
    row = con.execute(sql).fetchone()
    if row is not None and row[0] is not None:
        print(row[0])
except Exception:
    pass
PY
    fi
    return 0
  fi
  if [ -n "$MYSQL_DEFAULTS" ]; then
    have mysql || return 0
    mysql --defaults-file="$MYSQL_DEFAULTS" --connect-timeout=3 --batch --skip-column-names --raw \
      --database "$MYSQL_DATABASE" \
      --execute "SET SESSION TRANSACTION READ ONLY; $sql;" 2>/dev/null
  fi
}

dump_sqlite() {
  local db="$1" table="$2" cols="$3"
  if have sqlite3 && sqlite3 -readonly "$db" 'SELECT 1' >/dev/null 2>&1; then
    sqlite3 -readonly "$db" "SELECT $cols FROM $table LIMIT 200000;" 2>/dev/null
  elif have python3; then
    python3 - "$db" "$table" "$cols" <<'PY' 2>/dev/null
import sqlite3, sys
path, table, cols = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    con = sqlite3.connect("file:%s?mode=ro" % path, uri=True, timeout=3)
    con.execute("PRAGMA query_only=ON")
    for row in con.execute("SELECT %s FROM %s LIMIT 200000" % (cols, table)):
        print("\t".join("" if v is None else str(v) for v in row))
except Exception:
    pass
PY
  fi
}

dump_mysql() {
  local table="$1" cols="$2"
  mysql --defaults-file="$MYSQL_DEFAULTS" --connect-timeout=3 --batch --skip-column-names --raw \
    --database "$MYSQL_DATABASE" \
    --execute "SET SESSION TRANSACTION READ ONLY; SELECT $cols FROM $table LIMIT 200000;" 2>/dev/null
}

dump_table() {
  local table="$1" cols="$2"
  if [ -n "$SQLITE_DB" ]; then dump_sqlite "$SQLITE_DB" "$table" "$cols"
  elif [ -n "$MYSQL_DEFAULTS" ]; then dump_mysql "$table" "$cols"
  fi
}

# A working connection to an empty table must still produce aggregates, so probe
# reachability separately from row presence. "SELECT 1" proves only that the
# database itself is reachable and readable.
db_usable() {
  [ "$(db_scalar 'SELECT 1')" = "1" ]
}

# A table that exists but is empty is a real zero. A table that is missing is
# "unavailable". COUNT(*) fails on a missing table, so empty output means absent.
table_exists() {
  [ -n "$(db_scalar "SELECT COUNT(*) FROM $1")" ]
}

# Resolve the panel database the way the panel installer records it.
if [ -z "$SQLITE_DB" ] && [ -z "$MYSQL_DEFAULTS" ]; then
  # Same candidate list as the validated python collector, so a combined
  # panel+node host resolves its database the same way.
  for env_file in "${ANTIMAGE_ENV_FILE:-}" /opt/antimage/.env /opt/antimage/.env.dist \
                  /etc/antimage/antimage.env /app/.env /srv/antimage/.env /opt/antimage-node/.env; do
    [ -n "$env_file" ] && [ -r "$env_file" ] || continue
    DSN="$(grep -E '^(SQLALCHEMY_DATABASE_URL|DATABASE_URL)=' "$env_file" 2>/dev/null | head -n1 | cut -d= -f2- | tr -d '"' | tr -d "'")"
    case "$DSN" in
      sqlite:///*)
        CAND="${DSN#sqlite:///}"
        [ -f "/$CAND" ] && SQLITE_DB="/$CAND" || SQLITE_DB="$CAND"
        ;;
      mysql*) DB_MODE="mysql-dsn-detected"; ;;
    esac
    [ -n "$SQLITE_DB" ] && break
  done
fi

if [ -n "$SQLITE_DB" ]; then
  if [ -r "$SQLITE_DB" ]; then
    say "sqlite_database: $SQLITE_DB"
    say "sqlite_size_bytes: $(stat -c %s "$SQLITE_DB" 2>/dev/null || echo unavailable)"
  else
    say "sqlite_database: $SQLITE_DB (not readable)"
  fi
elif [ -n "$MYSQL_DEFAULTS" ]; then
  say "mysql_defaults_file: provided (contents never printed)"
else
  say "database: unavailable (no readable .env, --sqlite or --mysql-defaults supplied)"
  say "database_note: this host may be node-only; run this script on the panel host, or pass --sqlite <panel.db>"
  [ -n "$DB_MODE" ] && say "database_note_detail: a MySQL DSN was found; pass --mysql-defaults and --database (credentials are never read from the DSN)"
fi

NOW="$(date +%s)"

# Explains why a per-table aggregate is unavailable, distinguishing "the whole
# database is unreachable" from "this table does not exist here".
db_reason() {
  if [ -z "$SQLITE_DB" ] && [ -z "$MYSQL_DEFAULTS" ]; then
    printf 'panel database not supplied (node-only host, or pass --sqlite <panel.db> / --mysql-defaults)'
  elif db_usable; then
    printf 'table not found in the selected database'
  else
    printf 'panel database not readable (check the path, or the mysql client/defaults file)'
  fi
}

if table_exists users; then
  USERS_TSV="$(dump_table users "id,status,expire,data_limit,used_traffic,admin_disabled_at,service_limit_disabled_at" 2>/dev/null)"
  printf '%s\n' "$USERS_TSV" | awk -F'\t' -v now="$NOW" '
    function num(v){ return (v=="" || v=="None" || v=="null") ? 0 : v+0 }
    function truthy(v){ return (v!="" && v!="0" && v!="None" && v!="null" && tolower(v)!="false") }
    { total++; st=tolower($2)
      if (st!="deleted") configured++
      if (st=="active") active++
      if (st=="on_hold") onhold++
      if (st=="expired" || (num($3)>0 && num($3)<=now)) expired++
      if (truthy($6) || truthy($7)) disabled++
      if (num($4)>0 && num($5)>=num($4)) quota_exhausted++
      if ((st=="active" || st=="on_hold") && !truthy($6) && !truthy($7) && (num($3)==0 || num($3)>now) && (num($4)==0 || num($5)<num($4))) eligible++
    }
    END {
      printf "users_rows_seen: %d\n", total
      printf "users_configured_total: %d\n", configured
      printf "users_active: %d\n", active
      printf "users_on_hold: %d\n", onhold
      printf "users_expired: %d\n", expired
      printf "users_disabled: %d\n", disabled
      printf "users_quota_exhausted: %d\n", quota_exhausted
      printf "users_eligible: %d\n", eligible
    }' >> "$OUTPUT"
else
  unavailable "user_aggregates" "$(db_reason)"
fi

if table_exists proxies; then
  PROXIES_TSV="$(dump_table proxies "type,user_id" 2>/dev/null)"
  printf '%s\n' "$PROXIES_TSV" | awk -F'\t' '
    { type_count[$1]++; seen[$1" "$2]=1 }
    END { for (t in type_count) { n=0; for (k in seen) { split(k,p," "); if (p[1]==t) n++ }
            printf "users_by_protocol.%s: %d\n", t, n }
          printf "proxy_rows_total: %d\n", NR }' | sort >> "$OUTPUT"
else
  unavailable "users_by_protocol" "$(db_reason)"
fi

if table_exists hosts; then
  HOSTS_TSV="$(dump_table hosts "inbound_tag,is_disabled" 2>/dev/null)"
  printf '%s\n' "$HOSTS_TSV" | awk -F'\t' '{ if (tolower($2)=="1" || tolower($2)=="true") disabled++; else enabled++; tags[$1]++ }
    END { printf "hosts_total: %d\nhosts_enabled: %d\nhosts_disabled: %d\ndistinct_inbound_tags: %d\n", NR, enabled, disabled, length(tags) }' >> "$OUTPUT"
else
  unavailable "hosts_aggregates" "$(db_reason)"
fi

if table_exists inbounds; then
  INBOUNDS_TSV="$(dump_table inbounds "tag" 2>/dev/null)"
  printf '%s\n' "$INBOUNDS_TSV" | awk -F'\t' 'NF{count++; if ($1!="") tags[$1]=1}
    END { printf "inbounds_total: %d\ndistinct_inbound_tags: %d\n", count, length(tags) }' >> "$OUTPUT"
else
  unavailable "inbound_aggregates" "$(db_reason)"
fi

if table_exists service_hosts; then
  service_hosts="$(dump_table service_hosts "service_id,host_id" 2>/dev/null)"
  printf '%s\n' "$service_hosts" | awk -F'\t' 'NF{printf "%s\n", $1}' | sort -u | wc -l \
    | awk '{printf "distinct_service_ids_with_hosts: %d\n", $1}' >> "$OUTPUT"
fi

if table_exists node_usage_user_queue; then
  queue="$(dump_table node_usage_user_queue "node_id,user_id,used_traffic,processed_at" 2>/dev/null)"
  printf '%s\n' "$queue" | awk -F'\t' '
    { rows++; if ($4=="" || $4=="0" || $4=="None" || $4=="null") pending++; else processed++ }
    END { printf "node_usage_queue_rows: %d\nnode_usage_queue_pending: %d\nnode_usage_queue_processed: %d\n", rows, pending, processed }' >> "$OUTPUT"
else
  unavailable "node_usage_queue" "$(db_reason)"
fi

if table_exists node_wireguard_usage_reflection; then
  wg_reflection="$(dump_table node_wireguard_usage_reflection "node_id,user_id,batch_id" 2>/dev/null)"
  printf '%s\n' "$wg_reflection" | awk -F'\t' 'NF{rows++} END{printf "node_wireguard_reflection_rows: %d\n", rows}' >> "$OUTPUT"
else
  unavailable "node_wireguard_reflection_rows" "$(db_reason)"
fi

section "END"
say "finished_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
say "report: $OUTPUT"

cat <<EOF
status=ok report=$OUTPUT sample_seconds=$SECONDS_TO_SAMPLE
Note: this report is aggregate-only. Review it before sharing; the header section
explains how to redact hostname, binary sha256 and local paths.
EOF
