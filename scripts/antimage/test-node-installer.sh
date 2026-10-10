#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

colorized_echo() { :; }

for SCRIPT in "$ROOT/antimage-node.sh" "$ROOT/antimage-node-binary.sh"; do
    eval "$(sed -n '/^select_node_version() {$/,/^}$/p' "$SCRIPT")"
    select_node_version dev
    [ "$SELECTED_NODE_VERSION" = "dev" ]
    select_node_version latest
    [ "$SELECTED_NODE_VERSION" = "latest" ]

    eval "$(sed -n '/^get_node_binary_dev_artifact_metadata() {$/,/^}$/p' "$SCRIPT")"
    ANTIMAGE_NODE_RELEASE_REPO="devprogrmer/AntiMage"
    ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG="dev-builds"
    ANTIMAGE_NODE_BINARY_DEV_BRANCH="dev"
    curl() { return 22; }
    dev_asset=$(get_node_binary_dev_artifact_metadata amd64)
    unset -f curl
    [ "$dev_asset" = "dev-dev|https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-linux-amd64" ]
    if [ "$SCRIPT" = "$ROOT/antimage-node-binary.sh" ]; then
        eval "$(sed -n '/^get_node_binary_pinned_dev_artifact_metadata() {$/,/^}$/p' "$SCRIPT")"
        curl() {
            printf '%s\n' '{"assets":[{"name":"antimage-node-dev-abcdef0123456789-linux-amd64","browser_download_url":"https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-abcdef0123456789-linux-amd64"}]}'
        }
        jq() {
            local requested_name=""
            while [ "$#" -gt 0 ]; do
                if [ "$1" = "--arg" ]; then
                    [ "$2" = "name" ] && requested_name="$3"
                    shift 3
                else
                    shift
                fi
            done
            cat >/dev/null
            [ "$requested_name" = "antimage-node-dev-abcdef0123456789-linux-amd64" ] || return 0
            printf '%s\n' 'https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-abcdef0123456789-linux-amd64'
        }
        pinned_dev_asset=$(get_node_binary_pinned_dev_artifact_metadata dev-abcdef0123456789 amd64)
        unset -f curl
        unset -f jq
        [ "$pinned_dev_asset" = "dev-abcdef0123456789|https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-abcdef0123456789-linux-amd64" ]

        eval "$(sed -n '/^verify_node_binary_checksum() {$/,/^}$/p' "$SCRIPT")"
        checksum_asset="$TMP/antimage-node-dev-abcdef0123456789-linux-amd64"
        printf 'valid test binary payload\n' >"$checksum_asset"
        checksum_value=$(sha256sum "$checksum_asset" | awk '{print $1}')
        curl() {
            local output=""
            while [ "$#" -gt 0 ]; do
                if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
            done
            printf '%s  antimage-node-dev-abcdef0123456789-linux-amd64\n' "$checksum_value" >"$output"
        }
        verify_node_binary_checksum "https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-abcdef0123456789-linux-amd64" "$checksum_asset"
        checksum_value="$(printf '0%.0s' {1..64})"
        if verify_node_binary_checksum "https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-node-dev-abcdef0123456789-linux-amd64" "$checksum_asset"; then
            echo "Node dev checksum mismatch was accepted" >&2
            exit 1
        fi
        unset -f curl
    fi

    grep -Fq 'Downloading AntiMage-node dev release binary' "$SCRIPT"
    grep -Fq 'ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG="${ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG:-dev-builds}"' "$SCRIPT"
    if grep -Fq 'Downloading AntiMage-node dev binary artifact' "$SCRIPT"; then
        echo "Node dev update must not fall back to Actions artifacts" >&2
        exit 1
    fi
    grep -Fq 'rollback_binary_update()' "$SCRIPT"
    grep -Fq "trap 'rollback_binary_update' RETURN" "$SCRIPT"
    grep -Fq 'service did not become active after restart' "$SCRIPT"
    grep -Fq 'Restoring previous AntiMage-node binary' "$SCRIPT"

    eval "$(sed -n '/^read_node_certificate_bundle() {$/,/^}$/p' "$SCRIPT")"
    CERT_FILE="$TMP/cert.pem"
    CERT_KEY_FILE="$TMP/cert.key"
    BUNDLE_FILE="$TMP/bundle.pem"
    rm -f "$CERT_FILE" "$CERT_KEY_FILE"

    printf '%s\r\n' \
        '-----BEGIN CERTIFICATE-----' \
        'certificate' \
        '-----END CERTIFICATE-----' \
        '-----BEGIN PRIVATE KEY-----' \
        'private-key' >"$BUNDLE_FILE"
    printf '%s\r' '-----END PRIVATE KEY-----' >>"$BUNDLE_FILE"
    read_node_certificate_bundle <"$BUNDLE_FILE"

    grep -qx -- '-----END CERTIFICATE-----' "$CERT_FILE"
    grep -qx -- '-----END PRIVATE KEY-----' "$CERT_KEY_FILE"
    if [[ "$(uname -s)" == Linux* ]]; then
        [ "$(stat -c '%a' "$CERT_KEY_FILE")" = "600" ]
    fi

    sed -n '/^create_binary_antimage_node_service() {$/,/^}$/p' "$SCRIPT" | grep -Fq 'EnvironmentFile=-$APP_DIR/.env'

    bash -n "$SCRIPT"
    grep -Fq 'ensure_vpn_host_prerequisites()' "$SCRIPT"
    grep -Fq 'ensure_vpn_binary_prerequisites()' "$SCRIPT"
    grep -Fq '    ensure_vpn_binary_prerequisites' "$SCRIPT"
    grep -Fq '    ensure_vpn_host_prerequisites' "$SCRIPT"
	grep -Fq 'optimize_antimage_server()' "$SCRIPT"
	grep -Fq '    optimize_antimage_server' "$SCRIPT"
	grep -Fq '/etc/sysctl.d/99-antimage-network.conf' "$SCRIPT"
	grep -Fq 'tcp_congestion_control=bbr' "$SCRIPT"
	grep -Fq 'ANTIMAGE_MSS' "$SCRIPT"
	grep -Fq '/swapfile none swap sw 0 0' "$SCRIPT"
    grep -Fq '    cap_add:' "$SCRIPT"
    grep -Fq '      - NET_ADMIN' "$SCRIPT"
    grep -Fq '      - /dev/net/tun:/dev/net/tun' "$SCRIPT"
