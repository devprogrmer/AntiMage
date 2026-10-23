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

uri_line="$(apt-get download --print-uris "$module_package" 2>/dev/null | head -n 1 || true)"
deb_uri="$(printf '%s\n' "$uri_line" | awk '{print $1}')"
deb_uri="${deb_uri%\'}"; deb_uri="${deb_uri#\'}"
deb_path="${deb_uri#*://}"
deb_path="/${deb_path#*/}"
deb_file="$(basename "$deb_path")"

if [ -z "$deb_uri" ] || [ -z "$deb_file" ] || [ "$deb_file" = "$deb_path" ]; then
  echo "could not resolve a download URI for ${module_package}" >&2
  exit 1
fi

deb_target="/tmp/${deb_file}"

# Keep the archive path apt resolved and swap only the host, so the exact
# kernel build the runner needs is fetched from whichever mirror is healthy.
mirrors=(
  "http://archive.ubuntu.com"
  "http://azure.archive.ubuntu.com"
  "http://security.ubuntu.com"
  "http://mirrors.edge.kernel.org"
)

for mirror in "${mirrors[@]}"; do
  left="$(remaining_budget)"
  if [ "$left" -le 15 ]; then
    echo "provisioning budget exhausted before trying ${mirror}"
    break
  fi
  fetch_budget=$(( mirror_seconds < left ? mirror_seconds : left ))
  rm -f "$deb_target"
  echo "fetching ${deb_file} from ${mirror} (limit ${fetch_budget}s)"
  if ! timeout --signal=TERM --kill-after=15s "${fetch_budget}s" \
       curl -fSL --retry 1 --retry-delay 2 --connect-timeout 10 \
         --speed-limit "$min_rate_bytes" --speed-time 20 \
         -o "$deb_target" "${mirror}${deb_path}"; then
    echo "mirror ${mirror} did not deliver ${deb_file} at a usable rate"
    continue
  fi
  # dpkg unpacks the 71MB payload we just paid for; the -f install afterwards
  # resolves only the small remaining dependencies from the archive, so the
  # large package is never re-downloaded.
  sudo dpkg -i "$deb_target" >/dev/null 2>&1 || true
  sudo DEBIAN_FRONTEND=noninteractive timeout --signal=TERM --kill-after=15s 180s \
    apt-get -y -f install --no-install-recommends >/dev/null 2>&1 || true
  if module_available; then
    echo "${module_package} installed from ${mirror}"
    exit 0
  fi
  echo "mirror ${mirror} did not produce an installed l2tp_ppp module; trying the next mirror"
done

echo "could not install ${module_package} from any mirror" >&2
exit 1