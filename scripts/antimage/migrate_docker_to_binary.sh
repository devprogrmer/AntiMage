#!/usr/bin/env bash
set -e

ANTIMAGE_REPO="${ANTIMAGE_REPO:-devprogrmer/AntiMage}"
ANTIMAGE_REF="${ANTIMAGE_REF:-main}"
SCRIPT_URL="${ANTIMAGE_SCRIPT_URL:-https://raw.githubusercontent.com/${ANTIMAGE_REPO}/${ANTIMAGE_REF}/scripts/antimage/antimage.sh}"

if [ "$(id -u)" != "0" ]; then
    echo "This script must be run as root." >&2
    exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
    echo "curl is required." >&2
    exit 1
fi

tmp_script="$(mktemp)"
trap 'rm -f "$tmp_script"' EXIT

curl -fsSL "$SCRIPT_URL" -o "$tmp_script"
chmod 755 "$tmp_script"

exec "$tmp_script" migrate-binary "$@"