done

PANEL_SCRIPT="$ROOT/antimage-binary.sh"
eval "$(sed -n '/^verify_panel_artifact_checksum() {$/,/^}$/p' "$PANEL_SCRIPT")"
PANEL_ASSET="$TMP/antimage-linux-amd64-dev-abcdef0123456789.tar.gz"
printf 'valid panel package payload\n' >"$PANEL_ASSET"
PANEL_CHECKSUM=$(sha256sum "$PANEL_ASSET" | awk '{print $1}')
curl() {
    local output=""
    while [ "$#" -gt 0 ]; do
        if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
    done
    printf '%s  antimage-linux-amd64-dev-abcdef0123456789.tar.gz\n' "$PANEL_CHECKSUM" >"$output"
}
verify_panel_artifact_checksum "https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-linux-amd64-dev-abcdef0123456789.tar.gz" "$PANEL_ASSET"
PANEL_CHECKSUM="$(printf '0%.0s' {1..64})"
if verify_panel_artifact_checksum "https://github.com/devprogrmer/AntiMage/releases/download/dev-builds/antimage-linux-amd64-dev-abcdef0123456789.tar.gz" "$PANEL_ASSET"; then
    echo "Panel dev checksum mismatch was accepted" >&2
    exit 1
fi
unset -f curl

