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
