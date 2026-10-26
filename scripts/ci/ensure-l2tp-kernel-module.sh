#!/usr/bin/env bash
# Install the kernel package that ships the PPP-over-L2TP driver (l2tp_ppp).
#
# Hosted Azure runners intermittently throttle the 71MB linux-modules-extra
# download from the Azure archive mirror to roughly 39 kB/s. apt-get will not
# abandon a slow-but-still-progressing transfer, so a single apt-get invocation
# simply stalls until it is killed and l2tp_ppp stays absent. After one bounded
# apt attempt this script fetches the very same .deb directly, using curl's
# --speed-limit abort so a stalled mirror is dropped after ~20s.
#
# Everything is bounded by a global provisioning deadline so a slow runner can
# never consume the whole CI step; failing here still fails the job.
#
# The .deb version suffix rotates as Ubuntu publishes kernel updates, so the
# direct-download fallback resolves the pool filename from each source's own
# package index rather than reusing the path apt resolved against the runner's
# possibly stale local index. scripts/ci/l2tp-kernel-module-resolver-test.py
# covers the resolution and failover behaviour hermetically.
#
# This never skips the test: scripts/ci/native-l2tp-driver.sh still runs
# `modprobe l2tp_ppp`, so a genuinely unavailable package still fails the
# native job.
set -uo pipefail

provision_started_at="$(date +%s)"
kernel_release="$(uname -r)"
module_package="linux-modules-extra-${kernel_release}"

provision_budget="${ANTIMAGE_L2TP_PROVISION_SECONDS:-840}"
apt_attempt_seconds="${ANTIMAGE_L2TP_APT_ATTEMPT_SECONDS:-120}"
mirror_seconds="${ANTIMAGE_L2TP_MIRROR_SECONDS:-240}"
min_rate_bytes="${ANTIMAGE_L2TP_MIN_RATE_BYTES:-300000}"

remaining_budget() {
  local elapsed=$(( $(date +%s) - provision_started_at ))
  local left=$(( provision_budget - elapsed ))
  [ "$left" -lt 0 ] && left=0
  printf '%s' "$left"
}

module_available() {
  [ -n "$(find "/lib/modules/${kernel_release}" -name 'l2tp_ppp.ko*' -print -quit 2>/dev/null || true)" ]
}

if module_available; then
  echo "l2tp_ppp is already present in /lib/modules/${kernel_release}"
  exit 0
fi

echo "installing ${module_package} for kernel ${kernel_release} (budget ${provision_budget}s)"

# Fast path: the runner image cache, or a mirror that is not throttling.
# Bounded so a stalled transfer cannot consume the whole job.
apt_budget=$(( apt_attempt_seconds < $(remaining_budget) ? apt_attempt_seconds : $(remaining_budget) ))
if [ "$apt_budget" -gt 15 ] && \
   sudo timeout --signal=TERM --kill-after=15s "${apt_budget}s" \
     apt-get -o Acquire::Retries=1 -o Acquire::http::Timeout=20 \
     install -y --no-install-recommends "$module_package"; then
  if module_available; then
    echo "${module_package} installed via apt"
    exit 0
  fi
fi

echo "apt install of ${module_package} did not finish; falling back to a direct .deb fetch"
sudo apt-get clean >/dev/null 2>&1 || true

# The package name pins the kernel ABI, but the .deb version suffix rotates as
# Ubuntu publishes kernel updates. Resolve the pool file from each source's own
# package index instead of reusing the single path apt resolved against the
# runner's cached index: a superseded file is deleted from the pool, so a stale
# path 404s on every mirror even though a valid replacement is published.
index_sources=(
  "http://azure.archive.ubuntu.com"
  "http://archive.ubuntu.com"
  "http://security.ubuntu.com"
  "http://mirrors.edge.kernel.org"
)

resolve_pool_file() {
  local base="$1" suite listing file
  for suite in jammy-updates jammy-security jammy; do
    listing="${base}/ubuntu/dists/${suite}/main/binary-amd64/Packages.gz"
    file="$(curl -fsS --connect-timeout 10 --max-time 90 "$listing" 2>/dev/null |
      gzip -dc 2>/dev/null |
      awk -v p="$module_package" '
        $1 == "Package:" && $2 == p { want = 1 }
        want && $1 == "Filename:" { print $2; exit }
      ' || true)"
    if [ -n "$file" ]; then
      printf '%s\n' "$file"
      return 0
    fi
  done
  return 1
}

install_from_source() {
  local url="$1" label="$2" file="$3"
  local left fetch_budget deb_target
  left="$(remaining_budget)"
  if [ "$left" -le 15 ]; then
    echo "provisioning budget exhausted before ${label}"
    return 2
  fi
  fetch_budget=$(( mirror_seconds < left ? mirror_seconds : left ))
  deb_target="/tmp/${file}"
  rm -f "$deb_target"
  echo "fetching ${file} from ${label} (limit ${fetch_budget}s)"
  if ! timeout --signal=TERM --kill-after=15s "${fetch_budget}s" \
       curl -fSL --retry 1 --retry-delay 2 --connect-timeout 10 \
         --speed-limit "$min_rate_bytes" --speed-time 20 \
         -o "$deb_target" "$url"; then
    echo "${label} did not deliver ${file} at a usable rate"
    return 1
  fi
  # dpkg unpacks the 71MB payload we just paid for; the -f install afterwards
  # resolves only the small remaining dependencies from the archive, so the
  # large package is never re-downloaded.
  sudo dpkg -i "$deb_target" >/dev/null 2>&1 || true
  sudo DEBIAN_FRONTEND=noninteractive timeout --signal=TERM --kill-after=15s 180s \
    apt-get -y -f install --no-install-recommends >/dev/null 2>&1 || true
  if module_available; then
    echo "${module_package} installed from ${label}"
    return 0
  fi
  echo "${label} did not produce an installed l2tp_ppp module"
  return 1
}

pool_file=""
for base in "${index_sources[@]}"; do
  candidate="$(resolve_pool_file "$base" || true)"
  if [ -z "$candidate" ]; then
    echo "no ${module_package} published on ${base}"
    continue
  fi
  echo "resolved ${module_package} to ${candidate} on ${base}"
  if install_from_source "${base}/ubuntu/${candidate}" "$base" "$(basename "$candidate")"; then
    exit 0
  fi
  [ -n "$pool_file" ] || pool_file="$candidate"
done

# snapshot.ubuntu.com archives every published file, including ones the live
# pool has already rotated away. Use the newest filename the live indexes agree
# on, pinned to a recent snapshot date.
if [ -n "$pool_file" ]; then
  for days in 1 2 4 8 16; do
    left="$(remaining_budget)"
    if [ "$left" -le 15 ]; then
      echo "provisioning budget exhausted before snapshot fallback"
      break
    fi
    stamp="$(date -u -d "-${days} days" +%Y%m%dT000000Z 2>/dev/null || true)"
    [ -n "$stamp" ] || continue
    if install_from_source \
         "https://snapshot.ubuntu.com/ubuntu/${stamp}/${pool_file}" \
         "snapshot ${stamp}" "$(basename "$pool_file")"; then
      exit 0
    fi
  done
fi

echo "could not install ${module_package} from any source" >&2
exit 1