eval "$(sed -n '/^panel_update_restore_backup() {$/,/^}$/p' "$PANEL_SCRIPT")"
eval "$(sed -n '/^prepare_panel_update_backup() {$/,/^}$/p' "$PANEL_SCRIPT")"
eval "$(sed -n '/^panel_update_commit_backup() {$/,/^}$/p' "$PANEL_SCRIPT")"
source "$ROOT/managed-backup.sh"
# This fixture covers backup content; the real database/CLI ownership boundary
# is exercised separately by scripts/ci/panel-cli-fence-test.py.
panel_owned_boundary() { "$@"; }
PANEL_TEST_DIR="$TMP/panel-update"
APP_DIR="$PANEL_TEST_DIR"
APP_NAME="isolated-panel-backup"
UPDATE_OPERATION_ID="isolated-panel-backup-operation"
BINARY_BIN_DIR="$APP_DIR/bin"
BINARY_SERVER="$BINARY_BIN_DIR/antimage-server"
BINARY_CLI="$BINARY_BIN_DIR/antimage-cli"
BINARY_METADATA_FILE="$APP_DIR/.binary-release.json"
CHANNEL_FILE="$APP_DIR/.channel"
BINARY_UPDATE_BACKUP_DIR="$APP_DIR/.update-rollback"
BINARY_UPDATE_BACKUP_ACTIVE=0
ANTIMAGE_API_UPDATE=1
mkdir -p "$BINARY_BIN_DIR"
cp /bin/true "$BINARY_SERVER"
cp /bin/true "$BINARY_CLI"
printf '{"tag":"v1.2.2"}\n' > "$BINARY_METADATA_FILE"
printf 'v1.2.2\n' > "$CHANNEL_FILE"
chmod 751 "$BINARY_SERVER"
is_binary_install() { return 0; }
prepare_panel_update_backup 0
[ "$BINARY_UPDATE_BACKUP_ACTIVE" = 1 ]
cp /bin/false "$BINARY_SERVER"
cp /bin/false "$BINARY_CLI"
printf '{"tag":"v1.2.3"}\n' > "$BINARY_METADATA_FILE"
printf 'v1.2.3\n' > "$CHANNEL_FILE"
panel_update_restore_backup
cmp /bin/true "$BINARY_SERVER"
cmp /bin/true "$BINARY_CLI"
grep -Fq 'v1.2.2' "$BINARY_METADATA_FILE"
grep -qx 'v1.2.2' "$CHANNEL_FILE"
[ "$(stat -c '%a' "$BINARY_SERVER")" = 751 ]
panel_update_commit_backup
[ ! -e "$BINARY_UPDATE_BACKUP_DIR" ]
[ -f "$APP_DIR/.update-backups/$UPDATE_OPERATION_ID/manifest.json" ]

grep -Fq 'Resolved AntiMage version:' "$PANEL_SCRIPT"
grep -Fq 'update-rollback) update_rollback_command' "$PANEL_SCRIPT"
grep -Fq 'update-commit) update_commit_command' "$PANEL_SCRIPT"

for SCRIPT in "$ROOT/antimage.sh" "$ROOT/antimage-binary.sh"; do
	bash -n "$SCRIPT"
	grep -Fq 'optimize_antimage_server()' "$SCRIPT"
	grep -Fq 'optimize_antimage_server' "$SCRIPT"
	grep -Fq '/etc/sysctl.d/99-antimage-network.conf' "$SCRIPT"
	grep -Fq 'tcp_congestion_control=bbr' "$SCRIPT"
	grep -Fq '/swapfile none swap sw 0 0' "$SCRIPT"
done

NODE_DOCKERFILE="$ROOT/../../Dockerfile.node"

for package in openvpn wireguard-tools iproute2 iptables nftables procps; do
    grep -Eq "^[[:space:]]+${package}[[:space:]]*\\\\$" "$NODE_DOCKERFILE"
done

for installer in \
    "$ROOT/antimage-node.sh" \
    "$ROOT/antimage-node-binary.sh"; do

    grep -q 'ensure_l2tp_kernel_modules()' "$installer"
    grep -q 'ensure_l2tp_kernel_modules' "$installer"

    grep -q 'ppp_generic' "$installer"
    grep -q 'pppox' "$installer"
    grep -q 'l2tp_ppp' "$installer"

    grep -q 'linux-modules-extra-${kernel_release}' "$installer"

    grep -q 'packages+=("xl2tpd")' "$installer"
    grep -q 'packages+=("ppp")' "$installer"
    grep -q 'packages+=("strongswan" "strongswan-pki")' "$installer"

    grep -q 'command -v xl2tpd' "$installer"
    grep -q 'command -v pppd' "$installer"
    grep -q 'command -v ipsec' "$installer"
    grep -q 'command -v ocpasswd' "$installer"
    grep -q 'command -v occtl' "$installer"
    grep -q 'reinstall_package "ocserv"' "$installer"
done
